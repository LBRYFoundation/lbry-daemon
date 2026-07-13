package rpc

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	databasepkg "lbry/daemon/database"
	walletpkg "lbry/daemon/wallet"
)

func (rpcServer *RPCServer) handleSupportCreate(response http.ResponseWriter, params any) {
	result, err := rpcServer.supportCreate(params.(normalizedRPCParams))
	if err != nil {
		panic(err)
	}
	sendResultResponse(response, result)
}

func (rpcServer *RPCServer) supportCreate(normalized normalizedRPCParams) (map[string]any, error) {
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
	claimID, _ := normalized.named["claim_id"].(string)
	ledger := manager.DefaultLedger()
	if ledger == nil {
		return nil, errors.New("default ledger is unavailable")
	}
	channel, err := selectSigningChannel(normalized, ledger, selectedWallet)
	if err != nil {
		return nil, err
	}
	claim, err := ledger.GetClaimByClaimID(
		normalized.ctx, claimID,
		walletpkg.ResolvedTransactionOutputAnnotationOptions{},
	)
	if errors.Is(err, walletpkg.ErrClaimLookupMissing) {
		return nil, fmt.Errorf("Could not find claim with claim_id '%s'.", claimID)
	}
	if err != nil {
		return nil, err
	}
	amount, err := managedLBCToDewies(fmt.Sprint(normalized.named["amount"]))
	if err != nil || amount == 0 {
		return nil, fmt.Errorf("Invalid amount: %v", normalized.named["amount"])
	}
	holdingAddress, err := claim.Address(ledger.Network)
	if err != nil {
		return nil, err
	}
	if !transactionListTruthy(normalized.named["tip"]) {
		accountID, err := transactionListAccountID(normalized.named["account_id"])
		if err != nil {
			return nil, err
		}
		account, err := selectedWallet.AccountOrDefault(accountID)
		if err != nil {
			return nil, err
		}
		if account == nil || account.Receiving == nil {
			return nil, errors.New("support holding account is unavailable")
		}
		holdingAddress, err = account.Receiving.GetOrCreateUsableAddress(normalized.ctx)
		if err != nil {
			return nil, err
		}
	}
	comment := fileMutationOptionalString(normalized.named["comment"])
	transaction, err := walletpkg.CreateSupportTransaction(
		normalized.ctx, fundingAccounts, claim, amount, holdingAddress, comment, channel,
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
	if !transactionListTruthy(normalized.named["preview"]) {
		saver, ok := rpcServer.resolvedClaimSaver.(SupportSaver)
		if !ok {
			return nil, errors.New("support saver is unavailable")
		}
		if err := saver.SaveSupports(context.WithoutCancel(normalized.ctx), claimID, []databasepkg.SupportRow{{
			Outpoint: fmt.Sprintf("%s:%d", transaction.ID, transaction.Position),
			ClaimID:  claimID, Amount: int64(amount), Address: holdingAddress,
		}}); err != nil {
			return nil, err
		}
	}
	return ledger.LegacyTransactionJSON(transaction)
}
