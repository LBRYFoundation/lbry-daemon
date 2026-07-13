package wallet

import (
	"context"
	"errors"
	"fmt"

	"lbry/daemon/wallet/keys"
)

var ErrChannelKeyManagerUnavailable = errors.New("channel key manager is unavailable")

func CreateChannelTransaction(
	ctx context.Context,
	name string,
	amount uint64,
	claimAddress string,
	fundingAccounts []*Account,
	fields map[string]any,
) (*Transaction, *keys.PrivateKey, error) {
	if len(fundingAccounts) == 0 || fundingAccounts[0] == nil {
		return nil, nil, ErrPurchaseFundingAccount
	}
	account := fundingAccounts[0]
	if account.ledger == nil || account.DeterministicChannelKeys == nil {
		return nil, nil, ErrChannelKeyManagerUnavailable
	}
	privateKey, err := account.DeterministicChannelKeys.GenerateNextKey(
		account.ledger.ChannelKeyUsage(ctx),
	)
	if err != nil {
		return nil, nil, err
	}
	claim, err := BuildChannelClaim(nil, privateKey.PublicKey().CompressedBytes(), false, fields)
	if err != nil {
		return nil, nil, err
	}
	claimHash, err := transactionChangeAddressHash(claimAddress)
	if err != nil {
		return nil, nil, fmt.Errorf("channel claim address: %w", err)
	}
	output := NewClaimNameOutput(amount, name, claim, claimHash)
	transaction, err := CreateTransaction(
		ctx, nil, []TransactionOutput{output}, fundingAccounts, fundingAccounts[0], true,
	)
	if err != nil {
		return nil, nil, err
	}
	transaction.Outputs[0].PrivateKey = privateKey
	return transaction, privateKey, nil
}
