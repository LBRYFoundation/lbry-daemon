package wallet

import (
	"bytes"
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/ledgerdb"
)

func TestTransactionHistorySummaryRejectsUnknownOwnership(t *testing.T) {
	ledger := transactionHistorySummaryTestLedger()
	owned := true

	tests := []struct {
		name        string
		transaction *Transaction
		location    string
	}{
		{
			name: "resolved input",
			transaction: &Transaction{
				ID: "unknown-input", Height: 1,
				Inputs: []TransactionInput{{
					Position: 0,
					ResolvedOutput: &TransactionOutput{
						Amount: 25, IsMyOutput: nil,
					},
				}},
				Outputs: []TransactionOutput{{
					Position: 0, Amount: 20, IsMyOutput: &owned,
				}},
			},
			location: "input 0",
		},
		{
			name: "current output",
			transaction: &Transaction{
				ID: "unknown-output", Height: 1,
				Outputs: []TransactionOutput{{
					Position: 0, Amount: 20, IsMyOutput: nil,
				}},
			},
			location: "output 0",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			history, err := ledger.summarizeTransactionHistory([]*Transaction{test.transaction})
			if history != nil || !errors.Is(err, ErrTransactionOwnershipUnknown) ||
				!strings.Contains(err.Error(), test.location) {
				t.Fatalf("unknown ownership history = %#v, %v", history, err)
			}
		})
	}
}

