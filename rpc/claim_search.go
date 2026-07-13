package rpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	walletpkg "lbry/daemon/wallet"
	spvpkg "lbry/daemon/wallet/spv"
)

const claimSearchDefaultPageSize = 20

var claimSearchLegacyTrendingOrders = map[string]struct{}{
	"trending_mixed":  {},
	"trending_local":  {},
	"trending_global": {},
	"trending_group":  {},
}

type claimSearchRequest struct {
	HubParams              map[string]any
	WalletID               *string
	Page                   claimSearchNumber
	PageSize               claimSearchNumber
	IncludePurchaseReceipt bool
	IncludeIsMyOutput      bool
	SessionOverride        any
}

type claimSearchNumber struct {
	integer  *big.Int
	floating *float64
}

func (rpcServer *RPCServer) handleClaimSearch(w http.ResponseWriter, params any) {
	if rpcServer.walletManagerProvider == nil {
		panic(transactionListApplicationError{
			name:    "ComponentsNotStartedError",
			message: `the following required components have not yet started: ["wallet"]`,
		})
	}
	manager := rpcServer.walletManagerProvider()
	if manager == nil {
		panic(transactionListApplicationError{
			name:    "ComponentsNotStartedError",
			message: `the following required components have not yet started: ["wallet"]`,
		})
	}
	normalized := params.(normalizedRPCParams)
	if len(normalized.args) > 0 {
		panic(transactionListApplicationError{
			name: "TypeError",
			message: fmt.Sprintf(
				"Daemon.jsonrpc_claim_search() takes 1 positional argument but %d were given",
				len(normalized.args)+1,
			),
		})
	}
	request, err := normalizeClaimSearchParams(normalized)
	if err != nil {
		panic(err)
	}
	selectedWallet, err := manager.GetWalletOrDefault(request.WalletID)
	if err != nil {
		panic(transactionListRPCError(err))
	}
	ledger := manager.DefaultLedger()
	if ledger == nil {
		panic(transactionListNoneAttributeError("claim_search"))
	}
	if selectedWallet == nil {
		panic(transactionListNoneAttributeError("accounts"))
	}
	if transactionListTruthy(request.SessionOverride) {
		panic(transactionListApplicationError{
			name: "AttributeError",
			message: fmt.Sprintf(
				"'%s' object has no attribute 'send_request'",
				pythonTypeName(request.SessionOverride),
			),
		})
	}

	outputs, err := ledger.QueryClaimSearch(normalized.ctx, request.HubParams)
	if err != nil {
		panic(claimSearchRPCError(err))
	}
	includeTotals := !transactionListTruthy(request.HubParams["no_totals"])
	var totalPages json.Number
	snapshot, err := ledger.SnapshotHubOutputsBeforeEncoding(
		normalized.ctx,
		outputs,
		walletpkg.ResolvedTransactionOutputAnnotationOptions{
			Accounts:                 selectedWallet.Accounts,
			Wallet:                   selectedWallet,
			PurchaseReceiptRequested: request.IncludePurchaseReceipt,
			IncludeIsMyOutput:        request.IncludeIsMyOutput,
		},
		walletpkg.LegacyTransactionJSONOptions{
			IncludeProtobuf: transactionListTruthy(normalized.includeProtobuf),
		},
		func(page walletpkg.HubOutputsPage) error {
			if !includeTotals {
				return nil
			}
			var totalErr error
			totalPages, totalErr = claimSearchTotalPages(page.Total, request.PageSize)
			return totalErr
		},
	)
	if err != nil {
		var encodingError *walletpkg.HubOutputsSnapshotEncodingError
		if errors.As(err, &encodingError) {
			sendResultResponse(w, rpcEncodingFailure{err: encodingError})
			return
		}
		panic(err)
	}

	result := map[string]any{
		"items": snapshot.Items, "blocked": snapshot.Blocked,
		"page": request.Page.wireValue(), "page_size": request.PageSize.wireValue(),
	}
	if includeTotals {
		result["total_pages"] = totalPages
		result["total_items"] = snapshot.Total
	}
	sendResultResponse(w, result)
}

