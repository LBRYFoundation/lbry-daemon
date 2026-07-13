package keys

import (
	"bytes"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/hex"
	"errors"
	"testing"

	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

const (
	compactVectorPrivate = "2423f3dc6087d9683f73a684935abc0ccd8bc26370588f56653128c6a6f0bf7c"
	compactVectorPublic  = "0243671cb26d01375c75dca6c4a2adc57fdbb55e69c32db9db38c7d23f8ed5538b"
	compactVectorDigest  = "9fc0b0a4a1e7a2aa2b0cd0a5566f4847ed9f66f92c7f0fc3cc4e3cea6f29a0ff"
	compactVectorSig     = "100f7542643e64d9efa3c78c60210de67585889e5efa715eb2b30ae5d047d809" +
		"1a9f9d0e13182b030eefcb567f3a6c5597259bd21ac0275a2b394c28a6c5e61e"
	compactVectorHighS = "100f7542643e64d9efa3c78c60210de67585889e5efa715eb2b30ae5d047d809" +
		"e56062f1ece7d4fcf11034a980c593a923894114948878e19499126429705b23"
)

func TestCompactSignatureMatchesPinnedLibsecpVector(t *testing.T) {
	privateKey, err := NewPrivateKey(
		MainNet, mustSigningHex(t, compactVectorPrivate), make([]byte, 32), 0, 0, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	digest := mustSigningHex(t, compactVectorDigest)
	first, err := privateKey.SignCompact(digest)
	if err != nil {
		t.Fatal(err)
	}
	second, err := privateKey.SignCompact(digest)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(first); got != compactVectorSig {
		t.Fatalf("compact signature = %s, want %s", got, compactVectorSig)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("RFC6979 signing was not deterministic")
	}
	if len(first) != CompactSignatureLength {
		t.Fatalf("signature length = %d, want %d", len(first), CompactSignatureLength)
	}
	if bytes.Equal(first, ecdsa.SignCompact(privateKey.key, digest, true)) {
		t.Fatal("raw signature unexpectedly equals 65-byte recoverable format")
	}

	verified, err := privateKey.PublicKey().Verify(first, digest)
	if err != nil || !verified {
		t.Fatalf("extended public-key verify = %v, %v", verified, err)
	}
	verified, err = VerifyCompactSignature(
		mustSigningHex(t, compactVectorPublic), first, digest,
	)
	if err != nil || !verified {
		t.Fatalf("ledgerless verify = %v, %v", verified, err)
	}
}

func TestDERAndDoubleSHA256Signing(t *testing.T) {
	wantDER := "30440220" + compactVectorSig[:64] + "0220" + compactVectorSig[64:]
	der, err := serializeCompactSignatureDER(mustSigningHex(t, compactVectorSig))
	if err != nil || hex.EncodeToString(der) != wantDER {
		t.Fatalf("DER signature = %x, %v; want %s", der, err, wantDER)
	}
	if _, err := serializeCompactSignatureDER(make([]byte, 63)); !errors.Is(err, ErrInvalidSignatureLength) {
		t.Fatalf("short compact DER error = %v", err)
	}

	privateKey, err := NewPrivateKey(
		MainNet, mustSigningHex(t, compactVectorPrivate), make([]byte, 32), 0, 0, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	message := []byte("pinned transaction signing primitive")
	first, err := privateKey.Sign(message)
	if err != nil {
		t.Fatal(err)
	}
	second, err := privateKey.Sign(message)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("DER signing is not deterministic: %x / %x / %v", first, second, err)
	}
	var parsed derSignature
	rest, err := asn1.Unmarshal(first, &parsed)
	if err != nil || len(rest) != 0 || parsed.R == nil || parsed.S == nil ||
		parsed.R.Sign() <= 0 || parsed.S.Sign() <= 0 {
		t.Fatalf("parse DER signature = %#v, trailing %x, %v", parsed, rest, err)
	}
	compact := make([]byte, CompactSignatureLength)
	parsed.R.FillBytes(compact[:32])
	parsed.S.FillBytes(compact[32:])
	firstHash := sha256.Sum256(message)
	digest := sha256.Sum256(firstHash[:])
	verified, err := privateKey.PublicKey().Verify(compact, digest[:])
	if err != nil || !verified {
		t.Fatalf("verify double-SHA256 DER signature = %v, %v", verified, err)
	}
}

func TestCompactVerificationMatchesNormalizationAndFailureRules(t *testing.T) {
	publicKey, err := VerificationKeyFromCompressed(mustSigningHex(t, compactVectorPublic))
	if err != nil {
		t.Fatal(err)
	}
	digest := mustSigningHex(t, compactVectorDigest)
	for name, signature := range map[string][]byte{
		"low S":  mustSigningHex(t, compactVectorSig),
		"high S": mustSigningHex(t, compactVectorHighS),
	} {
		t.Run(name, func(t *testing.T) {
			verified, err := publicKey.Verify(signature, digest)
			if err != nil || !verified {
				t.Fatalf("verify = %v, %v", verified, err)
			}
		})
	}

	zeroR := mustSigningHex(t, compactVectorSig)
	clear(zeroR[:32])
	if verified, err := publicKey.Verify(zeroR, digest); err != nil || verified {
		t.Fatalf("zero-R verify = %v, %v", verified, err)
	}
	zeroS := mustSigningHex(t, compactVectorSig)
	clear(zeroS[32:])
	if verified, err := publicKey.Verify(zeroS, digest); err != nil || verified {
		t.Fatalf("zero-S verify = %v, %v", verified, err)
	}

	mutatedSignature := mustSigningHex(t, compactVectorSig)
	mutatedSignature[10] ^= 1
	if verified, err := publicKey.Verify(mutatedSignature, digest); err != nil || verified {
		t.Fatalf("mutated-signature verify = %v, %v", verified, err)
	}
	mutatedDigest := append([]byte(nil), digest...)
	mutatedDigest[0] ^= 1
	if verified, err := publicKey.Verify(mustSigningHex(t, compactVectorSig), mutatedDigest); err != nil || verified {
		t.Fatalf("mutated-digest verify = %v, %v", verified, err)
	}

	overflow := mustSigningHex(t, compactVectorSig)
	order := secp256k1.Params().N.Bytes()
	copy(overflow[:32], order)
	if _, err := publicKey.Verify(overflow, digest); !errors.Is(err, ErrInvalidCompactSignature) {
		t.Fatalf("overflow error = %v, want ErrInvalidCompactSignature", err)
	}
}

func TestCompactSignatureLengthAndPublicKeyValidationOrder(t *testing.T) {
	privateKey, err := NewPrivateKey(
		MainNet, mustSigningHex(t, compactVectorPrivate), make([]byte, 32), 0, 0, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, length := range []int{0, 31} {
		if _, err := privateKey.SignCompact(make([]byte, length)); !errors.Is(err, ErrInvalidDigestLength) {
			t.Fatalf("sign digest length %d error = %v", length, err)
		}
	}
	digest := append(mustSigningHex(t, compactVectorDigest), 0xff)
	longSignature, err := privateKey.SignCompact(digest)
	if err != nil || hex.EncodeToString(longSignature) != compactVectorSig {
		t.Fatalf("long digest signature = %x, %v", longSignature, err)
	}
	publicKey, err := VerificationKeyFromCompressed(mustSigningHex(t, compactVectorPublic))
	if err != nil {
		t.Fatal(err)
	}
	for _, length := range []int{0, 63, 65} {
		if _, err := publicKey.Verify(make([]byte, length), make([]byte, 31)); !errors.Is(err, ErrInvalidSignatureLength) {
			t.Fatalf("signature length %d error = %v", length, err)
		}
	}
	if _, err := publicKey.Verify(make([]byte, 64), make([]byte, 31)); !errors.Is(err, ErrInvalidDigestLength) {
		t.Fatalf("digest length error = %v", err)
	}

	for name, encoded := range map[string][]byte{
		"short":        make([]byte, 32),
		"uncompressed": append([]byte{4}, make([]byte, 64)...),
		"bad prefix":   append([]byte{5}, make([]byte, 32)...),
		"off curve":    append([]byte{2}, bytes.Repeat([]byte{0xff}, 32)...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := VerificationKeyFromCompressed(encoded); !errors.Is(err, ErrInvalidPublicKey) {
				t.Fatalf("public-key error = %v, want ErrInvalidPublicKey", err)
			}
		})
	}
}

func mustSigningHex(t *testing.T, encoded string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
