package wallet

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"sync"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

var (
	ErrInvalidClaimValue           = errors.New("invalid claim value")
	ErrUnsupportedLegacyClaimValue = errors.New("legacy claim value is not supported")
)

// ClaimValueDecodeError identifies a malformed v2 Signable or Claim message.
// PythonErrorName is useful at legacy JSON boundaries that only suppress the
// Python protobuf DecodeError class.
type ClaimValueDecodeError struct {
	Name    string
	Message string
}

func (err *ClaimValueDecodeError) Error() string {
	if err == nil {
		return ""
	}
	return err.Message
}

func (err *ClaimValueDecodeError) PythonErrorName() string {
	if err == nil {
		return ""
	}
	return err.Name
}

func (err *ClaimValueDecodeError) Unwrap() error { return ErrInvalidClaimValue }

// ClaimValue is the v2 Claim Signable projected through the pinned SDK's
// BaseClaim.to_dict implementation. Canonical is Claim.to_bytes(), including
// the unsigned or signed wrapper, after protobuf canonicalization.
type ClaimValue struct {
	Type               string
	Value              map[string]any
	SigningChannelHash []byte
	Signature          []byte
	Canonical          []byte

	signed bool
}

func (value *ClaimValue) IsSigned() bool {
	return value != nil && value.signed
}

// SigningChannelID mirrors Signable.signing_channel_id's display byte order.
func (value *ClaimValue) SigningChannelID() *string {
	if value == nil || len(value.SigningChannelHash) == 0 {
		return nil
	}
	reversed := make([]byte, len(value.SigningChannelHash))
	for index := range value.SigningChannelHash {
		reversed[len(reversed)-1-index] = value.SigningChannelHash[index]
	}
	encoded := hex.EncodeToString(reversed)
	return &encoded
}

// MarshalBinary returns the exact v2 bytes exposed by include_protobuf.
func (value *ClaimValue) MarshalBinary() ([]byte, error) {
	if value == nil || value.Canonical == nil {
		return nil, ErrInvalidClaimValue
	}
	return append([]byte(nil), value.Canonical...), nil
}

// UnmarshalBinary replaces value with DecodeClaimValue(data).
func (value *ClaimValue) UnmarshalBinary(data []byte) error {
	if value == nil {
		return ErrInvalidClaimValue
	}
	decoded, err := DecodeClaimValue(data)
	if err != nil {
		return err
	}
	*value = *decoded
	return nil
}

// DecodeClaimValue decodes the common v2 Claim wire formats. Legacy JSON and
// v1 protobuf values are reported separately so callers cannot mistake a
// partial compatibility projection for the pinned SDK's converted value.
func DecodeClaimValue(payload []byte) (*ClaimValue, error) {
	if len(payload) == 0 {
		return nil, claimValueError("IndexError", "index out of range")
	}
	if payload[0] != 0 && payload[0] != 1 {
		return nil, fmt.Errorf("%w: format byte 0x%02x", ErrUnsupportedLegacyClaimValue, payload[0])
	}

	value := &ClaimValue{}
	messageBytes := payload[1:]
	if payload[0] == 1 {
		value.signed = true
		hashEnd := min(len(payload), 21)
		value.SigningChannelHash = append([]byte(nil), payload[1:hashEnd]...)
		if len(payload) > 21 {
			signatureEnd := min(len(payload), 85)
			value.Signature = append([]byte(nil), payload[21:signatureEnd]...)
		} else {
			value.Signature = []byte{}
		}
		messageBytes = nil
		if len(payload) > 85 {
			messageBytes = payload[85:]
		}
	}

	descriptor, err := claimV2MessageDescriptor()
	if err != nil {
		return nil, err
	}
	message := dynamicpb.NewMessage(descriptor)
	if err := proto.Unmarshal(messageBytes, message); err != nil {
		return nil, claimValueError("DecodeError", err.Error())
	}
	value.Value, value.Type, err = projectClaimValue(message.ProtoReflect())
	if err != nil {
		return nil, err
	}
	canonicalMessage, err := marshalClaimProtoMessage(message.ProtoReflect())
	if err != nil {
		return nil, claimValueError("DecodeError", err.Error())
	}

	value.Canonical = make([]byte, 0, 1+len(value.SigningChannelHash)+len(value.Signature)+len(canonicalMessage))
	if value.signed {
		value.Canonical = append(value.Canonical, 1)
		value.Canonical = append(value.Canonical, value.SigningChannelHash...)
		value.Canonical = append(value.Canonical, value.Signature...)
	} else {
		value.Canonical = append(value.Canonical, 0)
	}
	value.Canonical = append(value.Canonical, canonicalMessage...)
	return value, nil
}

