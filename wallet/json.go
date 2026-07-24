package wallet

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

var ErrUnorderedObject = errors.New("unordered Go map cannot preserve Python JSON insertion order")
var ErrCircularReference = errors.New("Circular reference detected")

func decodeOrderedJSON(data []byte) (any, error) {
	parser := orderedJSONParser{data: data}
	value, err := parser.parseValue()
	if err != nil {
		return nil, err
	}
	parser.skipWhitespace()
	if parser.offset != len(parser.data) {
		return nil, fmt.Errorf("unexpected data after JSON value at byte %d", parser.offset)
	}
	return value, nil
}

type orderedJSONParser struct {
	data   []byte
	offset int
}

func (parser *orderedJSONParser) parseValue() (any, error) {
	parser.skipWhitespace()
	if parser.offset >= len(parser.data) {
		return nil, errors.New("unexpected end of JSON input")
	}
	switch parser.data[parser.offset] {
	case '{':
		return parser.parseObject()
	case '[':
		return parser.parseArray()
	case '"':
		return parser.parseString()
	case 't':
		return parser.parseLiteral("true", true)
	case 'f':
		return parser.parseLiteral("false", false)
	case 'n':
		return parser.parseLiteral("null", nil)
	case 'N':
		return parser.parseLiteral("NaN", math.NaN())
	case 'I':
		return parser.parseLiteral("Infinity", math.Inf(1))
	case '-':
		if bytes.HasPrefix(parser.data[parser.offset:], []byte("-Infinity")) {
			return parser.parseLiteral("-Infinity", math.Inf(-1))
		}
		return parser.parseNumber()
	default:
		if isJSONDigit(parser.data[parser.offset]) {
			return parser.parseNumber()
		}
		return nil, fmt.Errorf("unexpected character %q at byte %d", parser.data[parser.offset], parser.offset)
	}
}

func (parser *orderedJSONParser) parseObject() (*Object, error) {
	parser.offset++
	object := NewObject()
	parser.skipWhitespace()
	if parser.consumeByte('}') {
		return object, nil
	}
	for {
		parser.skipWhitespace()
		if parser.offset >= len(parser.data) || parser.data[parser.offset] != '"' {
			return nil, fmt.Errorf("expected JSON object key at byte %d", parser.offset)
		}
		key, err := parser.parseString()
		if err != nil {
			return nil, err
		}
		parser.skipWhitespace()
		if !parser.consumeByte(':') {
			return nil, fmt.Errorf("expected colon after JSON object key at byte %d", parser.offset)
		}
		value, err := parser.parseValue()
		if err != nil {
			return nil, err
		}
		object.Set(key, value)
		parser.skipWhitespace()
		if parser.consumeByte('}') {
			return object, nil
		}
		if !parser.consumeByte(',') {
			return nil, fmt.Errorf("expected comma or object end at byte %d", parser.offset)
		}
	}
}

func (parser *orderedJSONParser) parseArray() ([]any, error) {
	parser.offset++
	values := make([]any, 0)
	parser.skipWhitespace()
	if parser.consumeByte(']') {
		return values, nil
	}
	for {
		value, err := parser.parseValue()
		if err != nil {
			return nil, err
		}
		values = append(values, value)
		parser.skipWhitespace()
		if parser.consumeByte(']') {
			return values, nil
		}
		if !parser.consumeByte(',') {
			return nil, fmt.Errorf("expected comma or array end at byte %d", parser.offset)
		}
	}
}

func (parser *orderedJSONParser) parseString() (string, error) {
	start := parser.offset
	parser.offset++
	escaped := false
	for parser.offset < len(parser.data) {
		character := parser.data[parser.offset]
		parser.offset++
		if escaped {
			escaped = false
			continue
		}
		if character == '\\' {
			escaped = true
			continue
		}
		if character == '"' {
			var value string
			if err := json.Unmarshal(parser.data[start:parser.offset], &value); err != nil {
				return "", err
			}
			return value, nil
		}
	}
	return "", errors.New("unterminated JSON string")
}

