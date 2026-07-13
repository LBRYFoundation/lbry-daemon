package wallet

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"testing"

	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/ledgerdb"
)

func TestTransactionHistoryPagePagination(t *testing.T) {
	manager := transactionHistoryPagePopulatedManager(t)

	tests := []struct {
		name           string
		page           *int
		pageSize       *int
		wantPage       int
		wantPageSize   int
		wantItems      int
		wantTotalPages int64
	}{
		{
			name: "defaults", wantPage: 1,
			wantPageSize: TransactionHistoryDefaultPageSize, wantItems: 9, wantTotalPages: 1,
		},
		{
			name: "zero values use defaults", page: transactionHistoryPageInt(0),
			pageSize: transactionHistoryPageInt(0), wantPage: 1,
			wantPageSize: TransactionHistoryDefaultPageSize, wantItems: 9, wantTotalPages: 1,
		},
		{
			name: "negative values clamp to one", page: transactionHistoryPageInt(-7),
			pageSize: transactionHistoryPageInt(-3), wantPage: 1,
			wantPageSize: 1, wantItems: 1, wantTotalPages: 9,
		},
		{
			name: "out of range page", page: transactionHistoryPageInt(99),
			pageSize: transactionHistoryPageInt(2), wantPage: 99,
			wantPageSize: 2, wantItems: 0, wantTotalPages: 5,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			page, err := manager.GetTransactionHistoryPage(context.Background(), TransactionHistoryPageOptions{
				Page: test.page, PageSize: test.pageSize,
			})
			if err != nil {
				t.Fatal(err)
			}
			if page.Page != test.wantPage || page.PageSize != test.wantPageSize ||
				len(page.Items) != test.wantItems || page.TotalPages != test.wantTotalPages ||
				page.TotalItems != 9 {
				t.Fatalf("page = %+v, want page %d size %d items %d pages %d total 9",
					page, test.wantPage, test.wantPageSize, test.wantItems, test.wantTotalPages)
			}
			if page.Items == nil {
				t.Fatal("page items must be a non-nil list")
			}
		})
	}
}

