package rpc

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"time"

	walletpkg "lbry/daemon/wallet"
)

func (rpcServer *RPCServer) handleAccountList(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	selectedWallet, err := rpcServer.selectedWallet(normalized)
	if err != nil {
		panic(err)
	}
	accounts := selectedWallet.Accounts
	page, pageSize := walletListPositiveInteger(normalized.named["page"], 1), walletListPositiveInteger(normalized.named["page_size"], 20)
	if accountID, err := transactionListAccountID(normalized.named["account_id"]); err != nil {
		panic(err)
	} else if accountID != nil {
		account, err := selectedWallet.Account(*accountID)
		if err != nil {
			panic(err)
		}
		accounts, page, pageSize = []*walletpkg.Account{account}, 1, 1
	}
	total := len(accounts)
	start, end := pageSize*(page-1), min(pageSize*page, total)
	items := make([]any, 0)
	if start <= total {
		for _, account := range accounts[start:end] {
			item, err := accountListDetails(normalized, account)
			if err != nil {
				panic(err)
			}
			items = append(items, item)
		}
	}
	sendResultResponse(response, map[string]any{
		"items": items, "total_pages": (total + pageSize - 1) / pageSize,
		"total_items": total, "page": page, "page_size": pageSize,
	})
}

func accountListDetails(normalized normalizedRPCParams, account *walletpkg.Account) (*walletpkg.Object, error) {
	satoshis, err := account.GetBalance(normalized.ctx, walletpkg.AccountBalanceOptions{
		Confirmations: balanceConfirmations(normalized.named["confirmations"]),
	})
	if err != nil {
		return nil, err
	}
	serialized, err := account.ToObject()
	if err != nil {
		return nil, err
	}
	generator, _ := serialized.Get("address_generator")
	result := walletpkg.NewObject(
		walletpkg.Member{Key: "id", Value: account.ID},
		walletpkg.Member{Key: "name", Value: account.Name},
		walletpkg.Member{Key: "ledger", Value: account.Network.ID()},
		walletpkg.Member{Key: "coins", Value: math.Round(float64(satoshis)/1e6) / 100},
		walletpkg.Member{Key: "satoshis", Value: satoshis},
		walletpkg.Member{Key: "encrypted", Value: account.Encrypted},
		walletpkg.Member{Key: "public_key", Value: account.PublicKey.ExtendedKeyString()},
		walletpkg.Member{Key: "address_generator", Value: generator},
	)
	if transactionListTruthy(normalized.named["show_seed"]) {
		result.Set("seed", account.Seed)
	}
	result.Set("certificates", account.ChannelKeys.Len())
	return result, nil
}

func (rpcServer *RPCServer) handleAccountCreate(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	selectedWallet, err := rpcServer.selectedWallet(normalized)
	if err != nil {
		panic(err)
	}
	manager, ledger := rpcServer.walletManagerProvider(), rpcServer.walletManagerProvider().DefaultLedger()
	if ledger == nil {
		panic(errors.New("default ledger is unavailable"))
	}
	generator := walletpkg.DeterministicChainGenerator
	if transactionListTruthy(normalized.named["single_key"]) {
		generator = walletpkg.SingleAddressGenerator
	}
	name, _ := normalized.named["account_name"].(string)
	account, err := walletpkg.GenerateAccount(ledger.Network, name, generator)
	if err != nil {
		panic(err)
	}
	rpcServer.installAccount(normalized, manager, selectedWallet, account)
	sendResultResponse(response, accountMutationObject(selectedWallet, account))
}

func (rpcServer *RPCServer) handleAccountAdd(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	selectedWallet, err := rpcServer.selectedWallet(normalized)
	if err != nil {
		panic(err)
	}
	manager, ledger := rpcServer.walletManagerProvider(), rpcServer.walletManagerProvider().DefaultLedger()
	if ledger == nil {
		panic(errors.New("default ledger is unavailable"))
	}
	generator := walletpkg.DeterministicChainGenerator
	if transactionListTruthy(normalized.named["single_key"]) {
		generator = walletpkg.SingleAddressGenerator
	}
	data := walletpkg.NewObject(
		walletpkg.Member{Key: "name", Value: normalized.named["account_name"]},
		walletpkg.Member{Key: "address_generator", Value: walletpkg.NewObject(walletpkg.Member{Key: "name", Value: generator})},
	)
	for _, key := range []string{"seed", "private_key", "public_key"} {
		if value, exists := normalized.named[key]; exists && value != nil {
			data.Set(key, value)
		}
	}
	account, err := walletpkg.NewAccount(ledger.Network, data)
	if err != nil {
		panic(err)
	}
	rpcServer.installAccount(normalized, manager, selectedWallet, account)
	sendResultResponse(response, accountMutationObject(selectedWallet, account))
}