func (parser *orderedJSONParser) parseNumber() (json.Number, error) {
	start := parser.offset
	if parser.consumeByte('-') && parser.offset >= len(parser.data) {
		return "", errors.New("incomplete JSON number")
	}
	if parser.consumeByte('0') {
		// A following digit remains as trailing invalid input, matching JSON's
		// rejection of leading zeroes.
	} else {
		if parser.offset >= len(parser.data) || parser.data[parser.offset] < '1' || parser.data[parser.offset] > '9' {
			return "", fmt.Errorf("invalid JSON number at byte %d", start)
		}
		for parser.offset < len(parser.data) && isJSONDigit(parser.data[parser.offset]) {
			parser.offset++
		}
	}
	if parser.consumeByte('.') {
		fractionStart := parser.offset
		for parser.offset < len(parser.data) && isJSONDigit(parser.data[parser.offset]) {
			parser.offset++
		}
		if parser.offset == fractionStart {
			return "", fmt.Errorf("invalid JSON fraction at byte %d", start)
		}
	}
	if parser.offset < len(parser.data) && (parser.data[parser.offset] == 'e' || parser.data[parser.offset] == 'E') {
		parser.offset++
		if parser.offset < len(parser.data) && (parser.data[parser.offset] == '+' || parser.data[parser.offset] == '-') {
			parser.offset++
		}
		exponentStart := parser.offset
		for parser.offset < len(parser.data) && isJSONDigit(parser.data[parser.offset]) {
			parser.offset++
		}
		if parser.offset == exponentStart {
			return "", fmt.Errorf("invalid JSON exponent at byte %d", start)
		}
	}
	return json.Number(string(parser.data[start:parser.offset])), nil
}

func (parser *orderedJSONParser) parseLiteral(literal string, value any) (any, error) {
	if !bytes.HasPrefix(parser.data[parser.offset:], []byte(literal)) {
		return nil, fmt.Errorf("invalid JSON literal at byte %d", parser.offset)
	}
	parser.offset += len(literal)
	return value, nil
}

func (parser *orderedJSONParser) skipWhitespace() {
	for parser.offset < len(parser.data) {
		switch parser.data[parser.offset] {
		case ' ', '\t', '\r', '\n':
			parser.offset++
		default:
			return
		}
	}
}

func (parser *orderedJSONParser) consumeByte(wanted byte) bool {
	if parser.offset < len(parser.data) && parser.data[parser.offset] == wanted {
		parser.offset++
		return true
	}
	return false
}

func isJSONDigit(character byte) bool {
	return character >= '0' && character <= '9'
}

func encodeWalletJSON(value any) ([]byte, error) {
	encoder := pythonJSONEncoder{pretty: true, sortKeys: true}
	if err := encoder.writeValue(value, 0); err != nil {
		return nil, err
	}
	return encoder.buffer.Bytes(), nil
}

func encodePreferenceJSON(value any) ([]byte, error) {
	encoder := pythonJSONEncoder{}
	if err := encoder.writeValue(value, 0); err != nil {
		return nil, err
	}
	return encoder.buffer.Bytes(), nil
}

type pythonJSONEncoder struct {
	buffer   bytes.Buffer
	pretty   bool
	sortKeys bool
	active   map[jsonVisit]struct{}
}

type jsonVisit struct {
	kind    reflect.Kind
	typeOf  reflect.Type
	pointer uintptr
}

