package wallet

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"lbry/daemon/wallet/keys"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestTransactionWireLegacyClaimIntegrationMatchesPinnedOracle(t *testing.T) {
	oracle := runTransactionShowLegacyClaimOracle(t)
	ledger := &Ledger{
		Network: keys.MainNet,
		Headers: claimWireOracleHeaders(t, 10, 7_007),
	}

	for _, fixture := range oracle.Cases {
		fixture := fixture
		t.Run(fixture.Name, func(t *testing.T) {
			payload, err := hex.DecodeString(fixture.OriginalPayloadHex)
			if err != nil {
				t.Fatal(err)
			}
			decoded, decodeErr := decodeTransactionWireClaimValue(payload)
			assertTransactionWireLegacyClaimConversion(t, fixture, decoded, decodeErr)

			transaction := transactionWireLegacyClaimFixture(t, fixture, payload)
			encoded, encodeErr := ledger.LegacyTransactionJSONWithOptions(
				transaction,
				LegacyTransactionJSONOptions{IncludeProtobuf: fixture.IncludeProtobuf},
			)
			assertTransactionWireLegacyClaimEncoding(t, fixture, encoded, encodeErr)
		})
	}
}

func assertTransactionWireLegacyClaimConversion(
	t *testing.T,
	fixture transactionShowLegacyClaimOracleCase,
	decoded transactionWireClaimValue,
	err error,
) {
	t.Helper()
	if fixture.ConversionError != nil {
		if decoded.value != nil || transactionWirePythonErrorName(err) != fixture.ConversionError.Type {
			t.Fatalf("conversion = %+v, %T %v; want %s", decoded, err, err, fixture.ConversionError.Type)
		}
		return
	}
	if err != nil || decoded.value == nil || fixture.Conversion == nil {
		t.Fatalf("conversion = %+v, %v; oracle %+v", decoded, err, fixture.Conversion)
	}
	conversion := fixture.Conversion
	if decoded.value.Type != *conversion.ValueType ||
		hex.EncodeToString(decoded.value.Canonical) != conversion.CanonicalV2Hex ||
		decoded.value.IsSigned() != conversion.IsSigned {
		t.Fatalf("converted claim = %+v; oracle %+v", decoded.value, conversion)
	}
	expectedValue := transactionWireLegacyClaimValueWithoutPublicKeyID(conversion.Value)
	claimWireOracleEqual(t, decoded.value.Value, expectedValue)

	if got := optionalTransactionWireLegacyHex(decoded.value.Signature); !equalOptionalString(got, conversion.SignatureHex) {
		t.Fatalf("signature = %v, want %v", got, conversion.SignatureHex)
	}
	if got := optionalTransactionWireLegacyHex(decoded.value.SigningChannelHash); !equalOptionalString(got, conversion.SigningChannelHashHex) {
		t.Fatalf("signing channel hash = %v, want %v", got, conversion.SigningChannelHashHex)
	}
	if got := decoded.value.SigningChannelID(); !equalOptionalString(got, conversion.SigningChannelID) {
		t.Fatalf("signing channel ID = %v, want %v", got, conversion.SigningChannelID)
	}

	if conversion.SourceVersion == 0 {
		if decoded.legacyV1 != nil {
			t.Fatalf("v0 conversion retained v1 metadata: %+v", decoded.legacyV1)
		}
		return
	}
	if decoded.legacyV1 == nil || decoded.legacyV1.Version != 1 ||
		decoded.legacyV1.SignatureType != conversion.SignatureType {
		t.Fatalf("v1 metadata = %+v; oracle %+v", decoded.legacyV1, conversion)
	}
	if got := optionalTransactionWireLegacyHex(decoded.legacyV1.UnsignedPayload); !equalOptionalString(got, conversion.UnsignedV1PayloadHex) {
		t.Fatalf("unsigned v1 payload = %v, want %v", got, conversion.UnsignedV1PayloadHex)
	}
}

