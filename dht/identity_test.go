package dht

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestLoadOrCreateNodeIDPersistsAcrossRestarts(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "nested", "data")
	entropy := make([]byte, nodeIDEntropyBytes)
	for index := range entropy {
		entropy[index] = byte(index)
	}
	decodedWant, err := hex.DecodeString(
		"8b9123ed4bfab41f25b0659ff4107771f3ef68618fee588fca94a07754a4173f974ceff5dc0f6a7a156fd0b854b273e3",
	)
	if err != nil {
		t.Fatal(err)
	}
	var want [HashSize]byte
	copy(want[:], decodedWant)
	const wantEncoded = "67wYisr6djrmf9douMXZaFa2bCMyt5Qdrfr6AcMfMSAu4jKYviFshPWz7mvjcEyCNe"

	first, err := LoadOrCreateNodeIDWithReader(directory, bytes.NewReader(entropy))
	if err != nil {
		t.Fatal(err)
	}
	if first != want {
		t.Fatalf("generated node ID = %x, want %x", first, want)
	}

	second, err := LoadOrCreateNodeIDWithReader(directory, failingReader{errors.New("must not generate again")})
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("restarted node ID = %x, want %x", second, first)
	}

	contents, err := os.ReadFile(filepath.Join(directory, nodeIDFilename))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != wantEncoded {
		t.Fatalf("node_id contents = %q, want %q", contents, wantEncoded)
	}
	if strings.ContainsAny(string(contents), "\r\n") {
		t.Fatalf("node_id contains a newline: %q", contents)
	}
}

func TestLoadOrCreateNodeIDReadsLegacyWhitespaceWithoutRewriting(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, nodeIDFilename)
	var want [HashSize]byte
	for index := range want {
		want[index] = byte(index)
	}
	original := " \t" + encodeNodeIDBase58(want[:]) + "\r\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := LoadOrCreateNodeIDWithReader(directory, failingReader{errors.New("must not generate")})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("legacy node ID = %x, want %x", got, want)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != original {
		t.Fatalf("legacy node_id was rewritten: %q", contents)
	}
}

func TestLoadOrCreateNodeIDRejectsMalformedAndEmptyFiles(t *testing.T) {
	tests := []struct {
		name    string
		content string
		message string
	}{
		{name: "empty", content: "", message: "got 0 bytes, want 48"},
		{name: "whitespace", content: " \n\t", message: "got 0 bytes, want 48"},
		{name: "invalid character", content: "0", message: "invalid base58 character '0'"},
		{
			name:    "short",
			content: encodeNodeIDBase58(bytes.Repeat([]byte{0x42}, HashSize-1)),
			message: "got 47 bytes, want 48",
		},
		{
			name:    "long",
			content: encodeNodeIDBase58(bytes.Repeat([]byte{0x42}, HashSize+1)),
			message: "got 49 bytes, want 48",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, nodeIDFilename)
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadOrCreateNodeIDWithReader(directory, failingReader{errors.New("must not generate")})
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("error = %v, want containing %q", err, test.message)
			}
			contents, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(contents) != test.content {
				t.Fatalf("invalid node_id was overwritten: %q", contents)
			}
		})
	}
}

func TestLoadOrCreateNodeIDConcurrentCallersConverge(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "data")
	entropy := bytes.Repeat([]byte{0x7f}, nodeIDEntropyBytes)
	reader := bytes.NewReader(entropy)

	const callers = 64
	results := make(chan [HashSize]byte, callers)
	errorsChannel := make(chan error, callers)
	var start sync.WaitGroup
	start.Add(1)
	var calls sync.WaitGroup
	calls.Add(callers)
	for range callers {
		go func() {
			defer calls.Done()
			start.Wait()
			id, err := LoadOrCreateNodeIDWithReader(directory, reader)
			if err != nil {
				errorsChannel <- err
				return
			}
			results <- id
		}()
	}
	start.Done()
	calls.Wait()
	close(results)
	close(errorsChannel)

	for err := range errorsChannel {
		t.Errorf("concurrent call failed: %v", err)
	}
	var expected [HashSize]byte
	seen := false
	for id := range results {
		if !seen {
			expected = id
			seen = true
		}
		if id != expected {
			t.Errorf("callers returned different IDs: %x and %x", expected, id)
		}
	}
	if !seen {
		t.Fatal("no caller returned an ID")
	}
	contents, err := os.ReadFile(filepath.Join(directory, nodeIDFilename))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != encodeNodeIDBase58(expected[:]) {
		t.Fatalf("persisted ID %q differs from returned ID %x", contents, expected)
	}
	temporaryFiles, err := filepath.Glob(filepath.Join(directory, ".node_id-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temporaryFiles) != 0 {
		t.Fatalf("temporary files remain: %v", temporaryFiles)
	}
}

func TestLoadOrCreateNodeIDEntropyFailureLeavesNoFile(t *testing.T) {
	directory := t.TempDir()
	wantErr := errors.New("entropy failed")
	_, err := LoadOrCreateNodeIDWithReader(directory, failingReader{wantErr})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want wrapped %v", err, wantErr)
	}
	if _, err := os.Stat(filepath.Join(directory, nodeIDFilename)); !os.IsNotExist(err) {
		t.Fatalf("node_id exists after entropy failure: %v", err)
	}
}

func TestLoadOrCreateNodeIDFallsBackWhenHardLinksAreUnavailable(t *testing.T) {
	directory := t.TempDir()
	entropy := bytes.Repeat([]byte{0x35}, nodeIDEntropyBytes)
	wantLinkErr := errors.New("hard links unavailable")
	id, err := loadOrCreateNodeID(
		directory,
		bytes.NewReader(entropy),
		func(string, string) error { return wantLinkErr },
	)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(directory, nodeIDFilename))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != encodeNodeIDBase58(id[:]) {
		t.Fatalf("fallback node_id = %q, want %q", contents, encodeNodeIDBase58(id[:]))
	}
	reloaded, err := LoadOrCreateNodeIDWithReader(directory, failingReader{errors.New("must not regenerate")})
	if err != nil || reloaded != id {
		t.Fatalf("reloaded fallback ID = %x, %v; want %x", reloaded, err, id)
	}
}

func TestLoadOrCreateNodeIDRejectsNilEntropy(t *testing.T) {
	_, err := LoadOrCreateNodeIDWithReader(t.TempDir(), nil)
	if err == nil || err.Error() != "DHT node ID entropy source is nil" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewNodeWithIDUsesPersistentIdentity(t *testing.T) {
	var id [HashSize]byte
	for index := range id {
		id[index] = byte(index + 1)
	}
	node, err := NewNodeWithID(4444, id)
	if err != nil {
		t.Fatal(err)
	}
	if node.ID != id {
		t.Fatalf("node ID = %x, want %x", node.ID, id)
	}
	if node.routing.selfID != id {
		t.Fatalf("routing table ID = %x, want %x", node.routing.selfID, id)
	}
	if !reflect.DeepEqual(node.BootstrapNodes, SeedNodes) {
		t.Fatalf("bootstrap nodes = %#v, want %#v", node.BootstrapNodes, SeedNodes)
	}
	node.BootstrapNodes[0] = "changed:1"
	if SeedNodes[0] == "changed:1" {
		t.Fatal("node bootstrap list aliases package defaults")
	}
}

type failingReader struct {
	err error
}

func (reader failingReader) Read([]byte) (int, error) {
	return 0, reader.err
}

var _ io.Reader = failingReader{}