func (encoder *pythonJSONEncoder) writeValue(value any, depth int) error {
	if value == nil {
		encoder.buffer.WriteString("null")
		return nil
	}
	switch typed := value.(type) {
	case *Object:
		if typed == nil {
			encoder.buffer.WriteString("{}")
			return nil
		}
		return encoder.withVisit(reflect.ValueOf(typed), func() error {
			return encoder.writeObject(typed.Members(), depth)
		})
	case Object:
		return encoder.writeObject(typed.Members(), depth)
	case string:
		return encoder.writeString(typed)
	case bool:
		encoder.buffer.WriteString(strconv.FormatBool(typed))
		return nil
	case json.Number:
		return encoder.writeNumber(string(typed))
	case *big.Int:
		if typed == nil {
			encoder.buffer.WriteString("null")
		} else {
			encoder.buffer.WriteString(typed.String())
		}
		return nil
	case big.Int:
		encoder.buffer.WriteString(typed.String())
		return nil
	case float64:
		encoder.buffer.WriteString(formatPythonFloat(typed))
		return nil
	case float32:
		encoder.buffer.WriteString(formatPythonFloat(float64(typed)))
		return nil
	case []byte:
		return fmt.Errorf("Object of type bytes is not JSON serializable")
	}

	reflected := reflect.ValueOf(value)
	if reflected.Kind() == reflect.Pointer {
		if reflected.IsNil() {
			encoder.buffer.WriteString("null")
			return nil
		}
		return encoder.writeValue(reflected.Elem().Interface(), depth)
	}
	switch reflected.Kind() {
	case reflect.String:
		return encoder.writeString(reflected.String())
	case reflect.Bool:
		encoder.buffer.WriteString(strconv.FormatBool(reflected.Bool()))
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		encoder.buffer.WriteString(strconv.FormatInt(reflected.Int(), 10))
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		encoder.buffer.WriteString(strconv.FormatUint(reflected.Uint(), 10))
		return nil
	case reflect.Float32, reflect.Float64:
		encoder.buffer.WriteString(formatPythonFloat(reflected.Float()))
		return nil
	case reflect.Slice, reflect.Array:
		return encoder.writeArray(reflected, depth)
	case reflect.Map:
		return encoder.writeMap(reflected, depth)
	default:
		return fmt.Errorf("Object of type %s is not JSON serializable", reflected.Type())
	}
}

func (encoder *pythonJSONEncoder) writeObject(members []Member, depth int) error {
	if encoder.sortKeys {
		members = append([]Member(nil), members...)
		sort.Slice(members, func(left, right int) bool { return members[left].Key < members[right].Key })
	}
	encoder.buffer.WriteByte('{')
	if len(members) == 0 {
		encoder.buffer.WriteByte('}')
		return nil
	}
	for index, member := range members {
		encoder.writeItemPrefix(index, depth+1)
		if err := encoder.writeString(member.Key); err != nil {
			return err
		}
		encoder.buffer.WriteString(": ")
		if err := encoder.writeValue(member.Value, depth+1); err != nil {
			return err
		}
	}
	encoder.writeCollectionSuffix(depth)
	encoder.buffer.WriteByte('}')
	return nil
}

func (encoder *pythonJSONEncoder) writeMap(value reflect.Value, depth int) error {
	if value.Type().Key().Kind() != reflect.String {
		return fmt.Errorf("JSON object key type is %s, want string", value.Type().Key())
	}
	if !encoder.sortKeys && value.Len() > 1 {
		return ErrUnorderedObject
	}
	return encoder.withVisit(value, func() error {
		members := make([]Member, 0, value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			members = append(members, Member{Key: iterator.Key().String(), Value: iterator.Value().Interface()})
		}
		return encoder.writeObject(members, depth)
	})
}

func (encoder *pythonJSONEncoder) writeArray(value reflect.Value, depth int) error {
	if value.Kind() == reflect.Slice {
		return encoder.withVisit(value, func() error {
			return encoder.writeArrayContents(value, depth)
		})
	}
	return encoder.writeArrayContents(value, depth)
}

func (encoder *pythonJSONEncoder) writeArrayContents(value reflect.Value, depth int) error {
	encoder.buffer.WriteByte('[')
	if value.Len() == 0 {
		encoder.buffer.WriteByte(']')
		return nil
	}
	for index := 0; index < value.Len(); index++ {
		encoder.writeItemPrefix(index, depth+1)
		if err := encoder.writeValue(value.Index(index).Interface(), depth+1); err != nil {
			return err
		}
	}
	encoder.writeCollectionSuffix(depth)
	encoder.buffer.WriteByte(']')
	return nil
}

