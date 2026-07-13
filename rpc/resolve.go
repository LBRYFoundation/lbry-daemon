package rpc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"

	walletpkg "lbry/daemon/wallet"
	spvpkg "lbry/daemon/wallet/spv"
)

var resolveLocalParameterNames = map[string]struct{}{
	"urls": {}, "wallet_id": {},
	"include_purchase_receipt": {}, "include_is_my_output": {},
	"include_sent_supports": {}, "include_sent_tips": {},
	"include_received_tips": {},
}

type parsedResolveURL struct {
	value string
}

type rpcRequestCancellation struct {
	err error
}

func (err *rpcRequestCancellation) Error() string { return "" }

func (err *rpcRequestCancellation) PythonErrorName() string { return "CancelledError" }

func (err *rpcRequestCancellation) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.err
}

func (rpcServer *RPCServer) handleResolve(w http.ResponseWriter, params any) {
	if rpcServer.walletManagerProvider == nil {
		panic(resolveComponentsNotStartedError())
	}
	manager := rpcServer.walletManagerProvider()
	if manager == nil {
		panic(resolveComponentsNotStartedError())
	}
	normalized := params.(normalizedRPCParams)
	if _, duplicateSelf := normalized.kwargs["self"]; duplicateSelf {
		panic(transactionListApplicationError{
			name:    "TypeError",
			message: "Daemon.jsonrpc_resolve() got multiple values for argument 'self'",
		})
	}
	if len(normalized.args) > 2 {
		panic(transactionListApplicationError{
			name: "TypeError",
			message: formatTooManyArguments(
				"resolve", methodSpecs["resolve"], len(normalized.args),
			),
		})
	}
	walletID, err := transactionListWalletID(normalized.named["wallet_id"])
	if err != nil {
		panic(err)
	}
	selectedWallet, err := manager.GetWalletOrDefault(walletID)
	if err != nil {
		panic(transactionListRPCError(err))
	}

	values, err := resolveIterableValues(normalized.named["urls"])
	if err != nil {
		panic(err)
	}
	result := make(map[string]any)
	valid := make([]parsedResolveURL, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		url, ok := value.(string)
		if !ok {
			panic(transactionListApplicationError{
				name: "TypeError",
				message: fmt.Sprintf(
					"expected string or bytes-like object, got '%s'", pythonTypeName(value),
				),
			})
		}
		_, validURL := parseResolveURL(url)
		if !validURL {
			result[url] = map[string]any{"error": url + " is not a valid url"}
			continue
		}
		if _, duplicate := seen[url]; duplicate {
			continue
		}
		seen[url] = struct{}{}
		valid = append(valid, parsedResolveURL{value: url})
	}

	if selectedWallet == nil {
		panic(transactionListNoneAttributeError("accounts"))
	}
	ledger := manager.DefaultLedger()
	if ledger == nil {
		panic(transactionListNoneAttributeError("resolve"))
	}
	if len(valid) > 0 {
		if unexpected := firstUnexpectedResolveParameter(normalized); unexpected != "" {
			panic(transactionListApplicationError{
				name: "TypeError",
				message: fmt.Sprintf(
					"Ledger._inflate_outputs() got an unexpected keyword argument %s",
					pythonRepr(unexpected),
				),
			})
		}
	}

	requests := make([]walletpkg.ResolveRequest, len(valid))
	for index, url := range valid {
		requests[index] = walletpkg.ResolveRequest{URL: url.value}
	}
	encoded, err := ledger.ResolveAndSnapshot(
		normalized.ctx,
		requests,
		walletpkg.ResolvedTransactionOutputAnnotationOptions{
			Accounts:                 selectedWallet.Accounts,
			Wallet:                   selectedWallet,
			PurchaseReceiptRequested: transactionListTruthy(normalized.named["include_purchase_receipt"]),
			IncludeIsMyOutput:        transactionListTruthy(normalized.named["include_is_my_output"]),
			IncludeSentSupports:      transactionListTruthy(normalized.named["include_sent_supports"]),
			IncludeSentTips:          transactionListTruthy(normalized.named["include_sent_tips"]),
			IncludeReceivedTips:      transactionListTruthy(normalized.named["include_received_tips"]),
		},
		walletpkg.LegacyTransactionJSONOptions{
			IncludeProtobuf: transactionListTruthy(normalized.includeProtobuf),
		},
		rpcServer.resolveBeforeEncoding(normalized.ctx, ledger),
	)
	if err != nil {
		var encodingError *walletpkg.HubOutputsSnapshotEncodingError
		if errors.As(err, &encodingError) {
			sendResultResponse(w, rpcEncodingFailure{err: encodingError})
			return
		}
		panic(resolveRPCError(err))
	}
	for index, request := range requests {
		result[request.URL] = encoded[index]
	}
	sendResultResponse(w, result)
}

func resolveComponentsNotStartedError() error {
	return transactionListApplicationError{
		name:    "ComponentsNotStartedError",
		message: `the following required components have not yet started: ["wallet"]`,
	}
}

func resolveIterableValues(value any) ([]any, error) {
	switch typed := value.(type) {
	case string:
		return []any{typed}, nil
	case []any:
		return append([]any(nil), typed...), nil
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		values := make([]any, len(keys))
		for index, key := range keys {
			values[index] = key
		}
		return values, nil
	default:
		return nil, transactionListApplicationError{
			name:    "TypeError",
			message: fmt.Sprintf("'%s' object is not iterable", pythonTypeName(value)),
		}
	}
}

func firstUnexpectedResolveParameter(normalized normalizedRPCParams) string {
	seen := make(map[string]struct{}, len(normalized.named))
	for _, name := range normalized.orderedKwargs {
		if _, exists := normalized.named[name]; !exists {
			continue
		}
		seen[name] = struct{}{}
		if _, local := resolveLocalParameterNames[name]; !local {
			return name
		}
	}
	remaining := make([]string, 0)
	for name := range normalized.named {
		if _, ordered := seen[name]; ordered {
			continue
		}
		if _, local := resolveLocalParameterNames[name]; !local {
			remaining = append(remaining, name)
		}
	}
	sort.Strings(remaining)
	if len(remaining) > 0 {
		return remaining[0]
	}
	return ""
}

func (rpcServer *RPCServer) resolveBeforeEncoding(
	ctx context.Context, ledger *walletpkg.Ledger,
) func([]*walletpkg.TransactionOutput) error {
	if rpcServer.resolvedClaimSaver == nil {
		return nil
	}
	return func(outputs []*walletpkg.TransactionOutput) error {
		if value, exists := rpcServer.settings.Get("save_resolved_claims"); exists && !transactionListTruthy(value) {
			return nil
		}
		err := rpcServer.resolvedClaimSaver.SaveResolvedClaims(ctx, ledger, outputs)
		if err == nil {
			return nil
		}
		var named pythonErrorNamer
		if errors.As(err, &named) && named.PythonErrorName() == "DecodeError" {
			return nil
		}
		return err
	}
}

func resolveRPCError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, walletpkg.ErrResolveUnavailable) {
		return transactionListNoneAttributeError("resolve")
	}
	if errors.Is(err, spvpkg.ErrNetworkStopped) || errors.Is(err, context.Canceled) {
		return &rpcRequestCancellation{err: err}
	}
	if errors.Is(err, spvpkg.ErrConnection) {
		return transactionListApplicationError{
			name:    "ConnectionError",
			message: "Attempting to send rpc request when connection is not available.",
		}
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

func parseResolveURL(url string) (bool, bool) {
	parsed, err := walletpkg.ParseLBRYURL(url)
	return parsed.HasStreamInChannel, err == nil
}
