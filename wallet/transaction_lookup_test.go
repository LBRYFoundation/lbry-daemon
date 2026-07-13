package wallet

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"lbry/daemon/wallet/ledgerdb"
)

type transactionLookupTestCall struct {
	method     string
	params     []any
	restricted bool
}

type transactionLookupTestNetwork struct {
	mu             sync.Mutex
	calls          []transactionLookupTestCall
	retriableCalls []transactionLookupTestCall
	responses      []any
	errors         []error
	block          bool
	started        chan struct{}
}

func (network *transactionLookupTestNetwork) RetriableValue(
	_ context.Context, method string, params []any, restricted bool,
) (any, error) {
	network.mu.Lock()
	defer network.mu.Unlock()
	network.retriableCalls = append(network.retriableCalls, transactionLookupTestCall{
		method: method, params: append([]any(nil), params...), restricted: restricted,
	})
	return nil, fmt.Errorf("unexpected retriable transaction lookup RPC %s %#v", method, params)
}

func (network *transactionLookupTestNetwork) OneShotValue(
	ctx context.Context, method string, params []any, restricted bool,
) (any, error) {
	network.mu.Lock()
	index := len(network.calls)
	network.calls = append(network.calls, transactionLookupTestCall{
		method: method, params: append([]any(nil), params...), restricted: restricted,
	})
	block := network.block
	started := network.started
	var response any
	if index < len(network.responses) {
		response = network.responses[index]
	}
	var responseErr error
	if index < len(network.errors) {
		responseErr = network.errors[index]
	}
	network.mu.Unlock()

	if block {
		if started != nil {
			select {
			case started <- struct{}{}:
			default:
			}
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if responseErr != nil {
		return nil, responseErr
	}
	if index >= len(network.responses) {
		return nil, fmt.Errorf("unexpected transaction lookup RPC %s %#v", method, params)
	}
	return response, nil
}

func (*transactionLookupTestNetwork) RetriableCall(
	context.Context, string, []any, bool,
) (map[string]any, error) {
	return nil, errors.New("unexpected header RPC")
}

func (*transactionLookupTestNetwork) Start(context.Context) error { return nil }
func (*transactionLookupTestNetwork) Stop(context.Context) error  { return nil }
func (*transactionLookupTestNetwork) RemoteHeight() int           { return 0 }
func (*transactionLookupTestNetwork) IsConnected() bool           { return true }
func (*transactionLookupTestNetwork) SetHeaderNotificationHandler(func(context.Context, any)) {
}
func (*transactionLookupTestNetwork) SetAddressNotificationHandler(func(context.Context, any)) {
}
func (*transactionLookupTestNetwork) SetConnectedHandler(func(context.Context)) {}
func (*transactionLookupTestNetwork) SubscribeAddresses(
	context.Context, []string,
) ([]any, error) {
	return nil, errors.New("unexpected address subscription")
}

func (network *transactionLookupTestNetwork) snapshotCalls() []transactionLookupTestCall {
	network.mu.Lock()
	defer network.mu.Unlock()
	calls := make([]transactionLookupTestCall, len(network.calls))
	for index, call := range network.calls {
		calls[index] = transactionLookupTestCall{
			method: call.method, params: append([]any(nil), call.params...), restricted: call.restricted,
		}
	}
	return calls
}

func (network *transactionLookupTestNetwork) snapshotRetriableCalls() []transactionLookupTestCall {
	network.mu.Lock()
	defer network.mu.Unlock()
	calls := make([]transactionLookupTestCall, len(network.retriableCalls))
	copy(calls, network.retriableCalls)
	return calls
}

type transactionLookupTestRPCError struct {
	code    int64
	message string
}

func (lookupErr transactionLookupTestRPCError) Error() string      { return lookupErr.message }
func (lookupErr transactionLookupTestRPCError) RPCCode() int64     { return lookupErr.code }
func (lookupErr transactionLookupTestRPCError) RPCMessage() string { return lookupErr.message }

func TestWalletManagerGetTransactionLocalHydratedHitSkipsNetwork(t *testing.T) {
	database, fixture := transactionHistoryOracleFixture(t)
	network := &transactionLookupTestNetwork{}
	ledger := &Ledger{Database: database, SPVNetwork: network}
	manager := transactionLookupTestManager(ledger)

	want := fixture["outgoing"]
	result, err := manager.GetTransaction(context.Background(), want.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Transaction == nil || result.Failure != nil || result.Ledger != ledger {
		t.Fatalf("local lookup result = %#v", result)
	}
	transaction := result.Transaction
	if transaction.ID != want.ID || transaction.Height != 0 || transaction.Position != 9 ||
		transaction.IsVerified || len(transaction.Inputs) != 1 || len(transaction.Outputs) != 1 {
		t.Fatalf("local hydrated transaction = %#v", transaction)
	}
	if transaction.Inputs[0].ResolvedOutput == nil ||
		transaction.Inputs[0].ResolvedOutput.Amount != 111 {
		t.Fatalf("local resolved input = %#v", transaction.Inputs[0].ResolvedOutput)
	}
	if transaction.Inputs[0].IsMyInput() != nil || transaction.Outputs[0].IsMyOutput != nil {
		t.Fatalf("local zero-option annotations = input %v output %v",
			transaction.Inputs[0].IsMyInput(), transaction.Outputs[0].IsMyOutput)
	}
	if calls := network.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("local hit made SPV calls: %#v", calls)
	}
	if calls := network.snapshotRetriableCalls(); len(calls) != 0 {
		t.Fatalf("local hit made retriable SPV calls: %#v", calls)
	}
}

func TestWalletManagerGetTransactionRemoteMempoolAndZeroAreNotPersisted(t *testing.T) {
	remote, rawHex := transactionLookupTestRemoteTransaction(t)
	for _, test := range []struct {
		name   string
		height json.Number
	}{
		{name: "zero", height: json.Number("0")},
		{name: "mempool", height: json.Number("-1")},
	} {
		t.Run(test.name, func(t *testing.T) {
			ledger := newTransactionOutputQueryLedger(t)
			network := &transactionLookupTestNetwork{responses: []any{
				[]any{rawHex, map[string]any{"block_height": test.height}},
			}}
			ledger.SPVNetwork = network
			manager := transactionLookupTestManager(ledger)
			requestedID := "requested-" + test.name

			result, err := manager.GetTransaction(context.Background(), requestedID)
			if err != nil {
				t.Fatal(err)
			}
			if result.Transaction == nil || result.Failure != nil || result.Ledger != ledger ||
				result.Transaction.ID != remote.ID || result.Transaction.Height != int64(testHeight(test.height)) ||
				result.Transaction.Position != -1 || result.Transaction.IsVerified {
				t.Fatalf("remote lookup result = %#v", result)
			}
			wantCalls := []transactionLookupTestCall{{
				method: SPVTransactionInfoMethod, params: []any{requestedID}, restricted: true,
			}}
			if calls := network.snapshotCalls(); !reflect.DeepEqual(calls, wantCalls) {
				t.Fatalf("remote calls = %#v, want %#v", calls, wantCalls)
			}
			if calls := network.snapshotRetriableCalls(); len(calls) != 0 {
				t.Fatalf("transaction info used retriable calls: %#v", calls)
			}
			stored, err := ledger.Database.GetTransaction(context.Background(), remote.ID)
			if err != nil || stored != nil {
				t.Fatalf("remote transaction was persisted: %#v, %v", stored, err)
			}
		})
	}
}

func TestWalletManagerGetTransactionRemoteMissIsNotCached(t *testing.T) {
	remote, rawHex := transactionLookupTestRemoteTransaction(t)
	ledger := newTransactionOutputQueryLedger(t)
	response := []any{rawHex, map[string]any{"block_height": json.Number("-1")}}
	network := &transactionLookupTestNetwork{responses: []any{response, response}}
	ledger.SPVNetwork = network
	manager := transactionLookupTestManager(ledger)

	for attempt := 0; attempt < 2; attempt++ {
		result, err := manager.GetTransaction(context.Background(), "same-requested-id")
		if err != nil || result.Transaction == nil || result.Transaction.ID != remote.ID {
			t.Fatalf("remote attempt %d = %#v, %v", attempt, result, err)
		}
	}
	calls := network.snapshotCalls()
	if len(calls) != 2 {
		t.Fatalf("repeated remote calls = %#v, want two", calls)
	}
	for _, call := range calls {
		if call.method != SPVTransactionInfoMethod || !call.restricted ||
			!reflect.DeepEqual(call.params, []any{"same-requested-id"}) {
			t.Fatalf("repeated remote call = %#v", call)
		}
	}
	if retriable := network.snapshotRetriableCalls(); len(retriable) != 0 {
		t.Fatalf("repeated transaction info used retriable calls: %#v", retriable)
	}
	stored, err := ledger.Database.GetTransaction(context.Background(), remote.ID)
	if err != nil || stored != nil {
		t.Fatalf("repeated remote transaction was persisted: %#v, %v", stored, err)
	}
}

func TestWalletManagerGetTransactionPreservesPrimitiveRemoteTXIDs(t *testing.T) {
	_, rawHex := transactionLookupTestRemoteTransaction(t)
	values := []any{nil, false, true, json.Number("7"), json.Number("1.25"), "text"}
	responses := make([]any, len(values))
	for index := range responses {
		responses[index] = []any{rawHex, map[string]any{"block_height": json.Number("-1")}}
	}
	ledger := newTransactionOutputQueryLedger(t)
	network := &transactionLookupTestNetwork{responses: responses}
	ledger.SPVNetwork = network
	manager := transactionLookupTestManager(ledger)

	for _, value := range values {
		result, err := manager.GetTransaction(context.Background(), value)
		if err != nil || result.Transaction == nil {
			t.Fatalf("primitive TXID %#v lookup = %#v, %v", value, result, err)
		}
	}
	calls := network.snapshotCalls()
	if len(calls) != len(values) {
		t.Fatalf("primitive TXID calls = %#v", calls)
	}
	for index, value := range values {
		if !reflect.DeepEqual(calls[index].params, []any{value}) {
			t.Fatalf("primitive TXID call %d params = %#v, want %#v", index, calls[index].params, []any{value})
		}
	}
}

func TestWalletManagerGetTransactionMatchesSQLiteArgumentFailures(t *testing.T) {
	tests := []struct {
		name    string
		value   any
		errName string
		message string
	}{
		{name: "list", value: []any{"txid"}, errName: "ProgrammingError", message: "Error binding parameter 1: type 'list' is not supported"},
		{name: "dict", value: map[string]any{"txid": true}, errName: "ProgrammingError", message: "Error binding parameter 1: type 'dict' is not supported"},
		{name: "large integer", value: json.Number("9223372036854775808"), errName: "OverflowError", message: "Python int too large to convert to SQLite INTEGER"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ledger := newTransactionOutputQueryLedger(t)
			network := &transactionLookupTestNetwork{}
			ledger.SPVNetwork = network
			result, err := transactionLookupTestManager(ledger).GetTransaction(
				context.Background(), test.value,
			)
			var argumentErr TransactionLookupArgumentError
			if result != (TransactionLookupResult{}) || !errors.As(err, &argumentErr) ||
				argumentErr.PythonErrorName() != test.errName || err.Error() != test.message {
				t.Fatalf("argument failure = %#v, %v", result, err)
			}
			if calls := network.snapshotCalls(); len(calls) != 0 {
				t.Fatalf("invalid SQLite argument reached hub: %#v", calls)
			}
		})
	}
}

func TestWalletManagerGetTransactionPreservesMissingRemoteHeight(t *testing.T) {
	remote, rawHex := transactionLookupTestRemoteTransaction(t)
	for _, merkle := range []map[string]any{{}, {"block_height": nil}} {
		ledger := newTransactionOutputQueryLedger(t)
		ledger.SPVNetwork = &transactionLookupTestNetwork{responses: []any{[]any{rawHex, merkle}}}
		result, err := transactionLookupTestManager(ledger).GetTransaction(
			context.Background(), "missing",
		)
		if err != nil || result.Transaction == nil || result.Transaction.ID != remote.ID ||
			!result.Transaction.HeightMissing || result.Transaction.Height != 0 {
			t.Fatalf("missing remote height result = %#v, %v", result, err)
		}
	}
}

func TestWalletManagerGetTransactionVerifiesConfirmedRemoteProof(t *testing.T) {
	remote, rawHex := transactionLookupTestRemoteTransaction(t)
	for _, test := range []struct {
		name       string
		merkleRoot string
		verified   bool
	}{
		{name: "match", merkleRoot: remote.ID, verified: true},
		{name: "mismatch", merkleRoot: strings.Repeat("00", 32), verified: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ledger := newTransactionOutputQueryLedger(t)
			ledger.Headers = newTransactionExecutionHeaders(
				t, strings.Repeat("11", 32), test.merkleRoot,
			)
			network := &transactionLookupTestNetwork{responses: []any{[]any{
				rawHex,
				map[string]any{
					"block_height": json.Number("1"), "merkle": []any{}, "pos": json.Number("0"),
				},
			}}}
			ledger.SPVNetwork = network
			result, err := transactionLookupTestManager(ledger).GetTransaction(
				context.Background(), "requested-id",
			)
			if err != nil || result.Transaction == nil || result.Transaction.Height != 1 ||
				result.Transaction.Position != 0 || result.Transaction.IsVerified != test.verified ||
				result.Transaction.HeightMissing {
				t.Fatalf("confirmed proof result = %#v, %v", result, err)
			}
			if calls := network.snapshotCalls(); len(calls) != 1 ||
				calls[0].method != SPVTransactionInfoMethod {
				t.Fatalf("confirmed proof calls = %#v", calls)
			}
			if calls := network.snapshotRetriableCalls(); len(calls) != 0 {
				t.Fatalf("confirmed inline proof used retry path: %#v", calls)
			}
		})
	}
}

