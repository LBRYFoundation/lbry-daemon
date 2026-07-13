package wallet

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"lbry/daemon/wallet/keys"
)

func TestLedgerSPVCheckpointSyncFillsNewestFirstAndStopsBeforeHeaders(t *testing.T) {
	chunks := [][]byte{checkpointFetchFixture(71), checkpointFetchFixture(73)}
	headers := NewHeaders(":memory:", withHeaderCheckpoints(checkpointTableFromHashes(t,
		string(HashHeader(chunks[0])), string(HashHeader(chunks[1])),
	)))
	if err := headers.Open(); err != nil {
		t.Fatal(err)
	}
	ledger := &Ledger{Network: keys.MainNet, Headers: headers}
	stub := &ledgerSPVStub{
		remoteHeight: 1_050,
		responses: map[int]string{
			0:                      checkpointFetchEncoded(t, chunks[0], nil),
			CheckpointChunkHeaders: checkpointFetchEncoded(t, chunks[1], nil),
		},
		headers: headers,
	}
	if err := ledger.SetSPVNetwork(stub); err != nil {
		t.Fatal(err)
	}
	if err := ledger.StartSPVCheckpointSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForLedgerSPVFill(t, ledger)
	if snapshot := ledger.SPVSnapshot(); !snapshot.Running || !snapshot.FillDone || snapshot.FillErr != nil {
		t.Fatalf("filled snapshot = %#v", snapshot)
	}
	stub.mu.Lock()
	calls := append([]ledgerSPVCall(nil), stub.calls...)
	started := stub.started
	stub.mu.Unlock()
	want := []ledgerSPVCall{
		{Method: SPVHeaderRPCMethod, Params: []any{1_000, 1_000, 0, true}, Restricted: true},
		{Method: SPVHeaderRPCMethod, Params: []any{0, 1_000, 0, true}, Restricted: false},
	}
	if !started || !reflect.DeepEqual(calls, want) {
		t.Fatalf("SPV fill = started %t, calls %#v; want %#v", started, calls, want)
	}
	if missing := headers.MissingCheckpointedChunks(); len(missing) != 0 {
		t.Fatalf("checkpoint fill left missing chunks: %v", missing)
	}
	if err := ledger.SetSPVNetwork(nil); !errors.Is(err, ErrLedgerSPVRunning) {
		t.Fatalf("replace running SPV network error = %v", err)
	}
	if err := ledger.StopSPVCheckpointSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	stub.mu.Lock()
	stopped, headersOpenAtStop := stub.stopped, stub.headersOpenAtStop
	stub.mu.Unlock()
	if !stopped || !headersOpenAtStop {
		t.Fatalf("stop order = stopped %t, headers open %t", stopped, headersOpenAtStop)
	}
	if snapshot := ledger.SPVSnapshot(); snapshot.Running {
		t.Fatalf("stopped snapshot = %#v", snapshot)
	}
	if err := headers.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLedgerSPVCheckpointStopCancelsBlockedFill(t *testing.T) {
	chunk := checkpointFetchFixture(79)
	headers := checkpointFetchHeaders(t, chunk)
	ledger := &Ledger{Network: keys.MainNet, Headers: headers}
	stub := &ledgerSPVStub{blockCalls: true, callStarted: make(chan struct{})}
	if err := ledger.SetSPVNetwork(stub); err != nil {
		t.Fatal(err)
	}
	if err := ledger.StartSPVCheckpointSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stub.callStarted:
	case <-time.After(time.Second):
		t.Fatal("checkpoint call did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := ledger.StopSPVCheckpointSync(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot := ledger.SPVSnapshot()
	if snapshot.Running || !snapshot.FillDone || !errors.Is(snapshot.FillErr, context.Canceled) {
		t.Fatalf("cancelled fill snapshot = %#v", snapshot)
	}
}

func TestLedgerSPVCheckpointStartRequirementsAndManagerRunningGate(t *testing.T) {
	ledger := &Ledger{Network: keys.MainNet, Headers: NewHeaders(":memory:")}
	if err := ledger.StartSPVCheckpointSync(context.Background()); !errors.Is(err, ErrLedgerSPVUnavailable) {
		t.Fatalf("missing network error = %v", err)
	}
	stub := &ledgerSPVStub{}
	if err := ledger.SetSPVNetwork(stub); err != nil {
		t.Fatal(err)
	}
	if err := ledger.StartSPVCheckpointSync(context.Background()); !errors.Is(err, ErrHeadersNotOpen) {
		t.Fatalf("unopened headers error = %v", err)
	}
	if err := ledger.Headers.Open(); err != nil {
		t.Fatal(err)
	}
	manager := NewWalletManager()
	manager.Ledgers[keys.MainNet] = ledger
	manager.ledgerOrder = []*Ledger{ledger}
	if err := manager.StartSPVCheckpointSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !manager.IsRunning() {
		t.Fatal("wallet manager did not enter its running lifecycle before ledger startup")
	}
	if err := manager.StopSPVCheckpointSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if manager.IsRunning() {
		t.Fatal("wallet manager remained running after ledger shutdown")
	}
}

func TestLedgerReadyLatchDoesNotDependOnCheckpointFillOrLiveConnection(t *testing.T) {
	ledger := &Ledger{}
	ledger.spvSync.mu.Lock()
	ledger.spvSync.running = true
	ledger.spvSync.initialTipDone = true
	ledger.spvSync.fillErr = errors.New("background checkpoint still pending")
	ledger.spvSync.readyDone = make(chan struct{})
	ledger.maybeMarkReadyLocked()
	ledger.spvSync.mu.Unlock()
	if err := ledger.WaitSPVReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	if snapshot := ledger.SPVSnapshot(); !snapshot.WalletReady || snapshot.FillDone {
		t.Fatalf("latched readiness snapshot = %#v", snapshot)
	}
}

func TestLedgerSPVLateFillCannotOverwriteRestartedGeneration(t *testing.T) {
	chunk := checkpointFetchFixture(83)
	headers := checkpointFetchHeaders(t, chunk)
	ledger := &Ledger{Network: keys.MainNet, Headers: headers}
	stub := &generationSPVStub{
		firstStarted:  make(chan struct{}),
		releaseFirst:  make(chan struct{}),
		secondStarted: make(chan struct{}),
	}
	if err := ledger.SetSPVNetwork(stub); err != nil {
		t.Fatal(err)
	}
	if err := ledger.StartSPVCheckpointSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-stub.firstStarted
	stopCtx, cancelStop := context.WithTimeout(context.Background(), 30*time.Millisecond)
	err := ledger.StopSPVCheckpointSync(stopCtx)
	cancelStop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed-out stop error = %v", err)
	}
	if err := ledger.StartSPVCheckpointSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	close(stub.releaseFirst)
	select {
	case <-stub.secondStarted:
	case <-time.After(time.Second):
		t.Fatal("restarted fill did not acquire the checkpoint gate")
	}
	snapshot := ledger.SPVSnapshot()
	if !snapshot.Running || snapshot.FillDone || snapshot.FillErr != nil {
		t.Fatalf("late prior fill corrupted restarted state: %#v", snapshot)
	}
	if err := ledger.StopSPVCheckpointSync(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type ledgerSPVCall struct {
	Method     string
	Params     []any
	Restricted bool
}

type ledgerSPVStub struct {
	mu                sync.Mutex
	remoteHeight      int
	responses         map[int]string
	calls             []ledgerSPVCall
	started           bool
	stopped           bool
	blockCalls        bool
	callStarted       chan struct{}
	callStartedOnce   sync.Once
	headers           *Headers
	headersOpenAtStop bool
}

type generationSPVStub struct {
	calls         atomic.Int32
	firstStarted  chan struct{}
	releaseFirst  chan struct{}
	secondStarted chan struct{}
}

func (*generationSPVStub) Start(context.Context) error { return nil }

func (*generationSPVStub) Stop(context.Context) error { return nil }

func (*generationSPVStub) RemoteHeight() int { return 0 }

func (stub *generationSPVStub) RetriableCall(
	ctx context.Context, _ string, _ []any, _ bool,
) (map[string]any, error) {
	if stub.calls.Add(1) == 1 {
		close(stub.firstStarted)
		<-stub.releaseFirst
		return nil, errors.New("late prior fill failure")
	}
	close(stub.secondStarted)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (stub *ledgerSPVStub) Start(context.Context) error {
	stub.mu.Lock()
	stub.started = true
	stub.mu.Unlock()
	return nil
}

func (stub *ledgerSPVStub) Stop(context.Context) error {
	stub.mu.Lock()
	stub.stopped = true
	if stub.headers != nil {
		stub.headers.mu.RLock()
		stub.headersOpenAtStop = stub.headers.opened
		stub.headers.mu.RUnlock()
	}
	stub.mu.Unlock()
	return nil
}

func (stub *ledgerSPVStub) RemoteHeight() int { return stub.remoteHeight }

func (stub *ledgerSPVStub) RetriableCall(
	ctx context.Context, method string, params []any, restricted bool,
) (map[string]any, error) {
	stub.mu.Lock()
	stub.calls = append(stub.calls, ledgerSPVCall{
		Method: method, Params: append([]any(nil), params...), Restricted: restricted,
	})
	block := stub.blockCalls
	started := stub.callStarted
	stub.mu.Unlock()
	if block {
		stub.callStartedOnce.Do(func() {
			if started != nil {
				close(started)
			}
		})
		<-ctx.Done()
		return nil, ctx.Err()
	}
	start, _ := params[0].(int)
	return map[string]any{"base64": stub.responses[start]}, nil
}

func waitForLedgerSPVFill(t *testing.T, ledger *Ledger) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		if snapshot := ledger.SPVSnapshot(); snapshot.FillDone {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("checkpoint fill did not finish: %#v", ledger.SPVSnapshot())
		}
		time.Sleep(time.Millisecond)
	}
}

var _ LedgerSPVNetwork = (*ledgerSPVStub)(nil)
var _ LedgerSPVNetwork = (*generationSPVStub)(nil)
