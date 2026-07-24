package wallet

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"

	"lbry/daemon/wallet/keys"
)

var ErrChannelUpdateNotChannel = errors.New("claim is not a channel")

func CreateChannelUpdateTransaction(
	ctx context.Context,
	previous *TransactionOutput,
	amount uint64,
	claimAddress string,
	fundingAccounts []*Account,
	fields map[string]any,
	replace bool,
	newSigningKey bool,
) (*Transaction, *keys.PrivateKey, error) {
	if previous == nil {
		return nil, nil, ErrClaimLookupMissing
	}
	if len(fundingAccounts) == 0 || fundingAccounts[0] == nil {
		return nil, nil, ErrPurchaseFundingAccount
	}
	decoded, err := DecodeClaimValue(previous.Script.Claim)
	if err != nil {
		return nil, nil, err
	}
	if decoded.Type != "channel" {
		return nil, nil, ErrChannelUpdateNotChannel
	}
	publicKeyHex, _ := decoded.Value["public_key"].(string)
	publicKey, err := hex.DecodeString(publicKeyHex)
	if err != nil {
		return nil, nil, err
	}
	privateKey := previous.PrivateKey
	if newSigningKey {
		account := fundingAccounts[0]
		if account.ledger == nil || account.DeterministicChannelKeys == nil {
			return nil, nil, ErrChannelKeyManagerUnavailable
		}
		privateKey, err = account.DeterministicChannelKeys.GenerateNextKey(
			account.ledger.ChannelKeyUsage(ctx),
		)
		if err != nil {
			return nil, nil, err
		}
		publicKey = privateKey.PublicKey().CompressedBytes()
	}
	claim, err := BuildChannelClaim(previous.Script.Claim, publicKey, replace, fields)
	if err != nil {
		return nil, nil, err
	}
	claimHash, err := transactionChangeAddressHash(claimAddress)
	if err != nil {
		return nil, nil, fmt.Errorf("channel claim address: %w", err)
	}
	claimID, err := previous.ClaimID()
	if err != nil {
		return nil, nil, err
	}
	output, err := NewUpdateClaimOutput(
		amount, string(previous.Script.ClaimName), claimID, claim, claimHash,
	)
	if err != nil {
		return nil, nil, err
	}
	input, err := NewSpendInput(previous)
	if err != nil {
		return nil, nil, err
	}
	transaction, err := CreateTransaction(
		ctx, []TransactionInput{input}, []TransactionOutput{output},
		fundingAccounts, fundingAccounts[0], true,
	)
	if err != nil {
		return nil, nil, err
	}
	transaction.Outputs[0].PrivateKey = privateKey
	return transaction, privateKey, nil
}