// dynamicpb does not order ordinary populated fields, even with deterministic
// marshaling. Generated Python messages serialize fields by number and append
// unknown fields afterward, so canonical Claim bytes need an explicit walk.
func marshalClaimProtoMessage(message protoreflect.Message) ([]byte, error) {
	fields := message.Descriptor().Fields()
	ordered := make([]protoreflect.FieldDescriptor, fields.Len())
	for index := range ordered {
		ordered[index] = fields.Get(index)
	}
	sort.Slice(ordered, func(left, right int) bool {
		return ordered[left].Number() < ordered[right].Number()
	})

	var encoded []byte
	for _, field := range ordered {
		if field.IsList() {
			list := message.Get(field).List()
			if field.IsPacked() && list.Len() > 0 {
				var packed []byte
				for index := 0; index < list.Len(); index++ {
					var err error
					packed, err = appendClaimProtoScalar(packed, field, list.Get(index), false)
					if err != nil {
						return nil, err
					}
				}
				encoded = protowire.AppendTag(encoded, field.Number(), protowire.BytesType)
				encoded = protowire.AppendBytes(encoded, packed)
				continue
			}
			for index := 0; index < list.Len(); index++ {
				var err error
				encoded, err = appendClaimProtoScalar(encoded, field, list.Get(index), true)
				if err != nil {
					return nil, err
				}
			}
			continue
		}
		if !message.Has(field) {
			continue
		}
		var err error
		encoded, err = appendClaimProtoScalar(encoded, field, message.Get(field), true)
		if err != nil {
			return nil, err
		}
	}
	return append(encoded, message.GetUnknown()...), nil
}

func appendClaimProtoScalar(
	destination []byte, field protoreflect.FieldDescriptor, value protoreflect.Value, withTag bool,
) ([]byte, error) {
	appendTag := func(wireType protowire.Type) {
		if withTag {
			destination = protowire.AppendTag(destination, field.Number(), wireType)
		}
	}
	switch field.Kind() {
	case protoreflect.BoolKind:
		appendTag(protowire.VarintType)
		if value.Bool() {
			return protowire.AppendVarint(destination, 1), nil
		}
		return protowire.AppendVarint(destination, 0), nil
	case protoreflect.EnumKind:
		appendTag(protowire.VarintType)
		return protowire.AppendVarint(destination, uint64(int64(value.Enum()))), nil
	case protoreflect.Int32Kind, protoreflect.Int64Kind:
		appendTag(protowire.VarintType)
		return protowire.AppendVarint(destination, uint64(value.Int())), nil
	case protoreflect.Sint32Kind, protoreflect.Sint64Kind:
		appendTag(protowire.VarintType)
		return protowire.AppendVarint(destination, protowire.EncodeZigZag(value.Int())), nil
	case protoreflect.Uint32Kind, protoreflect.Uint64Kind:
		appendTag(protowire.VarintType)
		return protowire.AppendVarint(destination, value.Uint()), nil
	case protoreflect.Fixed32Kind:
		appendTag(protowire.Fixed32Type)
		return protowire.AppendFixed32(destination, uint32(value.Uint())), nil
	case protoreflect.Sfixed32Kind:
		appendTag(protowire.Fixed32Type)
		return protowire.AppendFixed32(destination, uint32(value.Int())), nil
	case protoreflect.FloatKind:
		appendTag(protowire.Fixed32Type)
		return protowire.AppendFixed32(destination, math.Float32bits(float32(value.Float()))), nil
	case protoreflect.Fixed64Kind:
		appendTag(protowire.Fixed64Type)
		return protowire.AppendFixed64(destination, value.Uint()), nil
	case protoreflect.Sfixed64Kind:
		appendTag(protowire.Fixed64Type)
		return protowire.AppendFixed64(destination, uint64(value.Int())), nil
	case protoreflect.DoubleKind:
		appendTag(protowire.Fixed64Type)
		return protowire.AppendFixed64(destination, math.Float64bits(value.Float())), nil
	case protoreflect.StringKind:
		appendTag(protowire.BytesType)
		return protowire.AppendString(destination, value.String()), nil
	case protoreflect.BytesKind:
		appendTag(protowire.BytesType)
		return protowire.AppendBytes(destination, value.Bytes()), nil
	case protoreflect.MessageKind:
		message, err := marshalClaimProtoMessage(value.Message())
		if err != nil {
			return nil, err
		}
		appendTag(protowire.BytesType)
		return protowire.AppendBytes(destination, message), nil
	default:
		return nil, fmt.Errorf("%w: unsupported protobuf wire kind %s", ErrInvalidClaimValue, field.Kind())
	}
}

