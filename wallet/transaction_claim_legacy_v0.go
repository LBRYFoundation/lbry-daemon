package wallet

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"lbry/daemon/wallet/keys"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// DecodeLegacyV0ClaimValue converts the JSON claim schema used before the v1
// protobuf into the v2 Claim representation returned by the pinned SDK. It is
// intentionally separate from DecodeClaimValue, whose contract remains v2
// only, so callers must opt in at the legacy format dispatch boundary.
func DecodeLegacyV0ClaimValue(payload []byte) (*ClaimValue, error) {
	if len(payload) == 0 || payload[0] != '{' {
		return nil, fmt.Errorf("%w: not a legacy v0 JSON claim", ErrUnsupportedLegacyClaimValue)
	}
	if !utf8.Valid(payload) {
		return nil, claimValueError("DecodeError", "Could not parse JSON.")
	}
	parsed, err := parseLegacyV0JSON(payload)
	if err != nil {
		return nil, claimValueError("DecodeError", "Could not parse JSON.")
	}
	root, ok := parsed.(*legacyV0JSONObject)
	if !ok {
		return nil, claimValueError("TypeError", "legacy JSON claim is not an object")
	}

	descriptor, err := claimV2MessageDescriptor()
	if err != nil {
		return nil, err
	}
	message := dynamicpb.NewMessage(descriptor)
	streamField := descriptor.Fields().ByName("stream")
	stream := message.Mutable(streamField).Message()

	sourcesValue, exists := root.get("sources")
	if !exists {
		return nil, claimValueError("KeyError", "'sources'")
	}
	sources, ok := sourcesValue.(*legacyV0JSONObject)
	if !ok {
		return nil, legacyV0SubscriptError(sourcesValue, "lbry_sd_hash")
	}
	sdHashValue, exists := sources.get("lbry_sd_hash")
	if !exists {
		return nil, claimValueError("KeyError", "'lbry_sd_hash'")
	}
	sdHash, err := legacyV0StringMethod(sdHashValue, "encode")
	if err != nil {
		return nil, err
	}
	if sdHash.InvalidUnicode {
		return nil, claimValueError("UnicodeEncodeError", "legacy JSON sd hash is not valid UTF-8")
	}
	sdHashBytes, err := hex.DecodeString(sdHash.Text)
	if err != nil {
		message := "Non-hexadecimal digit found"
		if len(sdHash.Text)%2 != 0 {
			message = "Odd-length string"
		}
		return nil, claimValueError("Error", message)
	}
	sourceField := stream.Descriptor().Fields().ByName("source")
	source := stream.Mutable(sourceField).Message()
	source.Set(source.Descriptor().Fields().ByName("sd_hash"), protoreflect.ValueOfBytes(sdHashBytes))

	mediaType, exists := root.get("content_type")
	if !exists {
		mediaType, _ = root.get("content-type")
	}
	if !legacyV0Truthy(mediaType) {
		mediaType = legacyV0JSONString{Text: "application/octet-stream"}
	}
	if err := legacyV0SetString(source, "media_type", mediaType, "pb.Source.media_type"); err != nil {
		return nil, err
	}

	for _, field := range []struct {
		name string
		path string
	}{
		{"title", "pb.Claim.title"},
		{"description", "pb.Claim.description"},
	} {
		value, exists := root.get(field.name)
		if !exists {
			value = legacyV0JSONString{}
		}
		if err := legacyV0SetString(message.ProtoReflect(), field.name, value, field.path); err != nil {
			return nil, err
		}
	}

	thumbnail, _ := root.get("thumbnail")
	if legacyV0Truthy(thumbnail) {
		thumbnailField := descriptor.Fields().ByName("thumbnail")
		thumbnailMessage := message.Mutable(thumbnailField).Message()
		if err := legacyV0SetString(thumbnailMessage, "url", thumbnail, "pb.Source.url"); err != nil {
			return nil, err
		}
	}

	for _, field := range []struct {
		name string
		path string
	}{
		{"author", "pb.Stream.author"},
		{"license", "pb.Stream.license"},
		{"license_url", "pb.Stream.license_url"},
	} {
		value, exists := root.get(field.name)
		if !exists {
			value = legacyV0JSONString{}
		}
		if err := legacyV0SetString(stream, field.name, value, field.path); err != nil {
			return nil, err
		}
	}

	language, _ := root.get("language")
	if legacyV0Truthy(language) {
		languageString, err := legacyV0StringMethod(language, "lower")
		if err != nil {
			return nil, err
		}
		if strings.ToLower(languageString.Text) == "english" {
			languageString = legacyV0JSONString{Text: "en"}
		}
		legacyV0AppendLanguage(message.ProtoReflect(), languageString)
	}

	nsfw, _ := root.get("nsfw")
	if legacyV0Truthy(nsfw) {
		tagsField := descriptor.Fields().ByName("tags")
		tags := message.Mutable(tagsField).List()
		tags.Append(protoreflect.ValueOfString("mature"))
	}

	if feeValue, exists := root.get("fee"); exists {
		if fee, ok := feeValue.(*legacyV0JSONObject); ok {
			if err := legacyV0SetFee(stream, fee); err != nil {
				return nil, err
			}
		}
	}

	projected, claimType, err := projectClaimValue(message.ProtoReflect())
	if err != nil {
		return nil, err
	}
	canonicalMessage, err := marshalClaimProtoMessage(message.ProtoReflect())
	if err != nil {
		return nil, claimValueError("DecodeError", err.Error())
	}
	return &ClaimValue{
		Type:      claimType,
		Value:     projected,
		Canonical: append([]byte{0}, canonicalMessage...),
	}, nil
}

