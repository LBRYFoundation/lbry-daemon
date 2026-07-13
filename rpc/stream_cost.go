package rpc

import (
	"errors"
	"fmt"
	"math"
	"net/http"

	walletpkg "lbry/daemon/wallet"
)

func (rpcServer *RPCServer) handleStreamCostEstimate(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	uri, _ := normalized.named["uri"].(string)
	selectedWallet, err := rpcServer.selectedWallet(normalized)
	if err != nil {
		panic(err)
	}
	ledger := rpcServer.walletManagerProvider().DefaultLedger()
	if ledger == nil {
		panic(errors.New("default ledger is unavailable"))
	}
	var resolved *walletpkg.TransactionOutput
	encoded, err := ledger.ResolveAndSnapshot(
		normalized.ctx, []walletpkg.ResolveRequest{{URL: uri}},
		walletpkg.ResolvedTransactionOutputAnnotationOptions{
			Accounts: selectedWallet.Accounts, Wallet: selectedWallet,
		},
		walletpkg.LegacyTransactionJSONOptions{},
		func(outputs []*walletpkg.TransactionOutput) error {
			if len(outputs) == 1 {
				resolved = outputs[0]
			}
			return nil
		},
	)
	if err != nil {
		panic(err)
	}
	if len(encoded) == 0 || resolved == nil {
		sendResultResponse(response, nil)
		return
	}
	if value, ok := encoded[0].(map[string]any); ok && value["error"] != nil {
		sendResultResponse(response, nil)
		return
	}
	_, claimValue, err := walletpkg.TransactionOutputStreamSource(resolved)
	if err != nil || claimValue == nil {
		sendResultResponse(response, nil)
		return
	}
	fee, hasFee := claimValue.Value["fee"].(map[string]any)
	if !hasFee {
		sendResultResponse(response, 0.0)
		return
	}
	currency, _ := fee["currency"].(string)
	amount, err := managedFeeToDewies(currency, fmt.Sprint(fee["amount"]), rpcServer.exchangeRates)
	if err != nil {
		panic(err)
	}
	cost := math.Round(float64(amount)/float64(walletpkg.TransactionCoin)*100000) / 100000
	sendResultResponse(response, cost)
}
