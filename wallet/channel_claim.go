package wallet

import (
	"bytes"
	"encoding/asn1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"google.golang.org/protobuf/encoding/protowire"
)

const (
	channelClaimNameOpcode   = 0xb5
	channelUpdateClaimOpcode = 0xb7
	channelPushData1Opcode   = 0x4c
	channelPushData2Opcode   = 0x4d
	channelPushData4Opcode   = 0x4e
)

var (
	ErrDecodedClaimNotChannel  = errors.New("decoded claim is not a channel")
	ErrInvalidChannelPublicKey = errors.New("invalid channel public key")
	channelECPublicKeyOID      = asn1.ObjectIdentifier{1, 2, 840, 10045, 2, 1}
)

// DecodeChannelClaimPublicKey mirrors the part of Database.is_channel_key_used
// that turns a stored output script into Claim.channel.public_key_bytes.
// Output-script and claim-message decode failures are deliberately reported as
// isChannel=false: Output.can_decode_claim catches those failures. Errors after
// a Claim has decoded, including a non-channel type or malformed SPKI key,
// remain visible because the Python database accesses claim.channel outside
// that guard.
func DecodeChannelClaimPublicKey(script []byte) (key []byte, isChannel bool, err error) {
	claimPayload, ok := channelClaimPayload(script)
	if !ok {
		return nil, false, nil
	}

	publicKey, claimType, err := decodeChannelClaimPayload(claimPayload)
	if err != nil {
		return nil, false, nil
	}
	if claimType != channelClaimTypeChannel {
		return nil, false, ErrDecodedClaimNotChannel
	}

	normalized, err := normalizeChannelPublicKey(publicKey)
	if err != nil {
		return nil, true, err
	}
	return normalized, true, nil
}

func channelClaimPayload(script []byte) ([]byte, bool) {
	if len(script) == 0 {
		return nil, false
	}
	offset := 1
	switch script[0] {
	case channelClaimNameOpcode:
		if _, next, ok := readChannelScriptPush(script, offset); ok {
			offset = next
		} else {
			return nil, false
		}
	case channelUpdateClaimOpcode:
		if _, next, ok := readChannelScriptPush(script, offset); ok {
			offset = next
		} else {
			return nil, false
		}
		if _, next, ok := readChannelScriptPush(script, offset); ok {
			offset = next
		} else {
			return nil, false
		}
	default:
		return nil, false
	}

	claim, next, ok := readChannelScriptPush(script, offset)
	if !ok {
		return nil, false
	}
	offset = next
	if script[0] == channelClaimNameOpcode {
		if !consumeChannelOpcodes(script, &offset, 0x6d, 0x75) { // OP_2DROP OP_DROP
			return nil, false
		}
	} else if !consumeChannelOpcodes(script, &offset, 0x6d, 0x6d) { // OP_2DROP OP_2DROP
		return nil, false
	}
	if !consumeChannelPayment(script, offset) {
		return nil, false
	}
	return claim, true
}

func readChannelScriptPush(script []byte, offset int) ([]byte, int, bool) {
	if offset >= len(script) {
		return nil, offset, false
	}
	opcode := script[offset]
	offset++
	var size uint64
	switch {
	case opcode == 0:
		return []byte{}, offset, true
	case opcode < channelPushData1Opcode:
		size = uint64(opcode)
	case opcode == channelPushData1Opcode:
		if offset >= len(script) {
			return nil, offset, false
		}
		size = uint64(script[offset])
		offset++
	case opcode == channelPushData2Opcode:
		if len(script)-offset < 2 {
			return nil, offset, false
		}
		size = uint64(script[offset]) | uint64(script[offset+1])<<8
		offset += 2
	case opcode == channelPushData4Opcode:
		if len(script)-offset < 4 {
			return nil, offset, false
		}
		size = uint64(script[offset]) |
			uint64(script[offset+1])<<8 |
			uint64(script[offset+2])<<16 |
			uint64(script[offset+3])<<24
		offset += 4
	default:
		return nil, offset, false
	}
	if size > uint64(len(script)-offset) {
		return nil, offset, false
	}
	end := offset + int(size)
	return script[offset:end], end, true
}

func consumeChannelOpcodes(script []byte, offset *int, opcodes ...byte) bool {
	if len(script)-*offset < len(opcodes) {
		return false
	}
	for _, opcode := range opcodes {
		if script[*offset] != opcode {
			return false
		}
		*offset++
	}
	return true
}

