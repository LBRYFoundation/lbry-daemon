package wallet

import (
	"bytes"
	"encoding/hex"
	"errors"
	"math/big"
	"testing"

	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"

	"lbry/daemon/wallet/keys"
)

const (
	claimSignaturePrivateKeyHex = "2423f3dc6087d9683f73a684935abc0ccd8bc26370588f56653128c6a6f0bf7c"
	claimSignaturePublicKeyHex  = "0243671cb26d01375c75dca6c4a2adc57fdbb55e69c32db9db38c7d23f8ed5538b"
	claimSignatureDigestHex     = "3dd4185ededd931d8d2d3460d9e4971057cc7cbc637d4488b58ca15b2230450e"
)

func TestClaimSignatureDigestMatchesPinnedPythonV2Fixture(t *testing.T) {
	value, firstInput, _, _ := claimSignatureFixture(t)
	digest, err := ClaimSignatureDigest(value, firstInput)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(digest[:]); got != claimSignatureDigestHex {
		t.Fatalf("v2 claim signature digest = %s, want pinned Python %s", got, claimSignatureDigestHex)
	}

	transaction := &Transaction{Inputs: []TransactionInput{firstInput}}
	fromTransaction, err := TransactionClaimSignatureDigest(value, transaction)
	if err != nil || fromTransaction != digest {
		t.Fatalf("transaction digest = %x, %v; want %x", fromTransaction, err, digest)
	}

	// The fixture's source protobuf fields are title then stream. Both Python's
	// generated message and DecodeClaimValue canonicalize them before hashing.
	if got := hex.EncodeToString(value.Canonical[85:]); got != "0a0042065369676e6564" {
		t.Fatalf("canonical message = %s", got)
	}
}

func TestVerifyClaimSignatureV2(t *testing.T) {
	value, firstInput, privateKey, channel := claimSignatureFixture(t)
	digest, err := ClaimSignatureDigest(value, firstInput)
	if err != nil {
		t.Fatal(err)
	}
	value.Signature, err = privateKey.SignCompact(digest[:])
	if err != nil {
		t.Fatal(err)
	}
	publicKey := privateKey.PublicKey().CompressedBytes()

	verified, err := VerifyClaimSignatureWithPublicKey(value, firstInput, publicKey)
	if err != nil || !verified {
		t.Fatalf("raw-key verification = %v, %v", verified, err)
	}
	verified, err = VerifyClaimSignature(value, firstInput, channel)
	if err != nil || !verified {
		t.Fatalf("channel verification = %v, %v", verified, err)
	}

	transaction := &Transaction{Inputs: []TransactionInput{
		firstInput,
		{PreviousIndex: 99},
	}}
	verified, err = VerifyTransactionClaimSignature(value, transaction, channel)
	if err != nil || !verified {
		t.Fatalf("transaction/channel verification = %v, %v", verified, err)
	}
	verified, err = VerifyTransactionClaimSignatureWithPublicKey(value, transaction, publicKey)
	if err != nil || !verified {
		t.Fatalf("transaction/raw-key verification = %v, %v", verified, err)
	}

	wrongInput := firstInput
	wrongInput.PreviousIndex++
	if verified, err = VerifyClaimSignature(value, wrongInput, channel); err != nil || verified {
		t.Fatalf("wrong-outpoint verification = %v, %v", verified, err)
	}

	highSignature := append([]byte(nil), value.Signature...)
	s := new(big.Int).SetBytes(highSignature[32:])
	highS := new(big.Int).Sub(secp256k1.Params().N, s).Bytes()
	clear(highSignature[32:])
	copy(highSignature[len(highSignature)-len(highS):], highS)
	value.Signature = highSignature
	if verified, err = VerifyClaimSignature(value, firstInput, channel); err != nil || !verified {
		t.Fatalf("Python-normalized high-S verification = %v, %v", verified, err)
	}

	value.Signature[0] ^= 1
	if verified, err = VerifyClaimSignature(value, firstInput, channel); err != nil || verified {
		t.Fatalf("mutated-signature verification = %v, %v", verified, err)
	}
}

