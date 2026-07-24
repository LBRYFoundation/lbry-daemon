package wallet

import (
	"context"
	"errors"
	"fmt"
)

var (
	ErrPurchaseClaimUnavailable = errors.New("purchase claim output is unavailable")
	ErrPurchaseFundingAccount   = errors.New("purchase funding account is unavailable")
)

// CreatePurchaseTransaction mirrors Transaction.purchase: a payment output is
// followed by the zero-value purchase metadata output and funded by the
// selected wallet accounts.
func CreatePurchaseTransaction(
	ctx context.Context,
	accounts []*Account,
	claim *TransactionOutput,
	amount uint64,
	merchantAddress string,
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
	if merchantAddress == "" {
		merchantAddress, err = claim.Address(accounts[0].Network)
		if err != nil {
			return nil, fmt.Errorf("purchase merchant address: %w", err)
		}
	}
	merchantHash, err := transactionChangeAddressHash(merchantAddress)
	if err != nil {
		return nil, fmt.Errorf("purchase merchant address: %w", err)
	}
	purchaseData, err := NewPurchaseDataOutput(claimID)
	if err != nil {
		return nil, err
	}
	return CreateTransaction(
		ctx,
		nil,
		[]TransactionOutput{
			NewPayPubKeyHashOutput(amount, merchantHash),
			purchaseData,
		},
		accounts,
		accounts[0],
		true,
	)
}