func (rpcServer *RPCServer) installAccount(normalized normalizedRPCParams, manager *walletpkg.WalletManager, selectedWallet *walletpkg.Wallet, account *walletpkg.Account) {
	selectedWallet.AddAccount(account)
	if err := manager.RegisterAccount(account.Network.ID(), account); err != nil {
		selectedWallet.Accounts = selectedWallet.Accounts[:len(selectedWallet.Accounts)-1]
		panic(err)
	}
	ledger := manager.DefaultLedger()
	if err := ledger.SubscribeAccount(normalized.ctx, account); err != nil {
		panic(err)
	}
	if _, err := selectedWallet.Save(); err != nil {
		panic(err)
	}
}

func (rpcServer *RPCServer) handleAccountRemove(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	selectedWallet, err := rpcServer.selectedWallet(normalized)
	if err != nil {
		panic(err)
	}
	accountID, _ := normalized.named["account_id"].(string)
	account, err := selectedWallet.Account(accountID)
	if err != nil {
		panic(err)
	}
	for index, candidate := range selectedWallet.Accounts {
		if candidate == account {
			selectedWallet.Accounts = append(selectedWallet.Accounts[:index], selectedWallet.Accounts[index+1:]...)
			break
		}
	}
	rpcServer.walletManagerProvider().UnregisterAccount(account)
	if _, err := selectedWallet.Save(); err != nil {
		panic(err)
	}
	sendResultResponse(response, accountMutationObject(selectedWallet, account))
}

func (rpcServer *RPCServer) handleAccountSet(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	selectedWallet, err := rpcServer.selectedWallet(normalized)
	if err != nil {
		panic(err)
	}
	accountID, _ := normalized.named["account_id"].(string)
	account, err := selectedWallet.Account(accountID)
	if err != nil {
		panic(err)
	}
	changed := false
	if account.GeneratorName == walletpkg.DeterministicChainGenerator {
		changed = setAddressManagerValue(normalized, "change_gap", &account.Change.Gap) || changed
		changed = setAddressManagerValue(normalized, "change_max_uses", &account.Change.MaximumUsesPerAddress) || changed
		changed = setAddressManagerValue(normalized, "receiving_gap", &account.Receiving.Gap) || changed
		changed = setAddressManagerValue(normalized, "receiving_max_uses", &account.Receiving.MaximumUsesPerAddress) || changed
	}
	if value, exists := normalized.named["new_name"]; exists && value != nil {
		account.Name = fmt.Sprint(value)
		changed = true
	}
	if transactionListTruthy(normalized.named["default"]) && selectedWallet.DefaultAccount() != account {
		for index, candidate := range selectedWallet.Accounts {
			if candidate == account {
				selectedWallet.Accounts = append([]*walletpkg.Account{account}, append(selectedWallet.Accounts[:index], selectedWallet.Accounts[index+1:]...)...)
				changed = true
				break
			}
		}
	}
	if changed {
		account.ModifiedOn = big.NewInt(time.Now().Unix())
		if _, err := selectedWallet.Save(); err != nil {
			panic(err)
		}
	}
	sendResultResponse(response, accountMutationObject(selectedWallet, account))
}

func setAddressManagerValue(normalized normalizedRPCParams, key string, target *any) bool {
	value, exists := normalized.named[key]
	if !exists || value == nil {
		return false
	}
	parsed, err := strconv.ParseInt(fmt.Sprint(value), 10, 64)
	if err != nil {
		panic(err)
	}
	*target = parsed
	return true
}

func (rpcServer *RPCServer) handleAccountMaxAddressGap(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	selectedWallet, err := rpcServer.selectedWallet(normalized)
	if err != nil {
		panic(err)
	}
	accountID, _ := normalized.named["account_id"].(string)
	account, err := selectedWallet.Account(accountID)
	if err != nil {
		panic(err)
	}
	change, err := account.Change.GetMaxGap(normalized.ctx)
	if err != nil {
		panic(err)
	}
	receiving, err := account.Receiving.GetMaxGap(normalized.ctx)
	if err != nil {
		panic(err)
	}
	sendResultResponse(response, map[string]any{"max_change_gap": change, "max_receiving_gap": receiving})
}

func accountMutationObject(selectedWallet *walletpkg.Wallet, account *walletpkg.Account) *walletpkg.Object {
	object, err := account.ToObject()
	if err != nil {
		panic(err)
	}
	object.Set("id", account.ID)
	object.Delete("certificates")
	object.Set("is_default", selectedWallet.DefaultAccount() == account)
	return object
}