func legacyV0SetString(message protoreflect.Message, name string, value any, path string) error {
	text, ok := value.(legacyV0JSONString)
	if !ok {
		display, typeName := legacyV0PythonValue(value)
		return claimValueError("TypeError", fmt.Sprintf(
			"Cannot set %s to %s: %s has type <class '%s'>, but expected one of: (<class 'bytes'>, <class 'str'>)",
			path, display, display, typeName,
		))
	}
	if text.InvalidUnicode {
		return claimValueError("ValueError", fmt.Sprintf("%s isn't a valid unicode string and can't be encoded in UTF-8.", legacyV0PythonString(text)))
	}
	message.Set(message.Descriptor().Fields().ByName(protoreflect.Name(name)), protoreflect.ValueOfString(text.Text))
	return nil
}

func legacyV0AppendLanguage(claim protoreflect.Message, value legacyV0JSONString) {
	field := claim.Descriptor().Fields().ByName("languages")
	list := claim.Mutable(field).List()
	element := list.NewElement()
	list.Append(element)
	language := element.Message()
	parts := strings.Split(value.Text, "-")
	first := parts[0]
	parts = parts[1:]
	if !legacyV0SetEnumName(language, "language", first) {
		return
	}
	if len(parts) > 0 && utf8.RuneCountInString(parts[0]) == 4 {
		if !legacyV0SetEnumName(language, "script", parts[0]) {
			return
		}
		parts = parts[1:]
	}
	if len(parts) > 0 {
		region := parts[0]
		runes := []rune(region)
		alpha := len(runes) == 2
		for _, character := range runes {
			alpha = alpha && unicode.IsLetter(character)
		}
		numeric := len(runes) == 3
		for _, character := range runes {
			numeric = numeric && unicode.IsDigit(character)
		}
		if alpha || numeric {
			if numeric {
				region = "R" + region
			}
			if !legacyV0SetEnumName(language, "region", region) {
				return
			}
			parts = parts[1:]
		}
	}
	// compat.py catches every language parsing failure. Any components left
	// here represent its final assertion, after retaining fields already set.
}

func legacyV0SetEnumName(message protoreflect.Message, name, value string) bool {
	field := message.Descriptor().Fields().ByName(protoreflect.Name(name))
	enumValue := field.Enum().Values().ByName(protoreflect.Name(value))
	if enumValue == nil {
		return false
	}
	message.Set(field, protoreflect.ValueOfEnum(enumValue.Number()))
	return true
}

