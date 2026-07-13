package wallet

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type transactionFetchRPCCall struct {
	method     string
	params     []any
	restricted bool
}

type transactionFetchExecutionRPC struct {
	mu        sync.Mutex
	calls     []transactionFetchRPCCall
	responses []any
	errors    []error
}

func (rpc *transactionFetchExecutionRPC) RetriableValue(
	_ context.Context, method string, params []any, restricted bool,
) (any, error) {
	rpc.mu.Lock()
	defer rpc.mu.Unlock()
	callIndex := len(rpc.calls)
	rpc.calls = append(rpc.calls, transactionFetchRPCCall{
		method: method, params: append([]any(nil), params...), restricted: restricted,
	})
	if callIndex < len(rpc.errors) && rpc.errors[callIndex] != nil {
		return nil, rpc.errors[callIndex]
	}
	if callIndex >= len(rpc.responses) {
		return nil, fmt.Errorf("unexpected RPC call %s %#v", method, params)
	}
	return rpc.responses[callIndex], nil
}

func (*transactionFetchExecutionRPC) RetriableCall(
	context.Context, string, []any, bool,
) (map[string]any, error) {
	return nil, errors.New("unexpected header RPC")
}

func (*transactionFetchExecutionRPC) Start(context.Context) error { return nil }
func (*transactionFetchExecutionRPC) Stop(context.Context) error  { return nil }
func (*transactionFetchExecutionRPC) RemoteHeight() int           { return 0 }
func (*transactionFetchExecutionRPC) IsConnected() bool           { return true }
func (*transactionFetchExecutionRPC) SetHeaderNotificationHandler(func(context.Context, any)) {
}
func (*transactionFetchExecutionRPC) SetAddressNotificationHandler(func(context.Context, any)) {
}
func (*transactionFetchExecutionRPC) SetConnectedHandler(func(context.Context)) {}
func (*transactionFetchExecutionRPC) SubscribeAddresses(context.Context, []string) ([]any, error) {
	return nil, errors.New("unexpected address subscription")
}

func (rpc *transactionFetchExecutionRPC) snapshotCalls() []transactionFetchRPCCall {
	rpc.mu.Lock()
	defer rpc.mu.Unlock()
	result := make([]transactionFetchRPCCall, len(rpc.calls))
	copy(result, rpc.calls)
	return result
}

func TestLedgerRequestTransactionsUsesFlatPlanAndComputedIDMap(t *testing.T) {
	transaction := mustFetchTransaction(t)
	headers := newTransactionExecutionHeaders(t, transaction.ID, transaction.ID, transaction.ID)
	setTransactionExecutionCheckpoints(headers, 2)

	secondRaw := transactionFetchFixtureHex + "00"
	secondTransaction, err := ParseTransaction(mustDecodeTransactionHex(t, secondRaw))
	if err != nil {
		t.Fatal(err)
	}
	rpc := &transactionFetchExecutionRPC{responses: []any{map[string]any{
		"requested-high": []any{secondRaw, map[string]any{"block_height": 2}},
		"requested-low":  []any{transactionFetchFixtureHex, map[string]any{"block_height": 1}},
	}}}
	ledger := &Ledger{Headers: headers, SPVNetwork: rpc}
	batches, err := ledger.RequestTransactions(context.Background(), []TransactionFetchRequest{
		{TxID: "requested-high", Height: 2},
		{TxID: "requested-low", Height: 1},
	})
	if err != nil {
		t.Fatal(err)
	}

	wantCalls := []transactionFetchRPCCall{{
		method: SPVTransactionBatchMethod, params: []any{"requested-low", "requested-high"}, restricted: false,
	}}
	if calls := rpc.snapshotCalls(); !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("RPC calls = %#v, want %#v", calls, wantCalls)
	}
	if len(batches) != 1 || len(batches[0].Results) != 2 ||
		batches[0].Results[0].Result.Request.TxID != "requested-low" ||
		batches[0].Results[1].Result.Request.TxID != "requested-high" {
		t.Fatalf("ordered batches = %#v", batches)
	}
	for _, result := range batches[0].Results {
		if result.Verification != TransactionMerkleProofMissing {
			t.Fatalf("verification = %q, want missing proof", result.Verification)
		}
	}
	if len(batches[0].Transactions) != 2 ||
		batches[0].Transactions[transaction.ID] == nil ||
		batches[0].Transactions[secondTransaction.ID] == nil ||
		batches[0].Transactions["requested-low"] != nil ||
		batches[0].Transactions["requested-high"] != nil {
		t.Fatalf("computed ID map = %#v", batches[0].Transactions)
	}
	if batches[0].Transactions[transaction.ID].Height != 1 ||
		batches[0].Transactions[secondTransaction.ID].Height != 2 {
		t.Fatalf("remote heights were not applied: %#v", batches[0].Transactions)
	}
}