func claimValueError(name, message string) error {
	return &ClaimValueDecodeError{Name: name, Message: message}
}

var claimV2Descriptor struct {
	sync.Once
	message protoreflect.MessageDescriptor
	err     error
}

func claimV2MessageDescriptor() (protoreflect.MessageDescriptor, error) {
	claimV2Descriptor.Do(func() {
		compressed, err := base64.StdEncoding.DecodeString(claimV2DescriptorGzipBase64)
		if err != nil {
			claimV2Descriptor.err = fmt.Errorf("decode claim descriptor: %w", err)
			return
		}
		reader, err := gzip.NewReader(bytes.NewReader(compressed))
		if err != nil {
			claimV2Descriptor.err = fmt.Errorf("open claim descriptor: %w", err)
			return
		}
		raw, readErr := io.ReadAll(reader)
		closeErr := reader.Close()
		if readErr != nil {
			claimV2Descriptor.err = fmt.Errorf("read claim descriptor: %w", readErr)
			return
		}
		if closeErr != nil {
			claimV2Descriptor.err = fmt.Errorf("close claim descriptor: %w", closeErr)
			return
		}
		fileProto := new(descriptorpb.FileDescriptorProto)
		if err := proto.Unmarshal(raw, fileProto); err != nil {
			claimV2Descriptor.err = fmt.Errorf("parse claim descriptor: %w", err)
			return
		}
		file, err := protodesc.NewFile(fileProto, nil)
		if err != nil {
			claimV2Descriptor.err = fmt.Errorf("build claim descriptor: %w", err)
			return
		}
		claimV2Descriptor.message = file.Messages().ByName("Claim")
		if claimV2Descriptor.message == nil {
			claimV2Descriptor.err = errors.New("claim descriptor has no Claim message")
		}
	})
	return claimV2Descriptor.message, claimV2Descriptor.err
}

func projectClaimValue(message protoreflect.Message) (map[string]any, string, error) {
	typeOneof := message.Descriptor().Oneofs().ByName("type")
	typeField := message.WhichOneof(typeOneof)
	if typeField == nil {
		return nil, "", claimValueError(
			"TypeError", "attribute name must be string, not 'NoneType'",
		)
	}
	typeName := string(typeField.Name())
	projected, err := claimProtoJSONMap(message)
	if err != nil {
		return nil, "", err
	}
	nested, ok := projected[typeName].(map[string]any)
	if !ok {
		return nil, "", fmt.Errorf("%w: Claim.%s is not an object", ErrInvalidClaimValue, typeName)
	}
	delete(projected, typeName)
	for name, value := range nested {
		projected[name] = value
	}
	typeMessage := message.Get(typeField).Message()

	if _, exists := projected["languages"]; exists {
		languages, err := projectClaimLanguages(message)
		if err != nil {
			return nil, "", err
		}
		projected["languages"] = languages
	}
	if _, exists := projected["locations"]; exists {
		projected["locations"] = projectClaimLocations(message)
	}

	switch typeName {
	case "stream":
		if err := projectStreamClaim(projected, typeMessage); err != nil {
			return nil, "", err
		}
	case "channel":
		if err := projectChannelClaim(projected, typeMessage); err != nil {
			return nil, "", err
		}
	case "collection":
		projectCollectionClaim(projected, typeMessage)
	case "repost":
		projectRepostClaim(projected, typeMessage)
	default:
		return nil, "", fmt.Errorf("%w: unsupported Claim type %q", ErrInvalidClaimValue, typeName)
	}
	return projected, typeName, nil
}

