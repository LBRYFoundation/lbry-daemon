package wallet

import (
	"encoding/hex"
	"errors"
	"fmt"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protowire"
)

var ErrInvalidSupportValue = errors.New("invalid support value")

// SupportValueDecodeError preserves the Python exception boundary used by
// JSONResponseEncoder.encode_output. DecodeError is omitted from an output;
// IndexError and UnicodeDecodeError abort encoding.
type SupportValueDecodeError struct {
	Name    string
	Message string
}

func (err *SupportValueDecodeError) Error() string {
	if err == nil {
		return ""
	}
	return err.Message
}

func (err *SupportValueDecodeError) PythonErrorName() string {
	if err == nil {
		return ""
	}
	return err.Name
}

func (err *SupportValueDecodeError) Unwrap() error { return ErrInvalidSupportValue }

// SupportValue is the v2 Support Signable decoded by the legacy Python SDK.
// Canonical contains Support.to_bytes(), including its unsigned or signed
// wrapper, and is the byte sequence exposed by include_protobuf.
type SupportValue struct {
	Emoji              string
	Comment            string
	SigningChannelHash []byte
	Signature          []byte
	Canonical          []byte

	signed bool
}

func (value *SupportValue) IsSigned() bool {
	return value != nil && value.signed
}

// SigningChannelID mirrors Signable.signing_channel_id. A signed wrapper with
// an empty channel hash still reports nil.
func (value *SupportValue) SigningChannelID() *string {
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

// Value returns the MessageToDict projection used by the Python JSON encoder.
// Proto3 default strings are absent rather than present as empty strings.
func (value *SupportValue) Value() map[string]any {
	result := make(map[string]any, 2)
	if value == nil {
		return result
	}
	if value.Emoji != "" {
		result["emoji"] = value.Emoji
	}
	if value.Comment != "" {
		result["comment"] = value.Comment
	}
	return result
}

// DecodeSupportValue implements Support.from_bytes for the v2 Support schema.
// The generated Python protobuf decoder recognizes only the canonical one-byte
// tags for emoji and comment. Denormalized tags and wrong wire types remain
// unknown fields and are preserved verbatim by SerializeToString.
func DecodeSupportValue(payload []byte) (*SupportValue, error) {
	if len(payload) == 0 {
		return nil, supportValueError("IndexError", "index out of range")
	}

	value := &SupportValue{}
	var message []byte
	switch payload[0] {
	case 0:
		message = payload[1:]
	case 1:
		value.signed = true
		hashEnd := min(len(payload), 21)
		value.SigningChannelHash = cloneSupportBytes(payload[1:hashEnd])
		if len(payload) > 21 {
			signatureEnd := min(len(payload), 85)
			value.Signature = cloneSupportBytes(payload[21:signatureEnd])
		} else {
			value.Signature = []byte{}
		}
		if len(payload) > 85 {
			message = payload[85:]
		}
	default:
		return nil, supportValueError("DecodeError", "Could not determine message format version.")
	}

	emoji, comment, canonicalMessage, err := decodeSupportMessage(message)
	if err != nil {
		return nil, err
	}
	value.Emoji, value.Comment = emoji, comment

	canonicalCapacity := 1 + len(canonicalMessage)
	if value.signed {
		canonicalCapacity += len(value.SigningChannelHash) + len(value.Signature)
	}
	value.Canonical = make([]byte, 0, canonicalCapacity)
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

func decodeSupportMessage(message []byte) (emoji, comment string, canonical []byte, err error) {
	unknown := make([]byte, 0, len(message))
	for position := 0; position != len(message); {
		fieldStart := position
		tag, valueStart, err := readPythonSupportTag(message, position)
		if err != nil {
			return "", "", nil, err
		}
		// Generated Python decoders dispatch through the encoded tag bytes. An
		// overlong representation of 0x0a or 0x12 is therefore unknown.
		knownString := len(tag) == 1 && (tag[0] == 0x0a || tag[0] == 0x12)
		if knownString {
			size, stringStart, err := decodePythonSupportVarint(message, valueStart)
			if err != nil {
				return "", "", nil, err
			}
			if size > uint64(len(message)-stringStart) {
				return "", "", nil, supportValueError("DecodeError", "Truncated string.")
			}
			stringEnd := stringStart + int(size)
			encoded := message[stringStart:stringEnd]
			fieldName := "emoji"
			if tag[0] == 0x12 {
				fieldName = "comment"
			}
			if !utf8.Valid(encoded) {
				return "", "", nil, supportUTF8Error(encoded, fieldName)
			}
			if fieldName == "emoji" {
				emoji = string(encoded)
			} else {
				comment = string(encoded)
			}
			position = stringEnd
			continue
		}

		tagValue, _, err := decodePythonSupportVarint(tag, 0)
		if err != nil {
			return "", "", nil, err
		}
		if tagValue>>3 == 0 {
			return "", "", nil, supportValueError("DecodeError", "Field number 0 is illegal.")
		}
		wireType := byte(tagValue & 7)
		decodedEnd, err := decodePythonSupportUnknownField(message, valueStart, wireType)
		if err != nil {
			return "", "", nil, err
		}
		if decodedEnd < 0 {
			return "", "", nil, supportValueError("DecodeError", "Unexpected end-group tag.")
		}
		fieldEnd, err := skipPythonSupportField(message, valueStart, len(message), tag)
		if err != nil {
			return "", "", nil, err
		}
		if fieldEnd < 0 {
			return "", "", nil, supportValueError("DecodeError", "Unexpected end-group tag.")
		}
		unknown = append(unknown, message[fieldStart:fieldEnd]...)
		position = fieldEnd
	}

	if emoji != "" {
		canonical = protowire.AppendTag(canonical, 1, protowire.BytesType)
		canonical = protowire.AppendString(canonical, emoji)
	}
	if comment != "" {
		canonical = protowire.AppendTag(canonical, 2, protowire.BytesType)
		canonical = protowire.AppendString(canonical, comment)
	}
	canonical = append(canonical, unknown...)
	return emoji, comment, canonical, nil
}

func readPythonSupportTag(message []byte, position int) ([]byte, int, error) {
	start := position
	for {
		if position >= len(message) {
			return nil, 0, supportValueError("DecodeError", "Truncated message.")
		}
		current := message[position]
		position++
		if current&0x80 == 0 {
			return message[start:position], position, nil
		}
	}
}

func decodePythonSupportVarint(message []byte, position int) (uint64, int, error) {
	var result uint64
	for shift := uint(0); ; shift += 7 {
		if position >= len(message) {
			return 0, 0, supportValueError("DecodeError", "Truncated message.")
		}
		current := message[position]
		position++
		result |= uint64(current&0x7f) << shift
		if current&0x80 == 0 {
			return result, position, nil
		}
		if shift+7 >= 64 {
			return 0, 0, supportValueError("DecodeError", "Too many bytes when decoding varint.")
		}
	}
}

// Python first decodes an unknown value into UnknownFieldSet and then walks it
// again to retain the original bytes. The first pass is intentionally loose
// about length-delimited bounds and group numbers, while fixed-width decoding
// exposes struct.error messages.
func decodePythonSupportUnknownField(message []byte, position int, wireType byte) (int, error) {
	switch wireType {
	case 0:
		_, end, err := decodePythonSupportVarint(message, position)
		return end, err
	case 1:
		if len(message)-position < 8 {
			return 0, supportValueError("DecodeError", "unpack requires a buffer of 8 bytes")
		}
		return position + 8, nil
	case 2:
		size, valueStart, err := decodePythonSupportVarint(message, position)
		if err != nil {
			return 0, err
		}
		return advancePythonSupportPosition(valueStart, size, len(message)), nil
	case 3:
		return decodePythonSupportUnknownSet(message, position)
	case 4:
		return -1, nil
	case 5:
		if len(message)-position < 4 {
			return 0, supportValueError("DecodeError", "unpack requires a buffer of 4 bytes")
		}
		return position + 4, nil
	default:
		return 0, supportValueError("DecodeError", "Wrong wire type in tag.")
	}
}

func decodePythonSupportUnknownSet(message []byte, position int) (int, error) {
	for {
		tag, valueStart, err := readPythonSupportTag(message, position)
		if err != nil {
			return 0, err
		}
		tagValue, _, err := decodePythonSupportVarint(tag, 0)
		if err != nil {
			return 0, err
		}
		wireType := byte(tagValue & 7)
		if wireType == 4 {
			return valueStart, nil
		}
		position, err = decodePythonSupportUnknownField(message, valueStart, wireType)
		if err != nil {
			return 0, err
		}
	}
}

func skipPythonSupportField(message []byte, position, end int, tag []byte) (int, error) {
	wireType := tag[0] & 7
	switch wireType {
	case 0:
		for {
			if position >= len(message) {
				return 0, supportValueError("DecodeError", "Truncated message.")
			}
			current := message[position]
			position++
			if current&0x80 == 0 {
				if position > end {
					return 0, supportValueError("DecodeError", "Truncated message.")
				}
				return position, nil
			}
		}
	case 1:
		position += 8
		if position > end {
			return 0, supportValueError("DecodeError", "Truncated message.")
		}
		return position, nil
	case 2:
		size, valueStart, err := decodePythonSupportVarint(message, position)
		if err != nil {
			return 0, err
		}
		position = advancePythonSupportPosition(valueStart, size, len(message))
		if position > end {
			return 0, supportValueError("DecodeError", "Truncated message.")
		}
		return position, nil
	case 3:
		for {
			nestedTag, valueStart, err := readPythonSupportTag(message, position)
			if err != nil {
				return 0, err
			}
			position, err = skipPythonSupportField(message, valueStart, end, nestedTag)
			if err != nil {
				return 0, err
			}
			if position < 0 {
				return valueStart, nil
			}
		}
	case 4:
		return -1, nil
	case 5:
		position += 4
		if position > end {
			return 0, supportValueError("DecodeError", "Truncated message.")
		}
		return position, nil
	default:
		return 0, supportValueError("DecodeError", "Tag had invalid wire type.")
	}
}

func advancePythonSupportPosition(position int, size uint64, messageLength int) int {
	if size > uint64(messageLength) || uint64(position) > uint64(messageLength)-size {
		return messageLength + 1
	}
	return position + int(size)
}

func supportUTF8Error(encoded []byte, fieldName string) error {
	start, end, reason := invalidSupportUTF8(encoded)
	var location string
	if end == start+1 {
		location = fmt.Sprintf("byte 0x%02x in position %d", encoded[start], start)
	} else {
		location = fmt.Sprintf("bytes in position %d-%d", start, end-1)
	}
	prefix := "'utf-8' codec can't decode " + location + ": "
	return supportValueError(
		"UnicodeDecodeError", prefix+prefix+reason+" in field: pb.Support."+fieldName,
	)
}

func invalidSupportUTF8(value []byte) (start, end int, reason string) {
	for offset := 0; offset < len(value); {
		decoded, size := utf8.DecodeRune(value[offset:])
		if decoded != utf8.RuneError || size != 1 {
			offset += size
			continue
		}
		lead := value[offset]
		if lead < 0xc2 || lead > 0xf4 {
			return offset, offset + 1, "invalid start byte"
		}
		required := 2
		if lead >= 0xe0 {
			required = 3
		}
		if lead >= 0xf0 {
			required = 4
		}
		for continuation := 1; continuation < required; continuation++ {
			position := offset + continuation
			if position >= len(value) {
				return offset, len(value), "unexpected end of data"
			}
			current := value[position]
			if current < 0x80 || current > 0xbf {
				return offset, position, "invalid continuation byte"
			}
			if continuation == 1 && ((lead == 0xe0 && current < 0xa0) ||
				(lead == 0xed && current > 0x9f) ||
				(lead == 0xf0 && current < 0x90) ||
				(lead == 0xf4 && current > 0x8f)) {
				return offset, offset + 1, "invalid continuation byte"
			}
		}
		return offset, offset + 1, "invalid continuation byte"
	}
	return 0, 1, "invalid start byte"
}

func supportValueError(name, message string) error {
	return &SupportValueDecodeError{Name: name, Message: message}
}

func cloneSupportBytes(source []byte) []byte {
	if source == nil {
		return nil
	}
	cloned := make([]byte, len(source))
	copy(cloned, source)
	return cloned
}