func TestWalletManagerGetTransactionMapsCodedSPVFailures(t *testing.T) {
	for _, test := range []struct {
		name string
		spv  transactionLookupTestRPCError
		want TransactionLookupFailure
	}{
		{
			name: "not found substring",
			spv: transactionLookupTestRPCError{
				code: 1, message: "server says: No such mempool or blockchain transaction. retry later",
			},
			want: TransactionLookupFailure{Success: false, Code: 404, Message: "transaction not found"},
		},
		{
			name: "other coded error",
			spv:  transactionLookupTestRPCError{code: -32042, message: "hub rejected lookup"},
			want: TransactionLookupFailure{Success: false, Code: -32042, Message: "hub rejected lookup"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ledger := newTransactionOutputQueryLedger(t)
			network := &transactionLookupTestNetwork{errors: []error{test.spv}}
			ledger.SPVNetwork = network
			result, err := transactionLookupTestManager(ledger).GetTransaction(
				context.Background(), "missing",
			)
			if err != nil {
				t.Fatal(err)
			}
			if result.Transaction != nil || result.Failure == nil || result.Ledger != ledger ||
				*result.Failure != test.want {
				t.Fatalf("coded failure = %#v, want %#v", result, test.want)
			}
		})
	}

	transportErr := errors.New("transport failed")
	ledger := newTransactionOutputQueryLedger(t)
	ledger.SPVNetwork = &transactionLookupTestNetwork{errors: []error{transportErr}}
	result, err := transactionLookupTestManager(ledger).GetTransaction(context.Background(), "missing")
	if !errors.Is(err, transportErr) || result != (TransactionLookupResult{}) {
		t.Fatalf("transport failure = %#v, %v", result, err)
	}
}