func normalizeClaimSearchParams(normalized normalizedRPCParams) (claimSearchRequest, error) {
	values := make(map[string]any, len(normalized.named)+2)
	for name, value := range normalized.named {
		values[name] = value
	}
	if claimIDs, exists := values["claim_ids"]; exists && !transactionListTruthy(claimIDs) {
		delete(values, "claim_ids")
	}
	if _, claimID := values["claim_id"]; claimID {
		if _, claimIDs := values["claim_ids"]; claimIDs {
			return claimSearchRequest{}, transactionListApplicationError{
				name:    "ConflictingInputValueError",
				message: "Only 'claim_id' or 'claim_ids' is allowed, not both.",
			}
		}
	}
	if value := claimSearchPop(values, "valid_channel_signature", false); transactionListTruthy(value) {
		values["signature_valid"] = json.Number("1")
	}
	if value := claimSearchPop(values, "invalid_channel_signature", false); transactionListTruthy(value) {
		values["signature_valid"] = json.Number("0")
	}
	if value, exists := values["has_no_source"]; exists {
		delete(values, "has_no_source")
		values["has_source"] = !transactionListTruthy(value)
	}
	if value, exists := values["order_by"]; exists {
		delete(values, "order_by")
		values["order_by"] = normalizeClaimSearchOrder(value)
	}

	pageValue := claimSearchPop(values, "page", json.Number("1"))
	page, err := claimSearchAbsoluteNumber(pageValue)
	if err != nil {
		return claimSearchRequest{}, err
	}
	pageSizeValue := claimSearchPop(
		values, "page_size", json.Number(strconv.Itoa(claimSearchDefaultPageSize)),
	)
	pageSize, err := claimSearchAbsoluteNumber(pageSizeValue)
	if err != nil {
		return claimSearchRequest{}, err
	}
	pageSize = pageSize.minimumInteger(50)

	walletIDValue := claimSearchPop(values, "wallet_id", nil)
	walletID, err := transactionListWalletID(walletIDValue)
	if err != nil {
		return claimSearchRequest{}, err
	}
	includePurchaseReceipt := transactionListTruthy(
		claimSearchPop(values, "include_purchase_receipt", false),
	)
	includeIsMyOutput := transactionListTruthy(
		claimSearchPop(values, "include_is_my_output", false),
	)
	sessionOverride := claimSearchPop(values, "session_override", nil)
	offset, err := claimSearchOffset(page, pageSize)
	if err != nil {
		return claimSearchRequest{}, err
	}
	values["offset"] = offset.wireValue()
	values["limit"] = pageSize.wireValue()
	return claimSearchRequest{
		HubParams: values, WalletID: walletID, Page: page, PageSize: pageSize,
		IncludePurchaseReceipt: includePurchaseReceipt,
		IncludeIsMyOutput:      includeIsMyOutput,
		SessionOverride:        sessionOverride,
	}, nil
}

func claimSearchPop(values map[string]any, name string, defaultValue any) any {
	value, exists := values[name]
	if !exists {
		return defaultValue
	}
	delete(values, name)
	return value
}

func normalizeClaimSearchOrder(value any) []any {
	order, ok := value.([]any)
	if !ok {
		order = []any{value}
	}
	normalized := make([]any, 0, len(order))
	for _, item := range order {
		migrated := item
		if text, ok := item.(string); ok {
			if _, legacy := claimSearchLegacyTrendingOrders[text]; legacy {
				migrated = "trending_score"
			}
		}
		duplicate := false
		for _, existing := range normalized {
			if claimSearchPythonEqual(existing, migrated) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			normalized = append(normalized, migrated)
		}
	}
	return normalized
}

