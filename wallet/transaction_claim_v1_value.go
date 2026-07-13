package wallet

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"strings"
	"sync"

	"lbry/daemon/wallet/keys"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

var (
	ErrInvalidLegacyV1ClaimValue = errors.New("invalid legacy v1 claim value")
	ErrNotLegacyV1ClaimValue     = errors.New("claim value is not legacy v1 protobuf")
	ErrLegacyV1SignatureMaterial = errors.New("legacy v1 claim signature material is incomplete")
)

// LegacyV1ClaimMetadata holds state retained by compat.from_types_v1 outside
// the converted v2 Claim message. UnsignedPayload is the canonical legacy
// proto2 Claim after publisherSignature is removed; the pinned SDK uses it in
// the address-based legacy signature digest.
type LegacyV1ClaimMetadata struct {
	Version         int
	SignatureType   string
	UnsignedPayload []byte
}

// LegacyV1ClaimDecodeError preserves the Python exception boundary exposed by
// Claim.from_bytes and compat.from_types_v1.
type LegacyV1ClaimDecodeError struct {
	Name    string
	Message string
}

func (err *LegacyV1ClaimDecodeError) Error() string {
	if err == nil {
		return ""
	}
	return err.Message
}

func (err *LegacyV1ClaimDecodeError) PythonErrorName() string {
	if err == nil {
		return ""
	}
	return err.Name
}

func (err *LegacyV1ClaimDecodeError) Unwrap() error { return ErrInvalidLegacyV1ClaimValue }

// DecodeLegacyV1ClaimValue converts the legacy proto2 Claim format through the
// same field mapping as the pinned SDK's compat.from_types_v1. It intentionally
// does not accept v0 JSON or v2 Signable values.
func DecodeLegacyV1ClaimValue(payload []byte) (*ClaimValue, error) {
	value, _, err := DecodeLegacyV1ClaimValueWithMetadata(payload)
	return value, err
}

// DecodeLegacyV1ClaimValueWithMetadata also returns the legacy-only signature
// state needed to verify publisherSignature values.
func DecodeLegacyV1ClaimValueWithMetadata(payload []byte) (*ClaimValue, *LegacyV1ClaimMetadata, error) {
	if len(payload) == 0 {
		return nil, nil, legacyV1ClaimError("IndexError", "index out of range")
	}
	if payload[0] == 0 || payload[0] == 1 || payload[0] == '{' {
		return nil, nil, fmt.Errorf("%w: format byte 0x%02x", ErrNotLegacyV1ClaimValue, payload[0])
	}

	descriptor, err := legacyV1ClaimMessageDescriptor()
	if err != nil {
		return nil, nil, err
	}
	legacy := dynamicpb.NewMessage(descriptor)
	if err := (proto.UnmarshalOptions{AllowPartial: true}).Unmarshal(payload, legacy); err != nil {
		return nil, nil, legacyV1ClaimError("DecodeError", legacyV1ParseErrorMessage(payload, err))
	}

	converted, metadata, err := convertLegacyV1Claim(legacy)
	if err != nil {
		return nil, nil, err
	}
	return converted, metadata, nil
}

// LegacyV1ClaimSignatureDigest reproduces Output.get_signature_digest's
// legacy branch. The address is decoded without checksum validation because
// lbry.crypto.base58.Base58.decode includes the Base58Check checksum bytes in
// the signed preimage.
func LegacyV1ClaimSignatureDigest(
	address string, value *ClaimValue, metadata *LegacyV1ClaimMetadata,
) ([sha256.Size]byte, error) {
	if value == nil || !value.IsSigned() || metadata == nil || metadata.UnsignedPayload == nil ||
		len(value.SigningChannelHash) == 0 {
		return [sha256.Size]byte{}, ErrLegacyV1SignatureMaterial
	}
	decodedAddress, err := keys.DecodeBase58(address)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	preimage := make([]byte, 0, len(decodedAddress)+len(metadata.UnsignedPayload)+len(value.SigningChannelHash))
	preimage = append(preimage, decodedAddress...)
	preimage = append(preimage, metadata.UnsignedPayload...)
	preimage = append(preimage, reverseLegacyV1Bytes(value.SigningChannelHash)...)
	return sha256.Sum256(preimage), nil
}

