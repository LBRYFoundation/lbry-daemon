package rpc

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	walletpkg "lbry/daemon/wallet"
	"lbry/daemon/wallet/ledgerdb"
)

func (rpcServer *RPCServer) handleChannelCreate(response http.ResponseWriter, params any) {
	result, err := rpcServer.channelCreate(params.(normalizedRPCParams))
	if err != nil {
		panic(err)
	}
	sendResultResponse(response, result)
}

func (rpcServer *RPCServer) channelCreate(normalized normalizedRPCParams) (map[string]any, error) {
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
	name, _ := normalized.named["name"].(string)
	if err := validateChannelCreateName(name); err != nil {
		return nil, err
	}
	amount, err := managedLBCToDewies(fmt.Sprint(normalized.named["bid"]))
	if err != nil || amount == 0 {
		return nil, fmt.Errorf("Invalid bid: %v", normalized.named["bid"])
	}
	accountID, err := transactionListAccountID(normalized.named["account_id"])
	if err != nil {
		return nil, err
	}
	holdingAccount, err := selectedWallet.AccountOrDefault(accountID)
	if err != nil {
		return nil, err
	}
	if holdingAccount == nil || holdingAccount.Receiving == nil {
		return nil, errors.New("channel holding account is unavailable")
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
	ledger := manager.DefaultLedger()
	if ledger == nil {
		return nil, errors.New("default ledger is unavailable")
	}
	accountIDs := make([]string, len(selectedWallet.Accounts))
	for index, account := range selectedWallet.Accounts {
		if account == nil {
			return nil, errors.New("channel account is unavailable")
		}
		accountIDs[index] = account.ID
	}
	existing, err := ledger.GetChannels(normalized.ctx, walletpkg.ClaimListOptions{
		Query:  ledgerdb.OutputQuery{AccountIDs: accountIDs, ClaimNames: []string{name}},
		Wallet: selectedWallet,
	})
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 && !transactionListTruthy(normalized.named["allow_duplicate_name"]) {
		return nil, fmt.Errorf(
			"You already have a channel under the name '%s'. Use --allow-duplicate-name flag to override.",
			name,
		)
	}
	claimAddress := fileMutationOptionalString(normalized.named["claim_address"])
	address := ""
	if claimAddress != nil {
		address = *claimAddress
	} else {
		address, err = holdingAccount.Receiving.GetOrCreateUsableAddress(normalized.ctx)
		if err != nil {
			return nil, err
		}
	}
	transaction, _, err := walletpkg.CreateChannelTransaction(
		normalized.ctx, name, amount, address, fundingAccounts, normalized.kwargs,
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

func validateChannelCreateName(name string) error {
	if name == "" {
		return errors.New("Channel name cannot be blank.")
	}
	parsed, err := walletpkg.ParseLBRYURL(name)
	if err != nil {
		return errors.New("Invalid channel name.")
	}
	if parsed.HasStreamInChannel {
		return errors.New("Channel name has invalid character")
	}
	if parsed.HasStream {
		return errors.New("Channel names must start with '@' symbol.")
	}
	if name[0] != '@' {
		return errors.New("Channel names must start with '@' symbol.")
	}
	return nil
}
