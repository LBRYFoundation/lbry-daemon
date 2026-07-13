package wallet

import (
	"context"
	"fmt"
	"math"

	"lbry/daemon/wallet/ledgerdb"
)

const PurchaseDefaultPageSize = 20

type PurchasePageOptions struct {
	AccountID *string
	WalletID  *string
	ClaimID   *string
	Page      int
	PageSize  int
	Offset    *int
	Resolve   bool
}

type PurchasePage struct {
	Ledger     *Ledger
	Items      []*TransactionOutput
	Page       int
	PageSize   int
	TotalPages int64
	TotalItems int64
}

// GetPurchasePage mirrors jsonrpc_purchase_list and paginate_rows. The
// selected wallet supplies accounts, but WalletManager.ledger always supplies
// the query implementation, even when a selected account belongs to another
// ledger.
func (manager *WalletManager) GetPurchasePage(
	ctx context.Context, options PurchasePageOptions,
) (PurchasePage, error) {
	if manager == nil {
		return PurchasePage{}, ErrDefaultWalletMissing
	}
	wallet, err := manager.GetWalletOrDefault(options.WalletID)
	if err != nil {
		return PurchasePage{}, err
	}
	if wallet == nil {
		return PurchasePage{}, ErrDefaultWalletMissing
	}

	accounts := wallet.Accounts
	if options.AccountID != nil && *options.AccountID != "" {
		account, accountErr := wallet.Account(*options.AccountID)
		if accountErr != nil {
			return PurchasePage{}, accountErr
		}
		accounts = []*Account{account}
	}
	ledger := manager.DefaultLedger()
	if ledger == nil {
		return PurchasePage{}, fmt.Errorf(
			"%w: default ledger is unavailable", ErrTransactionPurchaseQuery,
		)
	}

	page, pageSize := options.Page, options.PageSize
	if page < 1 {
		page = 1
	}
	if pageSize == 0 {
		pageSize = PurchaseDefaultPageSize
	} else if pageSize < 1 {
		pageSize = 1
	}
	if page > 1 && page-1 > math.MaxInt/pageSize {
		return PurchasePage{}, fmt.Errorf(
			"%w: purchase offset exceeds platform integer range",
			ErrTransactionPurchaseQuery,
		)
	}
	offset := pageSize * (page - 1)
	if options.Offset != nil {
		if *options.Offset < 0 {
			return PurchasePage{}, fmt.Errorf(
				"%w: purchase offset cannot be negative", ErrTransactionPurchaseQuery,
			)
		}
		offset = *options.Offset
	}
	query := ledgerdb.TransactionQuery{Limit: &pageSize, Offset: &offset}
	if options.ClaimID != nil {
		claimID := *options.ClaimID
		query.PurchasedClaimID = &claimID
	}

	items, totalItems, err := loadPurchasePage(
		ctx, query, accounts, wallet,
		func(ctx context.Context, listOptions PurchaseListOptions) ([]*TransactionOutput, error) {
			listOptions.Resolve = options.Resolve
			return ledger.GetPurchases(ctx, listOptions)
		},
		ledger.CountPurchases,
	)
	if err != nil {
		return PurchasePage{}, err
	}
	totalPages := totalItems / int64(pageSize)
	if totalItems%int64(pageSize) != 0 {
		totalPages++
	}
	return PurchasePage{
		Ledger: ledger, Items: items, Page: page, PageSize: pageSize,
		TotalPages: totalPages, TotalItems: totalItems,
	}, nil
}

func loadPurchasePage(
	ctx context.Context,
	query ledgerdb.TransactionQuery,
	accounts []*Account,
	wallet *Wallet,
	getRecords func(context.Context, PurchaseListOptions) ([]*TransactionOutput, error),
	getCount func(context.Context, ledgerdb.TransactionQuery, []*Account) (int64, error),
) ([]*TransactionOutput, int64, error) {
	items, err := getRecords(ctx, PurchaseListOptions{
		Query: query, Accounts: accounts, Wallet: wallet,
	})
	if err != nil {
		return nil, 0, err
	}
	totalItems, err := getCount(ctx, query, accounts)
	if err != nil {
		return nil, 0, err
	}
	return items, totalItems, nil
}