func consumeChannelPayment(script []byte, offset int) bool {
	start := offset
	// PAY_PUBKEY_HASH: OP_DUP OP_HASH160 <hash> OP_EQUALVERIFY OP_CHECKSIG
	if consumeChannelOpcodes(script, &offset, 0x76, 0xa9) {
		if _, next, ok := readChannelScriptPush(script, offset); ok {
			offset = next
		} else {
			return false
		}
		return consumeChannelOpcodes(script, &offset, 0x88, 0xac) && offset == len(script)
	}

	// PAY_SCRIPT_HASH: OP_HASH160 <hash> OP_EQUAL
	offset = start
	if !consumeChannelOpcodes(script, &offset, 0xa9) {
		return false
	}
	if _, next, ok := readChannelScriptPush(script, offset); ok {
		offset = next
	} else {
		return false
	}
	return consumeChannelOpcodes(script, &offset, 0x87) && offset == len(script)
}

type channelClaimType uint8

const (
	channelClaimTypeUnset channelClaimType = iota
	channelClaimTypeStream
	channelClaimTypeChannel
	channelClaimTypeCollection
	channelClaimTypeRepost
)

func decodeChannelClaimPayload(payload []byte) ([]byte, channelClaimType, error) {
	if len(payload) == 0 {
		return nil, channelClaimTypeUnset, errors.New("empty claim payload")
	}
	switch payload[0] {
	case 0:
		return decodeV2ChannelClaim(payload[1:])
	case 1:
		// Python slicing tolerates short signature wrappers; ParseFromString then
		// receives an empty protobuf message.
		message := []byte{}
		if len(payload) > 85 {
			message = payload[85:]
		}
		return decodeV2ChannelClaim(message)
	case '{':
		return decodeLegacyJSONClaim(payload)
	default:
		return decodeV1ChannelClaim(payload)
	}
}

func decodeLegacyJSONClaim(payload []byte) ([]byte, channelClaimType, error) {
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil || value == nil {
		return nil, channelClaimTypeUnset, errors.New("invalid old JSON claim")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, channelClaimTypeUnset, errors.New("invalid old JSON claim")
	}
	sources, ok := value["sources"].(map[string]any)
	if !ok {
		return nil, channelClaimTypeUnset, errors.New("old JSON claim has no sources object")
	}
	sdHash, ok := sources["lbry_sd_hash"].(string)
	if !ok {
		return nil, channelClaimTypeUnset, errors.New("old JSON claim has no string sd hash")
	}
	if _, err := hex.DecodeString(sdHash); err != nil {
		return nil, channelClaimTypeUnset, err
	}

	// compat.from_old_json_schema assigns these values directly to protobuf
	// string fields. A present JSON null or non-string therefore fails inside
	// can_decode_claim instead of producing a Stream Claim.
	for _, name := range []string{"title", "description", "author", "license", "license_url"} {
		if field, exists := value[name]; exists {
			if _, ok := field.(string); !ok {
				return nil, channelClaimTypeUnset, fmt.Errorf("old JSON claim field %s is not a string", name)
			}
		}
	}
	mediaType, exists := value["content_type"]
	if !exists {
		mediaType = value["content-type"]
	}
	if jsonTruthy(mediaType) {
		if _, ok := mediaType.(string); !ok {
			return nil, channelClaimTypeUnset, errors.New("old JSON media type is not a string")
		}
	}
	for _, name := range []string{"thumbnail", "language"} {
		if field := value[name]; jsonTruthy(field) {
			if _, ok := field.(string); !ok {
				return nil, channelClaimTypeUnset, fmt.Errorf("old JSON claim field %s is not a string", name)
			}
		}
	}
	if rawFee, exists := value["fee"]; exists {
		if fee, ok := rawFee.(map[string]any); ok {
			if len(fee) == 0 {
				return nil, channelClaimTypeUnset, errors.New("old JSON fee is empty")
			}
			// Python chooses the first JSON key. encoding/json maps do not retain
			// insertion order, so exact multi-currency behavior remains outside this
			// decoder; the emitted legacy schema contains one currency.
			if len(fee) == 1 {
				for currency, raw := range fee {
					if currency != "LBC" && currency != "USD" && currency != "BTC" {
						return nil, channelClaimTypeUnset, errors.New("old JSON fee has an unknown currency")
					}
					entry, ok := raw.(map[string]any)
					if !ok || !legacyJSONAmountFits(currency, entry["amount"]) {
						return nil, channelClaimTypeUnset, errors.New("old JSON fee has an invalid amount")
					}
					address, ok := entry["address"].(string)
					if !ok || !legacyJSONBase58(address) {
						return nil, channelClaimTypeUnset, errors.New("old JSON fee has an invalid address")
					}
				}
			}
		}
	}
	return nil, channelClaimTypeStream, nil
}

func jsonTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case json.Number:
		_, coefficient, _, ok := legacyJSONDecimalParts(typed)
		return !ok || coefficient != "0"
	case string:
		return typed != ""
	case []any:
		return len(typed) != 0
	case map[string]any:
		return len(typed) != 0
	default:
		return true
	}
}

const legacyJSONMaxUint64 = "18446744073709551615"

func legacyJSONAmountFits(currency string, value any) bool {
	scale, roundAway := int64(100_000_000), false
	if currency == "USD" {
		scale, roundAway = 100, true
	}
	if number, ok := value.(json.Number); ok && strings.ContainsAny(number.String(), ".eE") {
		parsed, err := strconv.ParseFloat(number.String(), 64)
		return err == nil && legacyScaledFloatFitsUint64(parsed, scale, roundAway)
	}
	negative, coefficient, exponent, ok := legacyJSONDecimalParts(value)
	if !ok {
		return false
	}
	if coefficient == "0" {
		return true
	}

	decimalScale := int64(8)
	if roundAway {
		decimalScale = 2
	}
	scaledExponent := saturatingLegacyDecimalAdd(exponent, decimalScale)
	magnitude, discarded := legacyJSONScaledInteger(coefficient, scaledExponent)
	if roundAway && discarded {
		magnitude = incrementLegacyDecimal(magnitude)
	}
	if negative && magnitude != "0" {
		return false
	}
	return compareLegacyDecimal(magnitude, legacyJSONMaxUint64) <= 0
}

func legacyJSONDecimalParts(value any) (negative bool, coefficient string, exponent int64, ok bool) {
	var source string
	switch typed := value.(type) {
	case json.Number:
		source = typed.String()
	case string:
		source = strings.TrimSpace(typed)
	case bool:
		if typed {
			return false, "1", 0, true
		}
		return false, "0", 0, true
	default:
		return false, "", 0, false
	}
	if source == "" {
		return false, "", 0, false
	}
	if source[0] == '+' || source[0] == '-' {
		negative = source[0] == '-'
		source = source[1:]
	}
	if source == "" {
		return false, "", 0, false
	}

	mantissa := source
	if index := strings.IndexAny(source, "eE"); index >= 0 {
		if strings.IndexAny(source[index+1:], "eE") >= 0 {
			return false, "", 0, false
		}
		mantissa = source[:index]
		var valid bool
		exponent, valid = parseLegacyDecimalExponent(source[index+1:])
		if !valid {
			return false, "", 0, false
		}
	}
	if mantissa == "" || strings.Count(mantissa, ".") > 1 {
		return false, "", 0, false
	}
	decimalIndex := strings.IndexByte(mantissa, '.')
	fractionDigits := 0
	if decimalIndex >= 0 {
		fractionDigits = len(mantissa) - decimalIndex - 1
		mantissa = mantissa[:decimalIndex] + mantissa[decimalIndex+1:]
	}
	if mantissa == "" {
		return false, "", 0, false
	}
	for _, digit := range mantissa {
		if digit < '0' || digit > '9' {
			return false, "", 0, false
		}
	}
	coefficient = strings.TrimLeft(mantissa, "0")
	if coefficient == "" {
		coefficient = "0"
	}
	exponent = saturatingLegacyDecimalAdd(exponent, -int64(fractionDigits))
	return negative, coefficient, exponent, true
}

func parseLegacyDecimalExponent(source string) (int64, bool) {
	if source == "" {
		return 0, false
	}
	negative := false
	if source[0] == '+' || source[0] == '-' {
		negative = source[0] == '-'
		source = source[1:]
	}
	if source == "" {
		return 0, false
	}
	for _, digit := range source {
		if digit < '0' || digit > '9' {
			return 0, false
		}
	}
	parsed, err := strconv.ParseInt(source, 10, 64)
	if err != nil || parsed > 1_000_000_000 {
		parsed = 1_000_000_000
	}
	if negative {
		parsed = -parsed
	}
	return parsed, true
}

func saturatingLegacyDecimalAdd(left, right int64) int64 {
	const limit = int64(1_000_000_000)
	if left > limit {
		left = limit
	} else if left < -limit {
		left = -limit
	}
	value := left + right
	if value > limit {
		return limit
	}
	if value < -limit {
		return -limit
	}
	return value
}

func legacyJSONScaledInteger(coefficient string, exponent int64) (string, bool) {
	if coefficient == "0" {
		return "0", false
	}
	if exponent >= 0 {
		if exponent > 20 || int64(len(coefficient))+exponent > 20 {
			return legacyJSONMaxUint64 + "0", false
		}
		return coefficient + strings.Repeat("0", int(exponent)), false
	}
	discard := -exponent
	if discard >= int64(len(coefficient)) {
		return "0", true
	}
	cut := len(coefficient) - int(discard)
	return coefficient[:cut], strings.Trim(coefficient[cut:], "0") != ""
}

