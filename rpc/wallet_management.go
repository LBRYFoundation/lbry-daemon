package rpc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"

	walletpkg "lbry/daemon/wallet"
)

type walletLifecycleError struct {
	name, message string
}

func (err *walletLifecycleError) Error() string           { return err.message }
func (err *walletLifecycleError) PythonErrorName() string { return err.name }

func (rpcServer *RPCServer) handleWalletCreate(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	manager := rpcServer.walletManagerProvider()
	walletID, _ := normalized.named["wallet_id"].(string)
	walletPath := rpcServer.walletPath(walletID)
	if walletLifecycleLoaded(manager, walletID) {
		panic(&walletLifecycleError{"WalletAlreadyLoadedError", fmt.Sprintf("Wallet %s is already loaded.", walletPath)})
	}
	if _, err := os.Stat(walletPath); err == nil {
		panic(&walletLifecycleError{"WalletAlreadyExistsError", fmt.Sprintf("Wallet %s already exists, use `wallet_add` to load it.", walletPath)})
	} else if !errors.Is(err, os.ErrNotExist) {
		panic(err)
	}
	selectedWallet, err := manager.ImportWallet(walletPath)
	if err != nil {
		panic(err)
	}
	if len(selectedWallet.Accounts) == 0 && transactionListTruthy(normalized.named["create_account"]) {
		ledger := manager.DefaultLedger()
		generator := walletpkg.DeterministicChainGenerator
		if transactionListTruthy(normalized.named["single_key"]) {
			generator = walletpkg.SingleAddressGenerator
		}
		account, err := walletpkg.GenerateAccount(ledger.Network, "", generator)
		if err != nil {
			panic(err)
		}
		selectedWallet.AddAccount(account)
		if err := manager.RegisterAccount(ledger.ID(), account); err != nil {
			panic(err)
		}
		if err := ledger.SubscribeAccount(normalized.ctx, account); err != nil {
			panic(err)
		}
	}
	if _, err := selectedWallet.Save(); err != nil {
		panic(err)
	}
	if !transactionListTruthy(normalized.named["skip_on_startup"]) {
		wallets := rpcServer.configuredWallets()
		if _, err := rpcServer.settings.Set("wallets", append(wallets, walletID)); err != nil {
			panic(err)
		}
	}
	sendResultResponse(response, walletLifecycleObject(selectedWallet))
}

func (rpcServer *RPCServer) handleWalletAdd(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	manager := rpcServer.walletManagerProvider()
	walletID, _ := normalized.named["wallet_id"].(string)
	walletPath := rpcServer.walletPath(walletID)
	if walletLifecycleLoaded(manager, walletID) {
		panic(&walletLifecycleError{"WalletAlreadyLoadedError", fmt.Sprintf("Wallet %s is already loaded.", walletPath)})
	}
	if _, err := os.Stat(walletPath); errors.Is(err, os.ErrNotExist) {
		panic(&walletLifecycleError{"WalletNotFoundError", fmt.Sprintf("Wallet not found at %s.", walletPath)})
	} else if err != nil {
		panic(err)
	}
	selectedWallet, err := manager.ImportWallet(walletPath)
	if err != nil {
		panic(err)
	}
	ledger := manager.DefaultLedger()
	for _, account := range selectedWallet.Accounts {
		if err := ledger.SubscribeAccount(normalized.ctx, account); err != nil {
			panic(err)
		}
	}
	sendResultResponse(response, walletLifecycleObject(selectedWallet))
}

func (rpcServer *RPCServer) handleWalletRemove(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	manager := rpcServer.walletManagerProvider()
	walletID, _ := normalized.named["wallet_id"].(string)
	selectedWallet, err := manager.GetWalletOrError(walletID)
	if err != nil {
		panic(err)
	}
	manager.RemoveWallet(selectedWallet)
	sendResultResponse(response, walletLifecycleObject(selectedWallet))
}

func (rpcServer *RPCServer) walletPath(walletID string) string {
	value, exists := rpcServer.settings.Get("wallet_dir")
	if !exists {
		panic(errors.New("wallet_dir setting is unavailable"))
	}
	walletDir, ok := value.(string)
	if !ok {
		panic(fmt.Errorf("wallet_dir has type %T", value))
	}
	return walletpkg.WalletFilePath(walletDir, walletID)
}

