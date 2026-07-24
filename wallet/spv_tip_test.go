package wallet

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"lbry/daemon/wallet/keys"
)

func TestSPVLiveHeaderParsingAndErrors(t *testing.T) {
	update, err := parseSPVLiveHeaderUpdate([]any{map[string]any{
		"height": json.Number("42"), "hex": "aabb",
	}})
	if err != nil || update != (SPVLiveHeaderUpdate{Height: 42, Hex: "aabb"}) {
		t.Fatalf("parsed notification = %#v, %v", update, err)
	}
	update, err = parseSPVLiveHeaderUpdate([]any{map[string]any{"height": 43, "hex": nil}})
	if err != nil || update != (SPVLiveHeaderUpdate{Height: 43}) {
		t.Fatalf("falsey notification hex = %#v, %v", update, err)
	}
	for _, params := range []any{
		nil,
		[]any{},
		[]any{map[string]any{"hex": "aa"}},
		[]any{map[string]any{"height": json.Number("1.5"), "hex": "aa"}},
	} {
		if _, err := parseSPVLiveHeaderUpdate(params); err == nil {
			t.Fatalf("invalid notification %#v was accepted", params)
		}
	}
	if _, err := spvLiveHeaderHex(map[string]any{}); !errors.Is(err, ErrSPVLiveHeaderHexMissing) {
		t.Fatalf("missing response hex error = %v", err)
	}
	if value, err := spvLiveHeaderHex(map[string]any{"hex": false}); err != nil || value != "" {
		t.Fatalf("falsey response hex = %q, %v", value, err)
	}
	if _, err := spvLiveHeaderHex(map[string]any{"hex": []any{"truthy"}}); !errors.Is(err, ErrSPVLiveHeaderHexType) {
		t.Fatalf("invalid response hex error = %v", err)
	}
}

func TestSPVLiveHeaderRewindLimit(t *testing.T) {
	responses := make(map[int][]map[string]any)
	for height := 149; height >= 51; height-- {
		responses[height] = []map[string]any{{"hex": "00"}}
	}
	store := &spvTipProbeStore{length: 150, results: make([]int, SPVLiveHeaderMaxRewind)}
	network := &spvTipProbeNetwork{remoteHeight: 150, responses: responses}
	rejected := 0
	err := updateSPVLiveHeaders(
		context.Background(), store, network, "lbc_mainnet",
		&SPVLiveHeaderUpdate{Height: 150, Hex: "00"},
		spvLiveHeaderHooks{onRejected: func() { rejected++ }},
	)
	if !errors.Is(err, ErrSPVLiveHeaderRewind) || rejected != SPVLiveHeaderMaxRewind {
		t.Fatalf("rewind limit = %v, rejected %d", err, rejected)
	}
	want := "Blockchain reorganization dropped 100 headers. This is highly unusual. " +
		"Will not continue to attempt reorganizing. Please, delete the ledger " +
		"synchronization directory inside your wallet directory (folder: 'lbc_mainnet') and " +
		"restart the program to synchronize from scratch."
	if err.Error() != want {
		t.Fatalf("rewind limit message = %q", err)
	}
}

func TestLedgerSPVTipCatchupAndNotificationUseRealHeaders(t *testing.T) {
	headers, chain := liveTipHeaders(t, 5, 2)
	network := &lifecycleTipNetwork{
		remoteHeight: 4,
		live: map[int]string{
			2: hex.EncodeToString(bytes.Join(chain[2:4], nil)),
			4: "",
		},
	}
	ledger := &Ledger{Network: keys.RegTest, Headers: headers}
	if err := ledger.SetSPVNetwork(network); err != nil {
		t.Fatal(err)
	}
	if err := ledger.StartSPVCheckpointSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForSPVTip(t, ledger, func(snapshot LedgerSPVSnapshot) bool {
		return snapshot.InitialTipDone && snapshot.TipHeight == 3 && snapshot.FillDone
	})
	if headers.Height() != 3 {
		t.Fatalf("initial live tip height = %d", headers.Height())
	}
	network.Emit([]any{map[string]any{"height": 4}})
	waitForSPVTip(t, ledger, func(snapshot LedgerSPVSnapshot) bool {
		return errors.Is(snapshot.TipErr, ErrSPVLiveHeaderHexMissing)
	})
	network.Emit([]any{map[string]any{"height": 4, "hex": hex.EncodeToString(chain[4])}})
	waitForSPVTip(t, ledger, func(snapshot LedgerSPVSnapshot) bool {
		return snapshot.TipHeight == 4 && snapshot.TipChange == 1 && snapshot.TipErr == nil
	})
	if headers.Height() != 4 {
		t.Fatalf("notification live tip height = %d", headers.Height())
	}
	network.mu.Lock()
	calls := append([]lifecycleTipCall(nil), network.calls...)
	network.mu.Unlock()
	wantCalls := []lifecycleTipCall{{Start: 2, Count: 2001}, {Start: 4, Count: 2001}}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("live tip calls = %#v, want %#v", calls, wantCalls)
	}
	if err := ledger.StopSPVCheckpointSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if snapshot := ledger.SPVSnapshot(); snapshot.Running || !snapshot.TipWorkerDone {
		t.Fatalf("stopped live tip snapshot = %#v", snapshot)
	}
	if err := headers.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLedgerSPVTipStartsBeforeGeneralCheckpointFill(t *testing.T) {
	chunks := [][]byte{checkpointFetchFixture(101), checkpointFetchFixture(103)}
	headers := NewHeaders(":memory:", withHeaderCheckpoints(checkpointTableFromHashes(t,
		string(HashHeader(chunks[0])), string(HashHeader(chunks[1])),
	)))
	if err := headers.Open(); err != nil {
		t.Fatal(err)
	}
	network := &lifecycleTipNetwork{
		remoteHeight: 1_999,
		live:         map[int]string{2_000: ""},
		checkpoints: map[int]string{
			0:                      checkpointFetchEncoded(t, chunks[0], nil),
			CheckpointChunkHeaders: checkpointFetchEncoded(t, chunks[1], nil),
		},
	}
	ledger := &Ledger{Network: keys.MainNet, Headers: headers}
	if err := ledger.SetSPVNetwork(network); err != nil {
		t.Fatal(err)
	}
	if err := ledger.StartSPVCheckpointSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForSPVTip(t, ledger, func(snapshot LedgerSPVSnapshot) bool {
		return snapshot.InitialTipDone && snapshot.FillDone
	})
	network.mu.Lock()
	calls := append([]lifecycleTipCall(nil), network.calls...)
	network.mu.Unlock()
	want := []lifecycleTipCall{
		{Start: 2_000, Count: 2_001},
		{Start: 1_000, Count: 1_000, Base64: true},
		{Start: 0, Count: 1_000, Base64: true},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("tip/checkpoint call order = %#v, want %#v", calls, want)
	}
	if err := ledger.StopSPVCheckpointSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := headers.Close(); err != nil {
		t.Fatal(err)
	}
}