func incrementLegacyDecimal(value string) string {
	digits := []byte(value)
	for index := len(digits) - 1; index >= 0; index-- {
		if digits[index] != '9' {
			digits[index]++
			return string(digits)
		}
		digits[index] = '0'
	}
	return "1" + string(digits)
}

func compareLegacyDecimal(left, right string) int {
	left = strings.TrimLeft(left, "0")
	right = strings.TrimLeft(right, "0")
	if left == "" {
		left = "0"
	}
	if right == "" {
		right = "0"
	}
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	return strings.Compare(left, right)
}

func legacyJSONBase58(address string) bool {
	if address == "" {
		return false
	}
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	for _, character := range address {
		if !strings.ContainsRune(alphabet, character) {
			return false
		}
	}
	return true
}

func decodeV2ChannelClaim(message []byte) ([]byte, channelClaimType, error) {
	claimType := channelClaimTypeUnset
	var publicKey []byte
	for len(message) > 0 {
		number, wireType, tagLength := protowire.ConsumeTag(message)
		if tagLength < 0 {
			return nil, channelClaimTypeUnset, protowire.ParseError(tagLength)
		}
		value := message[tagLength:]
		valueLength := protowire.ConsumeFieldValue(number, wireType, value)
		if valueLength < 0 {
			return nil, channelClaimTypeUnset, protowire.ParseError(valueLength)
		}

		switch number {
		case 1, 2, 3, 4:
			if wireType != protowire.BytesType {
				break // A mismatched known field is retained as an unknown field.
			}
			nested, length := protowire.ConsumeBytes(value)
			if length < 0 {
				return nil, channelClaimTypeUnset, protowire.ParseError(length)
			}
			nextType := channelClaimType(number)
			if nextType == channelClaimTypeChannel {
				if claimType != channelClaimTypeChannel {
					publicKey = nil
				}
				var err error
				publicKey, err = mergeV2Channel(nested, publicKey)
				if err != nil {
					return nil, channelClaimTypeUnset, err
				}
			} else {
				kind := v2MessageStream
				switch nextType {
				case channelClaimTypeCollection:
					kind = v2MessageClaimList
				case channelClaimTypeRepost:
					kind = v2MessageClaimReference
				}
				if err := validateV2Message(nested, kind); err != nil {
					return nil, channelClaimTypeUnset, err
				}
			}
			claimType = nextType
		case 8, 9, 11:
			if wireType != protowire.BytesType {
				break
			}
			encoded, length := protowire.ConsumeBytes(value)
			if length < 0 || !utf8.Valid(encoded) {
				return nil, channelClaimTypeUnset, errors.New("claim contains invalid UTF-8")
			}
		case 10, 12, 13:
			if wireType != protowire.BytesType {
				break
			}
			nested, length := protowire.ConsumeBytes(value)
			if length < 0 {
				return nil, channelClaimTypeUnset, protowire.ParseError(length)
			}
			kind := v2MessageSource
			if number == 12 {
				kind = v2MessageLanguage
			} else if number == 13 {
				kind = v2MessageLocation
			}
			if err := validateV2Message(nested, kind); err != nil {
				return nil, channelClaimTypeUnset, err
			}
		}
		message = value[valueLength:]
	}

	// Claim.channel sets an unset oneof to channel before reading its key.
	if claimType == channelClaimTypeUnset {
		claimType = channelClaimTypeChannel
	}
	return publicKey, claimType, nil
}

func mergeV2Channel(message, publicKey []byte) ([]byte, error) {
	for len(message) > 0 {
		number, wireType, tagLength := protowire.ConsumeTag(message)
		if tagLength < 0 {
			return nil, protowire.ParseError(tagLength)
		}
		value := message[tagLength:]
		valueLength := protowire.ConsumeFieldValue(number, wireType, value)
		if valueLength < 0 {
			return nil, protowire.ParseError(valueLength)
		}
		switch number {
		case 1:
			if wireType != protowire.BytesType {
				break
			}
			encoded, length := protowire.ConsumeBytes(value)
			if length < 0 {
				return nil, protowire.ParseError(length)
			}
			publicKey = append(publicKey[:0], encoded...)
		case 2, 3:
			if wireType != protowire.BytesType {
				break
			}
			encoded, length := protowire.ConsumeBytes(value)
			if length < 0 || !utf8.Valid(encoded) {
				return nil, errors.New("channel contains invalid UTF-8")
			}
		case 4, 5:
			if wireType != protowire.BytesType {
				break
			}
			nested, length := protowire.ConsumeBytes(value)
			if length < 0 {
				return nil, protowire.ParseError(length)
			}
			kind := v2MessageSource
			if number == 5 {
				kind = v2MessageClaimList
			}
			if err := validateV2Message(nested, kind); err != nil {
				return nil, err
			}
		}
		message = value[valueLength:]
	}
	return publicKey, nil
}

