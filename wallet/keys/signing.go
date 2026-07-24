package keys

import (
	"crypto/sha256"
	"encoding/asn1"
	"errors"
	"fmt"
	"math/big"

	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

type derSignature struct {
	R *big.Int
	S *big.Int
}

const CompactSignatureLength = 64

var (
	ErrInvalidSignatureLength  = errors.New("Signature must be 64 bytes long.")
	ErrInvalidDigestLength     = errors.New("Digest must be 32 bytes long.")
	ErrInvalidCompactSignature = errors.New(
		"compact signature scalar is outside the secp256k1 group order",
	)
)

// VerificationKey is the ledgerless counterpart to Python's
// PublicKey.from_compressed(..., ledger=None). It deliberately exposes no
// address or extended-key behavior.
type VerificationKey struct {
	key *secp256k1.PublicKey
}

func VerificationKeyFromCompressed(publicKey []byte) (*VerificationKey, error) {
	parsed, err := parseCompressedPublicKey(publicKey)
	if err != nil {
		return nil, err
	}
	return &VerificationKey{key: parsed}, nil
}

// SignCompact reproduces PrivateKey.sign_compact. Despite the legacy name,
// its format is raw 32-byte R followed by raw 32-byte S, with no recovery byte.
func (key *PrivateKey) SignCompact(digest []byte) ([]byte, error) {
	if len(digest) < 32 {
		return nil, ErrInvalidDigestLength
	}
	if key == nil || key.key == nil {
		return nil, ErrInvalidPrivateKey
	}
	// The pinned CFFI call passes a msg32 pointer without a Python length check.
	// Buffers longer than 32 bytes are therefore accepted and truncated by
	// libsecp256k1. Rejecting shorter buffers avoids reproducing its out-of-bounds
	// read while preserving every deterministic input case.
	signature := ecdsa.Sign(key.key, digest[:32])
	serialized := make([]byte, CompactSignatureLength)
	r := signature.R()
	s := signature.S()
	r.PutBytesUnchecked(serialized[:32])
	s.PutBytesUnchecked(serialized[32:])
	return serialized, nil
}

// Sign reproduces PrivateKey.sign: double-SHA256 the complete input and return
// the deterministic low-S ECDSA signature in canonical DER form.
func (key *PrivateKey) Sign(data []byte) ([]byte, error) {
	first := sha256.Sum256(data)
	digest := sha256.Sum256(first[:])
	compact, err := key.SignCompact(digest[:])
	if err != nil {
		return nil, err
	}
	return serializeCompactSignatureDER(compact)
}

func serializeCompactSignatureDER(signature []byte) ([]byte, error) {
	if len(signature) != CompactSignatureLength {
		return nil, ErrInvalidSignatureLength
	}
	encoded, err := asn1.Marshal(derSignature{
		R: new(big.Int).SetBytes(signature[:32]),
		S: new(big.Int).SetBytes(signature[32:]),
	})
	if err != nil {
		return nil, fmt.Errorf("serialize DER signature: %w", err)
	}
	return encoded, nil
}

// Verify matches PublicKey.verify, including signature-before-digest length
// validation and acceptance of mathematically equivalent high-S signatures.
func (key *PublicKey) Verify(signature, digest []byte) (bool, error) {
	if key == nil || key.key == nil {
		return false, ErrInvalidPublicKey
	}
	return verifyCompactSignature(key.key, signature, digest)
}

func (key *VerificationKey) Verify(signature, digest []byte) (bool, error) {
	if key == nil || key.key == nil {
		return false, ErrInvalidPublicKey
	}
	return verifyCompactSignature(key.key, signature, digest)
}

func VerifyCompactSignature(publicKey, signature, digest []byte) (bool, error) {
	key, err := VerificationKeyFromCompressed(publicKey)
	if err != nil {
		return false, err
	}
	return key.Verify(signature, digest)
}

func verifyCompactSignature(
	publicKey *secp256k1.PublicKey, signature, digest []byte,
) (bool, error) {
	if len(signature) != CompactSignatureLength {
		return false, ErrInvalidSignatureLength
	}
	if len(digest) != 32 {
		return false, ErrInvalidDigestLength
	}
	var rBytes, sBytes [32]byte
	copy(rBytes[:], signature[:32])
	copy(sBytes[:], signature[32:])
	var r, s secp256k1.ModNScalar
	if r.SetBytes(&rBytes) != 0 || s.SetBytes(&sBytes) != 0 {
		return false, ErrInvalidCompactSignature
	}
	parsed := ecdsa.NewSignature(&r, &s)
	if parsed == nil {
		return false, fmt.Errorf("%w: could not construct signature", ErrInvalidCompactSignature)
	}
	return parsed.Verify(digest, publicKey), nil
}
