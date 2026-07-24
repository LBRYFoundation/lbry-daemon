package wallet

import (
	"context"
	"reflect"
	"testing"

	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/ledgerdb"
)

func TestCollectionPageUsesPinnedLedgerSelectionAndFilters(t *testing.T) {
	ctx := context.Background()
	defaultLedger := newTransactionOutputQueryLedger(t)
	selectedLedger := newTransactionOutputQueryLedger(t)
	selectedLedger.Network = keys.TestNet
	accountA, addressA := newTransactionOutputQueryAccount(t, defaultLedger, 0x41, "account-a")
	accountB, addressBTest := newTransactionOutputQueryAccount(t, selectedLedger, 0x42, "account-b")
	addressBMain := "owned-b-main"
	if err := defaultLedger.Database.AddKeys(ctx, accountB.PublicKey.Address(), []ledgerdb.AddressKey{{
		Address: addressBMain, PublicKey: []byte{0x42}, ChainCode: []byte{0x43},
	}}); err != nil {
		t.Fatal(err)
	}

	walletA := NewWallet(WithWalletName("wallet-a"), WithWalletAccounts([]*Account{accountA}))
	walletB := NewWallet(WithWalletName("wallet-b"), WithWalletAccounts([]*Account{accountB}))
	manager := &WalletManager{Wallets: []*Wallet{walletA, walletB}}

	defaultCollection := persistTransactionOutputQueryOutput(
		t, defaultLedger, addressA, 101, 101, 4101,
		TransactionOutputTypeCollection, 10, false,
	)
	persistTransactionOutputQueryOutput(
		t, defaultLedger, addressA, 102, 102, 4102,
		TransactionOutputTypeStream, 11, false,
	)
	spentCollection := persistTransactionOutputQueryOutput(
		t, defaultLedger, addressA, 103, 103, 4103,
		TransactionOutputTypeCollection, 12, false,
	)
	markTransactionOutputQuerySpent(t, defaultLedger, addressA, spentCollection.ID(), 4199)

	mainFirst := persistTransactionOutputQueryOutput(
		t, defaultLedger, addressBMain, 201, 201, 4201,
		TransactionOutputTypeCollection, 20, false,
	)
	mainSecond := persistTransactionOutputQueryOutput(
		t, defaultLedger, addressBMain, 202, 202, 4202,
		TransactionOutputTypeCollection, 19, false,
	)
	testCollection := persistTransactionOutputQueryOutput(
		t, selectedLedger, addressBTest, 301, 301, 4301,
		TransactionOutputTypeCollection, 30, false,
	)

	page, err := manager.GetCollectionPage(ctx, CollectionPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertCollectionPage(t, page, defaultLedger, []string{defaultCollection.ID()}, 1, 20, 1, 1)

	pageSize, offset := 1, 0
	page, err = manager.GetCollectionPage(ctx, CollectionPageOptions{
		WalletID: &walletB.ID, Page: 1, PageSize: pageSize, Offset: &offset,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCollectionPage(t, page, defaultLedger, []string{mainFirst.ID()}, 1, 1, 2, 2)

	offset = 1
	page, err = manager.GetCollectionPage(ctx, CollectionPageOptions{
		WalletID: &walletB.ID, Page: 2, PageSize: pageSize, Offset: &offset,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCollectionPage(t, page, defaultLedger, []string{mainSecond.ID()}, 2, 1, 2, 2)

	page, err = manager.GetCollectionPage(ctx, CollectionPageOptions{
		WalletID: &walletB.ID, AccountID: &accountB.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCollectionPage(t, page, selectedLedger, []string{testCollection.ID()}, 1, 20, 1, 1)
}

func TestCollectionPageSelectionErrors(t *testing.T) {
	manager := transactionHistoryPagePopulatedManager(t)
	missingWallet := "missing-wallet"
	if _, err := manager.GetCollectionPage(context.Background(), CollectionPageOptions{
		WalletID: &missingWallet,
	}); err == nil {
		t.Fatal("missing collection wallet unexpectedly succeeded")
	}
	missingAccount := "missing-account"
	if _, err := manager.GetCollectionPage(context.Background(), CollectionPageOptions{
		AccountID: &missingAccount,
	}); err == nil || err.Error() != "Couldn't find account: missing-account." {
		t.Fatalf("missing collection account error = %v", err)
	}
}

func assertCollectionPage(
	t *testing.T, page TransactionOutputPage, ledger *Ledger, outputIDs []string,
	wantPage, wantPageSize int, wantTotalItems, wantTotalPages int64,
) {
	t.Helper()
	actualIDs := make([]string, len(page.Items))
	for index, output := range page.Items {
		actualIDs[index] = output.ID()
	}
	if page.Ledger != ledger || !reflect.DeepEqual(actualIDs, outputIDs) ||
		page.Page != wantPage || page.PageSize != wantPageSize ||
		page.TotalItems == nil || *page.TotalItems != wantTotalItems ||
		page.TotalPages == nil || *page.TotalPages != wantTotalPages {
		t.Fatalf(
			"collection page = ledger %p IDs %v page %d size %d totals %v/%v; want ledger %p IDs %v page %d size %d totals %d/%d",
			page.Ledger, actualIDs, page.Page, page.PageSize, page.TotalItems, page.TotalPages,
			ledger, outputIDs, wantPage, wantPageSize, wantTotalItems, wantTotalPages,
		)
	}
}