func TestLedgerRequestTransactionsEmptyCheckpointEquivalentIsRestricted(t *testing.T) {
	transaction := mustFetchTransaction(t)
	headers := newTransactionExecutionHeaders(t, transaction.ID, transaction.ID, transaction.ID)
	if height := transactionFetchHighestCheckpointHeight(headers); height != 0 {
		t.Fatalf("empty checkpoint maximum = %d, want 0", height)
	}
	rpc := &transactionFetchExecutionRPC{responses: []any{map[string]any{
		"one": []any{transactionFetchFixtureHex, map[string]any{"block_height": 1}},
		"two": []any{transactionFetchFixtureHex + "00", map[string]any{"block_height": 2}},
	}}}
	ledger := &Ledger{Headers: headers, SPVNetwork: rpc}
	if _, err := ledger.RequestTransactions(context.Background(), []TransactionFetchRequest{
		{TxID: "two", Height: 2}, {TxID: "one", Height: 1},
	}); err != nil {
		t.Fatal(err)
	}
	calls := rpc.snapshotCalls()
	if len(calls) != 1 || !calls[0].restricted ||
		!reflect.DeepEqual(calls[0].params, []any{"one", "two"}) {
		t.Fatalf("RPC calls = %#v", calls)
	}
}

func TestLedgerRequestTransactionBatchPreservesGlobalDuplicateHeight(t *testing.T) {
	transaction := mustFetchTransaction(t)
	headers := newTransactionExecutionHeaders(t, transaction.ID, transaction.ID)
	rpc := &transactionFetchExecutionRPC{responses: []any{map[string]any{
		"duplicate": []any{transactionFetchFixtureHex, map[string]any{"block_height": 9}},
	}}}
	ledger := &Ledger{Headers: headers, SPVNetwork: rpc}
	batch, err := ledger.requestTransactionBatch(
		context.Background(), rpc, TransactionFetchBatch{
			Requests: []TransactionFetchRequest{{TxID: "duplicate", Height: 1}},
			Params:   []any{"duplicate"},
			RemoteHeights: map[string]int64{
				"duplicate": 9,
			},
			Restricted: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Results) != 1 || batch.Results[0].Result.RemoteHeight != 9 ||
		batch.Results[0].Result.Transaction.Height != 9 ||
		batch.Results[0].Verification != TransactionMerkleHeightGated {
		t.Fatalf("executed global plan = %#v", batch)
	}
}

func TestLedgerRequestTransactionsFallbackMerkleMatches(t *testing.T) {
	transaction := mustFetchTransaction(t)
	headers := newTransactionExecutionHeaders(t, strings.Repeat("0", 64), transaction.ID)
	rpc := &transactionFetchExecutionRPC{responses: []any{
		map[string]any{"server-key": []any{transactionFetchFixtureHex, nil}},
		map[string]any{"merkle": []any{}, "pos": 0},
	}}
	ledger := &Ledger{Headers: headers, SPVNetwork: rpc}
	batches, err := ledger.RequestTransactions(context.Background(), []TransactionFetchRequest{{
		TxID: "server-key", Height: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}

	wantCalls := []transactionFetchRPCCall{
		{method: SPVTransactionBatchMethod, params: []any{"server-key"}, restricted: true},
		{method: SPVTransactionMerkleMethod, params: []any{transaction.ID, int64(1)}, restricted: false},
	}
	if calls := rpc.snapshotCalls(); !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("RPC calls = %#v, want %#v", calls, wantCalls)
	}
	result := batches[0].Results[0]
	if result.Verification != TransactionMerkleMatched ||
		!result.Result.Transaction.IsVerified || result.Result.Transaction.Position != 0 ||
		batches[0].Transactions[transaction.ID] != result.Result.Transaction {
		t.Fatalf("matched result = %#v", result)
	}
}

func TestLedgerRequestTransactionsBatchMerkleMismatchDoesNotFallback(t *testing.T) {
	transaction := mustFetchTransaction(t)
	headers := newTransactionExecutionHeaders(t, strings.Repeat("0", 64), strings.Repeat("f", 64))
	rpc := &transactionFetchExecutionRPC{responses: []any{map[string]any{
		"server-key": []any{transactionFetchFixtureHex, map[string]any{
			"merkle": []any{}, "pos": 3,
		}},
	}}}
	ledger := &Ledger{Headers: headers, SPVNetwork: rpc}
	batches, err := ledger.RequestTransactions(context.Background(), []TransactionFetchRequest{{
		TxID: "server-key", Height: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if calls := rpc.snapshotCalls(); len(calls) != 1 {
		t.Fatalf("RPC calls = %#v, want batch only", calls)
	}
	result := batches[0].Results[0]
	if result.Verification != TransactionMerkleMismatched ||
		result.Result.Transaction.IsVerified || result.Result.Transaction.Position != 3 ||
		result.Result.Transaction.ID != transaction.ID {
		t.Fatalf("mismatched result = %#v", result)
	}
}

func TestLedgerRequestTransactionsHeightGateAndProofMissingSkipFallback(t *testing.T) {
	transaction := mustFetchTransaction(t)
	for _, fixture := range []struct {
		name   string
		height int64
		proof  any
		status TransactionMerkleVerificationStatus
	}{
		{"zero height", 0, nil, TransactionMerkleHeightGated},
		{"height at length", 2, nil, TransactionMerkleHeightGated},
		{"truthy proof missing merkle", 1, map[string]any{"block_height": 1}, TransactionMerkleProofMissing},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			headers := newTransactionExecutionHeaders(t, strings.Repeat("0", 64), transaction.ID)
			rpc := &transactionFetchExecutionRPC{responses: []any{map[string]any{
				"server-key": []any{transactionFetchFixtureHex, fixture.proof},
			}}}
			ledger := &Ledger{Headers: headers, SPVNetwork: rpc}
			batches, err := ledger.RequestTransactions(context.Background(), []TransactionFetchRequest{{
				TxID: "server-key", Height: fixture.height,
			}})
			if err != nil {
				t.Fatal(err)
			}
			if calls := rpc.snapshotCalls(); len(calls) != 1 {
				t.Fatalf("RPC calls = %#v, want batch only", calls)
			}
			if status := batches[0].Results[0].Verification; status != fixture.status {
				t.Fatalf("verification = %q, want %q", status, fixture.status)
			}
		})
	}
}

func TestLedgerRequestTransactionsValidatesProofBeforeReadingHeader(t *testing.T) {
	transaction := mustFetchTransaction(t)
	for _, fixture := range []struct {
		name    string
		proof   map[string]any
		wantErr error
	}{
		{"missing proof", map[string]any{"block_height": 1}, nil},
		{"malformed proof", map[string]any{"merkle": "bad", "pos": 0}, ErrMalformedTransactionMerkle},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			headers := newTransactionExecutionHeaders(t, strings.Repeat("0", 64), transaction.ID)
			detachTransactionExecutionStorage(headers)
			rpc := &transactionFetchExecutionRPC{responses: []any{map[string]any{
				"server-key": []any{transactionFetchFixtureHex, fixture.proof},
			}}}
			ledger := &Ledger{Headers: headers, SPVNetwork: rpc}
			batches, err := ledger.RequestTransactions(context.Background(), []TransactionFetchRequest{{
				TxID: "server-key", Height: 1,
			}})
			if fixture.wantErr == nil {
				if err != nil || batches[0].Results[0].Verification != TransactionMerkleProofMissing {
					t.Fatalf("result = %#v, %v", batches, err)
				}
			} else if !errors.Is(err, fixture.wantErr) {
				t.Fatalf("error = %v, want %v", err, fixture.wantErr)
			}
		})
	}

	headers := newTransactionExecutionHeaders(t, strings.Repeat("0", 64), transaction.ID)
	detachTransactionExecutionStorage(headers)
	rpc := &transactionFetchExecutionRPC{responses: []any{map[string]any{
		"server-key": []any{transactionFetchFixtureHex, map[string]any{"merkle": []any{}, "pos": 0}},
	}}}
	ledger := &Ledger{Headers: headers, SPVNetwork: rpc}
	if _, err := ledger.RequestTransactions(context.Background(), []TransactionFetchRequest{{
		TxID: "server-key", Height: 1,
	}}); err == nil || errors.Is(err, ErrTransactionMerkle) {
		t.Fatalf("actual verification header error = %v", err)
	}
}

func TestLedgerRequestTransactionsPropagatesRPCAndDecodeErrors(t *testing.T) {
	transaction := mustFetchTransaction(t)
	batchErr := errors.New("batch failed")
	fallbackErr := errors.New("merkle failed")
	tests := []struct {
		name      string
		responses []any
		errors    []error
		want      error
	}{
		{"batch RPC", []any{nil}, []error{batchErr}, batchErr},
		{"fallback RPC", []any{
			map[string]any{"server-key": []any{transactionFetchFixtureHex, nil}}, nil,
		}, []error{nil, fallbackErr}, fallbackErr},
		{"fallback decode", []any{
			map[string]any{"server-key": []any{transactionFetchFixtureHex, nil}}, []any{},
		}, nil, ErrTransactionFetchResultMalformed},
		{"fallback null", []any{
			map[string]any{"server-key": []any{transactionFetchFixtureHex, nil}}, nil,
		}, nil, ErrTransactionFetchResultMalformed},
	}
	for _, fixture := range tests {
		t.Run(fixture.name, func(t *testing.T) {
			headers := newTransactionExecutionHeaders(t, strings.Repeat("0", 64), transaction.ID)
			rpc := &transactionFetchExecutionRPC{responses: fixture.responses, errors: fixture.errors}
			ledger := &Ledger{Headers: headers, SPVNetwork: rpc}
			_, err := ledger.RequestTransactions(context.Background(), []TransactionFetchRequest{{
				TxID: "server-key", Height: 1,
			}})
			if !errors.Is(err, fixture.want) {
				t.Fatalf("error = %v, want %v", err, fixture.want)
			}
		})
	}

	headers := newTransactionExecutionHeaders(t, strings.Repeat("0", 64), transaction.ID)
	rpc := &transactionFetchExecutionRPC{responses: []any{map[string]any{}}}
	ledger := &Ledger{Headers: headers, SPVNetwork: rpc}
	batches, err := ledger.RequestTransactions(context.Background(), []TransactionFetchRequest{{
		TxID: "server-key", Height: 1,
	}})
	if err != nil || len(batches) != 1 || len(batches[0].Results) != 0 {
		t.Fatalf("partial empty batch = %#v, %v", batches, err)
	}

	if _, err := (*Ledger)(nil).RequestTransactions(context.Background(), nil); !errors.Is(err, ErrTransactionFetchUnavailable) {
		t.Fatalf("nil ledger error = %v", err)
	}
	if _, err := (&Ledger{}).RequestTransactions(context.Background(), nil); !errors.Is(err, ErrTransactionFetchUnavailable) {
		t.Fatalf("missing headers error = %v", err)
	}
	if _, err := (&Ledger{Headers: &Headers{}}).RequestTransactions(context.Background(), nil); !errors.Is(err, ErrTransactionFetchUnavailable) {
		t.Fatalf("missing RPC error = %v", err)
	}
}

func newTransactionExecutionHeaders(t *testing.T, merkleRoots ...string) *Headers {
	t.Helper()
	headers := NewHeaders(
		":memory:", WithHeaderValidation(false), withHeaderCheckpoints(emptyCheckpoints),
	)
	headers.genesisHash = nil
	if err := headers.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		headers.mu.RLock()
		hasStorage := headers.storage != nil
		headers.mu.RUnlock()
		if hasStorage {
			_ = headers.Close()
		}
	})

	previous := strings.Repeat("0", 64)
	serialized := make([]byte, 0, len(merkleRoots)*HeaderSize)
	for height, merkleRoot := range merkleRoots {
		raw, err := SerializeHeader(BlockHeader{
			Version: 1, PreviousHash: []byte(previous), MerkleRoot: []byte(merkleRoot),
			ClaimTrieRoot: []byte(strings.Repeat("0", 64)), Timestamp: uint32(height + 1), Bits: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		serialized = append(serialized, raw...)
		previous = string(HashHeader(raw))
	}
	added, err := headers.Connect(0, serialized)
	if err != nil || added != len(merkleRoots) {
		t.Fatalf("connected headers = %d, %v; want %d", added, err, len(merkleRoots))
	}
	return headers
}

func setTransactionExecutionCheckpoints(headers *Headers, count int) {
	headers.mu.Lock()
	headers.checkpoints = checkpointTable{data: strings.Repeat("\x00", count*checkpointDigestSize)}
	headers.mu.Unlock()
}

func detachTransactionExecutionStorage(headers *Headers) {
	headers.mu.Lock()
	headers.storage = nil
	headers.mu.Unlock()
}

func mustDecodeTransactionHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
