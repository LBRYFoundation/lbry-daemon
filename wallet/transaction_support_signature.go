package wallet

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"

	"lbry/daemon/wallet/keys"
)

var (
	ErrUnsignedSupportValue         = errors.New("support value is not signed")
	ErrSupportSignatureMissingInput = errors.New("support signature transaction has no first input")
)

// SupportSignatureDigest reproduces Output.get_signature_digest for a v2
// Support. PreviousHash is already in transaction byte order and the previous
// output position is encoded as uint32 little endian.
func SupportSignatureDigest(
	value *SupportValue, firstInput TransactionInput,
) ([sha256.Size]byte, error) {
	message, err := signedSupportMessageBytes(value)
	if err != nil {
		return [sha256.Size]byte{}, err
	}

	preimage := make([]byte, 0, sha256.Size+4+len(value.SigningChannelHash)+len(message))
	preimage = append(preimage, firstInput.PreviousHash[:]...)
	var previousIndex [4]byte
	binary.LittleEndian.PutUint32(previousIndex[:], firstInput.PreviousIndex)
	preimage = append(preimage, previousIndex[:]...)
	preimage = append(preimage, value.SigningChannelHash...)
	preimage = append(preimage, message...)
	return sha256.Sum256(preimage), nil
}

// TransactionSupportSignatureDigest derives the support digest from the
// transaction's first input outpoint.
func TransactionSupportSignatureDigest(
	value *SupportValue, transaction *Transaction,
) ([sha256.Size]byte, error) {
	if transaction == nil || len(transaction.Inputs) == 0 {
		return [sha256.Size]byte{}, ErrSupportSignatureMissingInput
	}
	return SupportSignatureDigest(value, transaction.Inputs[0])
}

// VerifySupportSignatureWithPublicKey verifies a support's compact r||s
// signature using an already-normalized compressed channel public key.
func VerifySupportSignatureWithPublicKey(
	value *SupportValue, firstInput TransactionInput, publicKey []byte,
) (bool, error) {
	digest, err := SupportSignatureDigest(value, firstInput)
	if err != nil {
		return false, err
	}
	return keys.VerifyCompactSignature(publicKey, value.Signature, digest[:])
}

// VerifySupportSignature verifies a support against a hydrated channel claim.
// Digest construction precedes channel-key extraction, matching Python's
// argument evaluation order in Output.is_signed_by.
func VerifySupportSignature(
	value *SupportValue, firstInput TransactionInput, channel *ClaimValue,
) (bool, error) {
	digest, err := SupportSignatureDigest(value, firstInput)
	if err != nil {
		return false, err
	}
	publicKey, err := ClaimChannelPublicKey(channel)
	if err != nil {
		return false, err
	}
	return keys.VerifyCompactSignature(publicKey, value.Signature, digest[:])
}

// VerifyTransactionSupportSignature verifies a support using the containing
// transaction's first input and a hydrated channel claim.
func VerifyTransactionSupportSignature(
	value *SupportValue, transaction *Transaction, channel *ClaimValue,
) (bool, error) {
	if transaction == nil || len(transaction.Inputs) == 0 {
		return false, ErrSupportSignatureMissingInput
	}
	return VerifySupportSignature(value, transaction.Inputs[0], channel)
}

// VerifyTransactionSupportSignatureWithPublicKey is the raw-key counterpart
// to VerifyTransactionSupportSignature.
func VerifyTransactionSupportSignatureWithPublicKey(
	value *SupportValue, transaction *Transaction, publicKey []byte,
) (bool, error) {
	if transaction == nil || len(transaction.Inputs) == 0 {
		return false, ErrSupportSignatureMissingInput
	}
	return VerifySupportSignatureWithPublicKey(value, transaction.Inputs[0], publicKey)
}

func signedSupportMessageBytes(value *SupportValue) ([]byte, error) {
	if value == nil {
		return nil, fmt.Errorf("%w: support value is nil", ErrInvalidSupportValue)
	}
	if !value.IsSigned() {
		return nil, ErrUnsignedSupportValue
	}
	wrapperLength := 1 + len(value.SigningChannelHash) + len(value.Signature)
	if len(value.Canonical) < wrapperLength || len(value.Canonical) == 0 || value.Canonical[0] != 1 {
		return nil, fmt.Errorf("%w: malformed signed v2 wrapper", ErrInvalidSupportValue)
	}
	return value.Canonical[wrapperLength:], nil
}
