package rpc

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	walletpkg "lbry/daemon/wallet"
	"lbry/daemon/wallet/ledgerdb"
)

func (rpcServer *RPCServer) handlePurchaseList(w http.ResponseWriter, params any) {
	manager := rpcServer.walletManagerProvider()
	if manager == nil {
		panic(transactionListApplicationError{
			name:    "ComponentsNotStartedError",
			message: `the following required components have not yet started: ["wallet"]`,
		})
	}
	normalized := params.(normalizedRPCParams)
	walletID, err := transactionListWalletID(normalized.named["wallet_id"])
	if err != nil {
		panic(err)
	}
	selectedWallet, err := manager.GetWalletOrDefault(walletID)
	if err != nil {
		panic(purchaseListRPCError(err))
	}
	if selectedWallet == nil {
		panic(transactionListNoneAttributeError("accounts"))
	}
	accountID, err := transactionListAccountID(normalized.named["account_id"])
	if err != nil {
		panic(err)
	}
	if accountID != nil {
		if _, err := selectedWallet.Account(*accountID); err != nil {
			panic(purchaseListRPCError(err))
		}
	}
	if manager.DefaultLedger() == nil {
		panic(transactionListNoneAttributeError("get_purchases"))
	}
	claimID, err := purchaseListClaimID(normalized.named["claim_id"])
	if err != nil {
		panic(err)
	}
	pagination, err := transactionListPaginationParameters(normalized.named)
	if err != nil {
		panic(err)
	}
	result, err := manager.GetPurchasePage(normalized.ctx, walletpkg.PurchasePageOptions{
		AccountID: accountID,
		WalletID:  walletID,
		ClaimID:   claimID,
		Page:      pagination.page,
		PageSize:  pagination.pageSize,
		Offset:    &pagination.offset,
		Resolve:   transactionListTruthy(normalized.named["resolve"]),
	})
	if err != nil {
		panic(purchaseListRPCError(err))
	}
	if result.Ledger == nil {
		panic(transactionListNoneAttributeError("get_purchases"))
	}

	items := make([]any, len(result.Items))
	for index, output := range result.Items {
		encoded, encodeErr := result.Ledger.LegacyTransactionOutputJSONWithOptions(
			output,
			walletpkg.LegacyTransactionJSONOptions{
				IncludeProtobuf: transactionListTruthy(normalized.includeProtobuf),
			},
		)
		if encodeErr != nil {
			panic(encodeErr)
		}
		items[index] = encoded
	}
	sendResultResponse(w, map[string]any{
		"items":     items,
		"page":      pagination.wirePage,
		"page_size": pagination.wirePageSize,
		"total_pages": transactionListPythonTotalPages(
			result.TotalItems, pagination.pageSizeNumber,
		),
		"total_items": result.TotalItems,
	})
}

func purchaseListClaimID(value any) (*string, error) {
	if !transactionListTruthy(value) {
		return nil, nil
	}
	claimID, ok := value.(string)
	if !ok {
		return nil, transactionListApplicationError{
			name: "InterfaceError",
			message: fmt.Sprintf(
				"Error binding parameter 1: type '%s' is not supported", pythonTypeName(value),
			),
		}
	}
	return &claimID, nil
}

func purchaseListRPCError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, context.Canceled):
		return resolveRPCError(err)
	case errors.Is(err, walletpkg.ErrTransactionPurchaseQuery),
		errors.Is(err, walletpkg.ErrTransactionQueryUnavailable),
		errors.Is(err, ledgerdb.ErrNotOpen):
		return transactionListNoneAttributeError("get_purchases")
	case errors.Is(err, walletpkg.ErrPurchaseAccountsRequired):
		return transactionListApplicationError{name: "AssertionError", message: err.Error()}
	default:
		return transactionListRPCError(err)
	}
}
