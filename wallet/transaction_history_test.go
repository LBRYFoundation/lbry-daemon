package wallet

import (
	"bytes"
	"context"
	"errors"
	"math"
	"testing"

	"lbry/daemon/wallet/ledgerdb"
)

func TestTransactionHistoryAccountWrappersUseAccountScopeAndWholeWalletAnnotations(t *testing.T) {
	ctx := context.Background()
	database, fixture := transactionHistoryOracleFixture(t)
	ledger := &Ledger{Database: database}
	accountA := &Account{ID: "account-a", ledger: ledger}
	accountB := &Account{ID: "account-b", ledger: ledger}
	NewWallet(WithWalletAccounts([]*Account{accountA, accountB}))

	transactions, err := accountA.GetTransactions(ctx, TransactionListOptions{IncludeIsMyOutput: true})
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{
		"outgoing", "incoming", "parent-purchase", "internal",
		"parent-spent", "parent-internal", "missing-reference", "purchase",
	}
	if got := transactionHistoryUnitNames(transactions, fixture); !equalStrings(got, wantOrder) {
		t.Fatalf("account A transaction order = %v, want %v", got, wantOrder)
	}

	// The direct txid bypasses account visibility, while annotations still use
	// every account in accountA's wallet. The foreign output belongs to B.
	foreignID := fixture["foreign"].ID
	foreign, err := accountA.GetTransactions(ctx, TransactionListOptions{
		Query: ledgerdb.TransactionQuery{TXID: &foreignID}, IncludeIsMyOutput: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(foreign) != 1 || foreign[0].Outputs[0].IsMyOutput == nil ||
		!*foreign[0].Outputs[0].IsMyOutput {
		t.Fatalf("whole-wallet annotation through account A = %#v", foreign)
	}

	zero, farOffset := 0, 999
	countA, err := accountA.CountTransactions(ctx, ledgerdb.TransactionQuery{
		Limit: &zero, Offset: &farOffset, Order: ledgerdb.TransactionOrder(255),
	})
	if err != nil || countA != int64(len(wantOrder)) {
		t.Fatalf("account A count = %d, %v", countA, err)
	}
	countB, err := accountB.CountTransactions(nil, ledgerdb.TransactionQuery{})
	if err != nil || countB != 1 {
		t.Fatalf("account B count = %d, %v, want 1", countB, err)
	}
}

func TestTransactionHistoryHydratesRawAndNullableAnnotations(t *testing.T) {
	ctx := context.Background()
	database, fixture := transactionHistoryOracleFixture(t)
	ledger := &Ledger{Database: database}
	options := TransactionListOptions{
		Query:                ledgerdb.TransactionQuery{AccountIDs: []string{"account-a"}},
		AnnotationAccountIDs: []string{"account-a"},
		IncludeIsSpent:       true,
		IncludeIsMyInput:     true,
		IncludeIsMyOutput:    true,
	}
	transactions, err := ledger.GetTransactions(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	byName := transactionHistoryUnitByName(t, transactions, fixture)

	incoming := byName["incoming"]
	if incoming.Height != 12 || incoming.Position != 4 || !incoming.IsVerified ||
		incoming.JulianDay != nil || incoming.Outputs[0].Amount != 404 ||
		bytes.Equal(incoming.Outputs[0].Script.Source, []byte{0x51}) {
		t.Fatalf("incoming raw hydration/metadata = %#v / %#v", incoming, incoming.Outputs[0])
	}

	parentSpent := byName["parent-spent"]
	assertTransactionHistoryBool(t, "spent parent is spent", parentSpent.Outputs[0].IsSpent, true)
	internal := byName["internal"]
	assertTransactionHistoryBool(t, "internal is my input", internal.Outputs[0].IsMyInput, true)
	assertTransactionHistoryBool(t, "internal is my output", internal.Outputs[0].IsMyOutput, true)
	assertTransactionHistoryBool(t, "internal transfer", internal.Outputs[0].IsInternalTransfer, true)
	outgoing := byName["outgoing"]
	assertTransactionHistoryBool(t, "outgoing is my input", outgoing.Outputs[0].IsMyInput, true)
	assertTransactionHistoryBool(t, "outgoing is not my output", outgoing.Outputs[0].IsMyOutput, false)
	assertTransactionHistoryBool(t, "outgoing is not internal", outgoing.Outputs[0].IsInternalTransfer, false)
	assertTransactionHistoryBool(t, "resolved outgoing input", outgoing.Inputs[0].IsMyInput(), true)
	if outgoing.Inputs[0].ResolvedOutput == nil || outgoing.Inputs[0].ResolvedOutput.Amount != 111 {
		t.Fatalf("resolved outgoing input = %#v", outgoing.Inputs[0].ResolvedOutput)
	}

	missing := byName["missing-reference"]
	if missing.Inputs[0].ResolvedOutput != nil {
		t.Fatalf("missing input unexpectedly resolved = %#v", missing.Inputs[0].ResolvedOutput)
	}
	assertTransactionHistoryBool(t, "unresolved input ownership", missing.Inputs[0].IsMyInput(), false)

	purchase := byName["purchase"]
	if purchase.Outputs[0].Purchase != &purchase.Outputs[1] {
		t.Fatalf("purchase link = %p, want output 1 %p", purchase.Outputs[0].Purchase, &purchase.Outputs[1])
	}
	assertTransactionHistoryBool(t, "unstored purchase data is not mine", purchase.Outputs[1].IsMyOutput, false)
	if purchase.Outputs[1].IsSpent != nil || purchase.Outputs[1].IsMyInput != nil ||
		purchase.Outputs[1].IsInternalTransfer != nil {
		t.Fatalf("unstored purchase-data annotations = %#v", purchase.Outputs[1])
	}

	// A resolved input inherits nil when ownership annotations were not requested.
	outgoingID := fixture["outgoing"].ID
	plain, err := ledger.GetTransactions(ctx, TransactionListOptions{
		Query: ledgerdb.TransactionQuery{TXID: &outgoingID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) != 1 || plain[0].Inputs[0].ResolvedOutput == nil ||
		plain[0].Inputs[0].IsMyInput() != nil || plain[0].Outputs[0].IsMyOutput != nil {
		t.Fatalf("unannotated resolved transaction = %#v", plain)
	}
}

func TestTransactionHistoryPurchaseLinkRequiresDataAtOutputOne(t *testing.T) {
	ctx := context.Background()
	ledger := newTransactionOutputQueryLedger(t)
	purchaseData, err := NewPurchaseDataOutput("00112233445566778899aabbccddeeff00112233")
	if err != nil {
		t.Fatal(err)
	}
	late := transactionHistoryUnitCoinbase(t, 700,
		NewPayPubKeyHashOutput(10, bytes.Repeat([]byte{0x41}, 20)),
		NewReturnDataOutput([]byte("ordinary return data")),
		purchaseData,
	)
	if err := ledger.Database.SaveTransactionIOBatch(ctx, []ledgerdb.TransactionIORow{{
		Transaction: ledgerdb.TransactionRow{
			TXID: late.ID, Raw: append([]byte(nil), late.Raw...), Height: 6, Position: 7,
		},
	}}, "", ""); err != nil {
		t.Fatal(err)
	}
	lateID := late.ID
	transactions, err := ledger.GetTransactions(ctx, TransactionListOptions{
		Query: ledgerdb.TransactionQuery{TXID: &lateID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(transactions) != 1 || transactions[0].Outputs[0].Purchase != nil {
		t.Fatalf("late purchase data linked unexpectedly = %#v", transactions)
	}
	for index := range transactions[0].Outputs {
		assertTransactionHistoryBool(t, "unstored output sentinel", transactions[0].Outputs[index].IsMyOutput, false)
	}
}

func TestTransactionHistoryStoredRawMismatchAndCorruption(t *testing.T) {
	ctx := context.Background()

	t.Run("stored txid does not replace raw identity", func(t *testing.T) {
		ledger := newTransactionOutputQueryLedger(t)
		transaction := transactionHistoryUnitCoinbase(t, 800,
			NewPayPubKeyHashOutput(81, bytes.Repeat([]byte{0x51}, 20)),
		)
		alias := "stored-alias"
		if err := ledger.Database.SaveTransactionIOBatch(ctx, []ledgerdb.TransactionIORow{{
			Transaction: ledgerdb.TransactionRow{
				TXID: alias, Raw: append([]byte(nil), transaction.Raw...),
				Height: 23, Position: 9, IsVerified: true,
			},
			Outputs: []ledgerdb.TransactionOutputRow{{
				TXOID: alias + ":0", Position: 0, Amount: 9_999, Script: []byte{0x51},
			}},
		}}, "", ""); err != nil {
			t.Fatal(err)
		}
		transactions, err := ledger.GetTransactions(ctx, TransactionListOptions{
			Query: ledgerdb.TransactionQuery{TXID: &alias},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(transactions) != 1 || transactions[0].ID != transaction.ID ||
			transactions[0].ID == alias || transactions[0].Height != 23 ||
			transactions[0].Position != 9 || !transactions[0].IsVerified ||
			transactions[0].Outputs[0].Amount != 81 {
			t.Fatalf("stored/raw mismatch hydration = %#v", transactions)
		}
	})

	t.Run("corrupt selected raw", func(t *testing.T) {
		ledger := newTransactionOutputQueryLedger(t)
		corruptID := "corrupt"
		if err := ledger.Database.SaveTransactionIOBatch(ctx, []ledgerdb.TransactionIORow{{
			Transaction: ledgerdb.TransactionRow{TXID: corruptID, Raw: []byte{1}},
		}}, "", ""); err != nil {
			t.Fatal(err)
		}
		transactions, err := ledger.GetTransactions(ctx, TransactionListOptions{
			Query: ledgerdb.TransactionQuery{TXID: &corruptID},
		})
		if transactions != nil || !errors.Is(err, ErrInvalidStoredTransaction) {
			t.Fatalf("corrupt raw = %#v, %v", transactions, err)
		}
	})

	t.Run("stored output position past raw outputs", func(t *testing.T) {
		ledger := newTransactionOutputQueryLedger(t)
		transaction := transactionHistoryUnitCoinbase(t, 801,
			NewPayPubKeyHashOutput(82, bytes.Repeat([]byte{0x52}, 20)),
		)
		if err := ledger.Database.SaveTransactionIOBatch(ctx, []ledgerdb.TransactionIORow{{
			Transaction: ledgerdb.TransactionRow{
				TXID: transaction.ID, Raw: append([]byte(nil), transaction.Raw...),
			},
			Outputs: []ledgerdb.TransactionOutputRow{{
				TXOID: transaction.ID + ":invalid", Position: 2, Amount: 82, Script: []byte{0x51},
			}},
		}}, "", ""); err != nil {
			t.Fatal(err)
		}
		transactionID := transaction.ID
		transactions, err := ledger.GetTransactions(ctx, TransactionListOptions{
			Query: ledgerdb.TransactionQuery{TXID: &transactionID},
		})
		if transactions != nil || !errors.Is(err, ErrTransactionOutputOutOfRange) {
			t.Fatalf("out-of-range output = %#v, %v", transactions, err)
		}
	})
}

func TestTransactionHistoryHydrationBatchesAtPinnedVariableBoundary(t *testing.T) {
	ctx := context.Background()
	ledger := newTransactionOutputQueryLedger(t)
	const transactionCount = TransactionQueryBatchSize + 1
	if TransactionQueryBatchSize != 900 {
		t.Fatalf("transaction query batch size = %d, want 900", TransactionQueryBatchSize)
	}
	accountID, address := "batch-account", "batch-address"
	if err := ledger.Database.AddKeys(ctx, accountID, []ledgerdb.AddressKey{{
		Address: address, PublicKey: []byte{1}, ChainCode: []byte{2},
	}}); err != nil {
		t.Fatal(err)
	}
	rows := make([]ledgerdb.TransactionIORow, transactionCount)
	for index := range rows {
		transaction := transactionHistoryUnitCoinbase(t, uint32(1_000+index),
			NewPayPubKeyHashOutput(uint64(index+1), bytes.Repeat([]byte{byte(index)}, 20)),
		)
		rows[index] = ledgerdb.TransactionIORow{
			Transaction: ledgerdb.TransactionRow{
				TXID: transaction.ID, Raw: append([]byte(nil), transaction.Raw...),
				Height: int64(index + 1), Position: int64(index), IsVerified: true,
			},
			Outputs: []ledgerdb.TransactionOutputRow{{
				TXOID: transaction.Outputs[0].ID(), Address: &address, Position: 0,
				Amount: int64(index + 1), Script: append([]byte(nil), transaction.Outputs[0].Script.Source...),
			}},
		}
	}
	if err := ledger.Database.SaveTransactionIOBatch(ctx, rows, address, ""); err != nil {
		t.Fatal(err)
	}
	transactions, err := ledger.GetTransactions(ctx, TransactionListOptions{
		Query:                ledgerdb.TransactionQuery{AccountIDs: []string{accountID}},
		AnnotationAccountIDs: []string{accountID},
		IncludeIsMyOutput:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(transactions) != transactionCount || transactions[0].Height != transactionCount ||
		transactions[len(transactions)-1].Height != 1 {
		t.Fatalf("batched history size/order = %d, first %d, last %d",
			len(transactions), transactions[0].Height, transactions[len(transactions)-1].Height)
	}
	for _, index := range []int{0, TransactionQueryBatchSize - 1, TransactionQueryBatchSize} {
		assertTransactionHistoryBool(t, "batched output ownership", transactions[index].Outputs[0].IsMyOutput, true)
	}
}

func TestTransactionHistoryUnavailableBoundaries(t *testing.T) {
	ctx := context.Background()
	query := ledgerdb.TransactionQuery{TXIDs: []string{}}
	var nilLedger *Ledger
	if transactions, err := nilLedger.GetTransactions(ctx, TransactionListOptions{Query: query}); transactions != nil || !errors.Is(err, ErrTransactionQueryUnavailable) {
		t.Fatalf("nil ledger GetTransactions = %#v, %v", transactions, err)
	}
	if _, err := nilLedger.CountTransactions(ctx, query); !errors.Is(err, ErrTransactionQueryUnavailable) {
		t.Fatalf("nil ledger CountTransactions = %v", err)
	}
	if _, err := (&Ledger{}).GetTransactions(ctx, TransactionListOptions{Query: query}); !errors.Is(err, ErrTransactionQueryUnavailable) {
		t.Fatalf("database-less ledger = %v", err)
	}
	unopened := &Ledger{Database: ledgerdb.New(":memory:")}
	if _, err := unopened.GetTransactions(ctx, TransactionListOptions{Query: query}); !errors.Is(err, ledgerdb.ErrNotOpen) {
		t.Fatalf("unopened GetTransactions = %v", err)
	}
	if _, err := unopened.CountTransactions(ctx, query); !errors.Is(err, ledgerdb.ErrNotOpen) {
		t.Fatalf("unopened CountTransactions = %v", err)
	}

	var nilAccount *Account
	if _, err := nilAccount.GetTransactions(ctx, TransactionListOptions{}); !errors.Is(err, ErrTransactionOutputQueryUnavailable) {
		t.Fatalf("nil account GetTransactions = %v", err)
	}
	if _, err := (&Account{}).CountTransactions(ctx, ledgerdb.TransactionQuery{}); !errors.Is(err, ErrTransactionOutputQueryUnavailable) {
		t.Fatalf("detached account CountTransactions = %v", err)
	}

	t.Run("ownership annotations need wallet accounts", func(t *testing.T) {
		database, _ := transactionHistoryOracleFixture(t)
		ledger := &Ledger{Database: database}
		account := &Account{ID: "account-a", ledger: ledger}
		if _, err := account.GetTransactions(ctx, TransactionListOptions{IncludeIsMyOutput: true}); !errors.Is(err, ledgerdb.ErrOutputAnnotationAccountsRequired) {
			t.Fatalf("account without wallet annotations = %v", err)
		}
		NewWallet(WithWalletAccounts([]*Account{account, nil}))
		if _, err := account.GetTransactions(ctx, TransactionListOptions{}); !errors.Is(err, ErrNilWalletAccount) {
			t.Fatalf("nil wallet account = %v", err)
		}
	})
}

func transactionHistoryUnitCoinbase(
	t *testing.T, nonce uint32, outputs ...TransactionOutput,
) *Transaction {
	t.Helper()
	transaction := NewTransaction()
	transaction.LockTime = nonce
	transaction.AddInputs([]TransactionInput{{
		PreviousIndex: math.MaxUint32, Sequence: math.MaxUint32,
		Coinbase: []byte{byte(nonce), byte(nonce >> 8), byte(nonce >> 16), byte(nonce >> 24)},
	}})
	transaction.AddOutputs(outputs)
	if err := transaction.RebuildDerived(); err != nil {
		t.Fatal(err)
	}
	return transaction
}

func transactionHistoryUnitByName(
	t *testing.T, transactions []*Transaction, fixture map[string]*Transaction,
) map[string]*Transaction {
	t.Helper()
	namesByID := make(map[string]string, len(fixture))
	for name, transaction := range fixture {
		namesByID[transaction.ID] = name
	}
	result := make(map[string]*Transaction, len(transactions))
	for _, transaction := range transactions {
		name, ok := namesByID[transaction.ID]
		if !ok {
			t.Fatalf("unknown hydrated transaction %s", transaction.ID)
		}
		result[name] = transaction
	}
	return result
}

func transactionHistoryUnitNames(
	transactions []*Transaction, fixture map[string]*Transaction,
) []string {
	namesByID := make(map[string]string, len(fixture))
	for name, transaction := range fixture {
		namesByID[transaction.ID] = name
	}
	names := make([]string, len(transactions))
	for index, transaction := range transactions {
		names[index] = namesByID[transaction.ID]
	}
	return names
}

func assertTransactionHistoryBool(t *testing.T, name string, got *bool, want bool) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("%s = %v, want %t", name, got, want)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
