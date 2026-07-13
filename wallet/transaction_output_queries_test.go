package wallet

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/ledgerdb"
)

func TestTransactionOutputQueriesHydrateRawAndGroupParentTransactions(t *testing.T) {
	ctx := context.Background()
	ledger := newTransactionOutputQueryLedger(t)
	transaction := transactionOutputQueryTransaction(1, 11, 22)
	transaction.Height = 7
	transaction.Position = 3
	transaction.IsVerified = true

	address := "hydrated-address"
	wrongScript := NewPayPubKeyHashOutput(0, bytes.Repeat([]byte{0xee}, 20)).Script.Source
	if err := ledger.Database.SaveTransactionIOBatch(ctx, []ledgerdb.TransactionIORow{{
		Transaction: ledgerdb.TransactionRow{
			TXID: transaction.ID, Raw: append([]byte(nil), transaction.Raw...),
			Height: transaction.Height, Position: transaction.Position, IsVerified: true,
		},
		Outputs: []ledgerdb.TransactionOutputRow{{
			TXOID: transaction.Outputs[0].ID(), Address: &address, Position: 0,
			Amount: 1_001, Script: wrongScript, TXOType: TransactionOutputTypeOther,
		}, {
			TXOID: transaction.Outputs[1].ID(), Address: &address, Position: 1,
			Amount: 1_002, Script: wrongScript, TXOType: TransactionOutputTypePurchase,
		}},
	}}, address, ""); err != nil {
		t.Fatal(err)
	}

	outputs, err := ledger.GetUTXOs(ctx, ledgerdb.OutputQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(outputs) != 2 {
		t.Fatalf("hydrated outputs = %d, want 2", len(outputs))
	}
	for index, wantAmount := range []uint64{11, 22} {
		if outputs[index].Amount != wantAmount {
			t.Fatalf("output %d amount = %d, want raw amount %d", index, outputs[index].Amount, wantAmount)
		}
		if !bytes.Equal(outputs[index].Script.Source, transaction.Outputs[index].Script.Source) ||
			bytes.Equal(outputs[index].Script.Source, wrongScript) {
			t.Fatalf("output %d script = %x, want raw script %x", index,
				outputs[index].Script.Source, transaction.Outputs[index].Script.Source)
		}
	}
	if outputs[0].owner == nil || outputs[0].owner != outputs[1].owner {
		t.Fatalf("parent grouping = %p and %p, want one transaction", outputs[0].owner, outputs[1].owner)
	}
	parent := outputs[0].owner
	if outputs[0] != &parent.Outputs[0] || outputs[1] != &parent.Outputs[1] ||
		parent.Height != 7 || parent.Position != 3 || !parent.IsVerified {
		t.Fatalf("hydrated parent = %#v", parent)
	}
}

func TestTransactionOutputQueriesMaterializeWithoutParentTransaction(t *testing.T) {
	ctx := context.Background()
	ledger := newTransactionOutputQueryLedger(t)
	transaction := transactionOutputQueryTransaction(91, 17)
	storedScript := NewPayPubKeyHashOutput(0, bytes.Repeat([]byte{0xab}, 20)).Script.Source
	if err := ledger.Database.SaveTransactionIOBatch(ctx, []ledgerdb.TransactionIORow{{
		Transaction: ledgerdb.TransactionRow{
			TXID: transaction.ID, Raw: []byte{1}, Height: 23, Position: 4,
		},
		Outputs: []ledgerdb.TransactionOutputRow{{
			TXOID: transaction.Outputs[0].ID(), Position: 0,
			Amount: 1_234, Script: storedScript, TXOType: TransactionOutputTypeOther,
		}},
	}}, "unused", ""); err != nil {
		t.Fatal(err)
	}

	ordinary, err := ledger.GetTransactionOutputs(ctx, TransactionOutputListOptions{
		Query: ledgerdb.OutputQuery{TXID: transaction.ID},
	})
	if ordinary != nil || !errors.Is(err, ErrInvalidStoredTransaction) {
		t.Fatalf("ordinary corrupt output = %#v, %v", ordinary, err)
	}

	lightweight, err := ledger.GetTransactionOutputs(ctx, TransactionOutputListOptions{
		Query: ledgerdb.OutputQuery{TXID: transaction.ID}, NoTransaction: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(lightweight) != 1 {
		t.Fatalf("lightweight outputs = %d, want 1", len(lightweight))
	}
	output := lightweight[0]
	if output.owner != nil || output.TransactionID != transaction.ID || output.Position != 0 ||
		output.Amount != 1_234 || output.TransactionHeight() != 23 ||
		!bytes.Equal(output.Script.Source, storedScript) {
		t.Fatalf("lightweight output = %#v", output)
	}
	wantHash, err := transactionHashFromID(transaction.ID)
	if err != nil {
		t.Fatal(err)
	}
	if output.TransactionHash != wantHash {
		t.Fatalf("lightweight hash = %x, want %x", output.TransactionHash, wantHash)
	}
}

func TestTransactionOutputQueriesForceSpendingTypesAndUnspentState(t *testing.T) {
	ctx := context.Background()
	ledger := newTransactionOutputQueryLedger(t)
	ordinary := persistTransactionOutputQueryOutput(t, ledger, "owned", 10, 10, 1,
		TransactionOutputTypeOther, 8, false)
	persistTransactionOutputQueryOutput(t, ledger, "owned", 20, 20, 2,
		TransactionOutputTypePurchase, 7, false)
	persistTransactionOutputQueryOutput(t, ledger, "owned", 30, 30, 3,
		TransactionOutputTypeStream, 6, false)
	persistTransactionOutputQueryOutput(t, ledger, "owned", 40, 40, 4,
		TransactionOutputTypeSupport, 5, false)
	spent := persistTransactionOutputQueryOutput(t, ledger, "owned", 50, 50, 5,
		TransactionOutputTypeOther, 4, false)
	markTransactionOutputQuerySpent(t, ledger, "owned", spent.ID(), 50)

	spentOnly := true
	query := ledgerdb.OutputQuery{
		Types:   []int64{TransactionOutputTypeStream},
		IsSpent: &spentOnly,
	}
	outputs, err := ledger.GetUTXOs(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	if got := transactionOutputQueryAmounts(outputs); !equalTransactionOutputQueryAmounts(got, []uint64{10, 20}) {
		t.Fatalf("forced UTXOs = %v, want ordinary and purchase [10 20]", got)
	}
	count, err := ledger.GetUTXOCount(ctx, query)
	if err != nil || count != 2 {
		t.Fatalf("forced UTXO count = %d, %v, want 2", count, err)
	}
	if ordinary.ID() == spent.ID() {
		t.Fatal("fixture transaction IDs collided")
	}
}

func TestAccountTransactionOutputQueriesUsePublicKeyOwnership(t *testing.T) {
	ctx := context.Background()
	ledger := newTransactionOutputQueryLedger(t)
	first, firstAddress := newTransactionOutputQueryAccount(t, ledger, 0x11, "wrong-first-id")
	second, secondAddress := newTransactionOutputQueryAccount(t, ledger, 0x22, "wrong-second-id")
	persistTransactionOutputQueryOutput(t, ledger, firstAddress, 11, 11, 1,
		TransactionOutputTypeOther, 2, false)
	persistTransactionOutputQueryOutput(t, ledger, secondAddress, 22, 22, 2,
		TransactionOutputTypeOther, 2, false)
	persistTransactionOutputQueryOutput(t, ledger, "unowned-address", 33, 33, 3,
		TransactionOutputTypeOther, 2, false)

	outputs, err := first.GetUTXOs(nil, ledgerdb.OutputQuery{
		AccountIDs: []string{second.PublicKey.Address()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := transactionOutputQueryAmounts(outputs); !equalTransactionOutputQueryAmounts(got, []uint64{11}) {
		t.Fatalf("first account UTXOs = %v, want [11]", got)
	}
	count, err := first.GetUTXOCount(ctx, ledgerdb.OutputQuery{
		AccountIDs: []string{second.PublicKey.Address()},
	})
	if err != nil || count != 1 {
		t.Fatalf("first account count = %d, %v, want 1", count, err)
	}
	outputs, err = second.GetUTXOs(ctx, ledgerdb.OutputQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if got := transactionOutputQueryAmounts(outputs); !equalTransactionOutputQueryAmounts(got, []uint64{22}) {
		t.Fatalf("second account UTXOs = %v, want [22]", got)
	}
	outputs, err = ledger.GetUTXOs(ctx, ledgerdb.OutputQuery{})
	if err != nil || len(outputs) != 3 {
		t.Fatalf("global UTXOs = %d, %v, want 3", len(outputs), err)
	}
}

func TestAccountTransactionOutputQueriesHydrateWholeWalletAnnotations(t *testing.T) {
	ctx := context.Background()
	database, fixture := transactionHistoryOracleFixture(t)
	ledger := &Ledger{Database: database}
	accountA := &Account{ID: "account-a", ledger: ledger}
	accountB := &Account{ID: "account-b", ledger: ledger}
	NewWallet(WithWalletAccounts([]*Account{accountA, accountB}))

	outputs, err := accountA.GetUTXOs(ctx, ledgerdb.OutputQuery{
		IncludeIsSpent:    true,
		IncludeIsMyInput:  true,
		IncludeIsMyOutput: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]*TransactionOutput, len(outputs))
	for _, output := range outputs {
		byID[output.ID()] = output
	}
	incoming := byID[fixture["incoming"].Outputs[0].ID()]
	if incoming == nil {
		t.Fatalf("incoming UTXO missing from %#v", byID)
	}
	assertTransactionHistoryBool(t, "incoming UTXO is unspent", incoming.IsSpent, false)
	assertTransactionHistoryBool(t, "incoming UTXO is not my input", incoming.IsMyInput, false)
	assertTransactionHistoryBool(t, "incoming UTXO is my output", incoming.IsMyOutput, true)
	assertTransactionHistoryBool(t, "incoming UTXO is not internal", incoming.IsInternalTransfer, false)

	internal := byID[fixture["internal"].Outputs[0].ID()]
	if internal == nil {
		t.Fatalf("internal UTXO missing from %#v", byID)
	}
	assertTransactionHistoryBool(t, "internal UTXO is unspent", internal.IsSpent, false)
	assertTransactionHistoryBool(t, "internal UTXO is my input", internal.IsMyInput, true)
	assertTransactionHistoryBool(t, "internal UTXO is my output", internal.IsMyOutput, true)
	assertTransactionHistoryBool(t, "internal UTXO transfer", internal.IsInternalTransfer, true)
}

func TestAccountTransactionOutputQueriesIncludeWholeWalletReceivedTips(t *testing.T) {
	ctx := context.Background()
	ledger := newTransactionOutputQueryLedger(t)
	first, firstAddress := newTransactionOutputQueryAccount(t, ledger, 0x51, "wrong-first-id")
	second, secondAddress := newTransactionOutputQueryAccount(t, ledger, 0x52, "wrong-second-id")
	NewWallet(WithWalletAccounts([]*Account{first, second}))

	claimID := "claim-for-received-tips"
	persist := func(
		nonce uint32, address string, amount uint64, outputType int64, claim string,
	) *TransactionOutput {
		t.Helper()
		transaction := transactionOutputQueryTransaction(nonce, amount)
		storedAddress := address
		if err := ledger.Database.SaveTransactionIOBatch(ctx, []ledgerdb.TransactionIORow{{
			Transaction: ledgerdb.TransactionRow{
				TXID: transaction.ID, Raw: append([]byte(nil), transaction.Raw...),
				Height: int64(nonce), Position: int64(nonce),
			},
			Outputs: []ledgerdb.TransactionOutputRow{{
				TXOID: transaction.Outputs[0].ID(), Address: &storedAddress,
				Position: 0, Amount: int64(amount),
				Script:  append([]byte(nil), transaction.Outputs[0].Script.Source...),
				TXOType: outputType, ClaimID: &claim,
			}},
		}}, address, ""); err != nil {
			t.Fatal(err)
		}
		return &transaction.Outputs[0]
	}

	target := persist(61, firstAddress, 1, TransactionOutputTypeOther, claimID)
	persist(62, firstAddress, 11, TransactionOutputTypeSupport, claimID)
	persist(63, secondAddress, 22, TransactionOutputTypeSupport, claimID)
	persist(64, "foreign-tip-address", 33, TransactionOutputTypeSupport, claimID)
	spent := persist(65, secondAddress, 44, TransactionOutputTypeSupport, claimID)
	markTransactionOutputQuerySpent(t, ledger, secondAddress, spent.ID(), 66)

	outputs, err := first.GetUTXOs(ctx, ledgerdb.OutputQuery{IncludeReceivedTips: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(outputs) != 1 || outputs[0].ID() != target.ID() {
		t.Fatalf("received-tip target outputs = %#v, want %s", outputs, target.ID())
	}
	if outputs[0].ReceivedTips == nil || *outputs[0].ReceivedTips != 33 {
		t.Fatalf("whole-wallet received tips = %v, want 33", outputs[0].ReceivedTips)
	}

	outputs, err = first.GetUTXOs(ctx, ledgerdb.OutputQuery{})
	if err != nil || len(outputs) != 1 || outputs[0].ReceivedTips != nil {
		t.Fatalf("unrequested received tips = %#v, %v, want nil", outputs, err)
	}
}

func TestTransactionOutputAggregatesIgnorePaginationAndOrdering(t *testing.T) {
	ctx := context.Background()
	ledger := newTransactionOutputQueryLedger(t)
	persistTransactionOutputQueryOutput(t, ledger, "aggregate", 10, 10, 1,
		TransactionOutputTypeOther, 1, false)
	persistTransactionOutputQueryOutput(t, ledger, "aggregate", 20, 20, 2,
		TransactionOutputTypeStream, 2, false)
	persistTransactionOutputQueryOutput(t, ledger, "aggregate", 30, 30, 3,
		TransactionOutputTypePurchase, 3, false)
	zero, largeOffset := 0, 99
	query := ledgerdb.OutputQuery{
		Limit: &zero, Offset: &largeOffset, Order: ledgerdb.OutputOrder(255),
	}

	count, err := ledger.CountTransactionOutputs(ctx, query)
	if err != nil || count != 3 {
		t.Fatalf("aggregate count = %d, %v, want 3", count, err)
	}
	sum, err := ledger.SumTransactionOutputs(ctx, query)
	if err != nil || sum != 60 {
		t.Fatalf("aggregate sum = %d, %v, want 60", sum, err)
	}
	utxoCount, err := ledger.GetUTXOCount(ctx, query)
	if err != nil || utxoCount != 2 {
		t.Fatalf("aggregate UTXO count = %d, %v, want 2", utxoCount, err)
	}
}

func TestAccountBalanceConfirmationTypeReservedAndSpentMatrix(t *testing.T) {
	ctx := context.Background()
	ledger := newTransactionOutputQueryLedger(t)
	ledger.Headers = &Headers{size: 11}
	account, address := newTransactionOutputQueryAccount(t, ledger, 0x31, "wrong-balance-id")
	_, otherAddress := newTransactionOutputQueryAccount(t, ledger, 0x32, "other-balance-id")

	persistTransactionOutputQueryOutput(t, ledger, address, 10, 10, 1,
		TransactionOutputTypeOther, 10, false)
	persistTransactionOutputQueryOutput(t, ledger, address, 20, 20, 2,
		TransactionOutputTypePurchase, 9, false)
	persistTransactionOutputQueryOutput(t, ledger, address, 30, 30, 3,
		TransactionOutputTypeStream, 8, false)
	persistTransactionOutputQueryOutput(t, ledger, address, 40, 40, 4,
		TransactionOutputTypeSupport, 7, false)
	persistTransactionOutputQueryOutput(t, ledger, address, 50, 50, 5,
		TransactionOutputTypeOther, 0, false)
	persistTransactionOutputQueryOutput(t, ledger, address, 60, 60, 6,
		TransactionOutputTypeOther, -1, false)
	persistTransactionOutputQueryOutput(t, ledger, address, 70, 70, 7,
		TransactionOutputTypeOther, 5, false)
	persistTransactionOutputQueryOutput(t, ledger, address, 80, 80, 8,
		TransactionOutputTypeOther, 6, true)
	spent := persistTransactionOutputQueryOutput(t, ledger, address, 90, 90, 9,
		TransactionOutputTypeOther, 4, false)
	markTransactionOutputQuerySpent(t, ledger, address, spent.ID(), 90)
	persistTransactionOutputQueryOutput(t, ledger, otherAddress, 100, 100, 10,
		TransactionOutputTypeOther, 3, false)

	spentOnly := true
	one, nine := int64(1), int64(9)
	tests := []struct {
		name    string
		options AccountBalanceOptions
		want    int64
	}{
		{name: "default", want: 210},
		{
			name: "default overwrites caller type and spent",
			options: AccountBalanceOptions{Query: ledgerdb.OutputQuery{
				Types: []int64{TransactionOutputTypeStream}, IsSpent: &spentOnly,
			}},
			want: 210,
		},
		{name: "include claims means all types", options: AccountBalanceOptions{IncludeClaims: true}, want: 280},
		{
			name: "include claims preserves caller type",
			options: AccountBalanceOptions{IncludeClaims: true, Query: ledgerdb.OutputQuery{
				Types: []int64{TransactionOutputTypeStream},
			}},
			want: 30,
		},
		{name: "one confirmation includes tip", options: AccountBalanceOptions{Confirmations: 1}, want: 100},
		{
			name: "three confirmations overwrite caller heights",
			options: AccountBalanceOptions{Confirmations: 3, Query: ledgerdb.OutputQuery{
				HeightLTE: &one, HeightGT: &nine,
			}},
			want: 70,
		},
		{
			name:    "three confirmations with claims",
			options: AccountBalanceOptions{Confirmations: 3, IncludeClaims: true},
			want:    140,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			balance, err := account.GetBalance(ctx, test.options)
			if err != nil || balance != test.want {
				t.Fatalf("balance = %d, %v, want %d", balance, err, test.want)
			}
		})
	}
}

func TestTransactionOutputQueriesReleaseAccountThenGlobal(t *testing.T) {
	ctx := context.Background()
	ledger := newTransactionOutputQueryLedger(t)
	first, firstAddress := newTransactionOutputQueryAccount(t, ledger, 0x41, "wrong-release-id")
	_, secondAddress := newTransactionOutputQueryAccount(t, ledger, 0x42, "second-release-id")
	firstOutput := persistTransactionOutputQueryOutput(t, ledger, firstAddress, 10, 10, 1,
		TransactionOutputTypeStream, 1, true)
	secondOutput := persistTransactionOutputQueryOutput(t, ledger, secondAddress, 20, 20, 2,
		TransactionOutputTypePurchase, 1, true)
	globalOutput := persistTransactionOutputQueryOutput(t, ledger, "unowned-release", 30, 30, 3,
		TransactionOutputTypeOther, 1, true)

	if err := first.ReleaseAllOutputs(nil); err != nil {
		t.Fatal(err)
	}
	assertTransactionOutputQueryReservations(t, ledger.Database, map[string]bool{
		firstOutput.ID(): false, secondOutput.ID(): true, globalOutput.ID(): true,
	})
	if err := ledger.ReleaseAllOutputs(nil); err != nil {
		t.Fatal(err)
	}
	assertTransactionOutputQueryReservations(t, ledger.Database, map[string]bool{
		firstOutput.ID(): false, secondOutput.ID(): false, globalOutput.ID(): false,
	})
	if err := ledger.ReleaseAllOutputs(ctx); err != nil {
		t.Fatalf("idempotent global release: %v", err)
	}
}

func TestTransactionOutputQueriesUnavailableBoundaries(t *testing.T) {
	ctx := context.Background()
	query := ledgerdb.OutputQuery{}
	var nilLedger *Ledger
	if outputs, err := nilLedger.GetUTXOs(ctx, query); outputs != nil ||
		!errors.Is(err, ErrTransactionOutputQueryUnavailable) {
		t.Fatalf("nil ledger GetUTXOs = %#v, %v", outputs, err)
	}
	if _, err := nilLedger.GetUTXOCount(ctx, query); !errors.Is(err, ErrTransactionOutputQueryUnavailable) {
		t.Fatalf("nil ledger GetUTXOCount = %v", err)
	}
	if _, err := nilLedger.CountTransactionOutputs(ctx, query); !errors.Is(err, ErrTransactionOutputQueryUnavailable) {
		t.Fatalf("nil ledger count = %v", err)
	}
	if _, err := nilLedger.SumTransactionOutputs(ctx, query); !errors.Is(err, ErrTransactionOutputQueryUnavailable) {
		t.Fatalf("nil ledger sum = %v", err)
	}
	if err := nilLedger.ReleaseAllOutputs(ctx); !errors.Is(err, ErrTransactionOutputQueryUnavailable) {
		t.Fatalf("nil ledger release = %v", err)
	}

	var nilAccount *Account
	if outputs, err := nilAccount.GetUTXOs(ctx, query); outputs != nil ||
		!errors.Is(err, ErrTransactionOutputQueryUnavailable) {
		t.Fatalf("nil account GetUTXOs = %#v, %v", outputs, err)
	}
	if _, err := nilAccount.GetUTXOCount(ctx, query); !errors.Is(err, ErrTransactionOutputQueryUnavailable) {
		t.Fatalf("nil account GetUTXOCount = %v", err)
	}
	if _, err := nilAccount.GetBalance(ctx, AccountBalanceOptions{}); !errors.Is(err, ErrTransactionOutputQueryUnavailable) {
		t.Fatalf("nil account balance = %v", err)
	}
	if err := nilAccount.ReleaseAllOutputs(ctx); !errors.Is(err, ErrTransactionOutputQueryUnavailable) {
		t.Fatalf("nil account release = %v", err)
	}

	unopened := &Ledger{Database: ledgerdb.New(":memory:")}
	if _, err := unopened.GetUTXOs(ctx, query); !errors.Is(err, ledgerdb.ErrNotOpen) {
		t.Fatalf("unopened GetUTXOs = %v", err)
	}
	if _, err := unopened.GetUTXOCount(ctx, query); !errors.Is(err, ledgerdb.ErrNotOpen) {
		t.Fatalf("unopened GetUTXOCount = %v", err)
	}
	if _, err := unopened.CountTransactionOutputs(ctx, query); !errors.Is(err, ledgerdb.ErrNotOpen) {
		t.Fatalf("unopened count = %v", err)
	}
	if _, err := unopened.SumTransactionOutputs(ctx, query); !errors.Is(err, ledgerdb.ErrNotOpen) {
		t.Fatalf("unopened sum = %v", err)
	}
	if err := unopened.ReleaseAllOutputs(ctx); !errors.Is(err, ledgerdb.ErrNotOpen) {
		t.Fatalf("unopened release = %v", err)
	}
	detached := &Account{}
	if _, err := detached.GetUTXOs(ctx, query); !errors.Is(err, ErrTransactionOutputQueryUnavailable) {
		t.Fatalf("detached account GetUTXOs = %v", err)
	}

	ledger := newTransactionOutputQueryLedger(t)
	account := &Account{ID: "headerless", ledger: ledger}
	if _, err := account.GetBalance(ctx, AccountBalanceOptions{Confirmations: 1}); !errors.Is(err, ErrTransactionOutputQueryUnavailable) {
		t.Fatalf("headerless confirmed balance = %v", err)
	}
}

func TestTransactionOutputQueriesRejectCorruptRawAndOutputRanges(t *testing.T) {
	ctx := context.Background()
	t.Run("corrupt raw", func(t *testing.T) {
		ledger := newTransactionOutputQueryLedger(t)
		if err := ledger.Database.SaveTransactionIOBatch(ctx, []ledgerdb.TransactionIORow{{
			Transaction: ledgerdb.TransactionRow{TXID: "corrupt", Raw: []byte{1}},
			Outputs: []ledgerdb.TransactionOutputRow{{
				TXOID: "corrupt:0", Position: 0, Amount: 1, Script: []byte{0x51},
			}},
		}}, "unused", ""); err != nil {
			t.Fatal(err)
		}
		outputs, err := ledger.GetUTXOs(ctx, ledgerdb.OutputQuery{TXID: "corrupt"})
		if outputs != nil || !errors.Is(err, ErrInvalidStoredTransaction) {
			t.Fatalf("corrupt raw outputs = %#v, %v", outputs, err)
		}
	})

	for _, test := range []struct {
		name     string
		position int64
	}{
		{name: "negative", position: -1},
		{name: "past end", position: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			ledger := newTransactionOutputQueryLedger(t)
			transaction := transactionOutputQueryTransaction(77+uint32(test.position+1), 9)
			if err := ledger.Database.SaveTransactionIOBatch(ctx, []ledgerdb.TransactionIORow{{
				Transaction: ledgerdb.TransactionRow{
					TXID: transaction.ID, Raw: append([]byte(nil), transaction.Raw...),
				},
				Outputs: []ledgerdb.TransactionOutputRow{{
					TXOID: transaction.ID + ":invalid", Position: test.position,
					Amount: 9, Script: append([]byte(nil), transaction.Outputs[0].Script.Source...),
				}},
			}}, "unused", ""); err != nil {
				t.Fatal(err)
			}
			outputs, err := ledger.GetUTXOs(ctx, ledgerdb.OutputQuery{TXID: transaction.ID})
			if outputs != nil || !errors.Is(err, ErrTransactionOutputOutOfRange) {
				t.Fatalf("out-of-range outputs = %#v, %v", outputs, err)
			}
		})
	}
}

func newTransactionOutputQueryLedger(t *testing.T) *Ledger {
	t.Helper()
	database, err := ledgerdb.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(context.Background()); err != nil {
			t.Errorf("close transaction output query database: %v", err)
		}
	})
	return &Ledger{Network: keys.MainNet, Database: database}
}

func newTransactionOutputQueryAccount(
	t *testing.T, ledger *Ledger, seed byte, fallbackID string,
) (*Account, string) {
	t.Helper()
	privateKey, err := keys.PrivateKeyFromSeed(keys.MainNet, bytes.Repeat([]byte{seed}, 32))
	if err != nil {
		t.Fatal(err)
	}
	account := &Account{
		Network: keys.MainNet, ID: fallbackID, PublicKey: privateKey.PublicKey(), ledger: ledger,
	}
	address := "owned-" + account.PublicKey.Address()
	if err := ledger.Database.AddKeys(context.Background(), account.PublicKey.Address(), []ledgerdb.AddressKey{{
		Address: address, PublicKey: []byte{seed}, ChainCode: []byte{seed + 1},
	}}); err != nil {
		t.Fatal(err)
	}
	return account, address
}

func transactionOutputQueryTransaction(nonce uint32, amounts ...uint64) *Transaction {
	transaction := NewTransaction()
	transaction.LockTime = nonce
	transaction.AddInputs([]TransactionInput{{
		PreviousIndex: ^uint32(0), Sequence: ^uint32(0),
		Coinbase: []byte{byte(nonce), byte(nonce >> 8), byte(nonce >> 16), byte(nonce >> 24)},
	}})
	outputs := make([]TransactionOutput, len(amounts))
	for index, amount := range amounts {
		hash := make([]byte, 20)
		for byteIndex := range hash {
			hash[byteIndex] = byte(nonce) + byte(index) + byte(byteIndex)
		}
		outputs[index] = NewPayPubKeyHashOutput(amount, hash)
	}
	transaction.AddOutputs(outputs)
	_ = transaction.RebuildDerived()
	return transaction
}

func persistTransactionOutputQueryOutput(
	t *testing.T,
	ledger *Ledger,
	address string,
	rawAmount uint64,
	storedAmount int64,
	nonce uint32,
	outputType int64,
	height int64,
	reserved bool,
) *TransactionOutput {
	t.Helper()
	transaction := transactionOutputQueryTransaction(nonce, rawAmount)
	transaction.Height = height
	transaction.Position = int64(nonce)
	outputAddress := address
	if err := ledger.Database.SaveTransactionIOBatch(context.Background(), []ledgerdb.TransactionIORow{{
		Transaction: ledgerdb.TransactionRow{
			TXID: transaction.ID, Raw: append([]byte(nil), transaction.Raw...),
			Height: height, Position: transaction.Position,
		},
		Outputs: []ledgerdb.TransactionOutputRow{{
			TXOID: transaction.Outputs[0].ID(), Address: &outputAddress, Position: 0,
			Amount: storedAmount, Script: append([]byte(nil), transaction.Outputs[0].Script.Source...),
			TXOType: outputType,
		}},
	}}, address, ""); err != nil {
		t.Fatal(err)
	}
	if reserved {
		if err := ledger.Database.ReserveOutputs(
			context.Background(), []string{transaction.Outputs[0].ID()}, true,
		); err != nil {
			t.Fatal(err)
		}
	}
	return &transaction.Outputs[0]
}

func markTransactionOutputQuerySpent(
	t *testing.T, ledger *Ledger, address, outputID string, nonce uint32,
) {
	t.Helper()
	spender := transactionOutputQueryTransaction(nonce)
	if err := ledger.Database.SaveTransactionIOBatch(context.Background(), []ledgerdb.TransactionIORow{{
		Transaction: ledgerdb.TransactionRow{
			TXID: spender.ID, Raw: append([]byte(nil), spender.Raw...), Height: 0, Position: -1,
		},
		Inputs: []ledgerdb.TransactionInputRow{{TXOID: outputID, Position: 0}},
	}}, address, ""); err != nil {
		t.Fatal(err)
	}
}

func transactionOutputQueryAmounts(outputs []*TransactionOutput) []uint64 {
	amounts := make([]uint64, len(outputs))
	for index, output := range outputs {
		amounts[index] = output.Amount
	}
	return amounts
}

func equalTransactionOutputQueryAmounts(got, want []uint64) bool {
	if len(got) != len(want) {
		return false
	}
	counts := make(map[uint64]int, len(got))
	for _, amount := range got {
		counts[amount]++
	}
	for _, amount := range want {
		counts[amount]--
	}
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}
	return true
}

func assertTransactionOutputQueryReservations(
	t *testing.T, database *ledgerdb.DB, wanted map[string]bool,
) {
	t.Helper()
	outputIDs := make([]string, 0, len(wanted))
	for outputID := range wanted {
		outputIDs = append(outputIDs, outputID)
	}
	outputs, err := database.GetOutputsByID(context.Background(), outputIDs)
	if err != nil {
		t.Fatal(err)
	}
	for outputID, want := range wanted {
		output, ok := outputs[outputID]
		if !ok || output.IsReserved != want {
			t.Fatalf("output %s = %#v, want reserved %v", outputID, output, want)
		}
	}
}