func claimProtoJSONMap(message protoreflect.Message) (map[string]any, error) {
	result := make(map[string]any)
	fields := message.Descriptor().Fields()
	for index := 0; index < fields.Len(); index++ {
		field := fields.Get(index)
		if field.IsList() {
			list := message.Get(field).List()
			if list.Len() == 0 {
				continue
			}
			items := make([]any, list.Len())
			for item := 0; item < list.Len(); item++ {
				encoded, err := claimProtoJSONScalar(field, list.Get(item))
				if err != nil {
					return nil, err
				}
				items[item] = encoded
			}
			result[string(field.Name())] = items
			continue
		}
		if !message.Has(field) {
			continue
		}
		encoded, err := claimProtoJSONScalar(field, message.Get(field))
		if err != nil {
			return nil, err
		}
		result[string(field.Name())] = encoded
	}
	return result, nil
}

func claimProtoJSONScalar(field protoreflect.FieldDescriptor, value protoreflect.Value) (any, error) {
	switch field.Kind() {
	case protoreflect.BoolKind:
		return value.Bool(), nil
	case protoreflect.EnumKind:
		number := value.Enum()
		enumValue := field.Enum().Values().ByNumber(number)
		if enumValue == nil {
			return int32(number), nil
		}
		return string(enumValue.Name()), nil
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return int32(value.Int()), nil
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return uint32(value.Uint()), nil
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return strconv.FormatInt(value.Int(), 10), nil
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return strconv.FormatUint(value.Uint(), 10), nil
	case protoreflect.FloatKind:
		return float32(value.Float()), nil
	case protoreflect.DoubleKind:
		return value.Float(), nil
	case protoreflect.StringKind:
		return value.String(), nil
	case protoreflect.BytesKind:
		return base64.StdEncoding.EncodeToString(value.Bytes()), nil
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return claimProtoJSONMap(value.Message())
	default:
		return nil, fmt.Errorf("%w: unsupported protobuf kind %s", ErrInvalidClaimValue, field.Kind())
	}
}

func projectClaimLanguages(message protoreflect.Message) ([]any, error) {
	field := message.Descriptor().Fields().ByName("languages")
	list := message.Get(field).List()
	result := make([]any, list.Len())
	for index := 0; index < list.Len(); index++ {
		language := list.Get(index).Message()
		parts := make([]string, 0, 3)
		for _, name := range []protoreflect.Name{"language", "script", "region"} {
			componentField := language.Descriptor().Fields().ByName(name)
			number := language.Get(componentField).Enum()
			if number == 0 {
				continue
			}
			component := componentField.Enum().Values().ByNumber(number)
			if component == nil {
				return nil, fmt.Errorf("%w: unknown Language.%s enum %d", ErrInvalidClaimValue, name, number)
			}
			encoded := string(component.Name())
			if name == "region" && strings.HasPrefix(encoded, "R") {
				encoded = encoded[1:]
			}
			parts = append(parts, encoded)
		}
		result[index] = strings.Join(parts, "-")
	}
	return result, nil
}

func projectClaimLocations(message protoreflect.Message) []any {
	field := message.Descriptor().Fields().ByName("locations")
	list := message.Get(field).List()
	result := make([]any, list.Len())
	for index := 0; index < list.Len(); index++ {
		location := list.Get(index).Message()
		encoded, _ := claimProtoJSONMap(location)
		for _, name := range []protoreflect.Name{"latitude", "longitude"} {
			coordinate := location.Descriptor().Fields().ByName(name)
			value := location.Get(coordinate).Int()
			if value != 0 {
				encoded[string(name)] = claimScaledDecimal(big.NewInt(value), 7)
			}
		}
		result[index] = encoded
	}
	return result
}

func projectStreamClaim(projected map[string]any, stream protoreflect.Message) error {
	if source, ok := projected["source"].(map[string]any); ok {
		claimBase64FieldToHex(source, "hash")
		if _, exists := source["sd_hash"]; exists {
			claimBase64FieldToHex(source, "sd_hash")
		} else {
			claimBase64FieldToHex(source, "bt_infohash")
		}
		if mediaType, ok := source["media_type"].(string); ok {
			projected["stream_type"] = claimStreamType(mediaType)
		}
	}

	fee, ok := projected["fee"].(map[string]any)
	if !ok {
		return nil
	}
	if address, ok := fee["address"].(string); ok {
		raw, err := base64.StdEncoding.DecodeString(address)
		if err != nil {
			return fmt.Errorf("%w: fee address: %v", ErrInvalidClaimValue, err)
		}
		fee["address"] = claimBase58Encode(raw)
	}
	if _, exists := fee["amount"]; !exists {
		return nil
	}
	feeField := stream.Descriptor().Fields().ByName("fee")
	feeMessage := stream.Get(feeField).Message()
	currencyField := feeMessage.Descriptor().Fields().ByName("currency")
	amountField := feeMessage.Descriptor().Fields().ByName("amount")
	amount := new(big.Int).SetUint64(feeMessage.Get(amountField).Uint())
	switch currency := feeMessage.Get(currencyField).Enum(); currency {
	case 0:
		fee["amount"] = "None"
	case 1, 2:
		fee["amount"] = claimScaledDecimal(amount, 8)
	case 3:
		fee["amount"] = claimScaledDecimal(amount, 2)
	default:
		return fmt.Errorf("%w: unknown Fee.currency enum %d", ErrInvalidClaimValue, currency)
	}
	return nil
}