func convertLegacyV1Claim(legacy *dynamicpb.Message) (*ClaimValue, *LegacyV1ClaimMetadata, error) {
	descriptor, err := claimV2MessageDescriptor()
	if err != nil {
		return nil, nil, err
	}
	claim := dynamicpb.NewMessage(descriptor)
	legacyMessage := legacy.ProtoReflect()
	claimMessage := claim.ProtoReflect()
	metadata := &LegacyV1ClaimMetadata{Version: 1, SignatureType: "SECP256k1"}
	var publisherSignature, publisherCertificateID []byte
	legacySigned := false

	claimType := legacyV1EnumNumber(legacyMessage, "claimType")
	if claimType == 2 {
		legacyCertificate := legacyV1Message(legacyMessage, "certificate")
		channel := claimMessage.Mutable(legacyV1Field(claimMessage, "channel")).Message()
		legacyV1SetBytes(channel, "public_key", legacyV1Bytes(legacyCertificate, "publicKey"))
	} else {
		legacyStream := legacyV1Message(legacyMessage, "stream")
		legacyMetadata := legacyV1Message(legacyStream, "metadata")
		legacySource := legacyV1Message(legacyStream, "source")
		stream := claimMessage.Mutable(legacyV1Field(claimMessage, "stream")).Message()

		legacyV1SetString(claimMessage, "title", legacyV1String(legacyMetadata, "title"))
		legacyV1SetString(claimMessage, "description", legacyV1String(legacyMetadata, "description"))
		legacyV1SetString(stream, "author", legacyV1String(legacyMetadata, "author"))
		legacyV1SetString(stream, "license", legacyV1String(legacyMetadata, "license"))
		legacyV1SetString(stream, "license_url", legacyV1String(legacyMetadata, "licenseUrl"))

		thumbnail := claimMessage.Mutable(legacyV1Field(claimMessage, "thumbnail")).Message()
		legacyV1SetString(thumbnail, "url", legacyV1String(legacyMetadata, "thumbnail"))
		if legacyV1Has(legacyMetadata, "language") {
			languagesField := legacyV1Field(claimMessage, "languages")
			language := dynamicpb.NewMessage(languagesField.Message()).ProtoReflect()
			languageField := legacyV1Field(language, "language")
			language.Set(languageField, protoreflect.ValueOfEnum(legacyV1EnumNumber(legacyMetadata, "language")))
			claimMessage.Mutable(languagesField).List().Append(protoreflect.ValueOfMessage(language))
		}

		source := stream.Mutable(legacyV1Field(stream, "source")).Message()
		legacyV1SetString(source, "media_type", legacyV1String(legacySource, "contentType"))
		legacyV1SetBytes(source, "sd_hash", legacyV1Bytes(legacySource, "source"))
		if legacyV1Bool(legacyMetadata, "nsfw") {
			tags := claimMessage.Mutable(legacyV1Field(claimMessage, "tags")).List()
			tags.Append(protoreflect.ValueOfString("mature"))
		}

		if legacyV1Has(legacyMetadata, "fee") {
			if err := convertLegacyV1Fee(stream, legacyV1Message(legacyMetadata, "fee")); err != nil {
				return nil, nil, err
			}
		}
		if legacyV1Has(legacyMessage, "publisherSignature") {
			signature := legacyV1Message(legacyMessage, "publisherSignature")
			legacySigned = true
			publisherSignature = legacyV1Bytes(signature, "signature")
			publisherCertificateID = legacyV1Bytes(signature, "certificateId")
			metadata.SignatureType = legacyV1EnumName(signature, "signatureType")
			publisherField := legacyV1Field(legacyMessage, "publisherSignature")
			legacyMessage.Clear(publisherField)
			if err := proto.CheckInitialized(legacy); err != nil {
				return nil, nil, legacyV1ClaimError("EncodeError", legacyV1InitializationError(legacyMessage))
			}
			metadata.UnsignedPayload, err = marshalClaimProtoMessage(legacyMessage)
			if err != nil {
				return nil, nil, legacyV1ClaimError("EncodeError", err.Error())
			}
		}
	}

	valueMap, typeName, err := projectClaimValue(claimMessage)
	if err != nil {
		return nil, nil, err
	}
	canonicalMessage, err := marshalClaimProtoMessage(claimMessage)
	if err != nil {
		return nil, nil, legacyV1ClaimError("DecodeError", err.Error())
	}
	value := &ClaimValue{Type: typeName, Value: valueMap}

	// compat.from_types_v1 only imports publisher signatures for stream claims.
	if legacySigned {
		value.signed = true
		value.Signature = publisherSignature
		value.SigningChannelHash = reverseLegacyV1Bytes(publisherCertificateID)
		value.Canonical = make([]byte, 0, 1+len(value.SigningChannelHash)+len(value.Signature)+len(canonicalMessage))
		value.Canonical = append(value.Canonical, 1)
		value.Canonical = append(value.Canonical, value.SigningChannelHash...)
		value.Canonical = append(value.Canonical, value.Signature...)
	} else {
		value.Canonical = []byte{0}
	}
	value.Canonical = append(value.Canonical, canonicalMessage...)
	return value, metadata, nil
}