func decodeV1ChannelClaim(message []byte) ([]byte, channelClaimType, error) {
	claimType := uint64(0)
	var certificate []byte
	for len(message) > 0 {
		number, wireType, tagLength := protowire.ConsumeTag(message)
		if tagLength < 0 {
			return nil, channelClaimTypeUnset, protowire.ParseError(tagLength)
		}
		value := message[tagLength:]
		valueLength := protowire.ConsumeFieldValue(number, wireType, value)
		if valueLength < 0 {
			return nil, channelClaimTypeUnset, protowire.ParseError(valueLength)
		}
		switch number {
		case 1, 2:
			if wireType != protowire.VarintType {
				break
			}
			if number == 2 {
				decoded, _ := protowire.ConsumeVarint(value)
				// Unknown proto2 enum values are retained in unknown fields and do
				// not replace the last recognized value.
				if decoded <= 2 {
					claimType = decoded
				}
			}
		case 3, 4, 5:
			if wireType != protowire.BytesType {
				break
			}
			nested, length := protowire.ConsumeBytes(value)
			if length < 0 {
				return nil, channelClaimTypeUnset, protowire.ParseError(length)
			}
			kind := legacyMessageStream
			if number == 4 {
				kind = legacyMessageCertificate
			} else if number == 5 {
				kind = legacyMessageSignature
			}
			if err := validateLegacyMessage(nested, kind); err != nil {
				return nil, channelClaimTypeUnset, err
			}
			if number == 4 {
				// Singular protobuf messages merge across repeated occurrences.
				certificate = append(certificate, nested...)
			}
		}
		message = value[valueLength:]
	}
	if claimType != 2 {
		return nil, channelClaimTypeStream, nil
	}
	publicKey, err := decodeV1Certificate(certificate)
	if err != nil {
		return nil, channelClaimTypeUnset, err
	}
	return publicKey, channelClaimTypeChannel, nil
}

func decodeV1Certificate(message []byte) ([]byte, error) {
	var publicKey []byte
	for len(message) > 0 {
		number, wireType, tagLength := protowire.ConsumeTag(message)
		if tagLength < 0 {
			return nil, protowire.ParseError(tagLength)
		}
		value := message[tagLength:]
		valueLength := protowire.ConsumeFieldValue(number, wireType, value)
		if valueLength < 0 {
			return nil, protowire.ParseError(valueLength)
		}
		switch number {
		case 1, 2:
			if wireType != protowire.VarintType {
				break
			}
		case 4:
			if wireType != protowire.BytesType {
				break
			}
			encoded, length := protowire.ConsumeBytes(value)
			if length < 0 {
				return nil, protowire.ParseError(length)
			}
			publicKey = append(publicKey[:0], encoded...)
		}
		message = value[valueLength:]
	}
	return publicKey, nil
}

type v2MessageType uint8

const (
	v2MessageStream v2MessageType = iota
	v2MessageClaimReference
	v2MessageClaimList
	v2MessageSource
	v2MessageFee
	v2MessageImage
	v2MessageVideo
	v2MessageAudio
	v2MessageSoftware
	v2MessageLanguage
	v2MessageLocation
)

type protobufFieldType uint8

const (
	protobufUnknown protobufFieldType = iota
	protobufBytes
	protobufString
	protobufVarint
	protobufFixed32
	protobufMessage
)

func validateV2Message(message []byte, messageType v2MessageType) error {
	for len(message) > 0 {
		number, wireType, tagLength := protowire.ConsumeTag(message)
		if tagLength < 0 {
			return protowire.ParseError(tagLength)
		}
		value := message[tagLength:]
		valueLength := protowire.ConsumeFieldValue(number, wireType, value)
		if valueLength < 0 {
			return protowire.ParseError(valueLength)
		}
		fieldType, nestedType := v2Field(messageType, number)
		expectedWire := protowire.Type(-1)
		switch fieldType {
		case protobufBytes, protobufString, protobufMessage:
			expectedWire = protowire.BytesType
		case protobufVarint:
			expectedWire = protowire.VarintType
		case protobufFixed32:
			expectedWire = protowire.Fixed32Type
		}
		// Generated protobuf decoders preserve known fields using a mismatched
		// wire encoding as unknown fields.
		if fieldType != protobufUnknown && wireType == expectedWire {
			switch fieldType {
			case protobufString:
				encoded, length := protowire.ConsumeBytes(value)
				if length < 0 || !utf8.Valid(encoded) {
					return errors.New("protobuf string contains invalid UTF-8")
				}
			case protobufMessage:
				nested, length := protowire.ConsumeBytes(value)
				if length < 0 {
					return protowire.ParseError(length)
				}
				if err := validateV2Message(nested, nestedType); err != nil {
					return err
				}
			}
		}
		message = value[valueLength:]
	}
	return nil
}