func assertTransactionWireLegacyClaimEncoding(
	t *testing.T,
	fixture transactionShowLegacyClaimOracleCase,
	encoded map[string]any,
	err error,
) {
	t.Helper()
	if fixture.EncodingError != nil {
		if encoded != nil || transactionWirePythonErrorName(err) != fixture.EncodingError.Type {
			t.Fatalf("encoding = %#v, %T %v; want %s", encoded, err, err, fixture.EncodingError.Type)
		}
		return
	}
	if err != nil || encoded == nil {
		t.Fatalf("encoding = %#v, %v", encoded, err)
	}
	outputs, ok := encoded["outputs"].([]any)
	if !ok || len(outputs) != 1 {
		t.Fatalf("outputs = %#v", encoded["outputs"])
	}
	output, ok := outputs[0].(map[string]any)
	if !ok {
		t.Fatalf("output = %#v", outputs[0])
	}
	if fixture.ConversionError != nil {
		for _, absent := range []string{
			"value", "value_type", "protobuf", "signing_channel", "is_channel_signature_valid",
		} {
			if _, exists := output[absent]; exists {
				t.Fatalf("suppressed %s contains %s: %#v", fixture.ConversionError.Type, absent, output)
			}
		}
		return
	}

	conversion := fixture.Conversion
	if output["claim_op"] != fixture.Operation || output["value_type"] != *conversion.ValueType {
		t.Fatalf("claim operation/type = %v/%v", output["claim_op"], output["value_type"])
	}
	encodedValue, ok := output["value"].(map[string]any)
	if !ok {
		t.Fatalf("encoded claim value = %#v", output["value"])
	}
	claimWireOracleEqual(
		t,
		transactionWireLegacyClaimValueWithoutPublicKeyID(encodedValue),
		transactionWireLegacyClaimValueWithoutPublicKeyID(conversion.Value),
	)
	protobuf, hasProtobuf := output["protobuf"]
	if hasProtobuf != fixture.IncludeProtobuf ||
		(hasProtobuf && protobuf != conversion.CanonicalV2Hex) {
		t.Fatalf("protobuf = %v, %t; want %s, %t", protobuf, hasProtobuf,
			conversion.CanonicalV2Hex, fixture.IncludeProtobuf)
	}
	if *conversion.ValueType == "channel" {
		if output["has_signing_key"] != false {
			t.Fatalf("channel signing-key state = %#v", output["has_signing_key"])
		}
		publicKey, err := hex.DecodeString(encodedValue["public_key"].(string))
		if err != nil {
			t.Fatal(err)
		}
		publicKeyID, err := keys.AddressFromPublicKeyBytes(keys.MainNet, publicKey)
		if err != nil {
			t.Fatal(err)
		}
		if encodedValue["public_key_id"] != publicKeyID {
			t.Fatalf("channel public key ID = %v, want %s", encodedValue["public_key_id"], publicKeyID)
		}
	}
	if conversion.IsSigned {
		stub, ok := output["signing_channel"].(map[string]any)
		if !ok || conversion.SigningChannelID == nil ||
			stub["channel_id"] != *conversion.SigningChannelID ||
			output["is_channel_signature_valid"] != false {
			t.Fatalf("remote signing channel = %#v / %#v", stub, output["is_channel_signature_valid"])
		}
	}
}

func transactionWireLegacyClaimFixture(
	t *testing.T, fixture transactionShowLegacyClaimOracleCase, payload []byte,
) *Transaction {
	t.Helper()
	pubKeyHash := bytes.Repeat([]byte{0x51}, 20)
	var output TransactionOutput
	if fixture.Operation == "update" {
		var err error
		output, err = NewUpdateClaimOutput(
			100_000_000, "Legacy-Claim", strings.Repeat("11", 20), payload, pubKeyHash,
		)
		if err != nil {
			t.Fatal(err)
		}
	} else {
		output = NewClaimNameOutput(100_000_000, "Legacy-Claim", payload, pubKeyHash)
	}
	return claimWireOracleTransaction(t, strings.Repeat("ab", 32), 7, output)
}

