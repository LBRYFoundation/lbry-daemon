package rpc

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	walletpkg "lbry/daemon/wallet"
)

var txoSpendParameterNames = map[string]struct{}{
	"account_id": {}, "wallet_id": {}, "batch_size": {},
	"include_full_tx": {}, "preview": {}, "blocking": {},
}

func (rpcServer *RPCServer) handleTXOSpend(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	manager, selectedWallet, _, accountID := rpcServer.txoSelection(normalized, "get_txos", false)
	accounts := selectedWallet.Accounts
	if accountID != nil {
		account, err := selectedWallet.Account(*accountID)
		if err != nil {
			panic(err)
		}
		accounts = []*walletpkg.Account{account}
	}
	if len(accounts) == 0 {
		panic(errors.New("wallet has no accounts"))
	}
	query, err := txoOutputQuery(normalized, txoSpendParameterNames)
	if err != nil {
		panic(err)
	}
	accountIDs := make([]string, len(accounts))
	for index, account := range accounts {
		accountIDs[index] = account.ID
	}
	query.AccountIDs = accountIDs
	query.AnnotationAccountIDs = accountIDs
	query.IsSpent = txoBool(false)
	query.IsMyOutput = txoBool(true)
	query.IncludeIsMyOutput = true
	query.NoTransaction = true
	ledger := manager.DefaultLedger()
	outputs, err := ledger.GetTransactionOutputs(normalized.ctx, walletpkg.TransactionOutputListOptions{
		Query: query, Wallet: selectedWallet, NoTransaction: true,
	})
	if err != nil {
		panic(err)
	}
	batchSize, err := txoSpendBatchSize(normalized.named["batch_size"])
	if err != nil {
		panic(err)
	}
	transactions := make([]*walletpkg.Transaction, 0, (len(outputs)+batchSize-1)/batchSize)
	for len(outputs) > 0 {
		count := min(len(outputs), batchSize)
		inputs := make([]walletpkg.TransactionInput, count)
		for index := range inputs {
			last := len(outputs) - 1
			inputs[index], err = walletpkg.NewSpendInput(outputs[last])
			if err != nil {
				panic(err)
			}
			outputs = outputs[:last]
		}
		transaction, err := walletpkg.CreateTransaction(
			normalized.ctx, inputs, nil, accounts, accounts[0], true,
		)
		if err != nil {
			panic(err)
		}
		transactions = append(transactions, transaction)
	}
	if !transactionListTruthy(normalized.named["preview"]) {
		for _, transaction := range transactions {
			if err := ledger.BroadcastOrRelease(
				normalized.ctx, transaction, transactionListTruthy(normalized.named["blocking"]),
			); err != nil {
				panic(err)
			}
		}
	}
	result := make([]any, len(transactions))
	for index, transaction := range transactions {
		if transactionListTruthy(normalized.named["include_full_tx"]) {
			encoded, err := ledger.LegacyTransactionJSON(transaction)
			if err != nil {
				panic(err)
			}
			result[index] = encoded
		} else {
			result[index] = map[string]any{"txid": transaction.ID}
		}
	}
	sendResultResponse(response, result)
}

func txoSpendBatchSize(value any) (int, error) {
	if value == nil {
		return 100, nil
	}
	parsed, err := strconv.Atoi(fmt.Sprint(value))
	if err != nil {
		return 0, err
	}
	if parsed < 1 {
		return 0, errors.New("batch_size must be greater than zero")
	}
	return parsed, nil
}
