package wallet

import (
	"context"
	"errors"
	"testing"

	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/ledgerdb"
)

func TestWalletManagerPurchasePageUsesDefaultLedgerWithSelectedAccount(t *testing.T) {
	database, fixture := transactionHistoryOracleFixture(t)
	defaultLedger := &Ledger{Network: keys.MainNet, Database: database, Headers: &Headers{}}
	selectedLedger := &Ledger{Network: keys.TestNet}
	defaultAccount := &Account{ID: "default", ledger: defaultLedger}
	selectedAccount := &Account{ID: "account-a", ledger: selectedLedger}
	defaultWallet := NewWallet(
		WithWalletName("wallet-a"), WithWalletAccounts([]*Account{defaultAccount}),
	)
	defaultWallet.ID = "wallet-a"
	selectedWallet := NewWallet(
		WithWalletName("wallet-b"), WithWalletAccounts([]*Account{selectedAccount}),
	)
	selectedWallet.ID = "wallet-b"
	manager := NewWalletManager()
	manager.Wallets = []*Wallet{defaultWallet, selectedWallet}

	walletID, accountID := "wallet-b", "account-a"
	page, err := manager.GetPurchasePage(context.Background(), PurchasePageOptions{
		WalletID: &walletID, AccountID: &accountID, Page: 1, PageSize: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Ledger != defaultLedger || len(page.Items) != 1 ||
		page.Items[0].ID() != fixture["purchase"].Outputs[0].ID() ||
		page.Items[0].Purchase == nil || page.TotalItems != 1 || page.TotalPages != 1 {
		t.Fatalf("selected-account purchase page = %+v", page)
	}

	claimID := "001122334455"
	page, err = manager.GetPurchasePage(context.Background(), PurchasePageOptions{
		WalletID: &walletID, ClaimID: &claimID, Page: -3, PageSize: -2,
	})
	if err != nil || page.Page != 1 || page.PageSize != 1 || len(page.Items) != 1 {
		t.Fatalf("normalized filtered purchase page = %+v, %v", page, err)
	}
	missingClaimID := "missing"
	page, err = manager.GetPurchasePage(context.Background(), PurchasePageOptions{
		WalletID: &walletID, ClaimID: &missingClaimID,
	})
	if err != nil || len(page.Items) != 0 || page.TotalItems != 0 || page.TotalPages != 0 {
		t.Fatalf("missing-claim purchase page = %+v, %v", page, err)
	}
}

func TestWalletManagerPurchasePageSelectionBoundaries(t *testing.T) {
	var nilManager *WalletManager
	if _, err := nilManager.GetPurchasePage(context.Background(), PurchasePageOptions{}); !errors.Is(err, ErrDefaultWalletMissing) {
		t.Fatalf("nil manager purchase page error = %v", err)
	}
	manager := NewWalletManager()
	if _, err := manager.GetPurchasePage(context.Background(), PurchasePageOptions{}); !errors.Is(err, ErrDefaultWalletMissing) {
		t.Fatalf("missing wallet purchase page error = %v", err)
	}

	account := &Account{ID: "account"}
	wallet := NewWallet(WithWalletAccounts([]*Account{account}))
	wallet.ID = "wallet"
	manager.Wallets = []*Wallet{wallet}
	if _, err := manager.GetPurchasePage(context.Background(), PurchasePageOptions{}); !errors.Is(err, ErrTransactionPurchaseQuery) {
		t.Fatalf("missing default ledger purchase page error = %v", err)
	}
	missing := "missing"
	if _, err := manager.GetPurchasePage(context.Background(), PurchasePageOptions{
		AccountID: &missing,
	}); err == nil || err.Error() != "Couldn't find account: missing." {
		t.Fatalf("missing account purchase page error = %v", err)
	}
}

func TestPurchasePageLoadsRecordsBeforeCount(t *testing.T) {
	ctx := context.Background()
	query := ledgerdb.TransactionQuery{}
	account := &Account{ID: "account"}
	wallet := NewWallet(WithWalletAccounts([]*Account{account}))
	recordsErr := errors.New("records failed")
	countErr := errors.New("count failed")

	calls := make([]string, 0, 2)
	items, count, err := loadPurchasePage(
		ctx, query, []*Account{account}, wallet,
		func(context.Context, PurchaseListOptions) ([]*TransactionOutput, error) {
			calls = append(calls, "records")
			return nil, recordsErr
		},
		func(context.Context, ledgerdb.TransactionQuery, []*Account) (int64, error) {
			calls = append(calls, "count")
			return 0, nil
		},
	)
	if items != nil || count != 0 || !errors.Is(err, recordsErr) ||
		len(calls) != 1 || calls[0] != "records" {
		t.Fatalf("records failure = %#v, %d, %v, calls %v", items, count, err, calls)
	}

	calls = calls[:0]
	item := &TransactionOutput{}
	items, count, err = loadPurchasePage(
		ctx, query, []*Account{account}, wallet,
		func(context.Context, PurchaseListOptions) ([]*TransactionOutput, error) {
			calls = append(calls, "records")
			return []*TransactionOutput{item}, nil
		},
		func(context.Context, ledgerdb.TransactionQuery, []*Account) (int64, error) {
			calls = append(calls, "count")
			return 0, countErr
		},
	)
	if items != nil || count != 0 || !errors.Is(err, countErr) ||
		len(calls) != 2 || calls[0] != "records" || calls[1] != "count" {
		t.Fatalf("count failure = %#v, %d, %v, calls %v", items, count, err, calls)
	}
}