func TestWalletManagerGetTransactionRejectsMalformedRemoteResults(t *testing.T) {
	_, validRaw := transactionLookupTestRemoteTransaction(t)
	tests := []struct {
		name    string
		value   any
		errName string
		message string
	}{
		{name: "null result", value: nil, errName: "TypeError", message: "cannot unpack non-iterable NoneType object"},
		{name: "result type", value: map[string]any{}, errName: "ValueError", message: "not enough values to unpack (expected 2, got 0)"},
		{name: "result length", value: []any{validRaw}, errName: "ValueError", message: "not enough values to unpack (expected 2, got 1)"},
		{name: "result too long", value: []any{validRaw, map[string]any{}, true}, errName: "ValueError", message: "too many values to unpack (expected 2)"},
		{name: "raw type", value: []any{true, map[string]any{}}, errName: "TypeError", message: "argument should be bytes, buffer or ASCII string, not 'bool'"},
		{name: "merkle type", value: []any{validRaw, nil}, errName: "AttributeError", message: "'NoneType' object has no attribute 'get'"},
		{name: "merkle list", value: []any{validRaw, []any{}}, errName: "AttributeError", message: "'list' object has no attribute 'get'"},
		{name: "raw odd", value: []any{"0", map[string]any{}}, errName: "Error", message: "Odd-length string"},
		{name: "raw hexadecimal", value: []any{"zz", map[string]any{}}, errName: "Error", message: "Non-hexadecimal digit found"},
		{name: "raw transaction", value: []any{"00", map[string]any{}}, errName: "error", message: "unpack requires a buffer of 4 bytes"},
		{name: "raw empty", value: []any{"", map[string]any{}}, errName: "TypeError", message: "'<' not supported between instances of 'NoneType' and 'int'"},
		{name: "height string", value: []any{validRaw, map[string]any{"block_height": "one"}}},
		{name: "height fraction", value: []any{validRaw, map[string]any{"block_height": json.Number("1.5")}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ledger := newTransactionOutputQueryLedger(t)
			ledger.SPVNetwork = &transactionLookupTestNetwork{responses: []any{test.value}}
			result, err := transactionLookupTestManager(ledger).GetTransaction(
				context.Background(), "missing",
			)
			if result != (TransactionLookupResult{}) || !errors.Is(err, ErrTransactionInfoResult) {
				t.Fatalf("malformed result = %#v, %v", result, err)
			}
			if test.errName != "" {
				var compatibilityErr TransactionInfoCompatibilityError
				if !errors.As(err, &compatibilityErr) ||
					compatibilityErr.PythonErrorName() != test.errName || err.Error() != test.message {
					t.Fatalf("malformed compatibility error = %T %v", err, err)
				}
			}
		})
	}
}

