package rpc

import (
	"fmt"
	"net/http"
)

var txoListPositionalNames = []string{
	"account_id", "wallet_id", "page", "page_size",
	"resolve", "order_by", "no_totals", "include_received_tips",
}

func (rpcServer *RPCServer) handleClaimList(w http.ResponseWriter, params any) {
	normalized := cloneTXOWrapperParams(params.(normalizedRPCParams))
	claimType := normalized.named["claim_type"]
	delete(normalized.named, "claim_type")
	delete(normalized.kwargs, "claim_type")
	normalized.args = nil
	if transactionListTruthy(claimType) {
		normalized.named["type"] = claimType
		normalized.kwargs["type"] = claimType
	} else {
		claimTypes := []any{"stream", "channel", "collection", "repost"}
		normalized.named["type"] = claimTypes
		normalized.kwargs["type"] = claimTypes
	}
	if !transactionListTruthy(normalized.named["is_spent"]) {
		normalized.named["is_not_spent"] = true
		normalized.kwargs["is_not_spent"] = true
	}
	rpcServer.handleTXOList(w, normalized)
}

func (rpcServer *RPCServer) handleChannelList(w http.ResponseWriter, params any) {
	normalized := bindTXOWrapperPositional(params.(normalizedRPCParams))
	setTXOWrapperConstraint(&normalized, "type", "channel")
	if !transactionListTruthy(normalized.named["is_spent"]) {
		setTXOWrapperConstraint(&normalized, "is_not_spent", true)
	}
	rpcServer.handleTXOList(w, normalized)
}

func (rpcServer *RPCServer) handleStreamList(w http.ResponseWriter, params any) {
	normalized := bindTXOWrapperPositional(params.(normalizedRPCParams))
	setTXOWrapperConstraint(&normalized, "type", "stream")
	if _, exists := normalized.kwargs["is_spent"]; !exists {
		setTXOWrapperConstraint(&normalized, "is_not_spent", true)
	}
	rpcServer.handleTXOList(w, normalized)
}

func (rpcServer *RPCServer) handleSupportList(w http.ResponseWriter, params any) {
	normalized := bindTXOWrapperPositional(params.(normalizedRPCParams))
	received := normalized.named["received"]
	sent := normalized.named["sent"]
	staked := normalized.named["staked"]
	for _, name := range []string{"received", "sent", "staked"} {
		delete(normalized.named, name)
		delete(normalized.kwargs, name)
	}
	setTXOWrapperConstraint(&normalized, "type", "support")
	if _, exists := normalized.kwargs["is_spent"]; !exists {
		setTXOWrapperConstraint(&normalized, "is_not_spent", true)
	}
	switch {
	case transactionListTruthy(received):
		setTXOWrapperConstraint(&normalized, "is_not_my_input", true)
		setTXOWrapperConstraint(&normalized, "is_my_output", true)
	case transactionListTruthy(sent):
		setTXOWrapperConstraint(&normalized, "is_my_input", true)
		setTXOWrapperConstraint(&normalized, "is_not_my_output", true)
		delete(normalized.named, "is_spent")
		delete(normalized.kwargs, "is_spent")
		delete(normalized.named, "is_not_spent")
		delete(normalized.kwargs, "is_not_spent")
	case transactionListTruthy(staked):
		setTXOWrapperConstraint(&normalized, "is_my_input", true)
		setTXOWrapperConstraint(&normalized, "is_my_output", true)
	}
	rpcServer.handleTXOList(w, normalized)
}

func bindTXOWrapperPositional(source normalizedRPCParams) normalizedRPCParams {
	normalized := cloneTXOWrapperParams(source)
	if len(source.args) > len(txoListPositionalNames) {
		panic(transactionListApplicationError{
			name: "TypeError",
			message: formatTooManyArguments(
				"txo_list", methodSpecs["txo_list"], len(source.args),
			),
		})
	}
	for index, value := range source.args {
		name := txoListPositionalNames[index]
		if _, duplicate := source.kwargs[name]; duplicate {
			panic(transactionListApplicationError{
				name: "TypeError",
				message: fmt.Sprintf(
					"Daemon.jsonrpc_txo_list() got multiple values for argument '%s'", name,
				),
			})
		}
		normalized.named[name] = value
	}
	normalized.args = nil
	return normalized
}

func cloneTXOWrapperParams(source normalizedRPCParams) normalizedRPCParams {
	cloned := source
	cloned.args = append([]any(nil), source.args...)
	cloned.orderedKwargs = append([]string(nil), source.orderedKwargs...)
	cloned.kwargs = make(map[string]any, len(source.kwargs)+4)
	for name, value := range source.kwargs {
		cloned.kwargs[name] = value
	}
	cloned.named = make(map[string]any, len(source.named)+4)
	for name, value := range source.named {
		cloned.named[name] = value
	}
	return cloned
}

func setTXOWrapperConstraint(params *normalizedRPCParams, name string, value any) {
	params.named[name] = value
	params.kwargs[name] = value
}
