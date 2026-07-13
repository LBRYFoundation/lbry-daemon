package rpc

import (
	"errors"
	"fmt"

	walletpkg "lbry/daemon/wallet"
	"lbry/daemon/wallet/ledgerdb"
)

func selectSigningChannel(
	normalized normalizedRPCParams, ledger *walletpkg.Ledger, wallet *walletpkg.Wallet,
) (*walletpkg.TransactionOutput, error) {
	channelID := fileMutationOptionalString(normalized.named["channel_id"])
	channelName := fileMutationOptionalString(normalized.named["channel_name"])
	if channelID == nil && channelName == nil {
		return nil, nil
	}
	accountIDs, err := purchaseFundingAccountIDs(normalized.named["channel_account_id"])
	if err != nil {
		return nil, err
	}
	accounts, err := wallet.AccountsOrAll(accountIDs)
	if err != nil {
		return nil, err
	}
	query := ledgerdb.OutputQuery{AccountIDs: make([]string, len(accounts))}
	for index, account := range accounts {
		if account == nil {
			return nil, errors.New("channel account is unavailable")
		}
		query.AccountIDs[index] = account.ID
	}
	key, value := "id", ""
	if channelID != nil && *channelID != "" {
		value = *channelID
		query.ClaimIDs = []string{value}
	} else if channelName != nil {
		key, value = "name", *channelName
		query.ClaimNames = []string{value}
	} else {
		return nil, errors.New("Couldn't find channel because a channel_id or channel_name was not provided.")
	}
	channels, err := ledger.GetChannels(normalized.ctx, walletpkg.ClaimListOptions{Query: query, Wallet: wallet})
	if err != nil {
		return nil, err
	}
	if len(channels) > 1 {
		return nil, fmt.Errorf("Multiple channels found with channel_%s '%s', pass a channel_id to narrow it down.", key, value)
	}
	if len(channels) == 0 {
		return nil, fmt.Errorf("Couldn't find channel with channel_%s '%s'.", key, value)
	}
	if channels[0].PrivateKey == nil {
		return nil, fmt.Errorf("Could not find private key for channel_%s '%s'.", key, value)
	}
	return channels[0], nil
}
