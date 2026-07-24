package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	"lbry/daemon/componentgraph"
	"lbry/daemon/config"
	"lbry/daemon/wallet"
	walletspv "lbry/daemon/wallet/spv"
)

type desktopSPVNetwork struct {
	release     chan struct{}
	releaseOnce sync.Once
}

func newDesktopSPVNetwork() *desktopSPVNetwork {
	return &desktopSPVNetwork{release: make(chan struct{})}
}

func (network *desktopSPVNetwork) ReleaseReadiness() {
	network.releaseOnce.Do(func() { close(network.release) })
}

func (*desktopSPVNetwork) Start(context.Context) error { return nil }
func (*desktopSPVNetwork) Stop(context.Context) error  { return nil }
func (*desktopSPVNetwork) RemoteHeight() int           { return 0 }
func (*desktopSPVNetwork) SetHeaderNotificationHandler(func(context.Context, any)) {
}
func (network *desktopSPVNetwork) RetriableCall(
	ctx context.Context, _ string, _ []any, _ bool,
) (map[string]any, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-network.release:
		return map[string]any{"hex": ""}, nil
	}
}
func (*desktopSPVNetwork) RetriableValue(
	context.Context, string, []any, bool,
) (any, error) {
	errorMessage := protowire.AppendTag(nil, 1, protowire.VarintType)
	errorMessage = protowire.AppendVarint(errorMessage, uint64(wallet.HubErrorNotFound))
	errorMessage = protowire.AppendTag(errorMessage, 2, protowire.BytesType)
	errorMessage = protowire.AppendString(errorMessage, "not found")
	output := protowire.AppendTag(nil, 15, protowire.BytesType)
	output = protowire.AppendBytes(output, errorMessage)
	page := protowire.AppendTag(nil, 1, protowire.BytesType)
	page = protowire.AppendBytes(page, output)
	return base64.StdEncoding.EncodeToString(page), nil
}
func (*desktopSPVNetwork) Snapshot() walletspv.Snapshot {
	return walletspv.Snapshot{
		Connected: true,
		Server:    walletspv.Server{Host: "desktop.test", Port: 50001},
		Features:  map[string]any{"server_version": "desktop-test"},
	}
}
func (*desktopSPVNetwork) KnownHubCount() int { return 1 }