func v2Field(messageType v2MessageType, number protowire.Number) (protobufFieldType, v2MessageType) {
	switch messageType {
	case v2MessageStream:
		switch number {
		case 1:
			return protobufMessage, v2MessageSource
		case 2, 3, 4:
			return protobufString, 0
		case 5:
			return protobufVarint, 0
		case 6:
			return protobufMessage, v2MessageFee
		case 10:
			return protobufMessage, v2MessageImage
		case 11:
			return protobufMessage, v2MessageVideo
		case 12:
			return protobufMessage, v2MessageAudio
		case 13:
			return protobufMessage, v2MessageSoftware
		}
	case v2MessageClaimReference:
		if number == 1 {
			return protobufBytes, 0
		}
	case v2MessageClaimList:
		switch number {
		case 1:
			return protobufVarint, 0
		case 2:
			return protobufMessage, v2MessageClaimReference
		}
	case v2MessageSource:
		switch number {
		case 1, 6, 7:
			return protobufBytes, 0
		case 2, 4, 5:
			return protobufString, 0
		case 3:
			return protobufVarint, 0
		}
	case v2MessageFee:
		switch number {
		case 1, 3:
			return protobufVarint, 0
		case 2:
			return protobufBytes, 0
		}
	case v2MessageImage:
		if number == 1 || number == 2 {
			return protobufVarint, 0
		}
	case v2MessageVideo:
		if number >= 1 && number <= 3 {
			return protobufVarint, 0
		}
		if number == 15 {
			return protobufMessage, v2MessageAudio
		}
	case v2MessageAudio:
		if number == 1 {
			return protobufVarint, 0
		}
	case v2MessageSoftware:
		if number == 1 {
			return protobufString, 0
		}
	case v2MessageLanguage:
		if number >= 1 && number <= 3 {
			return protobufVarint, 0
		}
	case v2MessageLocation:
		switch number {
		case 1, 5, 6:
			return protobufVarint, 0
		case 2, 3, 4:
			return protobufString, 0
		}
	}
	return protobufUnknown, 0
}

type legacyMessageType uint8

const (
	legacyMessageStream legacyMessageType = iota
	legacyMessageMetadata
	legacyMessageSource
	legacyMessageFee
	legacyMessageCertificate
	legacyMessageSignature
)

func validateLegacyMessage(message []byte, messageType legacyMessageType) error {
	for len(message) > 0 {
		number, wireType, tagLength := protowire.ConsumeTag(message)
		if tagLength < 0 {
			return protowire.ParseError(tagLength)
		}
		value := message[tagLength:]
		valueLength := protowire.ConsumeFieldValue(number, wireType, value)
		if valueLength < 0 {
			return protowire.ParseError(valueLength)
		}
		fieldType, nestedType := legacyField(messageType, number)
		expectedWire := protowire.Type(-1)
		switch fieldType {
		case protobufBytes, protobufString, protobufMessage:
			expectedWire = protowire.BytesType
		case protobufVarint:
			expectedWire = protowire.VarintType
		case protobufFixed32:
			expectedWire = protowire.Fixed32Type
		}
		if fieldType != protobufUnknown && wireType == expectedWire {
			switch fieldType {
			case protobufString:
				encoded, length := protowire.ConsumeBytes(value)
				if length < 0 || !utf8.Valid(encoded) {
					return errors.New("legacy protobuf string contains invalid UTF-8")
				}
			case protobufMessage:
				nested, length := protowire.ConsumeBytes(value)
				if length < 0 {
					return protowire.ParseError(length)
				}
				if err := validateLegacyMessage(nested, nestedType); err != nil {
					return err
				}
			}
		}
		message = value[valueLength:]
	}
	return nil
}

