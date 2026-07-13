package rpc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	walletpkg "lbry/daemon/wallet"
	"lbry/daemon/wallet/ledgerdb"
)

func (rpcServer *RPCServer) handleStreamAbandon(response http.ResponseWriter, params any) {
	rpcServer.handleClaimAbandon(response, params.(normalizedRPCParams), false, false)
}

func (rpcServer *RPCServer) handleCollectionAbandon(response http.ResponseWriter, params any) {
	rpcServer.handleClaimAbandon(response, params.(normalizedRPCParams), false, false)
}

func (rpcServer *RPCServer) handleChannelAbandon(response http.ResponseWriter, params any) {
	rpcServer.handleClaimAbandon(response, params.(normalizedRPCParams), true, true)
}

func (rpcServer *RPCServer) handleClaimAbandon(
	response http.ResponseWriter,
	normalized normalizedRPCParams,
	defaultBlocking bool,
	defaultAccountFundingOnly bool,
) {
	result, err := rpcServer.claimAbandon(normalized, defaultBlocking, defaultAccountFundingOnly)
	if err != nil {
		panic(err)
	}
	sendResultResponse(response, result)
}

func (rpcServer *RPCServer) claimAbandon(
	normalized normalizedRPCParams,
	defaultBlocking bool,
	defaultAccountFundingOnly bool,
) (map[string]any, error) {
	manager := rpcServer.walletManagerProvider()
	if manager == nil {
		return nil, errors.New("wallet manager is unavailable")
	}
	walletID, err := transactionListWalletID(normalized.named["wallet_id"])
	if err != nil {
		return nil, err
	}
	selectedWallet, err := manager.GetWalletOrDefault(walletID)
	if err != nil {
		return nil, err
	}
	if selectedWallet == nil {
		return nil, errors.New("default wallet is unavailable")
	}
	if selectedWallet.IsLocked() {
		return nil, errors.New("Cannot spend funds with locked wallet, unlock first.")
	}
	accountID, err := transactionListAccountID(normalized.named["account_id"])
	if err != nil {
		return nil, err
	}
	changeAccount, err := selectedWallet.AccountOrDefault(accountID)
	if err != nil {
		return nil, err
	}
	if changeAccount == nil {
		return nil, errors.New("default account is unavailable")
	}
	lookupAccounts := selectedWallet.Accounts
	if accountID != nil {
		lookupAccounts = []*walletpkg.Account{changeAccount}
	}
	fundingAccounts := lookupAccounts
	if defaultAccountFundingOnly {
		fundingAccounts = []*walletpkg.Account{changeAccount}
	}
	ledger := manager.DefaultLedger()
	if ledger == nil {
		return nil, errors.New("default ledger is unavailable")
	}
	query := ledgerdb.OutputQuery{}
	claimID := fileMutationOptionalString(normalized.named["claim_id"])
	txid := fileMutationOptionalString(normalized.named["txid"])
	nout := normalized.named["nout"]
	if txid != nil && nout != nil {
		position, parseErr := strconv.ParseUint(fmt.Sprint(nout), 10, 32)
		if parseErr != nil {
			return nil, parseErr
		}
		query.TXOID = fmt.Sprintf("%s:%d", *txid, position)
	} else if claimID != nil {
		query.ClaimIDs = []string{*claimID}
	} else {
		return nil, errors.New("Must specify claim_id, or txid and nout")
	}
	query.AccountIDs = make([]string, len(lookupAccounts))
	for index, account := range lookupAccounts {
		if account == nil {
			return nil, errors.New("claim account is unavailable")
		}
		query.AccountIDs[index] = account.ID
	}
	claims, err := ledger.GetClaims(
		normalized.ctx, walletpkg.ClaimListOptions{Query: query, Wallet: selectedWallet},
	)
	if err != nil {
		return nil, err
	}
	if len(claims) == 0 {
		return nil, errors.New("No claim found for the specified claim_id or txid:nout")
	}
	transaction, err := walletpkg.CreateAbandonTransaction(
		normalized.ctx, claims, nil, fundingAccounts, changeAccount,
	)
	if err != nil {
		return nil, err
	}
	if transactionListTruthy(normalized.named["preview"]) {
		if err := ledger.ReleaseTransaction(context.WithoutCancel(normalized.ctx), transaction); err != nil {
			return nil, err
		}
	} else {
		blocking := defaultBlocking
		if normalized.named["blocking"] != nil {
			blocking = transactionListTruthy(normalized.named["blocking"])
		}
		if err := ledger.BroadcastOrRelease(normalized.ctx, transaction, blocking); err != nil {
			return nil, err
		}
	}
	return ledger.LegacyTransactionJSON(transaction)
}