func projectChannelClaim(projected map[string]any, channel protoreflect.Message) error {
	publicKeyField := channel.Descriptor().Fields().ByName("public_key")
	publicKey, err := normalizeChannelPublicKey(channel.Get(publicKeyField).Bytes())
	if err != nil {
		return err
	}
	projected["public_key"] = hex.EncodeToString(publicKey)

	featuredField := channel.Descriptor().Fields().ByName("featured")
	if channel.Has(featuredField) {
		projected["featured"] = projectClaimReferenceList(channel.Get(featuredField).Message())
	}
	return nil
}

func projectCollectionClaim(projected map[string]any, collection protoreflect.Message) {
	delete(projected, "claim_references")
	references := projectClaimReferenceList(collection)
	if len(references) > 0 {
		projected["claims"] = references
	}
}

func projectRepostClaim(projected map[string]any, repost protoreflect.Message) {
	delete(projected, "claim_hash")
	field := repost.Descriptor().Fields().ByName("claim_hash")
	claimHash := repost.Get(field).Bytes()
	if len(claimHash) > 0 {
		projected["claim_id"] = reverseClaimValueHex(claimHash)
	}
}

func projectClaimReferenceList(listMessage protoreflect.Message) []any {
	field := listMessage.Descriptor().Fields().ByName("claim_references")
	list := listMessage.Get(field).List()
	result := make([]any, list.Len())
	for index := 0; index < list.Len(); index++ {
		reference := list.Get(index).Message()
		claimHashField := reference.Descriptor().Fields().ByName("claim_hash")
		result[index] = reverseClaimValueHex(reference.Get(claimHashField).Bytes())
	}
	return result
}