func legacyField(messageType legacyMessageType, number protowire.Number) (protobufFieldType, legacyMessageType) {
	switch messageType {
	case legacyMessageStream:
		switch number {
		case 1:
			return protobufVarint, 0
		case 2:
			return protobufMessage, legacyMessageMetadata
		case 3:
			return protobufMessage, legacyMessageSource
		}
	case legacyMessageMetadata:
		switch number {
		case 1, 2, 7:
			return protobufVarint, 0
		case 3, 4, 5, 6, 9, 10, 11:
			return protobufString, 0
		case 8:
			return protobufMessage, legacyMessageFee
		}
	case legacyMessageSource:
		switch number {
		case 1, 2:
			return protobufVarint, 0
		case 3:
			return protobufBytes, 0
		case 4:
			return protobufString, 0
		}
	case legacyMessageFee:
		switch number {
		case 1, 2:
			return protobufVarint, 0
		case 3:
			return protobufBytes, 0
		case 4:
			return protobufFixed32, 0
		}
	case legacyMessageCertificate:
		switch number {
		case 1, 2:
			return protobufVarint, 0
		case 4:
			return protobufBytes, 0
		}
	case legacyMessageSignature:
		switch number {
		case 1, 2:
			return protobufVarint, 0
		case 3, 4:
			return protobufBytes, 0
		}
	}
	return protobufUnknown, 0
}

// normalizeChannelBERElement canonicalizes the BER forms accepted by
// asn1crypto.Asn1Value.load(strict=False), notably indefinite-length sequences
// and constructed bit strings, before encoding/asn1 reads the SPKI structure.
func normalizeChannelBERElement(encoded []byte, depth int) ([]byte, []byte, error) {
	if depth > 64 {
		return nil, nil, errors.New("ASN.1 nesting exceeds 64 levels")
	}
	if len(encoded) < 2 {
		return nil, nil, errors.New("truncated ASN.1 element")
	}
	class := int(encoded[0] >> 6)
	compound := encoded[0]&0x20 != 0
	tag := int(encoded[0] & 0x1f)
	offset := 1
	if tag == 0x1f {
		tag = 0
		firstOctet := true
		for {
			if offset >= len(encoded) {
				return nil, nil, errors.New("truncated ASN.1 high tag")
			}
			value := encoded[offset]
			offset++
			if firstOctet && value == 0x80 {
				return nil, nil, errors.New("non-minimal ASN.1 high tag")
			}
			firstOctet = false
			if tag > int(^uint(0)>>1)>>7 {
				return nil, nil, errors.New("ASN.1 tag overflows int")
			}
			tag = tag<<7 | int(value&0x7f)
			if value&0x80 == 0 {
				break
			}
		}
		if tag < 31 {
			return nil, nil, errors.New("non-minimal ASN.1 high tag")
		}
	}
	if offset >= len(encoded) {
		return nil, nil, errors.New("truncated ASN.1 length")
	}
	lengthByte := encoded[offset]
	offset++
	indefinite := lengthByte == 0x80
	length := 0
	if lengthByte&0x80 == 0 {
		length = int(lengthByte)
	} else if !indefinite {
		lengthBytes := int(lengthByte & 0x7f)
		if lengthBytes == 0 || lengthBytes > 8 || offset+lengthBytes > len(encoded) {
			return nil, nil, errors.New("invalid ASN.1 long length")
		}
		for _, value := range encoded[offset : offset+lengthBytes] {
			if length > (int(^uint(0)>>1)-int(value))/256 {
				return nil, nil, errors.New("ASN.1 length overflows int")
			}
			length = length*256 + int(value)
		}
		offset += lengthBytes
	}
	if indefinite && !compound {
		return nil, nil, errors.New("primitive ASN.1 element has indefinite length")
	}

	var contents, rest []byte
	if indefinite {
		cursor := encoded[offset:]
		for {
			if len(cursor) < 2 {
				return nil, nil, errors.New("unterminated indefinite ASN.1 element")
			}
			if cursor[0] == 0 && cursor[1] == 0 {
				rest = cursor[2:]
				break
			}
			child, childRest, err := normalizeChannelBERElement(cursor, depth+1)
			if err != nil {
				return nil, nil, err
			}
			if len(childRest) >= len(cursor) {
				return nil, nil, errors.New("ASN.1 parser made no progress")
			}
			contents = append(contents, child...)
			cursor = childRest
		}
	} else {
		if length > len(encoded)-offset {
			return nil, nil, errors.New("ASN.1 length exceeds input")
		}
		rawContents := encoded[offset : offset+length]
		rest = encoded[offset+length:]
		if compound {
			cursor := rawContents
			for len(cursor) > 0 {
				child, childRest, err := normalizeChannelBERElement(cursor, depth+1)
				if err != nil {
					return nil, nil, err
				}
				if len(childRest) >= len(cursor) {
					return nil, nil, errors.New("ASN.1 parser made no progress")
				}
				contents = append(contents, child...)
				cursor = childRest
			}
		} else {
			contents = append(contents, rawContents...)
		}
	}
	if class == asn1.ClassUniversal && tag == asn1.TagBitString && compound {
		flattened, err := flattenChannelBERBitString(contents)
		if err != nil {
			return nil, nil, err
		}
		contents = flattened
		compound = false
	}

	normalized := appendChannelBERIdentifier(nil, class, tag, compound)
	normalized = appendChannelBERLength(normalized, len(contents))
	normalized = append(normalized, contents...)
	return normalized, rest, nil
}

