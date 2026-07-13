package reflector

import (
	"bufio"
	"strings"
	"testing"

	"lbry/daemon/blob"
)

func TestReflectorControlMessageResourcePolicy(t *testing.T) {
	var decoded map[string]any
	if err := readControlMessage(bufio.NewReader(strings.NewReader("{\"version\":1}")), &decoded); err != nil {
		t.Fatalf("valid control message rejected: %v", err)
	}
	concatenated := bufio.NewReader(strings.NewReader("{\"version\":1}{\"blob_size\":16}"))
	for _, field := range []string{"version", "blob_size"} {
		decoded = nil
		if err := readControlMessage(concatenated, &decoded); err != nil || decoded[field] == nil {
			t.Fatalf("concatenated Python frame %q = %#v, %v", field, decoded, err)
		}
	}
	oversized := strings.Repeat(" ", blob.MaxBlobResponseHeaderSize) + "{}\n"
	if err := readControlMessage(bufio.NewReader(strings.NewReader(oversized)), &decoded); err == nil {
		t.Fatal("oversized control message accepted")
	}
}

func TestReflectorRequiresExactBoundedIntegerSizes(t *testing.T) {
	for _, value := range []float64{1, blob.MaxBlobSize} {
		if got, ok := exactInteger(value, 1, blob.MaxBlobSize); !ok || got != int(value) {
			t.Fatalf("exactInteger(%v) = %d, %t", value, got, ok)
		}
	}
	for _, value := range []float64{-1, 0, 1.5, blob.MaxBlobSize + 1} {
		if _, ok := exactInteger(value, 1, blob.MaxBlobSize); ok {
			t.Fatalf("exactInteger accepted %v", value)
		}
	}
}