func legacyV0SetFee(stream protoreflect.Message, object *legacyV0JSONObject) error {
	if len(object.pairs) == 0 {
		return claimValueError("IndexError", "list index out of range")
	}
	currency := object.pairs[0].key.Text
	entryValue := object.pairs[0].value
	entry, ok := entryValue.(*legacyV0JSONObject)
	if !ok {
		return legacyV0SubscriptError(entryValue, "amount")
	}
	amountValue, exists := entry.get("amount")
	if !exists {
		return claimValueError("KeyError", "'amount'")
	}
	amount, err := legacyV0FeeAmount(currency, amountValue)
	if err != nil {
		return err
	}
	currencyNumber := protoreflect.EnumNumber(0)
	switch currency {
	case "LBC":
		currencyNumber = 1
	case "BTC":
		currencyNumber = 2
	case "USD":
		currencyNumber = 3
	default:
		return claimValueError("DecodeError", "Unknown currency: "+currency)
	}

	feeField := stream.Descriptor().Fields().ByName("fee")
	fee := stream.Mutable(feeField).Message()
	fee.Set(fee.Descriptor().Fields().ByName("amount"), protoreflect.ValueOfUint64(amount))
	fee.Set(fee.Descriptor().Fields().ByName("currency"), protoreflect.ValueOfEnum(currencyNumber))

	addressValue, exists := entry.get("address")
	if !exists {
		return claimValueError("KeyError", "'address'")
	}
	address, ok := addressValue.(legacyV0JSONString)
	if !ok {
		return claimValueError("TypeError", "a string is required")
	}
	if address.InvalidUnicode {
		return claimValueError("Base58Error", "invalid base 58 character")
	}
	if address.Text == "" {
		return claimValueError("Base58Error", "string cannot be empty")
	}
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	for _, character := range address.Text {
		if !strings.ContainsRune(alphabet, character) {
			return claimValueError("Base58Error", fmt.Sprintf("invalid base 58 character %q", string(character)))
		}
	}
	decoded, err := keys.DecodeBase58(address.Text)
	if err != nil {
		return claimValueError("Base58Error", err.Error())
	}
	fee.Set(fee.Descriptor().Fields().ByName("address"), protoreflect.ValueOfBytes(decoded))
	return nil
}

func legacyV0FeeAmount(currency string, value any) (uint64, error) {
	if currency != "LBC" && currency != "BTC" && currency != "USD" {
		return 0, nil // compat.py rejects the currency before inspecting amount.
	}
	if amount, handled, err := legacyV0ExactDecimalFeeAmount(currency, value); handled {
		return amount, err
	}
	rational, err := legacyV0Decimal(value)
	if err != nil {
		return 0, err
	}
	scale := int64(100_000_000)
	roundAway := false
	if currency == "USD" {
		scale = 100
		roundAway = true
	}
	scaled := new(big.Rat).Mul(rational, new(big.Rat).SetInt64(scale))
	numerator := new(big.Int).Set(scaled.Num())
	denominator := scaled.Denom()
	negative := numerator.Sign() < 0
	if negative {
		numerator.Abs(numerator)
	}
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, denominator, remainder)
	if roundAway && remainder.Sign() != 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if negative && quotient.Sign() != 0 {
		quotient.Neg(quotient)
	}
	if quotient.Sign() < 0 || !quotient.IsUint64() {
		return 0, claimValueError("ValueError", "Value out of range")
	}
	return quotient.Uint64(), nil
}

func legacyV0ExactDecimalFeeAmount(currency string, value any) (uint64, bool, error) {
	var decimal any
	switch typed := value.(type) {
	case legacyV0Number:
		if typed.floating {
			return 0, false, nil
		}
		decimal = json.Number(typed.raw)
	case legacyV0JSONString:
		if typed.InvalidUnicode {
			return 0, true, claimValueError("InvalidOperation", "[<class 'decimal.ConversionSyntax'>]")
		}
		decimal = strings.ReplaceAll(typed.Text, "_", "")
	case bool:
		decimal = typed
	default:
		return 0, false, nil
	}
	negative, coefficient, exponent, ok := legacyJSONDecimalParts(decimal)
	if !ok {
		return 0, true, claimValueError("InvalidOperation", "[<class 'decimal.ConversionSyntax'>]")
	}
	scale := int64(8)
	roundAway := false
	if currency == "USD" {
		scale = 2
		roundAway = true
	}
	magnitude, discarded := legacyJSONScaledInteger(coefficient, saturatingLegacyDecimalAdd(exponent, scale))
	if roundAway && discarded {
		magnitude = incrementLegacyDecimal(magnitude)
	}
	if negative && magnitude != "0" || compareLegacyDecimal(magnitude, legacyJSONMaxUint64) > 0 {
		return 0, true, claimValueError("ValueError", "Value out of range")
	}
	amount, err := strconv.ParseUint(magnitude, 10, 64)
	if err != nil {
		return 0, true, claimValueError("ValueError", "Value out of range")
	}
	return amount, true, nil
}

