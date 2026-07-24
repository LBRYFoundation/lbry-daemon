package rpc

import (
	"encoding/hex"
	"errors"
	"net/http"

	walletpkg "lbry/daemon/wallet"
)

func (rpcServer *RPCServer) handleSyncHash(response http.ResponseWriter, params any) {
	selectedWallet, err := rpcServer.selectedWallet(params.(normalizedRPCParams))
	if err != nil {
		panic(err)
	}
	hash, err := selectedWallet.Hash()
	if err != nil {
		panic(err)
	}
	sendResultResponse(response, hex.EncodeToString(hash[:]))
}

func (rpcServer *RPCServer) handleSyncApply(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	selectedWallet, err := rpcServer.selectedWallet(normalized)
	if err != nil {
		panic(err)
	}
	password, ok := normalized.named["password"].(string)
	if !ok {
		panic(errors.New("password must be a string"))
	}
	walletChanged := false
	if data, exists := normalized.named["data"]; exists && data != nil {
		encoded, ok := data.(string)
		if !ok {
			panic(errors.New("data must be a string"))
		}
		added, merged, err := selectedWallet.Merge(rpcServer.walletManagerProvider(), &password, encoded)
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
		walletChanged = true
	}
	encryptOnDisk, err := selectedWallet.Preferences.GetOr(walletpkg.EncryptOnDisk, false)
	if err != nil {
		panic(err)
	}
	if transactionListTruthy(encryptOnDisk) &&
		(selectedWallet.EncryptionPassword == nil || *selectedWallet.EncryptionPassword != password) {
		passwordCopy := password
		selectedWallet.EncryptionPassword = &passwordCopy
		walletChanged = true
	}
	if walletChanged {
		if _, err := selectedWallet.Save(); err != nil {
			panic(err)
		}
	}
	packed, err := selectedWallet.Pack(password)
	if err != nil {
		panic(err)
	}
	hash, err := selectedWallet.Hash()
	if err != nil {
		panic(err)
	}
	sendResultResponse(response, map[string]any{
		"hash": hex.EncodeToString(hash[:]), "data": string(packed),
	})
}