func optionalTransactionWireLegacyHex(value []byte) *string {
	if value == nil {
		return nil
	}
	encoded := hex.EncodeToString(value)
	return &encoded
}

func equalOptionalString(left, right *string) bool {
	return left == nil && right == nil ||
		left != nil && right != nil && *left == *right
}

func transactionWireLegacyClaimValueWithoutPublicKeyID(value map[string]any) map[string]any {
	cloned := make(map[string]any, len(value))
	for key, item := range value {
		if key != "public_key_id" {
			cloned[key] = item
		}
	}
	return cloned
}

func TestTransactionWireLegacyClaimDispatcherKeepsV2DecoderContract(t *testing.T) {
	for _, payload := range [][]byte{{2}, []byte(`{"sources":`)} {
		if _, err := DecodeClaimValue(payload); !errors.Is(err, ErrUnsupportedLegacyClaimValue) {
			t.Fatalf("DecodeClaimValue(%x) = %v", payload, err)
		}
	}
}

func TestTransactionWireLegacyV1HydratedSignatureUsesAddressDigest(t *testing.T) {
	channelKey, err := keys.PrivateKeyFromSeed(keys.MainNet, bytes.Repeat([]byte{0x5a}, 32))
	if err != nil {
		t.Fatal(err)
	}
	channelPayload := transactionWireLegacyV1ChannelPayload(
		t, channelKey.PublicKey().CompressedBytes(),
	)
	channelTransaction := claimWireOracleTransaction(
		t, strings.Repeat("ca", 32), 7,
		NewClaimNameOutput(
			100_000_000, "@legacy",
			channelPayload, bytes.Repeat([]byte{0x52}, 20),
		),
	)
	channelID, err := channelTransaction.Outputs[0].ClaimID()
	if err != nil {
		t.Fatal(err)
	}
	certificateID, err := hex.DecodeString(channelID)
	if err != nil {
		t.Fatal(err)
	}

	placeholder := make([]byte, keys.CompactSignatureLength)
	payload := transactionWireLegacyV1StreamPayload(t, certificateID, placeholder)
	streamTransaction := claimWireOracleTransaction(
		t, strings.Repeat("cb", 32), 7,
		NewClaimNameOutput(
			100_000_000, "legacy-stream",
			payload, bytes.Repeat([]byte{0x53}, 20),
		),
	)
	decoded, err := decodeTransactionWireClaimValue(payload)
	if err != nil {
		t.Fatal(err)
	}
	address, err := streamTransaction.Outputs[0].Address(keys.MainNet)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := LegacyV1ClaimSignatureDigest(address, decoded.value, decoded.legacyV1)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := channelKey.SignCompact(digest[:])
	if err != nil {
		t.Fatal(err)
	}
	streamTransaction.Outputs[0].Script.Claim = transactionWireLegacyV1StreamPayload(
		t, certificateID, signature,
	)
	streamTransaction.Outputs[0].Channel = &channelTransaction.Outputs[0]

	ledger := &Ledger{
		Network: keys.MainNet,
		Headers: claimWireOracleHeaders(t, 10, 7_007),
	}
	encoded, err := ledger.LegacyTransactionJSON(streamTransaction)
	if err != nil {
		t.Fatal(err)
	}
	output := encoded["outputs"].([]any)[0].(map[string]any)
	if output["is_channel_signature_valid"] != true {
		t.Fatalf("valid legacy v1 signature = %#v", output)
	}
	channel, ok := output["signing_channel"].(map[string]any)
	if !ok || channel["claim_id"] != channelID || channel["value_type"] != "channel" {
		t.Fatalf("hydrated legacy v1 channel = %#v", output["signing_channel"])
	}

	invalidSignature := append([]byte(nil), signature...)
	invalidSignature[len(invalidSignature)-1] ^= 1
	streamTransaction.Outputs[0].Script.Claim = transactionWireLegacyV1StreamPayload(
		t, certificateID, invalidSignature,
	)
	encoded, err = ledger.LegacyTransactionJSON(streamTransaction)
	if err != nil {
		t.Fatal(err)
	}
	output = encoded["outputs"].([]any)[0].(map[string]any)
	if output["is_channel_signature_valid"] != false {
		t.Fatalf("invalid legacy v1 signature = %#v", output)
	}

	malformedChannel := claimWireOracleTransaction(
		t, strings.Repeat("cc", 32), 7,
		NewClaimNameOutput(
			100_000_000, "@malformed", []byte{0, 0x80}, bytes.Repeat([]byte{0x54}, 20),
		),
	)
	streamTransaction.Outputs[0].Channel = &malformedChannel.Outputs[0]
	encoded, err = ledger.LegacyTransactionJSON(streamTransaction)
	if err != nil {
		t.Fatal(err)
	}
	output = encoded["outputs"].([]any)[0].(map[string]any)
	if _, ok := output["signing_channel"].(map[string]any); !ok {
		t.Fatalf("malformed nested channel was not retained: %#v", output)
	}
	if _, exists := output["is_channel_signature_valid"]; exists {
		t.Fatalf("malformed nested channel gained signature validity: %#v", output)
	}
}