func TestTransactionHistoryPageCountIgnoresPageConstraints(t *testing.T) {
	manager := transactionHistoryPagePopulatedManager(t)
	page, err := manager.GetTransactionHistoryPage(context.Background(), TransactionHistoryPageOptions{
		Page: transactionHistoryPageInt(2), PageSize: transactionHistoryPageInt(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.TotalItems != 9 || page.TotalPages != 9 {
		t.Fatalf("second single-item page = %+v, want one item with unpaginated total 9", page)
	}
}

func TestTransactionHistoryPageOffsetOverride(t *testing.T) {
	manager := transactionHistoryPagePopulatedManager(t)
	pageValue, pageSize, offset := 1, 2, 3
	page, err := manager.GetTransactionHistoryPage(context.Background(), TransactionHistoryPageOptions{
		Page: &pageValue, PageSize: &pageSize, Offset: &offset,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Page != 1 || page.PageSize != 2 || len(page.Items) != 2 ||
		page.TotalItems != 9 || page.TotalPages != 5 {
		t.Fatalf("offset-overridden page = %+v", page)
	}
	overflowingDerivedPage := math.MaxInt
	page, err = manager.GetTransactionHistoryPage(context.Background(), TransactionHistoryPageOptions{
		Page: &overflowingDerivedPage, PageSize: &pageSize, Offset: &offset,
	})
	if err != nil || page.Page != math.MaxInt || len(page.Items) != 2 {
		t.Fatalf("overflow-bypassing offset page = %+v, %v", page, err)
	}

	negative := -1
	_, err = manager.GetTransactionHistoryPage(context.Background(), TransactionHistoryPageOptions{
		Page: &pageValue, PageSize: &pageSize, Offset: &negative,
	})
	if !errors.Is(err, ErrTransactionHistoryPagination) {
		t.Fatalf("negative offset error = %T %v, want pagination error", err, err)
	}
}

func TestTransactionHistoryPageWalletAndAccountLedgerSelection(t *testing.T) {
	manager, selectedWallet, selectedAccount := transactionHistoryPageSplitLedgerManager(t)
	selectedWalletID := selectedWallet.ID

	walletWide, err := manager.GetTransactionHistoryPage(context.Background(), TransactionHistoryPageOptions{
		WalletID: &selectedWalletID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if walletWide.Items == nil || len(walletWide.Items) != 0 ||
		walletWide.TotalItems != 0 || walletWide.TotalPages != 0 {
		t.Fatalf("selected wallet history through default ledger = %+v, want empty totals", walletWide)
	}

	emptyAccountID := ""
	walletWideFromEmptyAccount, err := manager.GetTransactionHistoryPage(
		context.Background(), TransactionHistoryPageOptions{
			WalletID: &selectedWalletID, AccountID: &emptyAccountID,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(walletWideFromEmptyAccount.Items) != 0 || walletWideFromEmptyAccount.TotalItems != 0 {
		t.Fatalf("empty account ID did not use wallet-wide default ledger: %+v", walletWideFromEmptyAccount)
	}

	selectedAccountID := selectedAccount.ID
	accountPage, err := manager.GetTransactionHistoryPage(context.Background(), TransactionHistoryPageOptions{
		WalletID: &selectedWalletID, AccountID: &selectedAccountID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(accountPage.Items) != 8 || accountPage.TotalItems != 8 || accountPage.TotalPages != 1 {
		t.Fatalf("selected account history through account ledger = %+v, want 8 items", accountPage)
	}
}

func TestTransactionHistoryPageUnknownWalletAndAccount(t *testing.T) {
	manager := transactionHistoryPagePopulatedManager(t)

	for _, walletID := range []string{"", "missing-wallet"} {
		_, err := manager.GetTransactionHistoryPage(context.Background(), TransactionHistoryPageOptions{
			WalletID: &walletID,
		})
		var notLoaded *WalletNotLoadedError
		if !errors.As(err, &notLoaded) || notLoaded.WalletID != walletID {
			t.Errorf("wallet %q error = %T %v, want WalletNotLoadedError", walletID, err, err)
		}
	}

	missingAccount := "missing-account"
	_, err := manager.GetTransactionHistoryPage(context.Background(), TransactionHistoryPageOptions{
		AccountID: &missingAccount,
	})
	if err == nil || err.Error() != "Couldn't find account: missing-account." {
		t.Fatalf("unknown account error = %T %v", err, err)
	}
}

func TestTransactionHistoryPageEmptyTotals(t *testing.T) {
	ledger := transactionHistoryPageEmptyLedger(t)
	account := &Account{ID: "empty-account", Network: keys.MainNet, ledger: ledger}
	wallet := NewWallet(WithWalletName("empty-wallet"), WithWalletAccounts([]*Account{account}))
	manager := &WalletManager{Wallets: []*Wallet{wallet}}

	page, err := manager.GetTransactionHistoryPage(context.Background(), TransactionHistoryPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Items == nil || len(page.Items) != 0 || page.TotalItems != 0 || page.TotalPages != 0 ||
		page.Page != 1 || page.PageSize != TransactionHistoryDefaultPageSize {
		t.Fatalf("empty history page = %+v", page)
	}
}

func TestTransactionHistoryPageRejectsOffsetOverflow(t *testing.T) {
	manager := transactionHistoryPagePopulatedManager(t)
	pageValue, pageSize := math.MaxInt, 2
	page, err := manager.GetTransactionHistoryPage(context.Background(), TransactionHistoryPageOptions{
		Page: &pageValue, PageSize: &pageSize,
	})
	if !errors.Is(err, ErrTransactionHistoryPagination) || page.Items != nil ||
		page.Page != 0 || page.PageSize != 0 || page.TotalPages != 0 || page.TotalItems != 0 {
		t.Fatalf("overflow page = %+v, %v, want zero page and pagination error", page, err)
	}
}

func transactionHistoryPagePopulatedManager(t *testing.T) *WalletManager {
	t.Helper()
	database, _ := transactionHistoryOracleFixture(t)
	ledger := transactionHistorySummaryTestLedger()
	ledger.Database = database
	accountA := &Account{ID: "account-a", Network: keys.MainNet, ledger: ledger}
	accountB := &Account{ID: "account-b", Network: keys.MainNet, ledger: ledger}
	wallet := NewWallet(
		WithWalletName("default-wallet"), WithWalletAccounts([]*Account{accountA, accountB}),
	)
	return &WalletManager{
		Wallets: []*Wallet{wallet}, Ledgers: map[keys.Network]*Ledger{keys.MainNet: ledger},
		ledgerOrder: []*Ledger{ledger},
	}
}

func transactionHistoryPageSplitLedgerManager(
	t *testing.T,
) (*WalletManager, *Wallet, *Account) {
	t.Helper()
	defaultLedger := transactionHistoryPageEmptyLedger(t)
	selectedDatabase, _ := transactionHistoryOracleFixture(t)
	selectedLedger := transactionHistorySummaryTestLedger()
	selectedLedger.Network = keys.TestNet
	selectedLedger.Database = selectedDatabase

	defaultAccount := &Account{ID: "default-account", Network: keys.MainNet, ledger: defaultLedger}
	defaultWallet := NewWallet(
		WithWalletName("default-wallet"), WithWalletAccounts([]*Account{defaultAccount}),
	)
	selectedAccount := &Account{ID: "account-a", Network: keys.TestNet, ledger: selectedLedger}
	selectedWallet := NewWallet(
		WithWalletName("selected-wallet"), WithWalletAccounts([]*Account{selectedAccount}),
	)
	manager := &WalletManager{
		Wallets: []*Wallet{defaultWallet, selectedWallet},
		Ledgers: map[keys.Network]*Ledger{
			keys.MainNet: defaultLedger, keys.TestNet: selectedLedger,
		},
		// Registration order deliberately disagrees with the default account.
		ledgerOrder: []*Ledger{selectedLedger, defaultLedger},
	}
	return manager, selectedWallet, selectedAccount
}

func transactionHistoryPageEmptyLedger(t *testing.T) *Ledger {
	t.Helper()
	database, err := ledgerdb.Open(
		context.Background(), filepath.Join(t.TempDir(), ledgerdb.Filename),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(context.Background()); err != nil {
			t.Errorf("close empty transaction history database: %v", err)
		}
	})
	ledger := transactionHistorySummaryTestLedger()
	ledger.Database = database
	return ledger
}

func transactionHistoryPageInt(value int) *int {
	return &value
}