func legacyV0Decimal(value any) (*big.Rat, error) {
	switch typed := value.(type) {
	case legacyV0Number:
		if typed.special != "" {
			return nil, claimValueError("OverflowError", "cannot convert Infinity to integer")
		}
		if typed.floating {
			parsed, err := strconv.ParseFloat(typed.raw, 64)
			if err != nil || math.IsInf(parsed, 0) || math.IsNaN(parsed) {
				return nil, claimValueError("OverflowError", "cannot convert Infinity to integer")
			}
			return new(big.Rat).SetFloat64(parsed), nil
		}
		integer, ok := new(big.Int).SetString(typed.raw, 10)
		if !ok {
			return nil, claimValueError("InvalidOperation", "invalid decimal value")
		}
		return new(big.Rat).SetInt(integer), nil
	case legacyV0JSONString:
		if typed.InvalidUnicode {
			return nil, claimValueError("InvalidOperation", "invalid decimal value")
		}
		raw := strings.TrimSpace(strings.ReplaceAll(typed.Text, "_", ""))
		if rational, ok := new(big.Rat).SetString(raw); ok {
			return rational, nil
		}
		return nil, claimValueError("InvalidOperation", "[<class 'decimal.ConversionSyntax'>]")
	case bool:
		if typed {
			return new(big.Rat).SetInt64(1), nil
		}
		return new(big.Rat), nil
	case nil:
		return nil, claimValueError("TypeError", "conversion from NoneType to Decimal is not supported")
	default:
		_, typeName := legacyV0PythonValue(value)
		return nil, claimValueError("TypeError", fmt.Sprintf("conversion from %s to Decimal is not supported", typeName))
	}
}

func legacyV0StringMethod(value any, method string) (legacyV0JSONString, error) {
	if text, ok := value.(legacyV0JSONString); ok {
		return text, nil
	}
	_, typeName := legacyV0PythonValue(value)
	return legacyV0JSONString{}, claimValueError("AttributeError", fmt.Sprintf("'%s' object has no attribute '%s'", typeName, method))
}

func legacyV0SubscriptError(value any, key string) error {
	_, typeName := legacyV0PythonValue(value)
	switch value.(type) {
	case []any:
		return claimValueError("TypeError", "list indices must be integers or slices, not str")
	case nil:
		return claimValueError("TypeError", fmt.Sprintf("'%s' object is not subscriptable", typeName))
	default:
		return claimValueError("TypeError", fmt.Sprintf("'%s' object is not subscriptable", typeName))
	}
}

func legacyV0PythonValue(value any) (string, string) {
	switch typed := value.(type) {
	case nil:
		return "None", "NoneType"
	case bool:
		if typed {
			return "True", "bool"
		}
		return "False", "bool"
	case legacyV0Number:
		if typed.floating || typed.special != "" {
			return typed.raw, "float"
		}
		return typed.raw, "int"
	case legacyV0JSONString:
		return legacyV0PythonString(typed), "str"
	case []any:
		return "[]", "list"
	case *legacyV0JSONObject:
		return "{}", "dict"
	default:
		return fmt.Sprint(value), fmt.Sprintf("%T", value)
	}
}

func legacyV0PythonString(value legacyV0JSONString) string {
	return strconv.Quote(value.Text)
}

func legacyV0Truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case legacyV0JSONString:
		return typed.Text != "" || typed.InvalidUnicode
	case legacyV0Number:
		if typed.special != "" {
			return true
		}
		if typed.floating {
			parsed, _ := strconv.ParseFloat(typed.raw, 64)
			return parsed != 0
		}
		integer, ok := new(big.Int).SetString(typed.raw, 10)
		return !ok || integer.Sign() != 0
	case []any:
		return len(typed) != 0
	case *legacyV0JSONObject:
		return len(typed.pairs) != 0
	default:
		return true
	}
}

type legacyV0JSONString struct {
	Text           string
	InvalidUnicode bool
}

type legacyV0Number struct {
	raw      string
	floating bool
	special  string
}

type legacyV0JSONPair struct {
	key   legacyV0JSONString
	value any
}

type legacyV0JSONObject struct {
	pairs []legacyV0JSONPair
}

func (object *legacyV0JSONObject) set(key legacyV0JSONString, value any) {
	for index := range object.pairs {
		if object.pairs[index].key == key {
			object.pairs[index].value = value
			return
		}
	}
	object.pairs = append(object.pairs, legacyV0JSONPair{key: key, value: value})
}

