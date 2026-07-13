package rpc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	walletpkg "lbry/daemon/wallet"
	"lbry/daemon/wallet/ledgerdb"
)

var publicTXOTypes = map[string]int64{
	"other":      walletpkg.TransactionOutputTypeOther,
	"stream":     walletpkg.TransactionOutputTypeStream,
	"channel":    walletpkg.TransactionOutputTypeChannel,
	"support":    walletpkg.TransactionOutputTypeSupport,
	"purchase":   walletpkg.TransactionOutputTypePurchase,
	"collection": walletpkg.TransactionOutputTypeCollection,
	"repost":     walletpkg.TransactionOutputTypeRepost,
}

var txoConstraintNames = map[string]struct{}{
	"type": {}, "txid": {}, "claim_id": {}, "channel_id": {},
	"not_channel_id": {}, "name": {}, "reposted_claim_id": {},
	"is_spent": {}, "is_not_spent": {}, "has_source": {}, "has_no_source": {},
	"is_my_input_or_output": {}, "exclude_internal_transfers": {},
	"is_my_output": {}, "is_not_my_output": {},
	"is_my_input": {}, "is_not_my_input": {},
}

var txoListParameterNames = map[string]struct{}{
	"account_id": {}, "wallet_id": {}, "page": {}, "page_size": {},
	"resolve": {}, "order_by": {}, "no_totals": {}, "include_received_tips": {},
}

var txoSumParameterNames = map[string]struct{}{
	"account_id": {}, "wallet_id": {},
}

