package wallet

import (
	"context"
	"fmt"

	"lbry/daemon/wallet/ledgerdb"
)

const TransactionOutputDefaultPageSize = 20

type TransactionOutputPageOptions struct {
	AccountID   *string
	WalletID    *string
	Page        int
	PageSize    int
	Offset      *int
	NoTotals    bool
	Resolve     bool
	Query       ledgerdb.OutputQuery
	BeforeCount func(context.Context, *Ledger, []*TransactionOutput) error
}

type TransactionOutputPage struct {
	Ledger     *Ledger
	Items      []*TransactionOutput
	Page       int
	PageSize   int
	TotalPages *int64
	TotalItems *int64
}

// GetTransactionOutputPage preserves paginate_rows' records-before-count call
// order and its optional totals behavior.
func (manager *WalletManager) GetTransactionOutputPage(
	ctx context.Context, options TransactionOutputPageOptions,
) (TransactionOutputPage, error) {
	wallet, err := manager.transactionOutputWallet(options.WalletID)
	if err != nil {
		return TransactionOutputPage{}, err
	}
	page := options.Page
	if page < 1 {
		page = 1
	}
	pageSize := options.PageSize
	if pageSize == 0 {
		pageSize = TransactionOutputDefaultPageSize
	} else if pageSize < 1 {
		pageSize = 1
	}
	query := options.Query
	limit, offset := pageSize, pageSize*(page-1)
	if options.Offset != nil {
		offset = *options.Offset
	}
	query.Limit, query.Offset = &limit, &offset

	var ledger *Ledger
	var items []*TransactionOutput
	var count func(context.Context, ledgerdb.OutputQuery) (int64, error)
	if options.AccountID != nil && *options.AccountID != "" {
		account, accountErr := wallet.Account(*options.AccountID)
		if accountErr != nil {
			return TransactionOutputPage{}, accountErr
		}
		ledger = account.ledger
		items, err = account.GetTransactionOutputs(ctx, TransactionOutputListOptions{
			Query: query, Resolve: options.Resolve,
		})
		count = account.CountTransactionOutputs
	} else {
		ledger = manager.DefaultLedger()
		if ledger == nil {
			return TransactionOutputPage{}, fmt.Errorf(
				"%w: default ledger is unavailable", ErrTransactionOutputQueryUnavailable,
			)
		}
		accountIDs, accountErr := transactionWalletAccountIDs(wallet)
		if accountErr != nil {
			return TransactionOutputPage{}, accountErr
		}
		query.AccountIDs = accountIDs
		if len(accountIDs) == 0 && !query.IncludeReceivedTips {
			query.IncludeIsMyInput = false
			query.IncludeIsMyOutput = false
		}
		items, err = ledger.GetTransactionOutputs(ctx, TransactionOutputListOptions{
			Query: query, Wallet: wallet, Resolve: options.Resolve,
		})
		count = ledger.CountTransactionOutputs
	}
	if err != nil {
		return TransactionOutputPage{}, err
	}
	if options.BeforeCount != nil {
		if err := options.BeforeCount(ctx, ledger, items); err != nil {
			return TransactionOutputPage{}, err
		}
	}
	result := TransactionOutputPage{
		Ledger: ledger, Items: items, Page: page, PageSize: pageSize,
	}
	if options.NoTotals {
		return result, nil
	}
	totalItems, err := count(ctx, query)
	if err != nil {
		return TransactionOutputPage{}, err
	}
	totalPages := totalItems / int64(pageSize)
	if totalItems%int64(pageSize) != 0 {
		totalPages++
	}
	result.TotalItems = &totalItems
	result.TotalPages = &totalPages
	return result, nil
}

func (manager *WalletManager) GetTransactionOutputSum(
	ctx context.Context, walletID, accountID *string, query ledgerdb.OutputQuery,
) (int64, error) {
	wallet, err := manager.transactionOutputWallet(walletID)
	if err != nil {
		return 0, err
	}
	ledger := manager.DefaultLedger()
	if ledger == nil {
		return 0, fmt.Errorf(
			"%w: default ledger is unavailable", ErrTransactionOutputQueryUnavailable,
		)
	}
	if accountID != nil && *accountID != "" {
		account, err := wallet.Account(*accountID)
		if err != nil {
			return 0, err
		}
		accountKey, err := transactionAccountQueryID(account)
		if err != nil {
			return 0, err
		}
		query.AccountIDs = []string{accountKey}
		return ledger.SumTransactionOutputs(ctx, query)
	}
	accountIDs, err := transactionWalletAccountIDs(wallet)
	if err != nil {
		return 0, err
	}
	query.AccountIDs = accountIDs
	return ledger.SumTransactionOutputs(ctx, query)
}

func transactionAccountQueryID(account *Account) (string, error) {
	if account == nil {
		return "", ErrNilWalletAccount
	}
	if account.PublicKey != nil {
		return account.PublicKey.Address(), nil
	}
	return account.ID, nil
}

func (manager *WalletManager) transactionOutputWallet(walletID *string) (*Wallet, error) {
	if manager == nil {
		return nil, ErrDefaultWalletMissing
	}
	wallet, err := manager.GetWalletOrDefault(walletID)
	if err != nil {
		return nil, err
	}
	if wallet == nil {
		return nil, ErrDefaultWalletMissing
	}
	return wallet, nil
}
