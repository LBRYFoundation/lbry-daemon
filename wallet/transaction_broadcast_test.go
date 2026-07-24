package wallet

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/ledgerdb"
)

func TestBroadcastTransactionUsesExactRawHexAndPreservesScalarAndTransaction(t *testing.T) {
	network := &transactionBroadcastTestNetwork{returnValue: "server-scalar"}
	transaction := &Transaction{
		Raw:      []byte{0x00, 0xab, 0x01, 0xff},
		ID:       "unchanged-id",
		Height:   42,
		Position: 7,
	}
	wantTransaction := *transaction
	wantTransaction.Raw = append([]byte(nil), transaction.Raw...)

	result, err := (&Ledger{SPVNetwork: network}).BroadcastTransaction(
		context.Background(), transaction,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result != "server-scalar" {
		t.Fatalf("broadcast result = %#v, want exact scalar %q", result, "server-scalar")
	}
	if calls := network.snapshotCalls(); !reflect.DeepEqual(calls, []string{"00ab01ff"}) {
		t.Fatalf("broadcast raw arguments = %v, want lowercase raw hex", calls)
	}
	if !reflect.DeepEqual(*transaction, wantTransaction) {
		t.Fatalf("broadcast mutated transaction:\n got  %#v\n want %#v", *transaction, wantTransaction)
	}
}

func TestBroadcastTransactionLazilyRebuildsNilRawAndUsesExactEncoding(t *testing.T) {
	network := &transactionBroadcastTestNetwork{returnValue: "rebuilt-scalar"}
	transaction := &Transaction{
		Version:  1,
		Height:   -2,
		Position: -1,
	}

	result, err := (&Ledger{SPVNetwork: network}).BroadcastTransaction(
		context.Background(), transaction,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result != "rebuilt-scalar" {
		t.Fatalf("broadcast result = %#v, want exact scalar %q", result, "rebuilt-scalar")
	}
	const wantRawHex = "01000000000000000000"
	if calls := network.snapshotCalls(); !reflect.DeepEqual(calls, []string{wantRawHex}) {
		t.Fatalf("broadcast raw arguments = %v, want lazy encoding %q", calls, wantRawHex)
	}
	wantRaw := []byte{0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	if !reflect.DeepEqual(transaction.Raw, wantRaw) ||
		!reflect.DeepEqual(transaction.RawSansSegWit, wantRaw) || transaction.ID == "" {
		t.Fatalf(
			"lazy derived fields = raw %x, sans-segwit %x, id %q",
			transaction.Raw, transaction.RawSansSegWit, transaction.ID,
		)
	}
}

func TestBroadcastTransactionRejectsNilAndMissingCapability(t *testing.T) {
	transaction := &Transaction{Raw: []byte{0x00}}
	var typedNilNetwork *transactionBroadcastTestNetwork
	tests := []struct {
		name   string
		ledger *Ledger
		tx     *Transaction
	}{
		{name: "nil ledger", ledger: nil, tx: transaction},
		{name: "nil transaction", ledger: &Ledger{}, tx: nil},
		{name: "nil SPV network", ledger: &Ledger{}, tx: transaction},
		{
			name:   "header-only SPV network",
			ledger: &Ledger{SPVNetwork: &transactionBroadcastHeaderOnlyNetwork{}},
			tx:     transaction,
		},
		{
			name:   "typed-nil broadcaster",
			ledger: &Ledger{SPVNetwork: typedNilNetwork},
			tx:     transaction,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := test.ledger.BroadcastTransaction(context.Background(), test.tx)
			if result != nil || !errors.Is(err, ErrTransactionBroadcastUnavailable) {
				t.Fatalf("broadcast result = %#v, %v, want unavailable error", result, err)
			}
		})
	}
}

func TestBroadcastOrReleaseFailureReleasesReservedResolvedInputs(t *testing.T) {
	ledger, transaction, outputID := newTransactionBroadcastFixture(t, false)
	broadcastErr := errors.New("hub rejected transaction")
	ledger.SPVNetwork = &transactionBroadcastTestNetwork{returnErr: broadcastErr}

	err := ledger.BroadcastOrRelease(context.Background(), transaction, false)
	if !errors.Is(err, broadcastErr) {
		t.Fatalf("broadcast-or-release error = %v, want %v", err, broadcastErr)
	}
	assertTransactionBroadcastReservation(t, ledger.Database, outputID, false)
}

func TestBroadcastOrReleaseCanceledBroadcastUsesUncanceledCleanup(t *testing.T) {
	ledger, transaction, outputID := newTransactionBroadcastFixture(t, false)
	network := &transactionBroadcastTestNetwork{
		broadcast: func(ctx context.Context, _ string) (any, error) {
			if !errors.Is(ctx.Err(), context.Canceled) {
				return nil, errors.New("broadcast context was not canceled")
			}
			return nil, ctx.Err()
		},
	}
	ledger.SPVNetwork = network
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := ledger.BroadcastOrRelease(ctx, transaction, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled broadcast error = %v, want context cancellation", err)
	}
	assertTransactionBroadcastReservation(t, ledger.Database, outputID, false)
}

func TestBroadcastOrReleaseReleaseErrorMasksBroadcastError(t *testing.T) {
	ledger, transaction, _ := newTransactionBroadcastFixture(t, false)
	broadcastErr := errors.New("hub rejected transaction")
	ledger.SPVNetwork = &transactionBroadcastTestNetwork{returnErr: broadcastErr}
	if err := ledger.Database.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	err := ledger.BroadcastOrRelease(context.Background(), transaction, false)
	if !errors.Is(err, ledgerdb.ErrNotOpen) {
		t.Fatalf("broadcast-or-release error = %v, want release error %v", err, ledgerdb.ErrNotOpen)
	}
	if errors.Is(err, broadcastErr) || strings.Contains(err.Error(), broadcastErr.Error()) {
		t.Fatalf("release error did not mask broadcast error: %v", err)
	}
}

func TestBroadcastOrReleaseSuccessKeepsInputReservation(t *testing.T) {
	ledger, transaction, outputID := newTransactionBroadcastFixture(t, false)
	network := &transactionBroadcastTestNetwork{returnValue: "accepted"}
	ledger.SPVNetwork = network

	if err := ledger.BroadcastOrRelease(context.Background(), transaction, false); err != nil {
		t.Fatal(err)
	}
	assertTransactionBroadcastReservation(t, ledger.Database, outputID, true)
	if len(network.snapshotCalls()) != 1 {
		t.Fatalf("broadcast calls = %d, want 1", len(network.snapshotCalls()))
	}
}

func TestBroadcastOrReleaseBlockingWaitFailureKeepsInputReservation(t *testing.T) {
	ledger, transaction, outputID := newTransactionBroadcastFixture(t, false)
	ledger.SPVNetwork = &transactionBroadcastTestNetwork{returnValue: "accepted"}

	err := ledger.BroadcastOrRelease(context.Background(), transaction, true)
	if !errors.Is(err, ErrTransactionWaitNoRecords) {
		t.Fatalf("blocking wait error = %v, want %v", err, ErrTransactionWaitNoRecords)
	}
	assertTransactionBroadcastReservation(t, ledger.Database, outputID, true)
}

func TestBroadcastOrReleaseBlockingWaitCancellationKeepsInputReservation(t *testing.T) {
	ledger, transaction, outputID := newTransactionBroadcastFixture(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	ledger.SPVNetwork = &transactionBroadcastTestNetwork{
		broadcast: func(ctx context.Context, _ string) (any, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			cancel()
			return "accepted", nil
		},
	}

	err := ledger.BroadcastOrRelease(ctx, transaction, true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("blocking wait error = %v, want context cancellation", err)
	}
	assertTransactionBroadcastReservation(t, ledger.Database, outputID, true)
}

type transactionBroadcastHeaderOnlyNetwork struct{}

func (*transactionBroadcastHeaderOnlyNetwork) Start(context.Context) error { return nil }
func (*transactionBroadcastHeaderOnlyNetwork) Stop(context.Context) error  { return nil }
func (*transactionBroadcastHeaderOnlyNetwork) RemoteHeight() int           { return 0 }
func (*transactionBroadcastHeaderOnlyNetwork) RetriableCall(
	context.Context, string, []any, bool,
) (map[string]any, error) {
	return nil, nil
}

type transactionBroadcastTestNetwork struct {
	transactionBroadcastHeaderOnlyNetwork

	mu          sync.Mutex
	calls       []string
	returnValue any
	returnErr   error
	broadcast   func(context.Context, string) (any, error)
}

func (network *transactionBroadcastTestNetwork) BroadcastTransaction(
	ctx context.Context, rawTransaction string,
) (any, error) {
	network.mu.Lock()
	network.calls = append(network.calls, rawTransaction)
	broadcast := network.broadcast
	result, err := network.returnValue, network.returnErr
	network.mu.Unlock()
	if broadcast != nil {
		return broadcast(ctx, rawTransaction)
	}
	return result, err
}

func (network *transactionBroadcastTestNetwork) snapshotCalls() []string {
	network.mu.Lock()
	defer network.mu.Unlock()
	return append([]string(nil), network.calls...)
}

func newTransactionBroadcastFixture(
	t *testing.T, ownedAddress bool,
) (*Ledger, *Transaction, string) {
	t.Helper()
	ctx := context.Background()
	database, err := ledgerdb.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(context.Background()); err != nil {
			t.Errorf("close transaction broadcast database: %v", err)
		}
	})
	ledger := &Ledger{Network: keys.MainNet, Database: database}

	pubKeyHash := []byte{
		0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09,
		0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13,
	}
	parent := NewTransaction()
	parent.AddInputs([]TransactionInput{{
		PreviousIndex: ^uint32(0),
		Sequence:      ^uint32(0),
		Coinbase:      []byte{0x01},
	}})
	parent.AddOutputs([]TransactionOutput{NewPayPubKeyHashOutput(100_000, pubKeyHash)})
	parent.Height = 1
	parent.Position = 2
	parent.IsVerified = true
	if err := parent.RebuildDerived(); err != nil {
		t.Fatal(err)
	}
	address, err := parent.Outputs[0].Address(keys.MainNet)
	if err != nil {
		t.Fatal(err)
	}
	if ownedAddress {
		if err := database.AddKeys(ctx, "account", []ledgerdb.AddressKey{{
			Address: address,
			Chain:   0,
			N:       0,
			Depth:   0,
		}}); err != nil {
			t.Fatal(err)
		}
	}
	outputAddress := address
	if err := database.SaveTransactionIOBatch(ctx, []ledgerdb.TransactionIORow{{
		Transaction: ledgerdb.TransactionRow{
			TXID:       parent.ID,
			Raw:        append([]byte(nil), parent.Raw...),
			Height:     parent.Height,
			Position:   parent.Position,
			IsVerified: parent.IsVerified,
		},
		Outputs: []ledgerdb.TransactionOutputRow{{
			TXOID:    parent.Outputs[0].ID(),
			Address:  &outputAddress,
			Position: 0,
			Amount:   int64(parent.Outputs[0].Amount),
			Script:   append([]byte(nil), parent.Outputs[0].Script.Source...),
		}},
	}}, address, ""); err != nil {
		t.Fatal(err)
	}
	if err := ledger.ReserveTransactionOutputs(
		ctx, []*TransactionOutput{&parent.Outputs[0]}, true,
	); err != nil {
		t.Fatal(err)
	}
	input, err := NewSpendInput(&parent.Outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	transaction := NewTransaction()
	transaction.AddInputs([]TransactionInput{input})
	if err := transaction.RebuildDerived(); err != nil {
		t.Fatal(err)
	}
	outputID := parent.Outputs[0].ID()
	assertTransactionBroadcastReservation(t, database, outputID, true)
	return ledger, transaction, outputID
}

func assertTransactionBroadcastReservation(
	t *testing.T, database *ledgerdb.DB, outputID string, want bool,
) {
	t.Helper()
	outputs, err := database.GetOutputsByID(context.Background(), []string{outputID})
	if err != nil {
		t.Fatal(err)
	}
	output, exists := outputs[outputID]
	if !exists || output.IsReserved != want {
		t.Fatalf("output %s = %#v, want reserved %v", outputID, output, want)
	}
}

var _ LedgerSPVNetwork = (*transactionBroadcastHeaderOnlyNetwork)(nil)
var _ LedgerSPVNetwork = (*transactionBroadcastTestNetwork)(nil)
var _ LedgerSPVTransactionBroadcaster = (*transactionBroadcastTestNetwork)(nil)
