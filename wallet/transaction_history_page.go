package wallet

import (
	"context"
	"errors"
	"fmt"
	"math"

	"lbry/daemon/wallet/ledgerdb"
)

const TransactionHistoryDefaultPageSize = 20

var ErrTransactionHistoryPagination = errors.New("invalid transaction history pagination")

type TransactionHistoryPageOptions struct {
	AccountID *string
	WalletID  *string
	Page      *int
	PageSize  *int
	// Offset overrides the page-derived query offset. It is used by the RPC
	// adapter for legacy float page values whose effective SQLite offset is an
	// integer even though the page itself is not.
	Offset *int
}

type TransactionHistoryPage struct {
	Items      []TransactionHistoryItem `json:"items"`
	Page       int                      `json:"page"`
	PageSize   int                      `json:"page_size"`
	TotalPages int64                    `json:"total_pages"`
	TotalItems int64                    `json:"total_items"`
}

// GetTransactionHistoryPage implements the selection and sequential
// records-then-count behavior wrapped by the legacy transaction_list RPC.
func (manager *WalletManager) GetTransactionHistoryPage(
	ctx context.Context, options TransactionHistoryPageOptions,
) (TransactionHistoryPage, error) {
	if manager == nil {
		return TransactionHistoryPage{}, ErrDefaultWalletMissing
	}
	selectedWallet, err := manager.GetWalletOrDefault(options.WalletID)
	if err != nil {
		return TransactionHistoryPage{}, err
	}
	if selectedWallet == nil {
		return TransactionHistoryPage{}, ErrDefaultWalletMissing
	}

	page := transactionHistoryPageValue(options.Page, 1)
	pageSize := transactionHistoryPageValue(options.PageSize, TransactionHistoryDefaultPageSize)
	var offset int
	if options.Offset != nil {
		if *options.Offset < 0 {
			return TransactionHistoryPage{}, fmt.Errorf(
				"%w: offset cannot be negative", ErrTransactionHistoryPagination,
			)
		}
		offset = *options.Offset
	} else {
		if page > 1 && page-1 > math.MaxInt/pageSize {
			return TransactionHistoryPage{}, fmt.Errorf(
				"%w: offset exceeds platform integer range", ErrTransactionHistoryPagination,
			)
		}
		offset = pageSize * (page - 1)
	}
	query := ledgerdb.TransactionQuery{Limit: &pageSize, Offset: &offset}

	var items []TransactionHistoryItem
	var totalItems int64
	if options.AccountID != nil && *options.AccountID != "" {
		account, err := selectedWallet.Account(*options.AccountID)
		if err != nil {
			return TransactionHistoryPage{}, err
		}
		items, err = account.GetTransactionHistory(ctx, query)
		if err != nil {
			return TransactionHistoryPage{}, err
		}
		totalItems, err = account.GetTransactionHistoryCount(ctx, query)
		if err != nil {
			return TransactionHistoryPage{}, err
		}
	} else {
		ledger := manager.DefaultLedger()
		if ledger == nil {
			return TransactionHistoryPage{}, fmt.Errorf(
				"%w: default ledger is unavailable", ErrTransactionHistoryUnavailable,
			)
		}
		accountIDs, err := transactionWalletAccountIDs(selectedWallet)
		if err != nil {
			return TransactionHistoryPage{}, err
		}
		query.AccountIDs = accountIDs
		items, err = ledger.GetTransactionHistory(ctx, TransactionHistoryOptions{
			Query: query, AnnotationAccountIDs: accountIDs,
			ChannelKeyAccounts: append([]*Account(nil), selectedWallet.Accounts...),
		})
		if err != nil {
			return TransactionHistoryPage{}, err
		}
		totalItems, err = ledger.GetTransactionHistoryCount(ctx, query)
		if err != nil {
			return TransactionHistoryPage{}, err
		}
	}

	totalPages := totalItems / int64(pageSize)
	if totalItems%int64(pageSize) != 0 {
		totalPages++
	}
	return TransactionHistoryPage{
		Items: items, Page: page, PageSize: pageSize,
		TotalPages: totalPages, TotalItems: totalItems,
	}, nil
}

func transactionHistoryPageValue(value *int, defaultValue int) int {
	if value == nil || *value == 0 {
		return defaultValue
	}
	if *value < 1 {
		return 1
	}
	return *value
}
