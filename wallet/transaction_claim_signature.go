package wallet

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"

	"lbry/daemon/wallet/keys"
)

var (
	// ErrUnsignedClaimValue marks a v2 Claim that has no signed wrapper. The
	// pinned SDK only calls Output.is_signed_by after checking is_signed.
	ErrUnsignedClaimValue = errors.New("claim value is not signed")
	// ErrClaimSignatureMissingInput marks a transaction that cannot supply the
	// first input outpoint used by the v2 signature digest.
	ErrClaimSignatureMissingInput = errors.New("claim signature transaction has no first input")
)

// ClaimSignatureDigest reproduces the v2 branch of Output.get_signature_digest.
// firstInput must be the signed transaction's first input. PreviousHash is
// already in raw transaction byte order; PreviousIndex is appended uint32 LE.
//
// Legacy v1 claims use unsigned_payload and an address-dependent digest. They
// are intentionally excluded: DecodeClaimValue reports those payloads as
// ErrUnsupportedLegacyClaimValue before a ClaimValue can reach this helper.
func ClaimSignatureDigest(value *ClaimValue, firstInput TransactionInput) ([sha256.Size]byte, error) {
	message, err := signedClaimMessageBytes(value)
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

// TransactionClaimSignatureDigest derives the v2 digest from transaction's
// first input, matching tx_ref.tx.inputs[0].txo_ref.hash in the pinned SDK.
func TransactionClaimSignatureDigest(
	value *ClaimValue, transaction *Transaction,
) ([sha256.Size]byte, error) {
	if transaction == nil || len(transaction.Inputs) == 0 {
		return [sha256.Size]byte{}, ErrClaimSignatureMissingInput
	}
	return ClaimSignatureDigest(value, transaction.Inputs[0])
}

// ClaimChannelPublicKey returns Claim.channel.public_key_bytes from a hydrated
// channel value. Both compressed keys and legacy SPKI encodings are normalized
// by the same helper used by DecodeClaimValue.
func ClaimChannelPublicKey(channel *ClaimValue) ([]byte, error) {
	if channel == nil {
		return nil, fmt.Errorf("%w: channel claim is nil", ErrInvalidChannelPublicKey)
	}
	if channel.Type != "channel" {
		return nil, fmt.Errorf("%w: claim type is %q, want channel", ErrInvalidChannelPublicKey, channel.Type)
	}
	encoded, ok := channel.Value["public_key"].(string)
	if !ok {
		return nil, fmt.Errorf("%w: channel claim has no public key", ErrInvalidChannelPublicKey)
	}
	publicKey, err := hex.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("%w: public key hex: %v", ErrInvalidChannelPublicKey, err)
	}
	publicKey, err = normalizeChannelPublicKey(publicKey)
	if err != nil {
		return nil, err
	}
	return publicKey, nil
}

// VerifyClaimSignatureWithPublicKey verifies a v2 claim's compact r||s
// signature with an already-hydrated compressed channel public key.
func VerifyClaimSignatureWithPublicKey(
	value *ClaimValue, firstInput TransactionInput, publicKey []byte,
) (bool, error) {
	digest, err := ClaimSignatureDigest(value, firstInput)
	if err != nil {
		return false, err
	}
	return keys.VerifyCompactSignature(publicKey, value.Signature, digest[:])
}

// VerifyClaimSignature verifies a v2 claim against a hydrated channel claim.
// Digest construction intentionally precedes channel-key extraction, matching
// Python's argument evaluation order in Output.is_signed_by.
func VerifyClaimSignature(
	value *ClaimValue, firstInput TransactionInput, channel *ClaimValue,
) (bool, error) {
	digest, err := ClaimSignatureDigest(value, firstInput)
	if err != nil {
		return false, err
	}
	publicKey, err := ClaimChannelPublicKey(channel)
	if err != nil {
		return false, err
	}
	return keys.VerifyCompactSignature(publicKey, value.Signature, digest[:])
}

// VerifyTransactionClaimSignature verifies a v2 claim using transaction's
// first input and a hydrated channel ClaimValue.
func VerifyTransactionClaimSignature(
	value *ClaimValue, transaction *Transaction, channel *ClaimValue,
) (bool, error) {
	if transaction == nil || len(transaction.Inputs) == 0 {
		return false, ErrClaimSignatureMissingInput
	}
	return VerifyClaimSignature(value, transaction.Inputs[0], channel)
}

// VerifyTransactionClaimSignatureWithPublicKey is the raw-key counterpart to
// VerifyTransactionClaimSignature.
func VerifyTransactionClaimSignatureWithPublicKey(
	value *ClaimValue, transaction *Transaction, publicKey []byte,
) (bool, error) {
	if transaction == nil || len(transaction.Inputs) == 0 {
		return false, ErrClaimSignatureMissingInput
	}
	return VerifyClaimSignatureWithPublicKey(value, transaction.Inputs[0], publicKey)
}

func signedClaimMessageBytes(value *ClaimValue) ([]byte, error) {
	if value == nil {
		return nil, fmt.Errorf("%w: claim value is nil", ErrInvalidClaimValue)
	}
	if !value.IsSigned() {
		return nil, ErrUnsignedClaimValue
	}
	const signedWrapperLength = 1 + 20 + keys.CompactSignatureLength
	if len(value.Canonical) < signedWrapperLength || value.Canonical[0] != 1 {
		return nil, fmt.Errorf("%w: malformed signed v2 wrapper", ErrInvalidClaimValue)
	}
	return value.Canonical[signedWrapperLength:], nil
}
