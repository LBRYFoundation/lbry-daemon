package wallet

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMemoryHeaderStorageOverwriteDoesNotShrink(t *testing.T) {
	storage := newMemoryHeaderStorage()
	if err := storage.Resize(16); err != nil {
		t.Fatal(err)
	}
	if written, err := storage.WriteAt([]byte{1, 2, 3}, 4); err != nil || written != 3 {
		t.Fatalf("write = %d, %v", written, err)
	}
	if storage.Size() != 16 || len(storage.data) != 16 {
		t.Fatalf("overwrite shrank storage to %d bytes", storage.Size())
	}
	if !bytes.Equal(storage.data[4:7], []byte{1, 2, 3}) {
		t.Fatalf("overwritten bytes = %x", storage.data[4:7])
	}
}

func TestDiskHeadersSparseLogicalExtensionHasNoHeapMirror(t *testing.T) {
	path := filepath.Join(t.TempDir(), "headers")
	headers := newCheckpointIndependentHeaders(path)
	if err := headers.Open(); err != nil {
		t.Fatal(err)
	}
	const paddedHeaderCount = 1_243_000
	headers.mu.Lock()
	written, err := headers.writeLocked(paddedHeaderCount, nil)
	headers.mu.Unlock()
	if err != nil || written != 0 {
		t.Fatalf("sparse extension = %d, %v", written, err)
	}
	if headers.Len() != paddedHeaderCount {
		t.Fatalf("sparse state length = %d", headers.Len())
	}
	if _, ok := headers.storage.(*stagedFileHeaderStorage); !ok {
		t.Fatalf("disk path uses storage %T", headers.storage)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("logical resize created the file before close: %v", err)
	}
	if err := headers.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	wantBytes := int64(paddedHeaderCount * HeaderSize)
	if info.Size() != wantBytes {
		t.Fatalf("sparse file size = %d, want %d", info.Size(), wantBytes)
	}

	reopened, err := openStagedFileHeaderStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Size() != wantBytes {
		t.Fatalf("reopened sparse raw size = %d, want %d", reopened.Size(), wantBytes)
	}
	raw := make([]byte, HeaderSize)
	read, err := reopened.ReadAt(raw, wantBytes-HeaderSize)
	if err != nil || read != HeaderSize || !bytes.Equal(raw, make([]byte, HeaderSize)) {
		t.Fatalf("sparse final header = %x, %v", raw, err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDiskHeaderStorageLogicalShrinkRetainsTailAndZeroesReextension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "headers")
	original := bytes.Repeat([]byte{0x7f}, 4*HeaderSize)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	storage, err := openStagedFileHeaderStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Resize(HeaderSize); err != nil {
		t.Fatal(err)
	}
	if err := storage.Resize(3 * HeaderSize); err != nil {
		t.Fatal(err)
	}
	if err := storage.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(contents) != len(original) {
		t.Fatalf("logical shrink truncated disk file to %d bytes", len(contents))
	}
	if !bytes.Equal(contents[:HeaderSize], original[:HeaderSize]) {
		t.Fatal("logical shrink changed the retained prefix")
	}
	if !bytes.Equal(contents[HeaderSize:3*HeaderSize], make([]byte, 2*HeaderSize)) {
		t.Fatalf("re-extended gap retained stale bytes: %x", contents[HeaderSize:3*HeaderSize])
	}
	if !bytes.Equal(contents[3*HeaderSize:], original[3*HeaderSize:]) {
		t.Fatal("legacy no-truncate tail was not retained")
	}
}

func TestDiskHeadersRepeatedOpenDiscardsUnclosedWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "headers")
	headers := newCheckpointIndependentHeaders(path)
	if err := headers.Open(); err != nil {
		t.Fatal(err)
	}
	serialized := decodeHeaderFixture(t)
	if added, err := headers.Connect(0, serialized); err != nil || added != 3 {
		t.Fatalf("connect = %d, %v", added, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unclosed write reached destination: %v", err)
	}
	if err := headers.Open(); err != nil {
		t.Fatal(err)
	}
	if headers.Len() != 0 {
		t.Fatalf("repeated open retained %d unclosed headers", headers.Len())
	}
	if err := headers.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() != 0 {
		t.Fatalf("discarded destination = %v, %v", info, err)
	}
}

func TestDiskHeadersFailedRepeatedOpenRetainsWorkingState(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "headers")
	serialized := decodeHeaderFixture(t)
	if err := os.WriteFile(path, serialized[:2*HeaderSize], 0o600); err != nil {
		t.Fatal(err)
	}
	headers := newCheckpointIndependentHeaders(path)
	if err := headers.Open(); err != nil {
		t.Fatal(err)
	}
	before, err := headers.GetRaw(1)
	if err != nil {
		t.Fatal(err)
	}
	invalidPath := filepath.Join(directory, "directory")
	if err := os.Mkdir(invalidPath, 0o755); err != nil {
		t.Fatal(err)
	}
	headers.path = invalidPath
	if err := headers.Open(); err == nil {
		t.Fatal("repeated open accepted a directory")
	}
	headers.path = path
	after, err := headers.GetRaw(1)
	if err != nil || headers.Len() != 2 || !bytes.Equal(after, before) {
		t.Fatalf("state after failed reopen = len %d, raw %x, %v", headers.Len(), after, err)
	}
	if err := headers.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDiskHeadersCloseFailureRetainsRetryableStagingState(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "missing")
	path := filepath.Join(parent, "headers")
	headers := newCheckpointIndependentHeaders(path)
	if err := headers.Open(); err != nil {
		t.Fatal(err)
	}
	serialized := decodeHeaderFixture(t)
	if added, err := headers.Connect(0, serialized); err != nil || added != 3 {
		t.Fatalf("connect = %d, %v", added, err)
	}
	if err := headers.Close(); err == nil {
		t.Fatal("close unexpectedly created a missing parent")
	}
	if !headers.opened || headers.Len() != 3 {
		t.Fatalf("failed close state = opened %t, len %d", headers.opened, headers.Len())
	}
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := headers.Close(); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(contents, serialized) {
		t.Fatalf("retried close = %x, %v", contents, err)
	}
}

func TestDiskHeadersPreserveUndetectedPartialTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "headers")
	contents := append(decodeHeaderFixture(t), 0xaa, 0xbb, 0xcc)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	headers := newCheckpointIndependentHeaders(path)
	if err := headers.Open(); err != nil {
		t.Fatal(err)
	}
	if headers.Len() != 3 || headers.storage.Size() != int64(len(contents)) {
		t.Fatalf("partial open = len %d, raw bytes %d", headers.Len(), headers.storage.Size())
	}
	if err := headers.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, contents) {
		t.Fatalf("partial tail after close = %x, %v", after, err)
	}
}