func claimSearchPythonEqual(left, right any) bool {
	if reflect.DeepEqual(left, right) {
		return true
	}
	leftNumber, leftOK := claimSearchComparableNumber(left)
	rightNumber, rightOK := claimSearchComparableNumber(right)
	if leftOK || rightOK {
		return leftOK && rightOK && leftNumber.Cmp(rightNumber) == 0
	}
	leftList, leftIsList := left.([]any)
	rightList, rightIsList := right.([]any)
	if leftIsList || rightIsList {
		if !leftIsList || !rightIsList || len(leftList) != len(rightList) {
			return false
		}
		for index := range leftList {
			if !claimSearchPythonEqual(leftList[index], rightList[index]) {
				return false
			}
		}
		return true
	}
	leftMap, leftIsMap := left.(map[string]any)
	rightMap, rightIsMap := right.(map[string]any)
	if leftIsMap || rightIsMap {
		if !leftIsMap || !rightIsMap || len(leftMap) != len(rightMap) {
			return false
		}
		for name, leftValue := range leftMap {
			rightValue, exists := rightMap[name]
			if !exists || !claimSearchPythonEqual(leftValue, rightValue) {
				return false
			}
		}
		return true
	}
	return false
}

func claimSearchComparableNumber(value any) (*big.Rat, bool) {
	text := ""
	switch typed := value.(type) {
	case bool:
		if typed {
			text = "1"
		} else {
			text = "0"
		}
	case json.Number:
		if strings.ContainsAny(typed.String(), ".eE") {
			floating, err := strconv.ParseFloat(typed.String(), 64)
			if err != nil || math.IsNaN(floating) || math.IsInf(floating, 0) {
				return nil, false
			}
			return new(big.Rat).SetFloat64(floating), true
		}
		text = typed.String()
	case int:
		text = strconv.Itoa(typed)
	case int64:
		text = strconv.FormatInt(typed, 10)
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return nil, false
		}
		return new(big.Rat).SetFloat64(typed), true
	default:
		return nil, false
	}
	number, ok := new(big.Rat).SetString(text)
	return number, ok
}

type rpcEncodingFailure struct {
	err error
}

func (failure rpcEncodingFailure) MarshalJSON() ([]byte, error) {
	return nil, failure.err
}

func claimSearchAbsoluteNumber(value any) (claimSearchNumber, error) {
	switch typed := value.(type) {
	case bool:
		if typed {
			return claimSearchIntegerNumber(big.NewInt(1)), nil
		}
		return claimSearchIntegerNumber(new(big.Int)), nil
	case json.Number:
		if !strings.ContainsAny(typed.String(), ".eE") {
			if integer, ok := new(big.Int).SetString(typed.String(), 10); ok {
				integer.Abs(integer)
				return claimSearchIntegerNumber(integer), nil
			}
		}
		floating, err := strconv.ParseFloat(typed.String(), 64)
		if err != nil && !math.IsInf(floating, 0) {
			return claimSearchNumber{}, claimSearchAbsTypeError(value)
		}
		return claimSearchFloatNumber(math.Abs(floating)), nil
	case int:
		integer := big.NewInt(int64(typed))
		integer.Abs(integer)
		return claimSearchIntegerNumber(integer), nil
	case int64:
		integer := big.NewInt(typed)
		integer.Abs(integer)
		return claimSearchIntegerNumber(integer), nil
	case uint64:
		integer := new(big.Int).SetUint64(typed)
		return claimSearchIntegerNumber(integer), nil
	case float64:
		return claimSearchFloatNumber(math.Abs(typed)), nil
	default:
		return claimSearchNumber{}, claimSearchAbsTypeError(value)
	}
}

func claimSearchAbsTypeError(value any) error {
	return transactionListApplicationError{
		name:    "TypeError",
		message: fmt.Sprintf("bad operand type for abs(): '%s'", pythonTypeName(value)),
	}
}

func claimSearchIntegerNumber(value *big.Int) claimSearchNumber {
	return claimSearchNumber{integer: new(big.Int).Set(value)}
}

