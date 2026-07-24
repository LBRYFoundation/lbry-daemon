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

func (rpcServer *RPCServer) handleSupportAbandon(response http.ResponseWriter, params any) {
	result, err := rpcServer.supportAbandon(params.(normalizedRPCParams))
	if err != nil {
		panic(err)
	}
	sendResultResponse(response, result)
}

func (rpcServer *RPCServer) supportAbandon(normalized normalizedRPCParams) (map[string]any, error) {
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
	accounts := selectedWallet.Accounts
	if accountID != nil {
		accounts = []*walletpkg.Account{changeAccount}
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
	accountIDs := make([]string, len(accounts))
	for index, account := range accounts {
		if account == nil {
			return nil, errors.New("support account is unavailable")
		}
		accountIDs[index] = account.ID
	}
	query.AccountIDs = accountIDs
	supports, err := ledger.GetSupports(
		normalized.ctx, walletpkg.ClaimListOptions{Query: query, Wallet: selectedWallet},
	)
	if err != nil {
		return nil, err
	}
	if len(supports) == 0 {
		return nil, errors.New("No supports found for the specified claim_id or txid:nout")
	}
	var replacements []walletpkg.TransactionOutput
	if keep := normalized.named["keep"]; keep != nil {
		keepAmount, keepErr := managedLBCToDewies(fmt.Sprint(keep))
		if keepErr != nil {
			return nil, fmt.Errorf("Invalid keep: %v", keep)
		}
		if keepAmount > 0 {
			first := supports[0]
			firstClaimID, claimErr := first.ClaimID()
			if claimErr != nil {
				return nil, claimErr
			}
			replacement, replacementErr := walletpkg.NewSupportOutput(
				keepAmount, string(first.Script.ClaimName), firstClaimID, first.Script.PubKeyHash,
			)
			if replacementErr != nil {
				return nil, replacementErr
			}
			replacements = []walletpkg.TransactionOutput{replacement}
		}
	}
	transaction, err := walletpkg.CreateAbandonTransaction(
		normalized.ctx, supports, replacements, accounts, changeAccount,
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
