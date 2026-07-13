package config

import (
	"bytes"
	"errors"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLoadOrCreateInstallationIDPreservesExistingValue(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, installationIDFilename)
	original := "  existing-installation-id\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	id, err := LoadOrCreateInstallationIDWithReader(directory, errReader{errors.New("must not read")})
	if err != nil {
		t.Fatal(err)
	}
	if id != "existing-installation-id" {
		t.Fatalf("unexpected ID: %q", id)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != original {
		t.Fatalf("existing file was rewritten: %q", contents)
	}
}

func TestLoadOrCreateInstallationIDCreatesBase58File(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "nested", "data")
	randomBytes := make([]byte, installationIDBytes)
	for index := range randomBytes {
		randomBytes[index] = byte(index)
	}

	id, err := LoadOrCreateInstallationIDWithReader(directory, bytes.NewReader(randomBytes))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeBase58(id)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, randomBytes) {
		t.Fatalf("decoded ID differs:\n got %x\nwant %x", decoded, randomBytes)
	}

	contents, err := os.ReadFile(filepath.Join(directory, installationIDFilename))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != id {
		t.Fatalf("file contents %q differ from returned ID %q", contents, id)
	}
	if strings.ContainsAny(string(contents), "\r\n") {
		t.Fatalf("installation ID contains a newline: %q", contents)
	}
}

func TestLoadOrCreateInstallationIDRepairsEmptyFile(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, installationIDFilename)
	if err := os.WriteFile(path, []byte(" \n\t"), 0o600); err != nil {
		t.Fatal(err)
	}
	randomBytes := bytes.Repeat([]byte{0x42}, installationIDBytes)

	id, err := LoadOrCreateInstallationIDWithReader(directory, bytes.NewReader(randomBytes))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeBase58(id)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, randomBytes) {
		t.Fatalf("decoded ID differs: got %x, want %x", decoded, randomBytes)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != id {
		t.Fatalf("empty file was not replaced: %q", contents)
	}
}

func TestLoadOrCreateInstallationIDRecoversPublishedCandidate(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, installationIDFilename)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".lock", []byte("candidate-from-other-process"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := LoadOrCreateInstallationIDWithReader(
		directory,
		bytes.NewReader(bytes.Repeat([]byte{0x33}, installationIDBytes)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if id != "candidate-from-other-process" {
		t.Fatalf("recovered ID = %q", id)
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != id {
		t.Fatalf("persisted recovered ID = %q, %v", contents, err)
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("candidate lock remains: %v", err)
	}
}

func TestLoadOrCreateInstallationIDConcurrentCallersConverge(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "data")
	randomBytes := bytes.Repeat([]byte{0x7f}, installationIDBytes)
	reader := bytes.NewReader(randomBytes)

	const callers = 64
	results := make(chan string, callers)
	errors := make(chan error, callers)
	var start sync.WaitGroup
	start.Add(1)
	var calls sync.WaitGroup
	calls.Add(callers)
	for range callers {
		go func() {
			defer calls.Done()
			start.Wait()
			id, err := LoadOrCreateInstallationIDWithReader(directory, reader)
			if err != nil {
				errors <- err
				return
			}
			results <- id
		}()
	}
	start.Done()
	calls.Wait()
	close(results)
	close(errors)

	for err := range errors {
		t.Errorf("concurrent call failed: %v", err)
	}
	var expected string
	for id := range results {
		if expected == "" {
			expected = id
		}
		if id != expected {
			t.Errorf("callers returned different IDs: %q and %q", expected, id)
		}
	}
	if expected == "" {
		t.Fatal("no caller returned an ID")
	}

	contents, err := os.ReadFile(filepath.Join(directory, installationIDFilename))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != expected {
		t.Fatalf("persisted ID %q differs from returned ID %q", contents, expected)
	}
	matches, err := filepath.Glob(filepath.Join(directory, ".install_id-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files remain: %v", matches)
	}
}

func TestLoadOrCreateInstallationIDReaderFailureLeavesNoFile(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	wantErr := errors.New("random source failed")
	_, err := LoadOrCreateInstallationIDWithReader(directory, errReader{wantErr})
	if !errors.Is(err, wantErr) {
		t.Fatalf("got error %v, want wrapped %v", err, wantErr)
	}
	if _, err := os.Stat(filepath.Join(directory, installationIDFilename)); !os.IsNotExist(err) {
		t.Fatalf("install_id exists after generation failure: %v", err)
	}
}

func TestLoadOrCreateInstallationIDRejectsNilReader(t *testing.T) {
	t.Parallel()

	_, err := LoadOrCreateInstallationIDWithReader(t.TempDir(), nil)
	if err == nil || err.Error() != "installation ID randomness source is nil" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEncodeBase58KnownVector(t *testing.T) {
	t.Parallel()

	const expected = "JxF12TrwUP45BMd"
	if actual := encodeBase58([]byte("Hello World")); actual != expected {
		t.Fatalf("got %q, want %q", actual, expected)
	}
}

type errReader struct {
	err error
}

func (reader errReader) Read([]byte) (int, error) {
	return 0, reader.err
}

func decodeBase58(encoded string) ([]byte, error) {
	number := new(big.Int)
	base := big.NewInt(58)
	for _, character := range encoded {
		index := strings.IndexRune(base58Alphabet, character)
		if index < 0 {
			return nil, errors.New("invalid base58 character")
		}
		number.Mul(number, base)
		number.Add(number, big.NewInt(int64(index)))
	}
	decoded := number.Bytes()
	for leading := 0; leading < len(encoded) && encoded[leading] == base58Alphabet[0]; leading++ {
		decoded = append([]byte{0}, decoded...)
	}
	return decoded, nil
}

var _ io.Reader = errReader{}