func claimSearchFloatNumber(value float64) claimSearchNumber {
	return claimSearchNumber{floating: &value}
}

func (number claimSearchNumber) minimumInteger(limit int64) claimSearchNumber {
	if number.integer != nil {
		if number.integer.Cmp(big.NewInt(limit)) > 0 {
			return claimSearchIntegerNumber(big.NewInt(limit))
		}
		return number
	}
	if *number.floating > float64(limit) {
		return claimSearchIntegerNumber(big.NewInt(limit))
	}
	return number
}

func (number claimSearchNumber) wireValue() any {
	if number.integer != nil {
		return json.Number(number.integer.String())
	}
	value := *number.floating
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return value
	}
	return json.Number(pythonFloatJSON(value))
}

func claimSearchOffset(page, pageSize claimSearchNumber) (claimSearchNumber, error) {
	if page.integer != nil && pageSize.integer != nil {
		pageMinusOne := new(big.Int).Sub(page.integer, big.NewInt(1))
		return claimSearchIntegerNumber(new(big.Int).Mul(pageSize.integer, pageMinusOne)), nil
	}
	pageFloat, err := claimSearchNumberFloat(page)
	if err != nil {
		return claimSearchNumber{}, err
	}
	pageSizeFloat, err := claimSearchNumberFloat(pageSize)
	if err != nil {
		return claimSearchNumber{}, err
	}
	return claimSearchFloatNumber(pageSizeFloat * (pageFloat - 1)), nil
}

func claimSearchNumberFloat(number claimSearchNumber) (float64, error) {
	if number.floating != nil {
		return *number.floating, nil
	}
	floating, _ := new(big.Float).SetInt(number.integer).Float64()
	if math.IsInf(floating, 0) {
		return 0, transactionListApplicationError{
			name: "OverflowError", message: "int too large to convert to float",
		}
	}
	return floating, nil
}

func claimSearchTotalPages(total uint32, pageSize claimSearchNumber) (json.Number, error) {
	if pageSize.integer != nil {
		if pageSize.integer.Sign() == 0 {
			return "", transactionListApplicationError{
				name: "ZeroDivisionError", message: "division by zero",
			}
		}
		numerator := new(big.Int).Add(
			new(big.Int).SetUint64(uint64(total)),
			new(big.Int).Sub(pageSize.integer, big.NewInt(1)),
		)
		return json.Number(new(big.Int).Quo(numerator, pageSize.integer).String()), nil
	}
	size := *pageSize.floating
	if size == 0 {
		return "", transactionListApplicationError{
			name: "ZeroDivisionError", message: "float division by zero",
		}
	}
	pages := (float64(total) + (size - 1)) / size
	switch {
	case math.IsNaN(pages):
		return "", transactionListApplicationError{
			name: "ValueError", message: "cannot convert float NaN to integer",
		}
	case math.IsInf(pages, 0):
		return "", transactionListApplicationError{
			name: "OverflowError", message: "cannot convert float infinity to integer",
		}
	}
	integer, _ := new(big.Float).SetFloat64(pages).Int(nil)
	return json.Number(integer.String()), nil
}

func claimSearchRPCError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, spvpkg.ErrConnection) || errors.Is(err, spvpkg.ErrNetworkStopped) {
		return transactionListApplicationError{
			name:    "ConnectionError",
			message: "Attempting to send rpc request when connection is not available.",
		}
	}
	if errors.Is(err, spvpkg.ErrRequestTimeout) {
		return transactionListApplicationError{name: "TimeoutError"}
	}
	if errors.Is(err, walletpkg.ErrClaimSearchUnavailable) {
		return transactionListNoneAttributeError("claim_search")
	}
	var rpcErr *spvpkg.RPCError
	if errors.As(err, &rpcErr) {
		return transactionListApplicationError{
			name: "RPCError",
			message: fmt.Sprintf(
				"(%d, %s)", rpcErr.RPCCode(), pythonRepr(rpcErr.RPCMessage()),
			),
		}
	}
	return err
}