func convertLegacyV1Fee(stream, oldFee protoreflect.Message) error {
	currency := legacyV1EnumName(oldFee, "currency")
	if currency != "LBC" && currency != "BTC" && currency != "USD" {
		return legacyV1ClaimError("DecodeError", "Unsupported currency: "+currency)
	}
	amount, err := legacyV1FeeAmount(currency, float32(legacyV1Float(oldFee, "amount")))
	if err != nil {
		return err
	}
	fee := stream.Mutable(legacyV1Field(stream, "fee")).Message()
	legacyV1SetBytes(fee, "address", legacyV1Bytes(oldFee, "address"))
	currencyField := legacyV1Field(fee, "currency")
	currencyValue := currencyField.Enum().Values().ByName(protoreflect.Name(currency))
	fee.Set(currencyField, protoreflect.ValueOfEnum(currencyValue.Number()))
	fee.Set(legacyV1Field(fee, "amount"), protoreflect.ValueOfUint64(amount))
	return nil
}

func legacyV1FeeAmount(currency string, amount float32) (uint64, error) {
	value := float64(amount)
	if math.IsNaN(value) {
		return 0, legacyV1ClaimError("ValueError", "cannot convert NaN to integer")
	}
	if math.IsInf(value, 1) {
		return 0, legacyV1ClaimError("OverflowError", "cannot convert Infinity to integer ratio")
	}
	if math.IsInf(value, -1) {
		return 0, legacyV1ClaimError("OverflowError", "cannot convert Infinity to integer ratio")
	}
	rat := new(big.Rat).SetFloat64(value)
	scale := int64(100_000_000)
	if currency == "USD" {
		scale = 100
	}
	rat.Mul(rat, big.NewRat(scale, 1))
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(rat.Num(), rat.Denom(), remainder)
	if currency == "USD" && remainder.Sign() != 0 {
		if rat.Sign() < 0 {
			quotient.Sub(quotient, big.NewInt(1))
		} else {
			quotient.Add(quotient, big.NewInt(1))
		}
	}
	if quotient.Sign() < 0 || !quotient.IsUint64() {
		return 0, legacyV1ClaimError("ValueError", "Value out of range: "+quotient.String())
	}
	return quotient.Uint64(), nil
}

func reverseLegacyV1Bytes(value []byte) []byte {
	reversed := make([]byte, len(value))
	for index := range value {
		reversed[len(value)-1-index] = value[index]
	}
	return reversed
}

func legacyV1Field(message protoreflect.Message, name protoreflect.Name) protoreflect.FieldDescriptor {
	return message.Descriptor().Fields().ByName(name)
}

func legacyV1Has(message protoreflect.Message, name protoreflect.Name) bool {
	return message.Has(legacyV1Field(message, name))
}

func legacyV1Message(message protoreflect.Message, name protoreflect.Name) protoreflect.Message {
	return message.Get(legacyV1Field(message, name)).Message()
}

func legacyV1String(message protoreflect.Message, name protoreflect.Name) string {
	return message.Get(legacyV1Field(message, name)).String()
}

func legacyV1Bytes(message protoreflect.Message, name protoreflect.Name) []byte {
	return append([]byte(nil), message.Get(legacyV1Field(message, name)).Bytes()...)
}

func legacyV1Bool(message protoreflect.Message, name protoreflect.Name) bool {
	return message.Get(legacyV1Field(message, name)).Bool()
}

func legacyV1Float(message protoreflect.Message, name protoreflect.Name) float64 {
	return message.Get(legacyV1Field(message, name)).Float()
}

func legacyV1EnumNumber(message protoreflect.Message, name protoreflect.Name) protoreflect.EnumNumber {
	return message.Get(legacyV1Field(message, name)).Enum()
}

func legacyV1EnumName(message protoreflect.Message, name protoreflect.Name) string {
	field := legacyV1Field(message, name)
	number := message.Get(field).Enum()
	value := field.Enum().Values().ByNumber(number)
	if value == nil {
		return strconvLegacyV1Enum(number)
	}
	return string(value.Name())
}

func strconvLegacyV1Enum(number protoreflect.EnumNumber) string {
	return fmt.Sprintf("%d", number)
}

func legacyV1SetString(message protoreflect.Message, name protoreflect.Name, value string) {
	message.Set(legacyV1Field(message, name), protoreflect.ValueOfString(value))
}

func legacyV1SetBytes(message protoreflect.Message, name protoreflect.Name, value []byte) {
	message.Set(legacyV1Field(message, name), protoreflect.ValueOfBytes(append([]byte(nil), value...)))
}

