package wallet

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"

	"lbry/daemon/wallet/keys"
)

const supportSignatureDigestHex = "7d806ccc67d99bea4c37eda457008311579bf2d9beb564b39ddf96bdcf6c6e43"

func TestSupportSignatureDigestAndVerification(t *testing.T) {
	value, firstInput, privateKey, channel := supportSignatureValueFixture(t)
	digest, err := SupportSignatureDigest(value, firstInput)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(digest[:]); got != supportSignatureDigestHex {
		t.Fatalf("support signature digest = %s, want pinned Python %s", got, supportSignatureDigestHex)
	}
	value.Signature, err = privateKey.SignCompact(digest[:])
	if err != nil {
		t.Fatal(err)
	}

	verified, err := VerifySupportSignature(value, firstInput, channel)
	if err != nil || !verified {
		t.Fatalf("support/channel verification = %v, %v", verified, err)
	}
	verified, err = VerifySupportSignatureWithPublicKey(
		value, firstInput, privateKey.PublicKey().CompressedBytes(),
	)
	if err != nil || !verified {
		t.Fatalf("support/raw-key verification = %v, %v", verified, err)
	}

	transaction := &Transaction{Inputs: []TransactionInput{firstInput, {PreviousIndex: 99}}}
	fromTransaction, err := TransactionSupportSignatureDigest(value, transaction)
	if err != nil || fromTransaction != digest {
		t.Fatalf("transaction digest = %x, %v; want %x", fromTransaction, err, digest)
	}
	verified, err = VerifyTransactionSupportSignature(value, transaction, channel)
	if err != nil || !verified {
		t.Fatalf("transaction/channel verification = %v, %v", verified, err)
	}
	verified, err = VerifyTransactionSupportSignatureWithPublicKey(
		value, transaction, privateKey.PublicKey().CompressedBytes(),
	)
	if err != nil || !verified {
		t.Fatalf("transaction/raw-key verification = %v, %v", verified, err)
	}

	wrongInput := firstInput
	wrongInput.PreviousIndex++
	if verified, err = VerifySupportSignature(value, wrongInput, channel); err != nil || verified {
		t.Fatalf("wrong-outpoint verification = %v, %v", verified, err)
	}
	wrongChannelPayload := []byte{0, 0x12, 0x23, 0x0a, 0x21}
	wrongChannelPayload = append(
		wrongChannelPayload, supportSignaturePrivateKey(t, 0x22).PublicKey().CompressedBytes()...,
	)
	wrongChannel, err := DecodeClaimValue(wrongChannelPayload)
	if err != nil {
		t.Fatal(err)
	}
	if verified, err = VerifySupportSignature(value, firstInput, wrongChannel); err != nil || verified {
		t.Fatalf("wrong-channel verification = %v, %v", verified, err)
	}
	value.Signature[0] ^= 1
	if verified, err = VerifySupportSignature(value, firstInput, channel); err != nil || verified {
		t.Fatalf("mutated-signature verification = %v, %v", verified, err)
	}
}

func TestSupportSignatureErrorBoundaries(t *testing.T) {
	value, firstInput, _, channel := supportSignatureValueFixture(t)
	if _, err := SupportSignatureDigest(nil, firstInput); !errors.Is(err, ErrInvalidSupportValue) {
		t.Fatalf("nil support digest error = %v", err)
	}
	unsigned, err := DecodeSupportValue([]byte{0})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SupportSignatureDigest(unsigned, firstInput); !errors.Is(err, ErrUnsignedSupportValue) {
		t.Fatalf("unsigned support digest error = %v", err)
	}
	if _, err := TransactionSupportSignatureDigest(value, nil); !errors.Is(err, ErrSupportSignatureMissingInput) {
		t.Fatalf("nil transaction digest error = %v", err)
	}
	if verified, err := VerifyTransactionSupportSignature(value, &Transaction{}, channel); verified || !errors.Is(err, ErrSupportSignatureMissingInput) {
		t.Fatalf("missing-input verification = %v, %v", verified, err)
	}
}

