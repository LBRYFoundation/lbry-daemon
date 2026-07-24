package wallet

import (
	"context"
	"fmt"

	"lbry/daemon/wallet/keys"
)

func placeholderSignedValue(unsigned []byte, channel *TransactionOutput) ([]byte, error) {
	if channel == nil || channel.PrivateKey == nil {
		return nil, ErrChannelKeyManagerUnavailable
	}
	if len(unsigned) == 0 || unsigned[0] != 0 {
		return nil, fmt.Errorf("%w: expected unsigned v2 value", ErrInvalidClaimValue)
	}
	claimID, err := channel.ClaimID()
	if err != nil {
		return nil, err
	}
	channelHash, err := decodeTransactionClaimID(claimID)
	if err != nil {
		return nil, err
	}
	signed := make([]byte, 0, len(unsigned)+20+keys.CompactSignatureLength)
	signed = append(signed, 1)
	signed = append(signed, channelHash...)
	signed = append(signed, make([]byte, keys.CompactSignatureLength)...)
	signed = append(signed, unsigned[1:]...)
	return signed, nil
}

func finalizeSignedTransaction(
	ctx context.Context, transaction *Transaction, funding []*Account,
	channel *TransactionOutput, support bool,
) error {
	if transaction == nil || len(transaction.Outputs) == 0 || channel == nil || channel.PrivateKey == nil {
		return ErrChannelKeyManagerUnavailable
	}
	output := &transaction.Outputs[0]
	var digest [32]byte
	var err error
	if support {
		value, decodeErr := DecodeSupportValue(output.Script.Support)
		if decodeErr != nil {
			return decodeErr
		}
		digest, err = TransactionSupportSignatureDigest(value, transaction)
	} else {
		value, decodeErr := DecodeClaimValue(output.Script.Claim)
		if decodeErr != nil {
			return decodeErr
		}
		digest, err = TransactionClaimSignatureDigest(value, transaction)
	}
	if err != nil {
		return err
	}
	signature, err := channel.PrivateKey.SignCompact(digest[:])
	if err != nil {
		return err
	}
	if support {
		copy(output.Script.Support[21:85], signature)
	} else {
		copy(output.Script.Claim[21:85], signature)
	}
	if err := output.Script.Generate(); err != nil {
		return err
	}
	output.Channel = channel
	transaction.ResetDerived()
	return transaction.SignWithAccounts(ctx, funding, nil)
}

func releaseFailedSignedTransaction(ctx context.Context, funding []*Account, transaction *Transaction, err error) error {
	if len(funding) == 0 || funding[0] == nil || funding[0].ledger == nil || transaction == nil {
		return err
	}
	if releaseErr := funding[0].ledger.ReleaseTransaction(context.WithoutCancel(ctx), transaction); releaseErr != nil {
		return releaseErr
	}
	return err
}