func legacyV1ClaimError(name, message string) error {
	return &LegacyV1ClaimDecodeError{Name: name, Message: message}
}

func legacyV1ParseErrorMessage(payload []byte, err error) string {
	remaining := payload
	for len(remaining) > 0 {
		number, wireType, tagLength := protowire.ConsumeTag(remaining)
		if tagLength < 0 {
			return "Truncated message."
		}
		if number == 0 {
			return "Field number 0 is illegal."
		}
		if wireType > protowire.Fixed32Type {
			return "Wrong wire type in tag."
		}
		valueLength := protowire.ConsumeFieldValue(number, wireType, remaining[tagLength:])
		if valueLength < 0 {
			if wireType == 6 || wireType == 7 {
				return "Wrong wire type in tag."
			}
			return "Truncated message."
		}
		remaining = remaining[tagLength+valueLength:]
	}
	message := err.Error()
	switch {
	case errors.Is(err, io.ErrUnexpectedEOF), strings.Contains(message, "unexpected EOF"):
		return "Truncated message."
	case strings.Contains(message, "invalid UTF-8"):
		return "Error parsing message with type 'legacy_pb.Claim'"
	default:
		return message
	}
}

func legacyV1InitializationError(message protoreflect.Message) string {
	missing := legacyV1MissingRequiredFields(message, "")
	return fmt.Sprintf("Message %s is missing required fields: %s", message.Descriptor().FullName(), strings.Join(missing, ","))
}

func legacyV1MissingRequiredFields(message protoreflect.Message, prefix string) []string {
	var missing []string
	fields := message.Descriptor().Fields()
	for index := 0; index < fields.Len(); index++ {
		field := fields.Get(index)
		name := prefix + string(field.Name())
		if field.Cardinality() == protoreflect.Required && !message.Has(field) {
			missing = append(missing, name)
			continue
		}
		if field.Kind() == protoreflect.MessageKind && message.Has(field) {
			missing = append(missing, legacyV1MissingRequiredFields(message.Get(field).Message(), name+".")...)
		}
	}
	return missing
}

var legacyV1ClaimDescriptor struct {
	sync.Once
	message protoreflect.MessageDescriptor
	err     error
}

func legacyV1ClaimMessageDescriptor() (protoreflect.MessageDescriptor, error) {
	legacyV1ClaimDescriptor.Do(func() {
		compressed, err := base64.StdEncoding.DecodeString(legacyV1DescriptorSetGzipBase64)
		if err != nil {
			legacyV1ClaimDescriptor.err = fmt.Errorf("decode legacy v1 descriptor: %w", err)
			return
		}
		reader, err := gzip.NewReader(bytes.NewReader(compressed))
		if err != nil {
			legacyV1ClaimDescriptor.err = fmt.Errorf("open legacy v1 descriptor: %w", err)
			return
		}
		raw, readErr := io.ReadAll(reader)
		closeErr := reader.Close()
		if readErr != nil {
			legacyV1ClaimDescriptor.err = fmt.Errorf("read legacy v1 descriptor: %w", readErr)
			return
		}
		if closeErr != nil {
			legacyV1ClaimDescriptor.err = fmt.Errorf("close legacy v1 descriptor: %w", closeErr)
			return
		}
		set := new(descriptorpb.FileDescriptorSet)
		if err := proto.Unmarshal(raw, set); err != nil {
			legacyV1ClaimDescriptor.err = fmt.Errorf("parse legacy v1 descriptor: %w", err)
			return
		}
		files, err := protodesc.NewFiles(set)
		if err != nil {
			legacyV1ClaimDescriptor.err = fmt.Errorf("build legacy v1 descriptor: %w", err)
			return
		}
		file, err := files.FindFileByPath("legacy_claim.proto")
		if err != nil {
			legacyV1ClaimDescriptor.err = fmt.Errorf("find legacy v1 Claim descriptor: %w", err)
			return
		}
		legacyV1ClaimDescriptor.message = file.Messages().ByName("Claim")
		if legacyV1ClaimDescriptor.message == nil {
			legacyV1ClaimDescriptor.err = errors.New("legacy v1 descriptor has no Claim message")
		}
	})
	return legacyV1ClaimDescriptor.message, legacyV1ClaimDescriptor.err
}

// FileDescriptorSet for the pinned SDK's seven lbry/schema/types/v1 files.
// Raw SHA256: 3bdb37ca5594b0f4bebbac8ba49df063231a81035584546066e8a9793d779c2f.
const legacyV1DescriptorSetGzipBase64 = "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"