func liveTipHeaders(t *testing.T, total, connected int) (*Headers, [][]byte) {
	t.Helper()
	headers := NewHeaders(
		":memory:", WithHeaderValidation(false), withHeaderCheckpoints(checkpointTable{}),
	)
	headers.genesisHash = nil
	if err := headers.Open(); err != nil {
		t.Fatal(err)
	}
	chain := make([][]byte, total)
	previous := bytes.Repeat([]byte{'0'}, 64)
	for height := 0; height < total; height++ {
		header := BlockHeader{
			Version:       1,
			PreviousHash:  previous,
			MerkleRoot:    []byte(fmt.Sprintf("%064x", height+1)),
			ClaimTrieRoot: []byte(fmt.Sprintf("%064x", height+101)),
			Timestamp:     uint32(1_500_000_000 + height*150),
			Bits:          0x207fffff,
			Nonce:         uint32(height),
		}
		raw, err := SerializeHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		chain[height] = raw
		previous = HashHeader(raw)
	}
	if connected > 0 {
		if added, err := headers.Connect(0, bytes.Join(chain[:connected], nil)); err != nil || added != connected {
			t.Fatalf("seed live headers = %d, %v", added, err)
		}
	}
	t.Cleanup(func() {
		if headers.opened {
			_ = headers.Close()
		}
	})
	return headers, chain
}

type lifecycleTipCall struct {
	Start  int
	Count  int
	Base64 bool
}

type lifecycleTipNetwork struct {
	mu           sync.Mutex
	handler      func(context.Context, any)
	remoteHeight int
	live         map[int]string
	checkpoints  map[int]string
	calls        []lifecycleTipCall
	started      bool
	stopped      bool
}

func (network *lifecycleTipNetwork) SetHeaderNotificationHandler(handler func(context.Context, any)) {
	network.mu.Lock()
	network.handler = handler
	network.mu.Unlock()
}

func (network *lifecycleTipNetwork) Start(context.Context) error {
	network.mu.Lock()
	network.started = true
	network.mu.Unlock()
	return nil
}

func (network *lifecycleTipNetwork) Stop(context.Context) error {
	network.mu.Lock()
	network.stopped = true
	network.mu.Unlock()
	return nil
}

func (network *lifecycleTipNetwork) RemoteHeight() int { return network.remoteHeight }

func (network *lifecycleTipNetwork) RetriableCall(
	ctx context.Context, method string, params []any, _ bool,
) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if method != SPVHeaderRPCMethod || len(params) != 4 {
		return nil, fmt.Errorf("unexpected lifecycle tip call %s %#v", method, params)
	}
	start := params[0].(int)
	count := params[1].(int)
	b64 := params[3].(bool)
	network.mu.Lock()
	network.calls = append(network.calls, lifecycleTipCall{Start: start, Count: count, Base64: b64})
	defer network.mu.Unlock()
	if b64 {
		return map[string]any{"base64": network.checkpoints[start]}, nil
	}
	return map[string]any{"hex": network.live[start]}, nil
}

func (network *lifecycleTipNetwork) Emit(params any) {
	network.mu.Lock()
	handler := network.handler
	network.mu.Unlock()
	if handler != nil {
		handler(context.Background(), params)
	}
}

func waitForSPVTip(t *testing.T, ledger *Ledger, ready func(LedgerSPVSnapshot) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		snapshot := ledger.SPVSnapshot()
		if ready(snapshot) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("SPV tip did not reach expected state: %#v", snapshot)
		}
		time.Sleep(time.Millisecond)
	}
}

var _ LedgerSPVNetwork = (*lifecycleTipNetwork)(nil)
var _ LedgerSPVHeaderSource = (*lifecycleTipNetwork)(nil)