func claimBase64FieldToHex(object map[string]any, name string) {
	encoded, ok := object[name].(string)
	if !ok {
		return
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err == nil {
		object[name] = hex.EncodeToString(decoded)
	}
}

func reverseClaimValueHex(value []byte) string {
	reversed := make([]byte, len(value))
	for index := range value {
		reversed[len(reversed)-1-index] = value[index]
	}
	return hex.EncodeToString(reversed)
}

func claimBase58Encode(value []byte) string {
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	integer := new(big.Int).SetBytes(value)
	base := big.NewInt(58)
	remainder := new(big.Int)
	encoded := make([]byte, 0, len(value)*138/100+1)
	for integer.Sign() > 0 {
		integer.QuoRem(integer, base, remainder)
		encoded = append(encoded, alphabet[remainder.Int64()])
	}
	for _, character := range value {
		if character != 0 {
			break
		}
		encoded = append(encoded, alphabet[0])
	}
	for left, right := 0, len(encoded)-1; left < right; left, right = left+1, right-1 {
		encoded[left], encoded[right] = encoded[right], encoded[left]
	}
	return string(encoded)
}

func claimScaledDecimal(value *big.Int, scale int) string {
	negative := value.Sign() < 0
	absolute := new(big.Int).Abs(new(big.Int).Set(value))
	if absolute.Sign() == 0 {
		return "0"
	}
	digits := absolute.String()
	for scale > 0 && len(digits) > 1 && digits[len(digits)-1] == '0' {
		digits = digits[:len(digits)-1]
		scale--
	}
	sign := ""
	if negative {
		sign = "-"
	}
	if scale <= 0 {
		return sign + digits + strings.Repeat("0", -scale)
	}
	adjustedExponent := len(digits) - scale - 1
	if adjustedExponent < -6 {
		coefficient := digits[:1]
		if len(digits) > 1 {
			coefficient += "." + digits[1:]
		}
		return sign + coefficient + "E" + strconv.Itoa(adjustedExponent)
	}
	if len(digits) <= scale {
		return sign + "0." + strings.Repeat("0", scale-len(digits)) + digits
	}
	position := len(digits) - scale
	return sign + digits[:position] + "." + digits[position:]
}

func claimStreamType(mediaType string) string {
	if streamType, ok := claimStreamTypes[mediaType]; ok {
		return streamType
	}
	return "binary"
}

var claimStreamTypes = map[string]string{
	"application/dash+xml":           "video",
	"application/epub+zip":           "document",
	"application/javascript":         "document",
	"application/json":               "document",
	"application/msword":             "document",
	"application/octet-stream":       "binary",
	"application/oda":                "binary",
	"application/pdf":                "document",
	"application/pkcs7-mime":         "binary",
	"application/postscript":         "image",
	"application/rtf":                "document",
	"application/vnd.comicbook+zip":  "document",
	"application/vnd.comicbook-rar":  "document",
	"application/vnd.ms-excel":       "document",
	"application/vnd.ms-powerpoint":  "document",
	"application/x-bcpio":            "binary",
	"application/x-cpio":             "binary",
	"application/x-csh":              "binary",
	"application/x-dvi":              "binary",
	"application/x-ext-lbry":         "document",
	"application/x-gtar":             "binary",
	"application/x-hdf":              "binary",
	"application/x-latex":            "binary",
	"application/x-mif":              "binary",
	"application/x-mobipocket-ebook": "document",
	"application/x-mpegurl":          "audio",
	"application/x-netcdf":           "binary",
	"application/x-pkcs12":           "binary",
	"application/x-pn-realaudio":     "audio",
	"application/x-python-code":      "binary",
	"application/x-sh":               "document",
	"application/x-shar":             "binary",
	"application/x-shockwave-flash":  "binary",
	"application/x-sv4cpio":          "binary",
	"application/x-sv4crc":           "binary",
	"application/x-tar":              "binary",
	"application/x-tcl":              "binary",
	"application/x-tex":              "binary",
	"application/x-texinfo":          "binary",
	"application/x-troff":            "binary",
	"application/x-troff-man":        "document",
	"application/x-troff-me":         "binary",
	"application/x-troff-ms":         "binary",
	"application/x-ustar":            "binary",
	"application/x-wais-source":      "binary",
	"application/xml":                "binary",
	"application/zip":                "binary",
	"audio/basic":                    "audio",
	"audio/flac":                     "audio",
	"audio/midi":                     "audio",
	"audio/mp4":                      "audio",
	"audio/mpeg":                     "audio",
	"audio/ogg":                      "audio",
	"audio/x-aiff":                   "audio",
	"audio/x-pn-realaudio":           "audio",
	"audio/x-wav":                    "audio",
	"image/bmp":                      "image",
	"image/gif":                      "image",
	"image/ief":                      "image",
	"image/jpeg":                     "image",
	"image/pict":                     "image",
	"image/png":                      "image",
	"image/svg+xml":                  "image",
	"image/tiff":                     "image",
	"image/vnd.microsoft.icon":       "image",
	"image/x-cmu-raster":             "image",
	"image/x-portable-anymap":        "image",
	"image/x-portable-bitmap":        "image",
	"image/x-portable-graymap":       "image",
	"image/x-portable-pixmap":        "image",
	"image/x-rgb":                    "image",
	"image/x-xbitmap":                "image",
	"image/x-xpixmap":                "image",
	"image/x-xwindowdump":            "image",
	"message/rfc822":                 "document",
	"model/iges":                     "model",
	"model/stl":                      "model",
	"text/css":                       "document",
	"text/csv":                       "document",
	"text/html":                      "document",
	"text/markdown":                  "document",
	"text/plain":                     "document",
	"text/richtext":                  "document",
	"text/tab-separated-values":      "document",
	"text/vtt":                       "document",
	"text/x-python":                  "document",
	"text/x-setext":                  "document",
	"text/x-sgml":                    "document",
	"text/x-vcard":                   "document",
	"text/xml":                       "document",
	"text/xul":                       "document",
	"video/iso.segment":              "binary",
	"video/m4v":                      "video",
	"video/mp2t":                     "video",
	"video/mp4":                      "video",
	"video/mpeg":                     "video",
	"video/ogg":                      "video",
	"video/quicktime":                "video",
	"video/webm":                     "video",
	"video/x-matroska":               "video",
	"video/x-ms-wmv":                 "video",
	"video/x-msvideo":                "video",
	"video/x-sgi-movie":              "video",
}
