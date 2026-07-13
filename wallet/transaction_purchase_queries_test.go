package wallet

import (
	"context"
	"errors"
	"testing"

	"lbry/daemon/wallet/ledgerdb"
)

func TestTransactionPurchaseQueriesMirrorPinnedInputScope(t *testing.T) {
	database, fixture := transactionHistoryOracleFixture(t)
	ledger := &Ledger{Database: database}
	accountA := &Account{ID: "account-a", ledger: ledger}
	accountB := &Account{ID: "account-b", ledger: ledger}
	wallet := NewWallet(WithWalletAccounts([]*Account{accountA, accountB}))

	purchases, err := ledger.GetPurchases(context.Background(), PurchaseListOptions{
		Accounts: []*Account{accountA}, Wallet: wallet,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(purchases) != 1 || purchases[0].ID() != fixture["purchase"].Outputs[0].ID() ||
		purchases[0].Purchase == nil {
		t.Fatalf("account purchases = %#v", purchases)
	}
	claimID, ok := decodeTransactionPurchase(purchases[0].Purchase.Script)
	if !ok || claimID != "001122334455" {
		t.Fatalf("purchase claim ID = %q, %v", claimID, ok)
	}
	count, err := ledger.CountPurchases(
		context.Background(), ledgerdb.TransactionQuery{}, []*Account{accountA},
	)
	if err != nil || count != 1 {
		t.Fatalf("purchase count = %d, %v", count, err)
	}

	wanted := "different"
	purchases, err = accountA.GetPurchases(context.Background(), ledgerdb.TransactionQuery{
		PurchasedClaimID: &wanted,
	})
	if err != nil || len(purchases) != 0 {
		t.Fatalf("filtered account purchases = %#v, %v", purchases, err)
	}
	count, err = accountB.CountPurchases(context.Background(), ledgerdb.TransactionQuery{})
	if err != nil || count != 0 {
		t.Fatalf("account B purchase count = %d, %v", count, err)
	}
}

func TestTransactionPurchaseQueryBoundaries(t *testing.T) {
	var nilLedger *Ledger
	if _, err := nilLedger.GetPurchases(context.Background(), PurchaseListOptions{
		Accounts: []*Account{{ID: "account"}},
	}); !errors.Is(err, ErrTransactionQueryUnavailable) {
		t.Fatalf("nil ledger purchase error = %v", err)
	}
	if _, err := (&Ledger{}).GetPurchases(context.Background(), PurchaseListOptions{
		Accounts: []*Account{{ID: "account"}},
	}); !errors.Is(err, ErrTransactionQueryUnavailable) {
		t.Fatalf("database-less purchase error = %v", err)
	}
	if _, err := (&Ledger{}).GetPurchases(context.Background(), PurchaseListOptions{}); !errors.Is(err, ErrPurchaseAccountsRequired) {
		t.Fatalf("missing purchase accounts error = %v", err)
	}
	if _, err := (&Ledger{}).GetPurchases(context.Background(), PurchaseListOptions{
		Accounts: []*Account{nil},
	}); !errors.Is(err, ErrNilWalletAccount) {
		t.Fatalf("nil purchase account error = %v", err)
	}

	var nilAccount *Account
	if _, err := nilAccount.GetPurchases(context.Background(), ledgerdb.TransactionQuery{}); !errors.Is(err, ErrTransactionPurchaseQuery) {
		t.Fatalf("nil account purchase error = %v", err)
	}
}
