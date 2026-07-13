package rpc

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"

	walletpkg "lbry/daemon/wallet"
)

func (rpcServer *RPCServer) handleAccountSend(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	selectedWallet, err := rpcServer.selectedWallet(normalized)
	if err != nil {
		panic(err)
	}
	accountID, err := transactionListAccountID(normalized.named["account_id"])
	if err != nil {
		panic(err)
	}
	account, err := selectedWallet.AccountOrDefault(accountID)
	if err != nil || account == nil {
		panic(err)
	}
	amount, err := accountTransactionAmount(normalized.named["amount"])
	if err != nil {
		panic(err)
	}
	addresses, err := accountTransactionStrings(normalized.named["addresses"])
	if err != nil {
		panic(err)
	}
	transaction, err := walletpkg.CreatePaymentTransaction(
		normalized.ctx, amount, addresses, []*walletpkg.Account{account}, account,
	)
	if err != nil {
		panic(err)
	}
	ledger := rpcServer.walletManagerProvider().DefaultLedger()
	if transactionListTruthy(normalized.named["preview"]) {
		err = ledger.ReleaseTransaction(normalized.ctx, transaction)
	} else {
		err = ledger.BroadcastOrRelease(normalized.ctx, transaction, transactionListTruthy(normalized.named["blocking"]))
	}
	if err != nil {
		panic(err)
	}
	encoded, err := ledger.LegacyTransactionJSON(transaction)
	if err != nil {
		panic(err)
	}
	sendResultResponse(response, encoded)
}

func (rpcServer *RPCServer) handleAccountFund(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	selectedWallet, err := rpcServer.selectedWallet(normalized)
	if err != nil {
		panic(err)
	}
	toAccount, err := accountTransactionAccount(selectedWallet, normalized.named["to_account"])
	if err != nil {
		panic(err)
	}
	fromAccount, err := accountTransactionAccount(selectedWallet, normalized.named["from_account"])
	if err != nil {
		panic(err)
	}
	var amount *uint64
	if value := normalized.named["amount"]; value != nil && fmt.Sprint(value) != "" {
		parsed, err := accountTransactionAmount(value)
		if err != nil {
			panic(err)
		}
		amount = &parsed
	}
	outputs, err := accountTransactionInteger(normalized.named["outputs"], 1)
	if err != nil {
		panic(errors.New("--outputs must be an integer."))
	}
	transaction, err := walletpkg.FundAccount(
		normalized.ctx, fromAccount, toAccount, amount,
		transactionListTruthy(normalized.named["everything"]), outputs,
		transactionListTruthy(normalized.named["broadcast"]),
	)
	if err != nil {
		panic(err)
	}
	encoded, err := rpcServer.walletManagerProvider().DefaultLedger().LegacyTransactionJSON(transaction)
	if err != nil {
		panic(err)
	}
	sendResultResponse(response, encoded)
}

func (rpcServer *RPCServer) handleAccountDeposit(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	selectedWallet, err := rpcServer.selectedWallet(normalized)
	if err != nil {
		panic(err)
	}
	account, err := accountTransactionAccount(selectedWallet, normalized.named["to_account"])
	if err != nil {
		panic(err)
	}
	lookup, err := rpcServer.walletManagerProvider().GetTransaction(normalized.ctx, normalized.named["txid"])
	if err != nil {
		panic(err)
	}
	if lookup.Transaction == nil {
		panic(errors.New("transaction was not found"))
	}
	nout, err := accountTransactionInteger(normalized.named["nout"], 0)
	if err != nil || nout < 0 {
		panic(errors.New("invalid output index"))
	}
	redeemScript, err := hex.DecodeString(fmt.Sprint(normalized.named["redeem_script"]))
	if err != nil {
		panic(err)
	}
	transaction, err := walletpkg.CreateTimeLockDepositTransaction(
		normalized.ctx, lookup.Transaction, uint32(nout), redeemScript,
		fmt.Sprint(normalized.named["private_key"]), account,
	)
	if err != nil {
		panic(err)
	}
	ledger := rpcServer.walletManagerProvider().DefaultLedger()
	if transactionListTruthy(normalized.named["preview"]) {
		err = ledger.ReleaseTransaction(normalized.ctx, transaction)
	} else {
		err = ledger.BroadcastOrRelease(normalized.ctx, transaction, transactionListTruthy(normalized.named["blocking"]))
	}
	if err != nil {
		panic(err)
	}
	encoded, err := ledger.LegacyTransactionJSON(transaction)
	if err != nil {
		panic(err)
	}
	sendResultResponse(response, encoded)
}

func accountTransactionAccount(selectedWallet *walletpkg.Wallet, value any) (*walletpkg.Account, error) {
	accountID, err := transactionListAccountID(value)
	if err != nil {
		return nil, err
	}
	return selectedWallet.AccountOrDefault(accountID)
}

func accountTransactionAmount(value any) (uint64, error) {
	return managedLBCToDewies(fmt.Sprint(value))
}

func accountTransactionInteger(value any, fallback int) (int, error) {
	if value == nil {
		return fallback, nil
	}
	return strconv.Atoi(fmt.Sprint(value))
}

func accountTransactionStrings(value any) ([]string, error) {
	if text, ok := value.(string); ok {
		return []string{text}, nil
	}
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() || (reflected.Kind() != reflect.Slice && reflected.Kind() != reflect.Array) {
		return nil, fmt.Errorf("addresses has type %T, want array", value)
	}
	result := make([]string, reflected.Len())
	for index := range result {
		text, ok := reflected.Index(index).Interface().(string)
		if !ok {
			return nil, fmt.Errorf("address %d has type %T, want string", index, reflected.Index(index).Interface())
		}
		result[index] = text
	}
	return result, nil
}
