package rpc

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	walletpkg "lbry/daemon/wallet"
	"lbry/daemon/wallet/ledgerdb"
)

func (rpcServer *RPCServer) handleStreamRepost(response http.ResponseWriter, params any) {
	result, err := rpcServer.streamRepost(params.(normalizedRPCParams))
	if err != nil {
		panic(err)
	}
	sendResultResponse(response, result)
}

func (rpcServer *RPCServer) streamRepost(normalized normalizedRPCParams) (map[string]any, error) {
	manager := rpcServer.walletManagerProvider()
	if manager == nil {
		return nil, errors.New("wallet manager is unavailable")
	}
	walletID, err := transactionListWalletID(normalized.named["wallet_id"])
	if err != nil {
		return nil, err
	}
	selectedWallet, err := manager.GetWalletOrDefault(walletID)
	if err != nil || selectedWallet == nil {
		return nil, errors.New("default wallet is unavailable")
	}
	accountID, err := transactionListAccountID(normalized.named["account_id"])
	if err != nil {
		return nil, err
	}
	account, err := selectedWallet.AccountOrDefault(accountID)
	if err != nil || account == nil {
		return nil, err
	}
	fundingIDs, err := purchaseFundingAccountIDs(normalized.named["funding_account_ids"])
	if err != nil {
		return nil, err
	}
	funding, err := selectedWallet.AccountsOrAll(fundingIDs)
	if err != nil || len(funding) == 0 {
		return nil, walletpkg.ErrPurchaseFundingAccount
	}
	ledger := manager.DefaultLedger()
	if ledger == nil {
		return nil, errors.New("default ledger is unavailable")
	}
	name, _ := normalized.named["name"].(string)
	if err := validateStreamName(name); err != nil {
		return nil, err
	}
	existing, err := ledger.GetClaims(normalized.ctx, walletpkg.ClaimListOptions{
		Query: ledgerdb.OutputQuery{AccountIDs: []string{account.ID}, ClaimNames: []string{name}}, Wallet: selectedWallet,
	})
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 && !transactionListTruthy(normalized.named["allow_duplicate_name"]) {
		return nil, fmt.Errorf(
			"You already have a stream claim published under the name '%s'. Use --allow-duplicate-name flag to override.", name,
		)
	}
	claimID, _ := normalized.named["claim_id"].(string)
	amount, err := managedLBCToDewies(fmt.Sprint(normalized.named["bid"]))
	if err != nil || amount == 0 {
		return nil, fmt.Errorf("Invalid bid: %v", normalized.named["bid"])
	}
	address, err := streamClaimAddress(normalized, account)
	if err != nil {
		return nil, err
	}
	channel, err := selectSigningChannel(normalized, ledger, selectedWallet)
	if err != nil {
		return nil, err
	}
	transaction, err := walletpkg.CreateRepostTransaction(
		normalized.ctx, name, claimID, amount, address, funding, normalized.kwargs, channel,
	)
	if err != nil {
		return nil, err
	}
	if transactionListTruthy(normalized.named["preview"]) {
		if err := ledger.ReleaseTransaction(context.WithoutCancel(normalized.ctx), transaction); err != nil {
			return nil, err
		}
	} else if err := ledger.BroadcastOrRelease(
		normalized.ctx, transaction, transactionListTruthy(normalized.named["blocking"]),
	); err != nil {
		return nil, err
	}
	return ledger.LegacyTransactionJSON(transaction)
}