func TestClaimSignatureVerificationErrorBoundaries(t *testing.T) {
	value, firstInput, privateKey, channel := claimSignatureFixture(t)
	digest, err := ClaimSignatureDigest(value, firstInput)
	if err != nil {
		t.Fatal(err)
	}
	value.Signature, err = privateKey.SignCompact(digest[:])
	if err != nil {
		t.Fatal(err)
	}

	if _, err := ClaimSignatureDigest(nil, firstInput); !errors.Is(err, ErrInvalidClaimValue) {
		t.Fatalf("nil claim digest error = %v", err)
	}
	unsigned, err := DecodeClaimValue([]byte{0, 0x0a, 0x00})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ClaimSignatureDigest(unsigned, firstInput); !errors.Is(err, ErrUnsignedClaimValue) {
		t.Fatalf("unsigned claim digest error = %v", err)
	}
	malformed := *value
	malformed.Canonical = []byte{1}
	if _, err := ClaimSignatureDigest(&malformed, firstInput); !errors.Is(err, ErrInvalidClaimValue) {
		t.Fatalf("malformed canonical digest error = %v", err)
	}
	if _, err := TransactionClaimSignatureDigest(value, nil); !errors.Is(err, ErrClaimSignatureMissingInput) {
		t.Fatalf("nil transaction digest error = %v", err)
	}
	if verified, err := VerifyTransactionClaimSignature(value, &Transaction{}, channel); verified || !errors.Is(err, ErrClaimSignatureMissingInput) {
		t.Fatalf("missing-input verification = %v, %v", verified, err)
	}

	stream, err := DecodeClaimValue([]byte{0, 0x0a, 0x00})
	if err != nil {
		t.Fatal(err)
	}
	if verified, err := VerifyClaimSignature(value, firstInput, stream); verified || !errors.Is(err, ErrInvalidChannelPublicKey) {
		t.Fatalf("non-channel verification = %v, %v", verified, err)
	}
	if _, err := ClaimChannelPublicKey(nil); !errors.Is(err, ErrInvalidChannelPublicKey) {
		t.Fatalf("nil channel key error = %v", err)
	}
	missingKey := &ClaimValue{Type: "channel", Value: map[string]any{}}
	if _, err := ClaimChannelPublicKey(missingKey); !errors.Is(err, ErrInvalidChannelPublicKey) {
		t.Fatalf("missing channel key error = %v", err)
	}
	invalidHex := &ClaimValue{Type: "channel", Value: map[string]any{"public_key": "zz"}}
	if _, err := ClaimChannelPublicKey(invalidHex); !errors.Is(err, ErrInvalidChannelPublicKey) {
		t.Fatalf("invalid channel key hex error = %v", err)
	}

	value.Signature = value.Signature[:63]
	if verified, err := VerifyClaimSignatureWithPublicKey(value, firstInput, privateKey.PublicKey().CompressedBytes()); verified || !errors.Is(err, keys.ErrInvalidSignatureLength) {
		t.Fatalf("short signature verification = %v, %v", verified, err)
	}
	// PublicKey.from_compressed runs before PublicKey.verify checks signature
	// length in Python, and keys.VerifyCompactSignature preserves that order.
	if verified, err := VerifyClaimSignatureWithPublicKey(value, firstInput, []byte{2}); verified || !errors.Is(err, keys.ErrInvalidPublicKey) {
		t.Fatalf("bad-key/short-signature verification = %v, %v", verified, err)
	}

	value.Signature = make([]byte, keys.CompactSignatureLength)
	copy(value.Signature[:32], secp256k1.Params().N.Bytes())
	if verified, err := VerifyClaimSignature(value, firstInput, channel); verified || !errors.Is(err, keys.ErrInvalidCompactSignature) {
		t.Fatalf("overflow signature verification = %v, %v", verified, err)
	}

	if decoded, err := DecodeClaimValue([]byte{2, 0x0a, 0x00}); decoded != nil || !errors.Is(err, ErrUnsupportedLegacyClaimValue) {
		t.Fatalf("legacy claim decode = %#v, %v", decoded, err)
	}
}

func TestClaimChannelPublicKeyReturnsIndependentCompressedBytes(t *testing.T) {
	_, _, _, channel := claimSignatureFixture(t)
	publicKey, err := ClaimChannelPublicKey(channel)
	if err != nil {
		t.Fatal(err)
	}
	want := claimSignatureMustHex(t, claimSignaturePublicKeyHex)
	if !bytes.Equal(publicKey, want) {
		t.Fatalf("channel public key = %x, want %x", publicKey, want)
	}
	publicKey[0] ^= 1
	again, err := ClaimChannelPublicKey(channel)
	if err != nil || !bytes.Equal(again, want) {
		t.Fatalf("channel public key after caller mutation = %x, %v", again, err)
	}
}

func claimSignatureFixture(
	t *testing.T,
) (*ClaimValue, TransactionInput, *keys.PrivateKey, *ClaimValue) {
	t.Helper()
	channelHash := make([]byte, 20)
	for index := range channelHash {
		channelHash[index] = byte(0xa0 + index)
	}
	payload := append([]byte{1}, channelHash...)
	payload = append(payload, make([]byte, keys.CompactSignatureLength)...)
	// Deliberately noncanonical protobuf field order: title precedes stream.
	payload = append(payload, claimSignatureMustHex(t, "42065369676e65640a00")...)
	value, err := DecodeClaimValue(payload)
	if err != nil {
		t.Fatal(err)
	}

	firstInput := TransactionInput{PreviousIndex: 0x78563412}
	for index := range firstInput.PreviousHash {
		firstInput.PreviousHash[index] = byte(index)
	}
	privateKey, err := keys.NewPrivateKey(
		keys.RegTest, claimSignatureMustHex(t, claimSignaturePrivateKeyHex), make([]byte, 32), 0, 0, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := privateKey.PublicKey().CompressedBytes()
	channelPayload := []byte{0, 0x12, 0x23, 0x0a, 0x21}
	channelPayload = append(channelPayload, publicKey...)
	channel, err := DecodeClaimValue(channelPayload)
	if err != nil {
		t.Fatal(err)
	}
	return value, firstInput, privateKey, channel
}

func claimSignatureMustHex(t *testing.T, encoded string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