func (rpcServer *RPCServer) handleTXOList(w http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	manager, _, walletID, accountID := rpcServer.txoSelection(
		normalized, "get_txos", false,
	)
	order, err := txoOutputOrder(normalized.named["order_by"])
	if err != nil {
		panic(err)
	}
	query, err := txoOutputQuery(normalized, txoListParameterNames)
	if err != nil {
		panic(err)
	}
	query.IncludeIsSpent = true
	query.IncludeIsMyInput = true
	query.IncludeIsMyOutput = true
	query.IncludeReceivedTips = transactionListTruthy(normalized.named["include_received_tips"])
	query.Order = order
	pagination, err := transactionListPaginationParameters(normalized.named)
	if err != nil {
		panic(err)
	}

	result, err := manager.GetTransactionOutputPage(
		normalized.ctx,
		walletpkg.TransactionOutputPageOptions{
			AccountID: accountID,
			WalletID:  walletID,
			Page:      pagination.page,
			PageSize:  pagination.pageSize,
			Offset:    &pagination.offset,
			NoTotals:  transactionListTruthy(normalized.named["no_totals"]),
			Resolve:   transactionListTruthy(normalized.named["resolve"]),
			Query:     query,
		},
	)
	if err != nil {
		panic(txoListRPCError(err, "get_txos"))
	}
	if result.Ledger == nil {
		panic(transactionListNoneAttributeError("get_txos"))
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
	wire := map[string]any{
		"items": items, "page": pagination.wirePage, "page_size": pagination.wirePageSize,
	}
	if result.TotalItems != nil {
		wire["total_items"] = *result.TotalItems
		wire["total_pages"] = transactionListPythonTotalPages(
			*result.TotalItems, pagination.pageSizeNumber,
		)
	}
	sendResultResponse(w, wire)
}

func (rpcServer *RPCServer) handleTXOSum(w http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	manager, _, walletID, accountID := rpcServer.txoSelection(
		normalized, "get_txo_sum", true,
	)
	query, err := txoOutputQuery(normalized, txoSumParameterNames)
	if err != nil {
		panic(err)
	}
	amount, err := manager.GetTransactionOutputSum(
		normalized.ctx, walletID, accountID, query,
	)
	if err != nil {
		panic(txoRPCError(err, "get_txo_sum"))
	}
	sendResultResponse(w, amount)
}

func (rpcServer *RPCServer) txoSelection(
	normalized normalizedRPCParams, missingLedgerAttribute string, sum bool,
) (*walletpkg.WalletManager, *walletpkg.Wallet, *string, *string) {
	manager := rpcServer.walletManagerProvider()
	if manager == nil {
		panic(transactionListApplicationError{
			name:    "ComponentsNotStartedError",
			message: `the following required components have not yet started: ["wallet"]`,
		})
	}
	walletID, err := transactionListWalletID(normalized.named["wallet_id"])
	if err != nil {
		panic(err)
	}
	selectedWallet, err := manager.GetWalletOrDefault(walletID)
	if err != nil {
		panic(txoRPCError(err, missingLedgerAttribute))
	}
	if selectedWallet == nil {
		panic(transactionListNoneAttributeError(missingLedgerAttribute))
	}
	if sum && manager.DefaultLedger() == nil {
		panic(transactionListNoneAttributeError(missingLedgerAttribute))
	}
	accountID, err := transactionListAccountID(normalized.named["account_id"])
	if err != nil {
		panic(err)
	}
	if accountID != nil {
		if _, err := selectedWallet.Account(*accountID); err != nil {
			panic(txoRPCError(err, missingLedgerAttribute))
		}
	}
	if !sum && accountID == nil && manager.DefaultLedger() == nil {
		panic(transactionListNoneAttributeError(missingLedgerAttribute))
	}
	return manager, selectedWallet, walletID, accountID
}

func txoOutputQuery(
	normalized normalizedRPCParams, methodParameters map[string]struct{},
) (ledgerdb.OutputQuery, error) {
	if unknown := txoUnknownConstraint(normalized, methodParameters); unknown != "" {
		return ledgerdb.OutputQuery{}, transactionListApplicationError{
			name: "TypeError",
			message: fmt.Sprintf(
				"Daemon._constrain_txo_from_kwargs() got an unexpected keyword argument '%s'",
				unknown,
			),
		}
	}
	query := ledgerdb.OutputQuery{}
	var err error
	if query.Types, err = txoTypeValues(normalized.named["type"]); err != nil {
		return ledgerdb.OutputQuery{}, err
	}
	filters := []struct {
		name        string
		destination *[]string
	}{
		{"channel_id", &query.ChannelIDs},
		{"not_channel_id", &query.NotChannelIDs},
		{"claim_id", &query.ClaimIDs},
		{"name", &query.ClaimNames},
		{"txid", &query.TXIDs},
		{"reposted_claim_id", &query.RepostedClaimIDs},
	}
	for _, filter := range filters {
		name, destination := filter.name, filter.destination
		*destination, err = txoStringValues(normalized.named[name])
		if err != nil {
			return ledgerdb.OutputQuery{}, err
		}
	}
	if transactionListTruthy(normalized.named["is_spent"]) {
		query.IsSpent = txoBool(true)
	} else if transactionListTruthy(normalized.named["is_not_spent"]) {
		query.IsSpent = txoBool(false)
	}
	if transactionListTruthy(normalized.named["has_source"]) {
		query.HasSource = txoBool(true)
	} else if transactionListTruthy(normalized.named["has_no_source"]) {
		query.HasSource = txoBool(false)
	}
	query.ExcludeInternalTransfers = transactionListTruthy(
		normalized.named["exclude_internal_transfers"],
	)
	if txoExactlyTrue(normalized.named["is_my_input_or_output"]) {
		query.IsMyInputOrOutput = true
		query.SkipAccountOutputConstraint = true
	} else {
		if txoExactlyTrue(normalized.named["is_my_input"]) {
			query.IsMyInput = txoBool(true)
		} else if txoExactlyTrue(normalized.named["is_not_my_input"]) {
			query.IsMyInput = txoBool(false)
		}
		if txoExactlyTrue(normalized.named["is_my_output"]) {
			query.IsMyOutput = txoBool(true)
		} else if txoExactlyTrue(normalized.named["is_not_my_output"]) {
			query.IsMyOutput = txoBool(false)
		}
	}
	return query, nil
}

func txoOutputOrder(value any) (ledgerdb.OutputOrder, error) {
	if value == nil {
		return ledgerdb.OutputOrderDefault, nil
	}
	if order, ok := value.(string); ok {
		switch order {
		case "name":
			return ledgerdb.OutputOrderName, nil
		case "height":
			return ledgerdb.OutputOrderHeight, nil
		case "amount":
			return ledgerdb.OutputOrderAmount, nil
		case "none":
			return ledgerdb.OutputOrderNone, nil
		}
	}
	return ledgerdb.OutputOrderDefault, transactionListApplicationError{
		name: "ValueError", message: fmt.Sprintf("'%s' is not a valid --order_by value.", pythonStr(value)),
	}
}

func txoUnknownConstraint(
	normalized normalizedRPCParams, methodParameters map[string]struct{},
) string {
	seen := make(map[string]struct{}, len(normalized.kwargs))
	for _, name := range normalized.orderedKwargs {
		if _, present := normalized.kwargs[name]; !present {
			continue
		}
		seen[name] = struct{}{}
		if _, known := methodParameters[name]; known {
			continue
		}
		if _, known := txoConstraintNames[name]; !known {
			return name
		}
	}
	unknown := make([]string, 0)
	for name := range normalized.kwargs {
		if _, alreadySeen := seen[name]; alreadySeen {
			continue
		}
		if _, known := methodParameters[name]; known {
			continue
		}
		if _, known := txoConstraintNames[name]; !known {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	if len(unknown) == 0 {
		return ""
	}
	return unknown[0]
}

func txoTypeValues(value any) ([]int64, error) {
	values, _, present := txoListValues(value)
	if !present {
		return nil, nil
	}
	result := make([]int64, len(values))
	for index, item := range values {
		name, ok := item.(string)
		outputType, exists := publicTXOTypes[name]
		if !ok || !exists {
			return nil, transactionListApplicationError{
				name: "KeyError", message: pythonRepr(item),
			}
		}
		result[index] = outputType
	}
	return result, nil
}

func txoStringValues(value any) ([]string, error) {
	values, _, present := txoListValues(value)
	if !present {
		return nil, nil
	}
	result := make([]string, len(values))
	for index, item := range values {
		text, ok := item.(string)
		if !ok {
			return nil, transactionListApplicationError{
				name: "InterfaceError", message: "Error binding parameter: unsupported type",
			}
		}
		result[index] = text
	}
	return result, nil
}

func txoListValues(value any) (values []any, scalar, present bool) {
	if value == nil {
		return nil, false, false
	}
	if list, ok := value.([]any); ok {
		if len(list) == 0 {
			return nil, false, false
		}
		return list, false, true
	}
	return []any{value}, true, true
}

func txoExactlyTrue(value any) bool {
	boolean, ok := value.(bool)
	return ok && boolean
}

func txoBool(value bool) *bool { return &value }

func txoRPCError(err error, missingLedgerAttribute string) error {
	var notLoaded *walletpkg.WalletNotLoadedError
	switch {
	case errors.As(err, &notLoaded):
		return transactionListApplicationError{name: "WalletNotLoadedError", message: err.Error()}
	case errors.Is(err, walletpkg.ErrDefaultWalletMissing):
		return transactionListNoneAttributeError(missingLedgerAttribute)
	case errors.Is(err, walletpkg.ErrTransactionOutputQueryUnavailable):
		return transactionListNoneAttributeError(missingLedgerAttribute)
	case errors.Is(err, ledgerdb.ErrOutputAnnotationAccountsRequired):
		return transactionListApplicationError{name: "AssertionError", message: err.Error()}
	case strings.HasPrefix(err.Error(), "Couldn't find account:"):
		return transactionListApplicationError{name: "ValueError", message: err.Error()}
	default:
		return err
	}
}

func txoListRPCError(err error, missingLedgerAttribute string) error {
	switch {
	case errors.Is(err, context.Canceled):
		return resolveRPCError(err)
	case errors.Is(err, walletpkg.ErrLocalTransactionResolve):
		return resolveRPCError(txoListResolutionCause(err))
	case errors.Is(err, walletpkg.ErrLocalSupportClaimSearch):
		return claimSearchRPCError(txoListResolutionCause(err))
	default:
		return txoRPCError(err, missingLedgerAttribute)
	}
}

func txoListResolutionCause(err error) error {
	var staged interface{ LocalResolutionCause() error }
	if errors.As(err, &staged) {
		return staged.LocalResolutionCause()
	}
	return err
}
