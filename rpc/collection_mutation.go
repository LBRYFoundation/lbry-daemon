package rpc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	walletpkg "lbry/daemon/wallet"
	"lbry/daemon/wallet/ledgerdb"
)

func (rpcServer *RPCServer) handleCollectionCreate(w http.ResponseWriter, params any) {
	result, err := rpcServer.collectionMutation(params.(normalizedRPCParams), false)
	if err != nil {
		panic(err)
	}
	sendResultResponse(w, result)
}

func (rpcServer *RPCServer) handleCollectionUpdate(w http.ResponseWriter, params any) {
	result, err := rpcServer.collectionMutation(params.(normalizedRPCParams), true)
	if err != nil {
		panic(err)
	}
	sendResultResponse(w, result)
}

func (rpcServer *RPCServer) collectionMutation(normalized normalizedRPCParams, update bool) (map[string]any, error) {
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
	fundingIDs, err := purchaseFundingAccountIDs(normalized.named["funding_account_ids"])
	if err != nil {
		return nil, err
	}
	funding, err := wallet.AccountsOrAll(fundingIDs)
	if err != nil {
		return nil, err
	}
	if len(funding) == 0 {
		return nil, walletpkg.ErrPurchaseFundingAccount
	}
	accountID, err := transactionListAccountID(normalized.named["account_id"])
	if err != nil {
		return nil, err
	}
	account, err := wallet.AccountOrDefault(accountID)
	if err != nil || account == nil {
		return nil, err
	}
	ledger := manager.DefaultLedger()
	if ledger == nil {
		return nil, errors.New("default ledger is unavailable")
	}
	channel, err := selectSigningChannel(normalized, ledger, wallet)
	if err != nil {
		return nil, err
	}
	var transaction *walletpkg.Transaction
	if !update {
		name, _ := normalized.named["name"].(string)
		if name == "" || strings.HasPrefix(name, "@") || strings.ContainsAny(name, "/:#$") {
			return nil, errors.New("Collection name has invalid characters.")
		}
		allAccountIDs := make([]string, len(wallet.Accounts))
		for index, item := range wallet.Accounts {
			if item == nil {
				return nil, errors.New("collection account is unavailable")
			}
			allAccountIDs[index] = item.ID
		}
		existing, err := ledger.GetCollections(normalized.ctx, walletpkg.ClaimListOptions{
			Query: ledgerdb.OutputQuery{AccountIDs: allAccountIDs, ClaimNames: []string{name}}, Wallet: wallet,
		})
		if err != nil {
			return nil, err
		}
		if len(existing) > 0 && !transactionListTruthy(normalized.named["allow_duplicate_name"]) {
			return nil, fmt.Errorf(
				"You already have a collection under the name '%s'. Use --allow-duplicate-name flag to override.", name,
			)
		}
		amount, err := managedLBCToDewies(fmt.Sprint(normalized.named["bid"]))
		if err != nil || amount == 0 {
			return nil, fmt.Errorf("Invalid bid: %v", normalized.named["bid"])
		}
		address := ""
		if supplied := fileMutationOptionalString(normalized.named["claim_address"]); supplied != nil {
			address = *supplied
		} else {
			address, err = account.Receiving.GetOrCreateUsableAddress(normalized.ctx)
		}
		if err != nil {
			return nil, err
		}
		transaction, err = walletpkg.CreateCollectionTransaction(normalized.ctx, name, amount, address, funding, normalized.kwargs, channel)
		if err != nil {
			return nil, err
		}
	} else {
		claimID, _ := normalized.named["claim_id"].(string)
		accounts := wallet.Accounts
		if accountID != nil {
			accounts = []*walletpkg.Account{account}
		}
		ids := make([]string, len(accounts))
		for i, item := range accounts {
			ids[i] = item.ID
		}
		items, err := ledger.GetCollections(normalized.ctx, walletpkg.ClaimListOptions{Query: ledgerdb.OutputQuery{AccountIDs: ids, ClaimIDs: []string{claimID}}, Wallet: wallet})
		if err != nil || len(items) != 1 {
			return nil, fmt.Errorf("Can't find the collection '%s'.", claimID)
		}
		old := items[0]
		if channel == nil && old.Channel != nil && !transactionListTruthy(normalized.named["clear_channel"]) &&
			!transactionListTruthy(normalized.named["replace"]) {
			channel = old.Channel
			if channel.PrivateKey == nil {
				return nil, errors.New("Could not find private key for signing channel.")
			}
		}
		amount := old.Amount
		if normalized.named["bid"] != nil {
			amount, err = managedLBCToDewies(fmt.Sprint(normalized.named["bid"]))
		}
		if err != nil || amount == 0 {
			return nil, fmt.Errorf("Invalid bid: %v", normalized.named["bid"])
		}
		address, err := old.Address(ledger.Network)
		if err != nil {
			return nil, err
		}
		if supplied := fileMutationOptionalString(normalized.named["claim_address"]); supplied != nil {
			address = *supplied
		}
		transaction, err = walletpkg.CreateCollectionUpdateTransaction(normalized.ctx, old, amount, address, funding, normalized.kwargs, transactionListTruthy(normalized.named["replace"]), channel)
		if err != nil {
			return nil, err
		}
	}
	if transactionListTruthy(normalized.named["preview"]) {
		if err := ledger.ReleaseTransaction(context.WithoutCancel(normalized.ctx), transaction); err != nil {
			return nil, err
		}
	} else if err := ledger.BroadcastOrRelease(normalized.ctx, transaction, transactionListTruthy(normalized.named["blocking"])); err != nil {
		return nil, err
	}
	return ledger.LegacyTransactionJSON(transaction)
}
