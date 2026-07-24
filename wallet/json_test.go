package wallet

import (
	"errors"
	"math"
	"testing"
)

func TestWalletJSONEncodingMatchesPythonFormatting(t *testing.T) {
	value := NewObject(
		Member{Key: "z", Value: "é😀<>&"},
		Member{Key: "a", Value: []any{1, 2.0}},
	)
	encoded, err := encodeWalletJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n" +
		"    \"a\": [\n" +
		"        1,\n" +
		"        2.0\n" +
		"    ],\n" +
		"    \"z\": \"\\u00e9\\ud83d\\ude00<>&\"\n" +
		"}"
	if string(encoded) != want {
		t.Fatalf("encoded wallet JSON differs\nGot:  %q\nWant: %q", encoded, want)
	}
}

func TestWalletJSONEscapesPythonASCIIControlBoundary(t *testing.T) {
	encoded, err := encodePreferenceJSON("\x00\x1f\x7f\u0080")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(encoded), `"\u0000\u001f\u007f\u0080"`; got != want {
		t.Fatalf("encoded controls = %q, want %q", got, want)
	}
}

func TestPythonFloatFormatting(t *testing.T) {
	values := []any{
		0.0, math.Copysign(0, -1), 2.0, 1e-5, 1e-4, 1e15, 1e16, 1e20,
		math.NaN(), math.Inf(1), math.Inf(-1),
	}
	encoded, err := encodePreferenceJSON(values)
	if err != nil {
		t.Fatal(err)
	}
	want := "[0.0, -0.0, 2.0, 1e-05, 0.0001, 1000000000000000.0, 1e+16, 1e+20, NaN, Infinity, -Infinity]"
	if string(encoded) != want {
		t.Fatalf("floats = %s, want %s", encoded, want)
	}
}

func TestPreferenceEncodingRejectsAmbiguousUnorderedMaps(t *testing.T) {
	_, err := encodePreferenceJSON(NewObject(Member{Key: "value", Value: map[string]any{
		"first": 1, "second": 2,
	}}))
	if !errors.Is(err, ErrUnorderedObject) {
		t.Fatalf("error = %v, want ErrUnorderedObject", err)
	}
}

func TestOrderedJSONAcceptsPythonSpecialFloats(t *testing.T) {
	decoded, err := decodeOrderedJSON([]byte(`{"nan":NaN,"positive":Infinity,"negative":-Infinity}`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := encodeWalletJSON(decoded)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n" +
		"    \"nan\": NaN,\n" +
		"    \"negative\": -Infinity,\n" +
		"    \"positive\": Infinity\n" +
		"}"
	if string(encoded) != want {
		t.Fatalf("special float round trip = %q, want %q", encoded, want)
	}
}

func TestWalletJSONReportsCircularReferences(t *testing.T) {
	object := NewObject()
	object.Set("self", object)
	if _, err := encodeWalletJSON(object); !errors.Is(err, ErrCircularReference) {
		t.Fatalf("object cycle error = %v, want ErrCircularReference", err)
	}

	mapping := map[string]any{}
	mapping["self"] = mapping
	if _, err := encodeWalletJSON(mapping); !errors.Is(err, ErrCircularReference) {
		t.Fatalf("map cycle error = %v, want ErrCircularReference", err)
	}

	list := make([]any, 1)
	list[0] = list
	if _, err := encodeWalletJSON(list); !errors.Is(err, ErrCircularReference) {
		t.Fatalf("slice cycle error = %v, want ErrCircularReference", err)
	}
}
