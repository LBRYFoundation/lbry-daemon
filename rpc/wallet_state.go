package rpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	walletpkg "lbry/daemon/wallet"
)

func (rpcServer *RPCServer) selectedWallet(normalized normalizedRPCParams) (*walletpkg.Wallet, error) {
	manager := rpcServer.walletManagerProvider()
	if manager == nil {
		return nil, errors.New("wallet manager is unavailable")
	}
	walletID, err := transactionListWalletID(normalized.named["wallet_id"])
	if err != nil {
		return nil, err
	}
	wallet, err := manager.GetWalletOrDefault(walletID)
	if err != nil {
		return nil, err
	}
	if wallet == nil {
		return nil, errors.New("default wallet is unavailable")
	}
	return wallet, nil
}

func (rpcServer *RPCServer) handlePreferenceGet(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	wallet, err := rpcServer.selectedWallet(normalized)
	if err != nil {
		panic(err)
	}
	if key, ok := normalized.named["key"].(string); ok && key != "" {
		value, exists, err := wallet.Preferences.Get(key)
		if err != nil {
			panic(err)
		}
		if !exists {
			sendResultResponse(response, nil)
			return
		}
		sendResultResponse(response, map[string]any{key: value})
		return
	}
	values, err := wallet.Preferences.WithoutTimestamps()
	if err != nil {
		panic(err)
	}
	sendResultResponse(response, values)
}

func (rpcServer *RPCServer) handlePreferenceSet(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	wallet, err := rpcServer.selectedWallet(normalized)
	if err != nil {
		panic(err)
	}
	key, _ := normalized.named["key"].(string)
	value := normalized.named["value"]
	if text, ok := value.(string); ok && text != "" && (text[0] == '[' || text[0] == '{') {
		decoder := json.NewDecoder(strings.NewReader(text))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			panic(err)
		}
	}
	wallet.Preferences.Set(key, value)
	if _, err := wallet.Save(); err != nil {
		panic(err)
	}
	sendResultResponse(response, map[string]any{key: value})
}

func (rpcServer *RPCServer) handleWalletStatus(response http.ResponseWriter, params any) {
	wallet, err := rpcServer.selectedWallet(params.(normalizedRPCParams))
	if err != nil {
		panic(err)
	}
	encrypted, err := wallet.IsEncrypted()
	if err != nil {
		panic(err)
	}
	isSyncing := false
	if manager := rpcServer.walletManagerProvider(); manager != nil {
		if ledger := manager.DefaultLedger(); ledger != nil {
			snapshot := ledger.SPVSnapshot()
			isSyncing = snapshot.Running && snapshot.UpdateTasks > 0
		}
	}
	sendResultResponse(response, map[string]any{
		"is_encrypted": encrypted, "is_syncing": isSyncing, "is_locked": wallet.IsLocked(),
	})
}

func (rpcServer *RPCServer) handleWalletList(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	manager := rpcServer.walletManagerProvider()
	if manager == nil {
		panic(errors.New("wallet manager is unavailable"))
	}
	var wallets []*walletpkg.Wallet
	page, pageSize := 1, 20
	if walletID, err := transactionListWalletID(normalized.named["wallet_id"]); err != nil {
		panic(err)
	} else if walletID != nil {
		wallet, err := manager.GetWalletOrError(*walletID)
		if err != nil {
			panic(err)
		}
		wallets, page, pageSize = []*walletpkg.Wallet{wallet}, 1, 1
	} else {
		page = walletListPositiveInteger(normalized.named["page"], 1)
		pageSize = walletListPositiveInteger(normalized.named["page_size"], 20)
		wallets = manager.Wallets
	}
	total := len(wallets)
	start := pageSize * (page - 1)
	end := min(start+pageSize, total)
	items := make([]any, 0)
	if start <= total {
		for _, wallet := range wallets[start:end] {
			items = append(items, walletLifecycleObject(wallet))
		}
	}
	totalPages := (total + pageSize - 1) / pageSize
	sendResultResponse(response, map[string]any{
		"items": items, "total_pages": totalPages, "total_items": total,
		"page": page, "page_size": pageSize,
	})
}

func walletListPositiveInteger(value any, fallback int) int {
	if value == nil {
		return fallback
	}
	parsed, err := strconv.Atoi(fmt.Sprint(value))
	if err != nil || parsed < 1 {
		return 1
	}
	return parsed
}

func (rpcServer *RPCServer) handleWalletUnlock(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	wallet, err := rpcServer.selectedWallet(normalized)
	if err != nil {
		panic(err)
	}
	password, _ := normalized.named["password"].(string)
	unlocked, err := wallet.Unlock(password)
	if err != nil {
		panic(err)
	}
	sendResultResponse(response, unlocked)
}

func (rpcServer *RPCServer) handleWalletLock(response http.ResponseWriter, params any) {
	wallet, err := rpcServer.selectedWallet(params.(normalizedRPCParams))
	if err != nil {
		panic(err)
	}
	if err := wallet.Lock(); err != nil {
		panic(err)
	}
	sendResultResponse(response, true)
}

func (rpcServer *RPCServer) handleWalletDecrypt(response http.ResponseWriter, params any) {
	wallet, err := rpcServer.selectedWallet(params.(normalizedRPCParams))
	if err != nil {
		panic(err)
	}
	if err := wallet.Decrypt(); err != nil {
		panic(err)
	}
	sendResultResponse(response, true)
}

func (rpcServer *RPCServer) handleWalletEncrypt(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	wallet, err := rpcServer.selectedWallet(normalized)
	if err != nil {
		panic(err)
	}
	password, _ := normalized.named["new_password"].(string)
	if err := wallet.Encrypt(password); err != nil {
		panic(err)
	}
	sendResultResponse(response, true)
}