func TestTransactionWireLegacyV1DatabaseHydrationAndVerification(t *testing.T) {
	fixture := newTransactionChannelHydrationFixture(t)
	channelPayload := transactionWireLegacyV1ChannelPayload(
		t, fixture.channelKey.PublicKey().CompressedBytes(),
	)
	channel := transactionChannelHydrationTransaction(
		t, 0x5c01, nil,
		NewClaimNameOutput(
			10, "@legacy-db", channelPayload, bytes.Repeat([]byte{0x5c}, 20),
		),
	)
	channelID, err := channel.Outputs[0].ClaimID()
	if err != nil {
		t.Fatal(err)
	}
	certificateID, err := hex.DecodeString(channelID)
	if err != nil {
		t.Fatal(err)
	}
	placeholder := transactionWireLegacyV1StreamPayload(
		t, certificateID, make([]byte, keys.CompactSignatureLength),
	)
	unsignedStream := NewClaimNameOutput(
		4, "legacy-db-stream", placeholder, bytes.Repeat([]byte{0x5d}, 20),
	)
	decoded, err := decodeTransactionWireClaimValue(placeholder)
	if err != nil {
		t.Fatal(err)
	}
	address, err := unsignedStream.Address(keys.MainNet)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := LegacyV1ClaimSignatureDigest(address, decoded.value, decoded.legacyV1)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := fixture.channelKey.SignCompact(digest[:])
	if err != nil {
		t.Fatal(err)
	}
	stream := transactionChannelHydrationTransaction(
		t, 0x5d01, nil,
		NewClaimNameOutput(
			4, "legacy-db-stream",
			transactionWireLegacyV1StreamPayload(t, certificateID, signature),
			bytes.Repeat([]byte{0x5d}, 20),
		),
	)
	channel.Height, channel.Position, channel.IsVerified = 101, 1, true
	stream.Height, stream.Position, stream.IsVerified = 102, 2, true
	fixture.store(t, channel, []uint32{0}, nil, nil)
	fixture.store(t, stream, []uint32{0}, nil, nil)

	hydrated := fixture.get(t, stream)
	if len(hydrated.Outputs) != 1 || hydrated.Outputs[0].Channel == nil ||
		hydrated.Outputs[0].Channel.ID() != channel.Outputs[0].ID() {
		t.Fatalf("database-hydrated legacy v1 channel = %#v", hydrated.Outputs)
	}
	fixture.ledger.Headers = claimWireOracleHeaders(t, 102, 7_007)
	encoded, err := fixture.ledger.LegacyTransactionJSON(hydrated)
	if err != nil {
		t.Fatal(err)
	}
	output := encoded["outputs"].([]any)[0].(map[string]any)
	if output["is_channel_signature_valid"] != true {
		t.Fatalf("database-hydrated legacy v1 signature = %#v", output)
	}
	signingChannel, ok := output["signing_channel"].(map[string]any)
	if !ok || signingChannel["claim_id"] != channelID ||
		signingChannel["value_type"] != "channel" {
		t.Fatalf("database-hydrated legacy v1 signing channel = %#v", signingChannel)
	}
}

