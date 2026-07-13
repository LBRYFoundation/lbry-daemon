package wallet

import (
	"bytes"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"
)

func TestDecodeSupportValueUnsignedFieldsAndCanonicalization(t *testing.T) {
	payload := mustSupportValueHex(t, "00120368657918070a04f09f98800a00")
	decoded, err := DecodeSupportValue(payload)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.IsSigned() || decoded.Emoji != "" || decoded.Comment != "hey" ||
		decoded.SigningChannelHash != nil || decoded.Signature != nil {
		t.Fatalf("decoded support = %+v", decoded)
	}
	if got, want := hex.EncodeToString(decoded.Canonical), "0012036865791807"; got != want {
		t.Fatalf("canonical = %s, want %s", got, want)
	}
	if got, want := decoded.Value(), map[string]any{"comment": "hey"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("value = %#v, want %#v", got, want)
	}
	if decoded.SigningChannelID() != nil {
		t.Fatalf("unsigned channel ID = %v, want nil", decoded.SigningChannelID())
	}

	payload[1] = 0xff
	if got := hex.EncodeToString(decoded.Canonical); got != "0012036865791807" {
		t.Fatalf("canonical aliases input after mutation: %s", got)
	}
}

func TestDecodeSupportValueSignedWrapperAndChannelID(t *testing.T) {
	header := make([]byte, 20)
	for index := range header {
		header[index] = byte(index + 1)
	}
	signature := make([]byte, 64)
	for index := range signature {
		signature[index] = byte(index + 21)
	}
	payload := append([]byte{1}, header...)
	payload = append(payload, signature...)
	payload = append(payload, mustSupportValueHex(t, "1201780a0179")...)

	decoded, err := DecodeSupportValue(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.IsSigned() || decoded.Emoji != "y" || decoded.Comment != "x" ||
		!bytes.Equal(decoded.SigningChannelHash, header) || !bytes.Equal(decoded.Signature, signature) {
		t.Fatalf("decoded signed support = %+v", decoded)
	}
	channelID := decoded.SigningChannelID()
	if channelID == nil || *channelID != "14131211100f0e0d0c0b0a090807060504030201" {
		t.Fatalf("channel ID = %v", channelID)
	}
	wantCanonical := append([]byte{1}, header...)
	wantCanonical = append(wantCanonical, signature...)
	wantCanonical = append(wantCanonical, mustSupportValueHex(t, "0a0179120178")...)
	if !bytes.Equal(decoded.Canonical, wantCanonical) {
		t.Fatalf("canonical = %x, want %x", decoded.Canonical, wantCanonical)
	}

	payload[1], header[0], signature[0] = 0xff, 0xff, 0xff
	if decoded.SigningChannelHash[0] != 1 || decoded.Signature[0] != 21 || decoded.Canonical[1] != 1 {
		t.Fatal("decoded signed value aliases caller-owned slices")
	}
}

func TestDecodeSupportValueAcceptsShortSignedWrapper(t *testing.T) {
	cases := []struct {
		name      string
		payload   string
		hashBytes int
		sigBytes  int
		channelID *string
	}{
		{name: "empty", payload: "01", hashBytes: 0, sigBytes: 0},
		{name: "short hash", payload: "01010203", hashBytes: 3, sigBytes: 0, channelID: supportString("030201")},
		{name: "full hash", payload: "010102030405060708090a0b0c0d0e0f1011121314", hashBytes: 20, sigBytes: 0,
			channelID: supportString("14131211100f0e0d0c0b0a090807060504030201")},
		{name: "one signature byte", payload: "010102030405060708090a0b0c0d0e0f101112131415", hashBytes: 20, sigBytes: 1,
			channelID: supportString("14131211100f0e0d0c0b0a090807060504030201")},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			payload := mustSupportValueHex(t, test.payload)
			decoded, err := DecodeSupportValue(payload)
			if err != nil {
				t.Fatal(err)
			}
			if !decoded.IsSigned() || len(decoded.SigningChannelHash) != test.hashBytes ||
				len(decoded.Signature) != test.sigBytes || !reflect.DeepEqual(decoded.SigningChannelID(), test.channelID) ||
				!bytes.Equal(decoded.Canonical, payload) {
				t.Fatalf("decoded = %+v, channel ID %v", decoded, decoded.SigningChannelID())
			}
		})
	}
}