func (encoder *pythonJSONEncoder) withVisit(value reflect.Value, encode func() error) error {
	visit := jsonVisit{kind: value.Kind(), typeOf: value.Type(), pointer: value.Pointer()}
	if encoder.active == nil {
		encoder.active = make(map[jsonVisit]struct{})
	}
	if _, exists := encoder.active[visit]; exists {
		return ErrCircularReference
	}
	encoder.active[visit] = struct{}{}
	defer delete(encoder.active, visit)
	return encode()
}

func (encoder *pythonJSONEncoder) writeItemPrefix(index, depth int) {
	if index > 0 {
		encoder.buffer.WriteByte(',')
	}
	if encoder.pretty {
		encoder.buffer.WriteByte('\n')
		encoder.buffer.WriteString(strings.Repeat("    ", depth))
	} else if index > 0 {
		encoder.buffer.WriteByte(' ')
	}
}

func (encoder *pythonJSONEncoder) writeCollectionSuffix(depth int) {
	if encoder.pretty {
		encoder.buffer.WriteByte('\n')
		encoder.buffer.WriteString(strings.Repeat("    ", depth))
	}
}

func (encoder *pythonJSONEncoder) writeString(value string) error {
	var encoded bytes.Buffer
	jsonEncoder := json.NewEncoder(&encoded)
	jsonEncoder.SetEscapeHTML(false)
	if err := jsonEncoder.Encode(value); err != nil {
		return err
	}
	data := bytes.TrimSuffix(encoded.Bytes(), []byte{'\n'})
	encoder.buffer.Write(escapeNonASCII(data))
	return nil
}

func (encoder *pythonJSONEncoder) writeNumber(value string) error {
	if strings.ContainsAny(value, ".eE") {
		number, err := strconv.ParseFloat(value, 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return fmt.Errorf("invalid JSON number %q: %w", value, err)
		}
		encoder.buffer.WriteString(formatPythonFloat(number))
		return nil
	}
	integer, ok := new(big.Int).SetString(value, 10)
	if !ok {
		return fmt.Errorf("invalid JSON integer %q", value)
	}
	encoder.buffer.WriteString(integer.String())
	return nil
}

func formatPythonFloat(value float64) string {
	switch {
	case math.IsNaN(value):
		return "NaN"
	case math.IsInf(value, 1):
		return "Infinity"
	case math.IsInf(value, -1):
		return "-Infinity"
	}
	formatted := strconv.FormatFloat(value, 'g', -1, 64)
	if exponentIndex := strings.IndexByte(formatted, 'e'); exponentIndex >= 0 {
		exponent, _ := strconv.Atoi(formatted[exponentIndex+1:])
		if exponent >= -4 && exponent < 16 {
			formatted = strconv.FormatFloat(value, 'f', -1, 64)
			if !strings.ContainsRune(formatted, '.') {
				formatted += ".0"
			}
			return formatted
		}
		mantissa := formatted[:exponentIndex]
		return fmt.Sprintf("%se%+03d", mantissa, exponent)
	}
	if !strings.ContainsRune(formatted, '.') {
		formatted += ".0"
	}
	return formatted
}

func escapeNonASCII(data []byte) []byte {
	var escaped strings.Builder
	for len(data) > 0 {
		runeValue, size := utf8.DecodeRune(data)
		data = data[size:]
		if runeValue <= 0x7e {
			escaped.WriteRune(runeValue)
			continue
		}
		if runeValue <= 0xffff {
			_, _ = fmt.Fprintf(&escaped, "\\u%04x", runeValue)
			continue
		}
		high, low := utf16.EncodeRune(runeValue)
		_, _ = fmt.Fprintf(&escaped, "\\u%04x\\u%04x", high, low)
	}
	return []byte(escaped.String())
}