func TestDesktopStartupRPCSequence(t *testing.T) {
	root := t.TempDir()
	network := newDesktopSPVNetwork()
	managerFactory := func(*config.Store) (*wallet.WalletManager, error) {
		manager, err := wallet.WalletManagerFromLBRYNetConfig(wallet.LBRYNetConfig{
			BlockchainName: "lbrycrd_regtest",
			WalletDir:      filepath.Join(root, "wallet"),
			Wallets:        []string{"default_wallet"},
		}, nil)
		if err != nil {
			return nil, err
		}
		ledger := manager.DefaultLedger()
		if ledger == nil {
			return nil, errors.New("desktop test wallet has no default ledger")
		}
		if err := ledger.SetSPVNetwork(network); err != nil {
			return nil, err
		}
		return manager, nil
	}
	app, err := newProductionAppWithDependencies(map[string]any{
		"api":             "127.0.0.1:0",
		"blockchain_name": "lbrycrd_regtest",
		"components_to_skip": []string{
			componentgraph.DHT,
			componentgraph.HashAnnouncer,
			componentgraph.PeerProtocolServer,
			componentgraph.ExchangeRateManager,
		},
		"config":           filepath.Join(root, "data", "daemon_settings.yml"),
		"data_dir":         filepath.Join(root, "data"),
		"download_dir":     filepath.Join(root, "downloads"),
		"fixed_peers":      []string{},
		"reflect_streams":  false,
		"streaming_server": "127.0.0.1:0",
		"tcp_port":         0,
		"tracker_servers":  []string{},
		"wallet_dir":       filepath.Join(root, "wallet"),
	}, "", nil, managerFactory)
	if err != nil {
		t.Fatal(err)
	}

	var rpcAddress string
	for _, service := range app.services {
		if service.name == "rpc" && service.listener != nil {
			rpcAddress = service.listener.Addr().String()
			break
		}
	}
	if rpcAddress == "" {
		_ = app.Shutdown()
		t.Fatal("desktop test app has no RPC listener")
	}
	endpoint := "http://" + rpcAddress + "/"
	runResult := make(chan error, 1)
	go func() { runResult <- app.Run(context.Background()) }()
	finished := false
	t.Cleanup(func() {
		network.ReleaseReadiness()
		app.RequestStop()
		if !finished {
			select {
			case <-runResult:
			case <-time.After(5 * time.Second):
				t.Errorf("desktop test daemon did not stop")
			}
		}
	})

	status := waitDesktopRPC(t, endpoint, "status", nil, 3*time.Second)
	statusResult := desktopRPCResult(t, status)
	if statusResult["is_running"] != false {
		t.Fatalf("status before wallet readiness = %#v", statusResult)
	}
	version := desktopRPCResult(t, callDesktopRPC(t, endpoint, "version", nil))
	if version["lbrynet_version"] != "0.113.0" {
		t.Fatalf("desktop daemon version = %#v", version["lbrynet_version"])
	}

	network.ReleaseReadiness()
	deadline := time.Now().Add(5 * time.Second)
	for {
		status = callDesktopRPC(t, endpoint, "status", nil)
		statusResult = desktopRPCResult(t, status)
		walletStatus, _ := statusResult["wallet"].(map[string]any)
		if statusResult["is_running"] == true && walletStatus["available_servers"] == float64(1) {
			torrentStatus, _ := statusResult[componentgraph.Libtorrent].(map[string]any)
			if torrentStatus["running"] != true {
				t.Fatalf("Desktop became ready without its torrent session: %#v", statusResult)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("desktop readiness did not complete: %#v", statusResult)
		}
		time.Sleep(10 * time.Millisecond)
	}

	walletStatus := desktopRPCResult(t, callDesktopRPC(t, endpoint, "wallet_status", nil))
	if walletStatus["is_encrypted"] != false || walletStatus["is_locked"] != false ||
		walletStatus["is_syncing"] != false {
		t.Fatalf("wallet_status result = %#v", walletStatus)
	}
	resolved := desktopRPCResult(t, callDesktopRPC(t, endpoint, "resolve", map[string]any{"urls": "lbry://one"}))
	if _, exists := resolved["lbry://one"]; !exists {
		t.Fatalf("resolve result = %#v", resolved)
	}

	assertDesktopPostReadyShapes(t, endpoint)
	stop := callDesktopRPC(t, endpoint, "stop", nil)
	if result := stop["result"]; result != "Shutting down" {
		t.Fatalf("stop result = %#v", result)
	}
	select {
	case err := <-runResult:
		finished = true
		if err != nil {
			t.Fatalf("desktop daemon run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("desktop daemon did not stop after RPC response")
	}
}

func assertDesktopPostReadyShapes(t *testing.T, endpoint string) {
	t.Helper()
	balance := desktopRPCResult(t, callDesktopRPC(t, endpoint, "wallet_balance", nil))
	reserved, ok := balance["reserved_subtotals"].(map[string]any)
	if !ok || reserved["claims"] == nil || reserved["supports"] == nil || reserved["tips"] == nil {
		t.Fatalf("wallet_balance result = %#v", balance)
	}
	for _, method := range []string{"file_list", "channel_list", "collection_list"} {
		result := desktopRPCResult(t, callDesktopRPC(t, endpoint, method, nil))
		if _, ok := result["items"].([]any); !ok {
			t.Fatalf("%s result = %#v", method, result)
		}
	}
	settings := desktopRPCResult(t, callDesktopRPC(t, endpoint, "settings_get", nil))
	if _, exists := settings["share_usage_data"]; !exists {
		t.Fatalf("settings_get result = %#v", settings)
	}
	_ = desktopRPCResult(t, callDesktopRPC(t, endpoint, "ffmpeg_find", nil))
}

func waitDesktopRPC(t *testing.T, endpoint, method string, params any, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		payload, err := desktopRPC(endpoint, method, params)
		if err == nil {
			return payload
		}
		last = err
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("desktop %s RPC unavailable: %v", method, last)
	return nil
}

func callDesktopRPC(t *testing.T, endpoint, method string, params any) map[string]any {
	t.Helper()
	payload, err := desktopRPC(endpoint, method, params)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func desktopRPC(endpoint, method string, params any) (map[string]any, error) {
	envelope := map[string]any{"jsonrpc": "2.0", "method": method, "id": 1}
	if params != nil {
		envelope["params"] = params
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	requestURL := endpoint + "?m=" + url.QueryEscape(method)
	request, err := http.NewRequest(http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json-rpc")
	client := &http.Client{Timeout: time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("desktop %s RPC returned HTTP %d: %s", method, response.StatusCode, data)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("decode desktop %s RPC: %w", method, err)
	}
	return payload, nil
}

func desktopRPCResult(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	if rpcError, exists := payload["error"]; exists {
		t.Fatalf("desktop RPC error = %#v", rpcError)
	}
	result, ok := payload["result"].(map[string]any)
	if !ok {
		t.Fatalf("desktop RPC result = %T %#v", payload["result"], payload["result"])
	}
	return result
}

var _ wallet.LedgerSPVNetwork = (*desktopSPVNetwork)(nil)
var _ wallet.LedgerSPVHeaderSource = (*desktopSPVNetwork)(nil)
var _ wallet.LedgerSPVRetriableValueSource = (*desktopSPVNetwork)(nil)