func (object *legacyV0JSONObject) get(name string) (any, bool) {
	for _, pair := range object.pairs {
		if !pair.key.InvalidUnicode && pair.key.Text == name {
			return pair.value, true
		}
	}
	return nil, false
}

type legacyV0JSONParser struct {
	data  []byte
	index int
	depth int
}

func parseLegacyV0JSON(data []byte) (any, error) {
	parser := &legacyV0JSONParser{data: data}
	value, err := parser.value()
	if err != nil {
		return nil, err
	}
	parser.space()
	if parser.index != len(parser.data) {
		return nil, fmt.Errorf("unexpected trailing JSON data")
	}
	return value, nil
}

func (parser *legacyV0JSONParser) value() (any, error) {
	parser.space()
	if parser.index >= len(parser.data) {
		return nil, fmt.Errorf("unexpected end of JSON")
	}
	switch parser.data[parser.index] {
	case '{':
		return parser.object()
	case '[':
		return parser.array()
	case '"':
		return parser.string()
	case 't':
		if parser.literal("true") {
			return true, nil
		}
	case 'f':
		if parser.literal("false") {
			return false, nil
		}
	case 'n':
		if parser.literal("null") {
			return nil, nil
		}
	case 'N':
		if parser.literal("NaN") {
			return legacyV0Number{raw: "NaN", floating: true, special: "NaN"}, nil
		}
	case 'I':
		if parser.literal("Infinity") {
			return legacyV0Number{raw: "Infinity", floating: true, special: "Infinity"}, nil
		}
	case '-':
		if strings.HasPrefix(string(parser.data[parser.index:]), "-Infinity") {
			parser.index += len("-Infinity")
			return legacyV0Number{raw: "-Infinity", floating: true, special: "-Infinity"}, nil
		}
		return parser.number()
	default:
		if parser.data[parser.index] >= '0' && parser.data[parser.index] <= '9' {
			return parser.number()
		}
	}
	return nil, fmt.Errorf("invalid JSON value")
}

func (parser *legacyV0JSONParser) object() (any, error) {
	if parser.depth >= 1000 {
		return nil, fmt.Errorf("JSON nesting is too deep")
	}
	parser.depth++
	defer func() { parser.depth-- }()
	parser.index++
	object := new(legacyV0JSONObject)
	parser.space()
	if parser.take('}') {
		return object, nil
	}
	for {
		parser.space()
		if parser.index >= len(parser.data) || parser.data[parser.index] != '"' {
			return nil, fmt.Errorf("JSON object key is not a string")
		}
		key, err := parser.string()
		if err != nil {
			return nil, err
		}
		parser.space()
		if !parser.take(':') {
			return nil, fmt.Errorf("JSON object has no colon")
		}
		value, err := parser.value()
		if err != nil {
			return nil, err
		}
		object.set(key, value)
		parser.space()
		if parser.take('}') {
			return object, nil
		}
		if !parser.take(',') {
			return nil, fmt.Errorf("JSON object has no comma")
		}
	}
}

func (parser *legacyV0JSONParser) array() (any, error) {
	if parser.depth >= 1000 {
		return nil, fmt.Errorf("JSON nesting is too deep")
	}
	parser.depth++
	defer func() { parser.depth-- }()
	parser.index++
	values := make([]any, 0)
	parser.space()
	if parser.take(']') {
		return values, nil
	}
	for {
		value, err := parser.value()
		if err != nil {
			return nil, err
		}
		values = append(values, value)
		parser.space()
		if parser.take(']') {
			return values, nil
		}
		if !parser.take(',') {
			return nil, fmt.Errorf("JSON array has no comma")
		}
	}
}