func transactionWireLegacyV1ChannelPayload(t *testing.T, publicKey []byte) []byte {
	t.Helper()
	return transactionWireLegacyV1Payload(t, 2, func(claim protoreflect.Message) {
		certificate := claim.Mutable(legacyV1Field(claim, "certificate")).Message()
		transactionWireLegacyV1SetNumber(t, certificate, "version", 1)
		transactionWireLegacyV1SetNumber(t, certificate, "keyType", 3)
		legacyV1SetBytes(certificate, "publicKey", publicKey)
	})
}

func transactionWireLegacyV1StreamPayload(
	t *testing.T, certificateID, signature []byte,
) []byte {
	t.Helper()
	return transactionWireLegacyV1Payload(t, 1, func(claim protoreflect.Message) {
		stream := claim.Mutable(legacyV1Field(claim, "stream")).Message()
		transactionWireLegacyV1SetNumber(t, stream, "version", 1)
		metadata := stream.Mutable(legacyV1Field(stream, "metadata")).Message()
		transactionWireLegacyV1SetNumber(t, metadata, "version", 4)
		transactionWireLegacyV1SetNumber(t, metadata, "language", 1)
		legacyV1SetString(metadata, "title", "Signed legacy v1")
		legacyV1SetString(metadata, "description", "Legacy signature fixture")
		legacyV1SetString(metadata, "author", "Fixture author")
		legacyV1SetString(metadata, "license", "")
		metadata.Set(legacyV1Field(metadata, "nsfw"), protoreflect.ValueOfBool(false))
		source := stream.Mutable(legacyV1Field(stream, "source")).Message()
		transactionWireLegacyV1SetNumber(t, source, "version", 1)
		transactionWireLegacyV1SetNumber(t, source, "sourceType", 1)
		legacyV1SetBytes(source, "source", []byte{0x00, 0x01, 0x02, 0x03})
		legacyV1SetString(source, "contentType", "video/mp4")

		publisher := claim.Mutable(legacyV1Field(claim, "publisherSignature")).Message()
		transactionWireLegacyV1SetNumber(t, publisher, "version", 1)
		transactionWireLegacyV1SetNumber(t, publisher, "signatureType", 3)
		legacyV1SetBytes(publisher, "signature", signature)
		legacyV1SetBytes(publisher, "certificateId", certificateID)
	})
}

func transactionWireLegacyV1Payload(
	t *testing.T, claimType int64, populate func(protoreflect.Message),
) []byte {
	t.Helper()
	descriptor, err := legacyV1ClaimMessageDescriptor()
	if err != nil {
		t.Fatal(err)
	}
	claim := dynamicpb.NewMessage(descriptor)
	message := claim.ProtoReflect()
	transactionWireLegacyV1SetNumber(t, message, "version", 1)
	transactionWireLegacyV1SetNumber(t, message, "claimType", claimType)
	populate(message)
	if err := proto.CheckInitialized(claim); err != nil {
		t.Fatal(err)
	}
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func transactionWireLegacyV1SetNumber(
	t *testing.T, message protoreflect.Message, name protoreflect.Name, value int64,
) {
	t.Helper()
	field := legacyV1Field(message, name)
	var reflected protoreflect.Value
	switch field.Kind() {
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		reflected = protoreflect.ValueOfInt32(int32(value))
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		reflected = protoreflect.ValueOfInt64(value)
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		reflected = protoreflect.ValueOfUint32(uint32(value))
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		reflected = protoreflect.ValueOfUint64(uint64(value))
	case protoreflect.EnumKind:
		reflected = protoreflect.ValueOfEnum(protoreflect.EnumNumber(value))
	default:
		t.Fatalf("legacy field %s has non-numeric kind %s", name, field.Kind())
	}
	message.Set(field, reflected)
}