func TestTransactionHistorySummaryEmptyInputsAreAllMine(t *testing.T) {
	owned := true
	transaction := &Transaction{
		ID: "empty-inputs", Height: 0,
		Inputs: []TransactionInput{},
		Outputs: []TransactionOutput{{
			Position: 0, Amount: 200_000_000, IsMyOutput: &owned,
		}},
	}
	if !transactionHistoryAllInputsMine(transaction) {
		t.Fatal("all([]) ownership = false, want true")
	}

	history, err := transactionHistorySummaryTestLedger().summarizeTransactionHistory(
		[]*Transaction{transaction},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Value != "0.0" || history[0].Fee != "2.0" ||
		history[0].Timestamp != nil || history[0].Date != nil || history[0].Confirmations != 0 {
		t.Fatalf("empty-input history = %+v", history)
	}
	if history[0].ClaimInfo == nil || history[0].UpdateInfo == nil ||
		history[0].SupportInfo == nil || history[0].AbandonInfo == nil ||
		history[0].PurchaseInfo == nil {
		t.Fatalf("empty summary lists must be non-nil: %+v", history[0])
	}
}

func TestTransactionHistorySummaryPreservesNullAddress(t *testing.T) {
	const claimID = "00112233445566778899aabbccddeeff00112233"
	purchaseData, err := NewPurchaseDataOutput(claimID)
	if err != nil {
		t.Fatal(err)
	}
	payment := NewReturnDataOutput([]byte("addressless payment"))
	payment.Amount = 100_000_000
	transaction := NewTransaction()
	transaction.AddOutputs([]TransactionOutput{payment, purchaseData})
	transaction.Height = 1
	owned, unspent := true, false
	for index := range transaction.Outputs {
		transaction.Outputs[index].IsMyOutput = &owned
		transaction.Outputs[index].IsSpent = &unspent
	}
	transaction.Outputs[0].Purchase = &transaction.Outputs[1]

	history, err := transactionHistorySummaryTestLedger().summarizeTransactionHistory(
		[]*Transaction{transaction},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || len(history[0].PurchaseInfo) != 1 {
		t.Fatalf("addressless purchase history = %+v", history)
	}
	purchase := history[0].PurchaseInfo[0]
	if purchase.Address != nil || purchase.ClaimID != claimID ||
		purchase.Amount != "1.0" || purchase.BalanceDelta != "-1.0" ||
		purchase.IsSpent == nil || *purchase.IsSpent {
		t.Fatalf("addressless purchase = %+v", purchase)
	}
}

func TestTransactionHistorySummaryRejectsInvalidClaimNameUTF8(t *testing.T) {
	script, err := NewClaimNamePubKeyHashOutputScript(
		[]byte{0xff}, []byte{0}, bytes.Repeat([]byte{0x31}, 20),
	)
	if err != nil {
		t.Fatal(err)
	}
	transaction := NewTransaction()
	transaction.AddOutputs([]TransactionOutput{{Amount: 1, Script: script}})
	transaction.Height = 1
	owned, unspent := true, false
	transaction.Outputs[0].IsMyOutput = &owned
	transaction.Outputs[0].IsSpent = &unspent

	history, err := transactionHistorySummaryTestLedger().summarizeTransactionHistory(
		[]*Transaction{transaction},
	)
	if history != nil || !errors.Is(err, ErrInvalidTransactionClaimName) {
		t.Fatalf("invalid UTF-8 history = %#v, %v", history, err)
	}
}

func TestTransactionHistorySummaryHeaderRequirementDependsOnRows(t *testing.T) {
	ledger := &Ledger{}
	empty, err := ledger.summarizeTransactionHistory(nil)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty history without headers = %#v, %v", empty, err)
	}

	nonempty, err := ledger.summarizeTransactionHistory([]*Transaction{{ID: "one"}})
	if nonempty != nil || !errors.Is(err, ErrTransactionHistoryUnavailable) {
		t.Fatalf("nonempty history without headers = %#v, %v", nonempty, err)
	}
}

func TestTransactionHistorySummaryAccountWrappers(t *testing.T) {
	ctx := context.Background()
	database, fixture := transactionHistoryOracleFixture(t)
	ledger := transactionHistorySummaryTestLedger()
	ledger.Database = database
	accountA := &Account{ID: "account-a", ledger: ledger}
	accountB := &Account{ID: "account-b", ledger: ledger}
	NewWallet(WithWalletAccounts([]*Account{accountA, accountB}))

	history, err := accountA.GetTransactionHistory(ctx, ledgerdb.TransactionQuery{
		AccountIDs: []string{"account-b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 8 {
		t.Fatalf("account A scoped history count = %d, want 8", len(history))
	}

	zero, farOffset := 0, 999
	count, err := accountA.GetTransactionHistoryCount(ctx, ledgerdb.TransactionQuery{
		Limit: &zero, Offset: &farOffset, Order: ledgerdb.TransactionOrder(255),
	})
	if err != nil || count != 8 {
		t.Fatalf("account A history count = %d, %v, want 8", count, err)
	}
	count, err = accountB.GetTransactionHistoryCount(nil, ledgerdb.TransactionQuery{})
	if err != nil || count != 1 {
		t.Fatalf("account B history count = %d, %v, want 1", count, err)
	}

	foreignID := fixture["foreign"].ID
	foreign, err := accountA.GetTransactionHistory(ctx, ledgerdb.TransactionQuery{TXID: &foreignID})
	if err != nil {
		t.Fatal(err)
	}
	if len(foreign) != 1 || foreign[0].TXID != foreignID ||
		foreign[0].Value != "0.00000606" || foreign[0].Fee != "0.0" {
		t.Fatalf("whole-wallet annotations through account A = %+v", foreign)
	}
}

func TestTransactionHistorySummaryDewiesDateAndConfirmations(t *testing.T) {
	dewies := []struct {
		value string
		want  string
	}{
		{"0", "0.0"},
		{"1", "0.00000001"},
		{"10000000", "0.1"},
		{"200000000", "2.0"},
		{"-200000000", "-2.0"},
		{"200000000000000000", "2000000000.0"},
		{"9007199254740993", "90071992.54740994"},
		{"18446744073709551615", "184467440737.09552002"},
		{"-18446744073709551615", "-184467440737.09552002"},
	}
	for _, test := range dewies {
		value, ok := new(big.Int).SetString(test.value, 10)
		if !ok {
			t.Fatalf("parse dewies %q", test.value)
		}
		if got := transactionHistoryDewies(value); got != test.want {
			t.Errorf("dewies %s = %q, want %q", test.value, got, test.want)
		}
	}

	ledger := &Ledger{
		Network: keys.MainNet,
		Headers: &Headers{
			size: 10, firstBlockTimestamp: 1_700_000_000,
			timestampAverageOffset: 60.5,
		},
	}
	owned := true
	transaction := &Transaction{
		ID: "confirmed", Height: 2,
		Outputs: []TransactionOutput{{
			Position: 0, Amount: 10_000_000, IsMyOutput: &owned,
		}},
	}
	history, err := ledger.summarizeTransactionHistory([]*Transaction{transaction})
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Timestamp == nil || *history[0].Timestamp != 1_700_000_121 {
		t.Fatalf("confirmed timestamp = %+v", history)
	}
	wantDate := time.Unix(*history[0].Timestamp, 0).In(time.Local).Format("2006-01-02 15:04")
	if history[0].Date == nil || *history[0].Date != wantDate ||
		history[0].Confirmations != 8 || history[0].Value != "0.0" ||
		history[0].Fee != "0.1" {
		t.Fatalf("confirmed summary = %+v, want date %q", history[0], wantDate)
	}
}

func transactionHistorySummaryTestLedger() *Ledger {
	return &Ledger{
		Network: keys.MainNet,
		Headers: &Headers{
			size: 31, firstBlockTimestamp: defaultFirstBlockTimestamp,
			timestampAverageOffset: defaultTimestampAverageDelta,
		},
	}
}