func flattenChannelBERBitString(contents []byte) ([]byte, error) {
	var fragments []asn1.RawValue
	for len(contents) > 0 {
		var fragment asn1.RawValue
		rest, err := asn1.Unmarshal(contents, &fragment)
		if err != nil {
			return nil, err
		}
		if fragment.Class != asn1.ClassUniversal || fragment.Tag != asn1.TagBitString ||
			fragment.IsCompound || len(fragment.Bytes) == 0 || fragment.Bytes[0] > 7 {
			return nil, errors.New("constructed BIT STRING contains an invalid fragment")
		}
		fragments = append(fragments, fragment)
		contents = rest
	}
	if len(fragments) == 0 {
		return []byte{0}, nil
	}
	flattened := []byte{fragments[len(fragments)-1].Bytes[0]}
	for index, fragment := range fragments {
		if index < len(fragments)-1 && fragment.Bytes[0] != 0 {
			return nil, errors.New("non-final BIT STRING fragment has unused bits")
		}
		flattened = append(flattened, fragment.Bytes[1:]...)
	}
	return flattened, nil
}

func appendChannelBERIdentifier(destination []byte, class, tag int, compound bool) []byte {
	first := byte(class << 6)
	if compound {
		first |= 0x20
	}
	if tag < 31 {
		return append(destination, first|byte(tag))
	}
	destination = append(destination, first|0x1f)
	var encoded [10]byte
	index := len(encoded)
	for {
		index--
		encoded[index] = byte(tag & 0x7f)
		tag >>= 7
		if tag == 0 {
			break
		}
	}
	for current := index; current < len(encoded)-1; current++ {
		encoded[current] |= 0x80
	}
	return append(destination, encoded[index:]...)
}

func appendChannelBERLength(destination []byte, length int) []byte {
	if length < 128 {
		return append(destination, byte(length))
	}
	var encoded [8]byte
	index := len(encoded)
	for length > 0 {
		index--
		encoded[index] = byte(length)
		length >>= 8
	}
	destination = append(destination, 0x80|byte(len(encoded)-index))
	return append(destination, encoded[index:]...)
}

func normalizeChannelPublicKey(publicKey []byte) ([]byte, error) {
	if len(publicKey) == secp256k1.PubKeyBytesLenCompressed {
		return append([]byte(nil), publicKey...), nil
	}
	normalized, _, err := normalizeChannelBERElement(publicKey, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: SPKI BER: %v", ErrInvalidChannelPublicKey, err)
	}
	var info struct {
		Algorithm struct {
			Algorithm  asn1.ObjectIdentifier
			Parameters asn1.RawValue `asn1:"optional"`
		}
		PublicKey asn1.BitString
	}
	if _, err := asn1.Unmarshal(normalized, &info); err != nil {
		return nil, fmt.Errorf("%w: SPKI: %v", ErrInvalidChannelPublicKey, err)
	}
	if !info.Algorithm.Algorithm.Equal(channelECPublicKeyOID) {
		return nil, fmt.Errorf("%w: SPKI algorithm is %s", ErrInvalidChannelPublicKey, info.Algorithm.Algorithm.String())
	}
	if info.PublicKey.BitLength%8 != 0 {
		return nil, fmt.Errorf("%w: SPKI point has unused bits", ErrInvalidChannelPublicKey)
	}
	point := info.PublicKey.Bytes
	if !((len(point) == secp256k1.PubKeyBytesLenCompressed && (point[0] == 2 || point[0] == 3)) ||
		(len(point) == secp256k1.PubKeyBytesLenUncompressed && point[0] == 4)) {
		return nil, fmt.Errorf("%w: SPKI point has unsupported encoding", ErrInvalidChannelPublicKey)
	}
	parsed, err := secp256k1.ParsePubKey(point)
	if err != nil {
		return nil, fmt.Errorf("%w: SPKI point: %v", ErrInvalidChannelPublicKey, err)
	}
	return parsed.SerializeCompressed(), nil
}