func TestWalletManagerGetTransactionCancellationAndUnavailableBoundaries(t *testing.T) {
	t.Run("remote cancellation", func(t *testing.T) {
		ledger := newTransactionOutputQueryLedger(t)
		network := &transactionLookupTestNetwork{
			block: true, started: make(chan struct{}, 1),
		}
		ledger.SPVNetwork = network
		manager := transactionLookupTestManager(ledger)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		type lookupOutcome struct {
			result TransactionLookupResult
			err    error
		}
		outcome := make(chan lookupOutcome, 1)
		go func() {
			result, err := manager.GetTransaction(ctx, "missing")
			outcome <- lookupOutcome{result: result, err: err}
		}()
		select {
		case <-network.started:
			cancel()
		case <-time.After(2 * time.Second):
			t.Fatal("transaction info RPC did not start")
		}
		select {
		case got := <-outcome:
			if got.result != (TransactionLookupResult{}) || !errors.Is(got.err, context.Canceled) {
				t.Fatalf("canceled lookup = %#v, %v", got.result, got.err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("canceled transaction lookup did not return")
		}
	})

	t.Run("missing default ledger", func(t *testing.T) {
		var manager *WalletManager
		result, err := manager.GetTransaction(context.Background(), "missing")
		if result != (TransactionLookupResult{}) || !errors.Is(err, ErrTransactionLookupUnavailable) {
			t.Fatalf("nil manager lookup = %#v, %v", result, err)
		}
	})

	t.Run("missing database", func(t *testing.T) {
		result, err := transactionLookupTestManager(&Ledger{}).GetTransaction(
			context.Background(), "missing",
		)
		if result != (TransactionLookupResult{}) || !errors.Is(err, ErrTransactionLookupUnavailable) {
			t.Fatalf("database-less lookup = %#v, %v", result, err)
		}
	})

	t.Run("unopened database", func(t *testing.T) {
		ledger := &Ledger{Database: ledgerdb.New(":memory:")}
		result, err := transactionLookupTestManager(ledger).GetTransaction(
			context.Background(), "missing",
		)
		if result != (TransactionLookupResult{}) || !errors.Is(err, ledgerdb.ErrNotOpen) {
			t.Fatalf("unopened database lookup = %#v, %v", result, err)
		}
	})

	t.Run("missing SPV transaction info", func(t *testing.T) {
		ledger := newTransactionOutputQueryLedger(t)
		result, err := transactionLookupTestManager(ledger).GetTransaction(
			context.Background(), "missing",
		)
		if result != (TransactionLookupResult{}) || !errors.Is(err, ErrTransactionLookupUnavailable) {
			t.Fatalf("SPV-less lookup = %#v, %v", result, err)
		}
	})
}

func transactionLookupTestManager(ledger *Ledger) *WalletManager {
	account := &Account{ID: "default", ledger: ledger}
	return &WalletManager{Wallets: []*Wallet{
		NewWallet(WithWalletAccounts([]*Account{account})),
	}}
}

func transactionLookupTestRemoteTransaction(t *testing.T) (*Transaction, string) {
	t.Helper()
	transaction := transactionHistoryUnitCoinbase(
		t, 7_001, NewPayPubKeyHashOutput(25_000_000, bytes.Repeat([]byte{0x41}, 20)),
	)
	return transaction, hex.EncodeToString(transaction.Raw)
}

func testHeight(value json.Number) int {
	height, _ := value.Int64()
	return int(height)
}