func (rpcServer *RPCServer) configuredWallets() []string {
	value, exists := rpcServer.settings.Get("wallets")
	if !exists || value == nil {
		return []string{}
	}
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		result := make([]string, len(typed))
		for index, item := range typed {
			result[index], _ = item.(string)
		}
		return result
	default:
		panic(fmt.Errorf("wallets has type %T", value))
	}
}

func walletLifecycleLoaded(manager *walletpkg.WalletManager, walletID string) bool {
	_, err := manager.GetWalletOrError(walletID)
	return err == nil
}

func walletLifecycleObject(selectedWallet *walletpkg.Wallet) *walletpkg.Object {
	return walletpkg.NewObject(
		walletpkg.Member{Key: "id", Value: selectedWallet.ID},
		walletpkg.Member{Key: "name", Value: selectedWallet.Name},
	)
}

func (rpcServer *RPCServer) handleWalletReconnect(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	if err := rpcServer.walletManagerProvider().Reset(normalized.ctx); err != nil {
		panic(err)
	}
	sendResultResponse(response, nil)
}

func (rpcServer *RPCServer) handleWalletExport(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	selectedWallet, err := rpcServer.selectedWallet(normalized)
	if err != nil {
		panic(err)
	}
	if password, exists := normalized.named["password"]; exists && password != nil {
		text, ok := password.(string)
		if !ok {
			panic(errors.New("password must be a string"))
		}
		packed, err := selectedWallet.Pack(text)
		if err != nil {
			panic(err)
		}
		sendResultResponse(response, string(packed))
		return
	}
	serialized, err := selectedWallet.ToJSON()
	if err != nil {
		panic(err)
	}
	sendResultResponse(response, string(serialized))
}

func (rpcServer *RPCServer) handleWalletImport(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	selectedWallet, err := rpcServer.selectedWallet(normalized)
	if err != nil {
		panic(err)
	}
	data, ok := normalized.named["data"].(string)
	if !ok {
		panic(errors.New("data must be a string"))
	}
	var password *string
	if value, exists := normalized.named["password"]; exists && value != nil {
		text, ok := value.(string)
		if !ok {
			panic(errors.New("password must be a string"))
		}
		password = &text
	}
	added, merged, err := selectedWallet.Merge(rpcServer.walletManagerProvider(), password, data)
	if err != nil {
		panic(err)
	}
	for _, account := range append(append([]*walletpkg.Account(nil), added...), merged...) {
		if _, err := account.MigrateChannelKeys(); err != nil {
			panic(err)
		}
	}
	ledger := rpcServer.walletManagerProvider().DefaultLedger()
	if source, connected := ledger.SPVNetwork.(walletpkg.LedgerSPVAddressSource); connected && source.IsConnected() {
		for _, account := range added {
			if _, err := account.EnsureAddressGap(normalized.ctx); err != nil {
				panic(err)
			}
		}
	}
	if _, err := selectedWallet.Save(); err != nil {
		panic(err)
	}
	if password == nil {
		serialized, err := selectedWallet.ToJSON()
		if err != nil {
			panic(err)
		}
		sendResultResponse(response, string(serialized))
		return
	}
	packed, err := selectedWallet.Pack(*password)
	if err != nil {
		panic(err)
	}
	sendResultResponse(response, string(packed))
}

func (rpcServer *RPCServer) handleWalletSend(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	selectedWallet, err := rpcServer.selectedWallet(normalized)
	if err != nil {
		panic(err)
	}
	if selectedWallet.IsLocked() {
		panic(errors.New("Cannot spend funds with locked wallet, unlock first."))
	}
	changeID, err := transactionListAccountID(normalized.named["change_account_id"])
	if err != nil {
		panic(err)
	}
	changeAccount, err := selectedWallet.AccountOrDefault(changeID)
	if err != nil || changeAccount == nil {
		panic(err)
	}
	fundingIDs, err := purchaseFundingAccountIDs(normalized.named["funding_account_ids"])
	if err != nil {
		panic(err)
	}
	funding, err := selectedWallet.AccountsOrAll(fundingIDs)
	if err != nil {
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
		normalized.ctx, amount, addresses, funding, changeAccount,
	)
	if err != nil {
		panic(err)
	}
	ledger := rpcServer.walletManagerProvider().DefaultLedger()
	if transactionListTruthy(normalized.named["preview"]) {
		err = ledger.ReleaseTransaction(context.WithoutCancel(normalized.ctx), transaction)
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
