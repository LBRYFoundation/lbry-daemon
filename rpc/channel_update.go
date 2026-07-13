package rpc

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	walletpkg "lbry/daemon/wallet"
	"lbry/daemon/wallet/ledgerdb"
)

func (rpcServer *RPCServer) handleChannelUpdate(response http.ResponseWriter, params any) {
	result, err := rpcServer.channelUpdate(params.(normalizedRPCParams))
	if err != nil {
		panic(err)
	}
	sendResultResponse(response, result)
}

func (rpcServer *RPCServer) channelUpdate(normalized normalizedRPCParams) (map[string]any, error) {
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
	fundingIDs, err := purchaseFundingAccountIDs(normalized.named["funding_account_ids"])
	if err != nil {
		return nil, err
	}
	fundingAccounts, err := selectedWallet.AccountsOrAll(fundingIDs)
	if err != nil {
		return nil, err
	}
	if len(fundingAccounts) == 0 {
		return nil, walletpkg.ErrPurchaseFundingAccount
	}
	accountID, err := transactionListAccountID(normalized.named["account_id"])
	if err != nil {
		return nil, err
	}
	changeAccount, err := selectedWallet.AccountOrDefault(accountID)
	if err != nil {
		return nil, err
	}
	lookupAccounts := selectedWallet.Accounts
	if accountID != nil {
		lookupAccounts = []*walletpkg.Account{changeAccount}
	}
	ledger := manager.DefaultLedger()
	if ledger == nil {
		return nil, errors.New("default ledger is unavailable")
	}
	claimID, _ := normalized.named["claim_id"].(string)
	accountIDs := make([]string, len(lookupAccounts))
	for index, account := range lookupAccounts {
		accountIDs[index] = account.ID
	}
	claims, err := ledger.GetClaims(normalized.ctx, walletpkg.ClaimListOptions{
		Query:  ledgerdb.OutputQuery{AccountIDs: accountIDs, ClaimIDs: []string{claimID}},
		Wallet: selectedWallet,
	})
	if err != nil {
		return nil, err
	}
	if len(claims) != 1 {
		quoted := ""
		for index, account := range lookupAccounts {
			if index > 0 {
				quoted += ", "
			}
			quoted += fmt.Sprintf("'%s'", account.ID)
		}
		return nil, fmt.Errorf("Can't find the channel '%s' in account(s) %s.", claimID, quoted)
	}
	old := claims[0]
	decoded, err := walletpkg.DecodeClaimValue(old.Script.Claim)
	if err != nil {
		return nil, err
	}
	if decoded.Type != "channel" {
		return nil, fmt.Errorf("A claim with id '%s' was found but it is not a channel.", claimID)
	}
	amount := old.Amount
	if normalized.named["bid"] != nil {
		amount, err = managedLBCToDewies(fmt.Sprint(normalized.named["bid"]))
		if err != nil || amount == 0 {
			return nil, fmt.Errorf("Invalid bid: %v", normalized.named["bid"])
		}
	}
	address, err := old.Address(ledger.Network)
	if err != nil {
		return nil, err
	}
	if claimAddress := fileMutationOptionalString(normalized.named["claim_address"]); claimAddress != nil {
		address = *claimAddress
	}
	transaction, _, err := walletpkg.CreateChannelUpdateTransaction(
		normalized.ctx, old, amount, address, fundingAccounts, normalized.kwargs,
		transactionListTruthy(normalized.named["replace"]),
		transactionListTruthy(normalized.named["new_signing_key"]),
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