func TestLegacyTransactionJSONSignedSupportUsesHydratedChannel(t *testing.T) {
	firstInput := supportSignatureFirstInput()
	privateKey := supportSignaturePrivateKey(t, 0x21)
	channel := supportSignatureChannelTransaction(t, privateKey, "@signer", 6)
	channelID, err := channel.Outputs[0].ClaimID()
	if err != nil {
		t.Fatal(err)
	}
	channelHash, err := decodeTransactionClaimID(channelID)
	if err != nil {
		t.Fatal(err)
	}
	rawMessage := supportSignatureRawMessage()
	placeholder := append([]byte{1}, channelHash...)
	placeholder = append(placeholder, make([]byte, keys.CompactSignatureLength)...)
	placeholder = append(placeholder, rawMessage...)
	value, err := DecodeSupportValue(placeholder)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := SupportSignatureDigest(value, firstInput)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := privateKey.SignCompact(digest[:])
	if err != nil {
		t.Fatal(err)
	}
	payload := append([]byte{1}, channelHash...)
	payload = append(payload, signature...)
	payload = append(payload, rawMessage...)

	support, err := NewSupportDataOutput(
		1, "supported", "00112233445566778899aabbccddeeff00112233", payload,
		bytes.Repeat([]byte{0x65}, 20),
	)
	if err != nil {
		t.Fatal(err)
	}
	transaction := NewTransaction()
	transaction.AddInputs([]TransactionInput{firstInput})
	transaction.AddOutputs([]TransactionOutput{support})
	transaction.Height = 7
	transaction.Outputs[0].Channel = &channel.Outputs[0]
	ledger := &Ledger{Network: keys.MainNet, Headers: claimWireOracleHeaders(t, 10, 7_007)}

	assertSignature := func(want bool) map[string]any {
		t.Helper()
		encoded, err := ledger.LegacyTransactionJSON(transaction)
		if err != nil {
			t.Fatal(err)
		}
		output := encoded["outputs"].([]any)[0].(map[string]any)
		if output["is_channel_signature_valid"] != want {
			t.Fatalf("support signature validity = %v, want %v", output["is_channel_signature_valid"], want)
		}
		signingChannel, ok := output["signing_channel"].(map[string]any)
		if !ok || signingChannel["claim_id"] != channelID || signingChannel["name"] != "@signer" ||
			signingChannel["value_type"] != "channel" {
			t.Fatalf("hydrated signing channel = %#v", output["signing_channel"])
		}
		return output
	}

	assertSignature(true)
	transaction.Outputs[0].Script.Support[21] ^= 1
	assertSignature(false)
}

func supportSignatureValueFixture(
	t *testing.T,
) (*SupportValue, TransactionInput, *keys.PrivateKey, *ClaimValue) {
	t.Helper()
	channelHash := make([]byte, 20)
	for index := range channelHash {
		channelHash[index] = byte(0xa0 + index)
	}
	payload := append([]byte{1}, channelHash...)
	payload = append(payload, make([]byte, keys.CompactSignatureLength)...)
	payload = append(payload, supportSignatureRawMessage()...)
	value, err := DecodeSupportValue(payload)
	if err != nil {
		t.Fatal(err)
	}

	privateKey := supportSignaturePrivateKey(t, 0x21)
	channelPayload := []byte{0, 0x12, 0x23, 0x0a, 0x21}
	channelPayload = append(channelPayload, privateKey.PublicKey().CompressedBytes()...)
	channel, err := DecodeClaimValue(channelPayload)
	if err != nil {
		t.Fatal(err)
	}
	return value, supportSignatureFirstInput(), privateKey, channel
}

func supportSignatureFirstInput() TransactionInput {
	input := TransactionInput{PreviousIndex: 0x78563412}
	for index := range input.PreviousHash {
		input.PreviousHash[index] = byte(index)
	}
	return input
}

func supportSignatureRawMessage() []byte {
	// Deliberately put comment before emoji. Python's generated protobuf and
	// DecodeSupportValue both canonicalize these fields before signing.
	return []byte{0x12, 0x06, 's', 't', 'e', 'a', 'd', 'y', 0x0a, 0x02, ':', ')'}
}

func supportSignaturePrivateKey(t *testing.T, scalar byte) *keys.PrivateKey {
	t.Helper()
	privateKey, err := keys.NewPrivateKey(
		keys.MainNet, bytes.Repeat([]byte{scalar}, 32), make([]byte, 32), 0, 0, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	return privateKey
}

func supportSignatureChannelTransaction(
	t *testing.T, privateKey *keys.PrivateKey, name string, height int64,
) *Transaction {
	t.Helper()
	publicKey := privateKey.PublicKey().CompressedBytes()
	publicKeyHash := keys.Hash160(publicKey)
	channel := NewTransaction()
	channel.AddOutputs([]TransactionOutput{NewClaimNameOutput(
		10, name, append([]byte{0, 0x12, 0x23, 0x0a, 0x21}, publicKey...), publicKeyHash[:],
	)})
	channel.Height = height
	return channel
}
