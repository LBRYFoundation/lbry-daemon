package wallet

import (
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/spv"
)

const resolveLiveMissingURL = "lbry://lbry-go-live-resolve-missing#0000000000000000000000000000000000000000"

type resolveLiveCall struct {
	method     string
	params     []any
	restricted bool
}

type resolveLiveNetwork struct {
	*spv.Network

	mu    sync.Mutex
	calls []resolveLiveCall
}

func (network *resolveLiveNetwork) RetriableValue(
	ctx context.Context, method string, params []any, restricted bool,
) (any, error) {
	network.mu.Lock()
	network.calls = append(network.calls, resolveLiveCall{
		method: method, params: append([]any(nil), params...), restricted: restricted,
	})
	network.mu.Unlock()
	return network.Network.RetriableValue(ctx, method, params, restricted)
}

func (network *resolveLiveNetwork) snapshotCalls() []resolveLiveCall {
	network.mu.Lock()
	defer network.mu.Unlock()
	calls := make([]resolveLiveCall, len(network.calls))
	for index, call := range network.calls {
		calls[index] = resolveLiveCall{
			method: call.method, params: append([]any(nil), call.params...), restricted: call.restricted,
		}
	}
	return calls
}

func TestResolveAndSnapshotLiveHub(t *testing.T) {
	hubAddress := strings.TrimSpace(os.Getenv("LBRY_LIVE_HUB"))
	resolveURL := strings.TrimSpace(os.Getenv("LBRY_LIVE_RESOLVE_URL"))
	if hubAddress == "" || resolveURL == "" {
		t.Skip("set LBRY_LIVE_HUB and LBRY_LIVE_RESOLVE_URL to run the live SPV resolve")
	}
	_, err := ParseLBRYURL(resolveURL)
	if err != nil {
		t.Fatalf("parse LBRY_LIVE_RESOLVE_URL: %v", err)
	}
	host, portText, err := net.SplitHostPort(hubAddress)
	if err != nil {
		t.Fatalf("parse LBRY_LIVE_HUB: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse LBRY_LIVE_HUB port: %v", err)
	}

	productionNetwork, err := spv.NewNetwork(spv.NetworkConfig{
		ExplicitServers: []spv.Server{{Host: host, Port: port}},
		Client: spv.ClientConfig{
			ConnectTimeout: 10 * time.Second,
			RequestTimeout: 20 * time.Second,
		},
		ReconnectDelay:   time.Second,
		VersionTimeout:   10 * time.Second,
		KeepaliveIdle:    time.Hour,
		KeepaliveTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	network := &resolveLiveNetwork{Network: productionNetwork}
	networkContext, cancelNetwork := context.WithCancel(context.Background())
	if err := network.Start(networkContext); err != nil {
		cancelNetwork()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancelNetwork()
		stopContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := network.Stop(stopContext); err != nil {
			t.Errorf("stop live resolve SPV network: %v", err)
		}
	})
	waitForResolveLiveHub(t, network, 30*time.Second)

	ledger, err := newLedger(keys.MainNet, LedgerConfig{"data_path": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ledger.SPVNetwork = network
	resolveContext, cancelResolve := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancelResolve()
	callbackOutputs := -1
	results, err := ledger.ResolveAndSnapshot(
		resolveContext,
		[]ResolveRequest{
			{URL: resolveURL},
			{URL: resolveLiveMissingURL},
		},
		ResolvedTransactionOutputAnnotationOptions{},
		LegacyTransactionJSONOptions{IncludeProtobuf: true},
		func(outputs []*TransactionOutput) error {
			callbackOutputs = len(outputs)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("live resolve result count = %d, want 2: %#v", len(results), results)
	}

	resolved := resolveLiveResultObject(t, results[0], resolveURL)
	if errorValue, exists := resolved["error"]; exists {
		t.Fatalf("configured live URL resolved to an error: %#v", errorValue)
	}
	txid, txidOK := resolved["txid"].(string)
	claimID, claimIDOK := resolved["claim_id"].(string)
	name, nameOK := resolved["name"].(string)
	if !txidOK || len(txid) != 64 || !claimIDOK || len(claimID) != 40 ||
		!nameOK || name == "" {
		t.Fatalf("configured live claim envelope = %#v", resolved)
	}

	missing := resolveLiveResultObject(t, results[1], resolveLiveMissingURL)
	missingError, ok := missing["error"].(map[string]any)
	if !ok || missingError["name"] != "NOT_FOUND" {
		t.Fatalf("clearly missing live URL result = %#v", missing)
	}
	if text, ok := missingError["text"].(string); !ok || text == "" {
		t.Fatalf("clearly missing live URL error text = %#v", missingError["text"])
	}
	if callbackOutputs != 1 {
		t.Fatalf("live resolve callback outputs = %d, want 1", callbackOutputs)
	}

	calls := network.snapshotCalls()
	resolveCalls, transactionCalls := 0, 0
	for _, call := range calls {
		switch call.method {
		case SPVResolveMethod:
			resolveCalls++
			if call.restricted || len(call.params) != 2 ||
				call.params[0] != resolveURL || call.params[1] != resolveLiveMissingURL {
				t.Fatalf("live resolve RPC call = %#v", call)
			}
		case SPVTransactionBatchMethod:
			transactionCalls++
			if len(call.params) == 0 {
				t.Fatalf("live transaction batch call = %#v", call)
			}
		}
	}
	if resolveCalls != 1 || transactionCalls == 0 {
		t.Fatalf("live retriable calls = %#v, want one resolve and a transaction batch", calls)
	}
	t.Logf(
		"resolved %s as %s (%s) through %s with %d transaction batch call(s)",
		resolveURL, claimID, txid, hubAddress, transactionCalls,
	)
}

func resolveLiveResultObject(t *testing.T, value any, url string) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("live resolve result for %s = %T %#v, want object", url, value, value)
	}
	return object
}

func waitForResolveLiveHub(t *testing.T, network *resolveLiveNetwork, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !network.IsConnected() {
		if time.Now().After(deadline) {
			t.Fatalf("live resolve SPV hub did not connect: %#v", network.Snapshot())
		}
		time.Sleep(25 * time.Millisecond)
	}
}

var _ LedgerSPVNetwork = (*resolveLiveNetwork)(nil)
var _ LedgerSPVAddressSource = (*resolveLiveNetwork)(nil)
var _ LedgerSPVRetriableValueSource = (*resolveLiveNetwork)(nil)