func TestDecodeSupportValuePreservesUnknownProtobufFields(t *testing.T) {
	cases := []struct {
		name, payload, emoji, comment, canonical string
	}{
		{name: "reverse known order", payload: "0012036865790a04f09f9880", emoji: string([]byte{0xf0, 0x9f, 0x98, 0x80}), comment: "hey", canonical: "000a04f09f98801203686579"},
		{name: "duplicate known", payload: "000a01610a0162", emoji: "b", canonical: "000a0162"},
		{name: "duplicate then empty", payload: "000a01610a00", canonical: "00"},
		{name: "unknown before", payload: "0018070a0161", emoji: "a", canonical: "000a01611807"},
		{name: "unknown order", payload: "0020051801", canonical: "0020051801"},
		{name: "duplicate unknown", payload: "0018011802", canonical: "0018011802"},
		{name: "wrong wire known", payload: "0008010a0161", emoji: "a", canonical: "000a01610801"},
		{name: "overlong known tag is unknown", payload: "008a000161", canonical: "008a000161"},
		{name: "overlong known length canonicalizes", payload: "000a810061", emoji: "a", canonical: "000a0161"},
		{name: "overlong unknown retained", payload: "0098008700", canonical: "0098008700"},
		{name: "nested unknown groups", payload: "001b231824241c", canonical: "001b231824241c"},
		{name: "mismatched group end", payload: "001b24", canonical: "001b24"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			decoded, err := DecodeSupportValue(mustSupportValueHex(t, test.payload))
			if err != nil {
				t.Fatal(err)
			}
			if decoded.Emoji != test.emoji || decoded.Comment != test.comment ||
				hex.EncodeToString(decoded.Canonical) != test.canonical {
				t.Fatalf("decoded = emoji %q, comment %q, canonical %x", decoded.Emoji, decoded.Comment, decoded.Canonical)
			}
		})
	}
}

func TestDecodeSupportValuePythonErrorBoundaries(t *testing.T) {
	cases := []struct {
		name, payload, errorName, message string
	}{
		{name: "empty wrapper", errorName: "IndexError", message: "index out of range"},
		{name: "unknown wrapper", payload: "02", errorName: "DecodeError", message: "Could not determine message format version."},
		{name: "truncated tag", payload: "0080", errorName: "DecodeError", message: "Truncated message."},
		{name: "field zero", payload: "000001", errorName: "DecodeError", message: "Field number 0 is illegal."},
		{name: "reserved wire", payload: "000e", errorName: "DecodeError", message: "Wrong wire type in tag."},
		{name: "unexpected end group", payload: "001c", errorName: "DecodeError", message: "Unexpected end-group tag."},
		{name: "open group", payload: "001b0801", errorName: "DecodeError", message: "Truncated message."},
		{name: "truncated known tag", payload: "000a", errorName: "DecodeError", message: "Truncated message."},
		{name: "truncated known string", payload: "000a02ff", errorName: "DecodeError", message: "Truncated string."},
		{name: "invalid emoji UTF-8", payload: "000a01ff", errorName: "UnicodeDecodeError",
			message: "'utf-8' codec can't decode byte 0xff in position 0: 'utf-8' codec can't decode byte 0xff in position 0: invalid start byte in field: pb.Support.emoji"},
		{name: "invalid comment UTF-8", payload: "001201ff", errorName: "UnicodeDecodeError",
			message: "'utf-8' codec can't decode byte 0xff in position 0: 'utf-8' codec can't decode byte 0xff in position 0: invalid start byte in field: pb.Support.comment"},
		{name: "truncated emoji UTF-8", payload: "000a02e282", errorName: "UnicodeDecodeError",
			message: "'utf-8' codec can't decode bytes in position 0-1: 'utf-8' codec can't decode bytes in position 0-1: unexpected end of data in field: pb.Support.emoji"},
		{name: "surrogate emoji UTF-8", payload: "000a03eda080", errorName: "UnicodeDecodeError",
			message: "'utf-8' codec can't decode byte 0xed in position 0: 'utf-8' codec can't decode byte 0xed in position 0: invalid continuation byte in field: pb.Support.emoji"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			decoded, err := DecodeSupportValue(mustSupportValueHex(t, test.payload))
			if decoded != nil || err == nil || !errors.Is(err, ErrInvalidSupportValue) {
				t.Fatalf("decode = %+v, %v", decoded, err)
			}
			var pythonError *SupportValueDecodeError
			if !errors.As(err, &pythonError) || pythonError.PythonErrorName() != test.errorName || err.Error() != test.message {
				t.Fatalf("error = %T %q/%q, want %q/%q", err, pythonError.PythonErrorName(), err, test.errorName, test.message)
			}
		})
	}
}

func TestSupportValueNilProjection(t *testing.T) {
	var value *SupportValue
	if value.IsSigned() || value.SigningChannelID() != nil || len(value.Value()) != 0 {
		t.Fatalf("nil projection = signed %v, channel %v, value %#v", value.IsSigned(), value.SigningChannelID(), value.Value())
	}
}

func mustSupportValueHex(t *testing.T, encoded string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func supportString(value string) *string { return &value }