func (parser *legacyV0JSONParser) string() (legacyV0JSONString, error) {
	parser.index++
	var output strings.Builder
	invalidUnicode := false
	for parser.index < len(parser.data) {
		character := parser.data[parser.index]
		parser.index++
		switch character {
		case '"':
			return legacyV0JSONString{Text: output.String(), InvalidUnicode: invalidUnicode}, nil
		case '\\':
			if parser.index >= len(parser.data) {
				return legacyV0JSONString{}, fmt.Errorf("truncated JSON escape")
			}
			escape := parser.data[parser.index]
			parser.index++
			switch escape {
			case '"', '\\', '/':
				output.WriteByte(escape)
			case 'b':
				output.WriteByte('\b')
			case 'f':
				output.WriteByte('\f')
			case 'n':
				output.WriteByte('\n')
			case 'r':
				output.WriteByte('\r')
			case 't':
				output.WriteByte('\t')
			case 'u':
				code, err := parser.hexCodeUnit()
				if err != nil {
					return legacyV0JSONString{}, err
				}
				if utf16.IsSurrogate(rune(code)) {
					if code >= 0xd800 && code <= 0xdbff && parser.index+6 <= len(parser.data) &&
						parser.data[parser.index] == '\\' && parser.data[parser.index+1] == 'u' {
						saved := parser.index
						parser.index += 2
						low, lowErr := parser.hexCodeUnit()
						if lowErr == nil && low >= 0xdc00 && low <= 0xdfff {
							output.WriteRune(utf16.DecodeRune(rune(code), rune(low)))
							continue
						}
						parser.index = saved
					}
					invalidUnicode = true
					output.WriteRune(utf8.RuneError)
				} else {
					output.WriteRune(rune(code))
				}
			default:
				return legacyV0JSONString{}, fmt.Errorf("invalid JSON escape")
			}
		default:
			if character < 0x20 {
				return legacyV0JSONString{}, fmt.Errorf("unescaped JSON control character")
			}
			output.WriteByte(character)
		}
	}
	return legacyV0JSONString{}, fmt.Errorf("unterminated JSON string")
}

func (parser *legacyV0JSONParser) hexCodeUnit() (uint16, error) {
	if parser.index+4 > len(parser.data) {
		return 0, fmt.Errorf("truncated JSON unicode escape")
	}
	value, err := strconv.ParseUint(string(parser.data[parser.index:parser.index+4]), 16, 16)
	if err != nil {
		return 0, fmt.Errorf("invalid JSON unicode escape")
	}
	parser.index += 4
	return uint16(value), nil
}

func (parser *legacyV0JSONParser) number() (any, error) {
	start := parser.index
	if parser.take('-') && parser.index >= len(parser.data) {
		return nil, fmt.Errorf("truncated JSON number")
	}
	if parser.take('0') {
		if parser.index < len(parser.data) && parser.data[parser.index] >= '0' && parser.data[parser.index] <= '9' {
			return nil, fmt.Errorf("leading zero in JSON number")
		}
	} else {
		if parser.index >= len(parser.data) || parser.data[parser.index] < '1' || parser.data[parser.index] > '9' {
			return nil, fmt.Errorf("invalid JSON number")
		}
		for parser.index < len(parser.data) && parser.data[parser.index] >= '0' && parser.data[parser.index] <= '9' {
			parser.index++
		}
	}
	floating := false
	if parser.take('.') {
		floating = true
		if parser.index >= len(parser.data) || parser.data[parser.index] < '0' || parser.data[parser.index] > '9' {
			return nil, fmt.Errorf("invalid JSON fraction")
		}
		for parser.index < len(parser.data) && parser.data[parser.index] >= '0' && parser.data[parser.index] <= '9' {
			parser.index++
		}
	}
	if parser.index < len(parser.data) && (parser.data[parser.index] == 'e' || parser.data[parser.index] == 'E') {
		floating = true
		parser.index++
		if parser.index < len(parser.data) && (parser.data[parser.index] == '+' || parser.data[parser.index] == '-') {
			parser.index++
		}
		if parser.index >= len(parser.data) || parser.data[parser.index] < '0' || parser.data[parser.index] > '9' {
			return nil, fmt.Errorf("invalid JSON exponent")
		}
		for parser.index < len(parser.data) && parser.data[parser.index] >= '0' && parser.data[parser.index] <= '9' {
			parser.index++
		}
	}
	raw := string(parser.data[start:parser.index])
	return legacyV0Number{raw: raw, floating: floating}, nil
}

func (parser *legacyV0JSONParser) literal(value string) bool {
	if !strings.HasPrefix(string(parser.data[parser.index:]), value) {
		return false
	}
	parser.index += len(value)
	return true
}

func (parser *legacyV0JSONParser) take(character byte) bool {
	if parser.index >= len(parser.data) || parser.data[parser.index] != character {
		return false
	}
	parser.index++
	return true
}

func (parser *legacyV0JSONParser) space() {
	for parser.index < len(parser.data) {
		switch parser.data[parser.index] {
		case ' ', '\t', '\n', '\r':
			parser.index++
		default:
			return
		}
	}
}
