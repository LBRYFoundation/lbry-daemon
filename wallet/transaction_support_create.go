package wallet

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/encoding/protowire"
)

func CreateSupportTransaction(
	ctx context.Context,
	accounts []*Account,
	claim *TransactionOutput,
	amount uint64,
	holdingAddress string,
	comment *string,
	channel *TransactionOutput,
) (*Transaction, error) {
	if claim == nil {
		return nil, ErrPurchaseClaimUnavailable
	}
	if len(accounts) == 0 || accounts[0] == nil {
		return nil, ErrPurchaseFundingAccount
	}
	claimID, err := claim.ClaimID()
	if err != nil {
		return nil, err
	}
	holdingHash, err := transactionChangeAddressHash(holdingAddress)
	if err != nil {
		return nil, fmt.Errorf("support holding address: %w", err)
	}
	claimName := string(claim.Script.ClaimName)
	var output TransactionOutput
	if comment == nil && channel == nil {
		output, err = NewSupportOutput(amount, claimName, claimID, holdingHash)
	} else {
		support := []byte{0}
		if comment != nil && *comment != "" {
			support = protowire.AppendTag(support, 2, protowire.BytesType)
			support = protowire.AppendString(support, *comment)
		}
		if channel != nil {
			support, err = placeholderSignedValue(support, channel)
			if err != nil {
				return nil, err
			}
		}
		output, err = NewSupportDataOutput(amount, claimName, claimID, support, holdingHash)
	}
	if err != nil {
		return nil, err
	}
	transaction, err := CreateTransaction(ctx, nil, []TransactionOutput{output}, accounts, accounts[0], channel == nil)
	if err != nil || channel == nil {
		return transaction, err
	}
	if err = finalizeSignedTransaction(ctx, transaction, accounts, channel, true); err != nil {
		return nil, releaseFailedSignedTransaction(ctx, accounts, transaction, err)
	}
	return transaction, nil
}
