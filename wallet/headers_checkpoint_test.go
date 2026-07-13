package wallet

import (
	"bytes"
	"encoding/hex"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSyntheticCheckpointPaddingHashAndPresence(t *testing.T) {
	zeroChunk := make([]byte, checkpointInterval*HeaderSize)
	zeroHash := string(HashHeader(zeroChunk))
	table := checkpointTableFromHashes(t, zeroHash, zeroHash)
	path := filepath.Join(t.TempDir(), "headers")
	headers := NewHeaders(path, withHeaderCheckpoints(table))
	if err := headers.Open(); err != nil {
		t.Fatal(err)
	}
	if headers.Len() != 2*checkpointInterval || headers.Height() != 2*checkpointInterval-1 {
		t.Fatalf("padded dimensions = len %d, height %d", headers.Len(), headers.Height())
	}
	if missing := headers.MissingCheckpointedChunks(); len(missing) != 0 {
		t.Fatalf("zero checkpoints marked missing: %v", missing)
	}
	for _, height := range []int{0, checkpointInterval - 1, checkpointInterval, 2*checkpointInterval - 1} {
		has, err := headers.HasHeader(height)
		if err != nil || !has {
			t.Fatalf("HasHeader(%d) = %t, %v", height, has, err)
		}
	}
	if has, err := headers.HasHeader(2 * checkpointInterval); err != nil || has {
		t.Fatalf("HasHeader(first noncheckpoint) = %t, %v", has, err)
	}
	if got, err := headers.ChunkHash(0, checkpointInterval); err != nil || got != zeroHash {
		t.Fatalf("zero chunk hash = %q, %v", got, err)
	}
	if timestamp, ok := headers.EstimatedTimestamp(checkpointInterval, true); !ok || timestamp != 0 {
		t.Fatalf("present zero checkpoint timestamp = %d, %t", timestamp, ok)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("checkpoint staging reached destination before close: %v", err)
	}
	if err := headers.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() != int64(2*checkpointInterval*HeaderSize) {
		t.Fatalf("checkpoint destination = %v, %v", info, err)
	}
}

func TestCheckpointMissingScanIsNewestFirstAndSticky(t *testing.T) {
	table := checkpointTableFromHashes(t,
		hex.EncodeToString(bytes.Repeat([]byte{1}, checkpointDigestSize)),
		hex.EncodeToString(bytes.Repeat([]byte{2}, checkpointDigestSize)),
	)
	headers := NewHeaders(":memory:", withHeaderCheckpoints(table))
	if err := headers.Open(); err != nil {
		t.Fatal(err)
	}
	want := []int{checkpointInterval, 0}
	if got := headers.MissingCheckpointedChunks(); !reflect.DeepEqual(got, want) {
		t.Fatalf("missing checkpoint order = %v, want %v", got, want)
	}
	if has, err := headers.HasHeader(1); err != nil || has {
		t.Fatalf("missing checkpoint presence = %t, %v", has, err)
	}
	wantEstimate := int64(math.Trunc(float64(defaultFirstBlockTimestamp) + defaultTimestampAverageDelta))
	if timestamp, ok := headers.EstimatedTimestamp(1, true); !ok || timestamp != wantEstimate {
		t.Fatalf("missing checkpoint timestamp = %d, %t; want %d", timestamp, ok, wantEstimate)
	}

	zeroHash := string(HashHeader(make([]byte, checkpointInterval*HeaderSize)))
	headers.mu.Lock()
	headers.checkpoints = checkpointTableFromHashes(t, zeroHash, zeroHash)
	if err := headers.findMissingCheckpointsLocked(); err != nil {
		headers.mu.Unlock()
		t.Fatal(err)
	}
	headers.mu.Unlock()
	if got := headers.MissingCheckpointedChunks(); !reflect.DeepEqual(got, want) {
		t.Fatalf("known missing checkpoints were rechecked: %v", got)
	}
}

func TestCheckpointFinalChunkBoundaryDoesNotExtend(t *testing.T) {
	zeroHash := string(HashHeader(make([]byte, checkpointInterval*HeaderSize)))
	table := checkpointTableFromHashes(t, zeroHash, zeroHash)
	path := filepath.Join(t.TempDir(), "headers")
	contents := make([]byte, (checkpointInterval+1)*HeaderSize)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	headers := NewHeaders(path, withHeaderCheckpoints(table))
	if err := headers.Open(); err != nil {
		t.Fatal(err)
	}
	if headers.Len() != checkpointInterval+1 {
		t.Fatalf("boundary open extended to %d headers", headers.Len())
	}
	if got := headers.MissingCheckpointedChunks(); !reflect.DeepEqual(got, []int{checkpointInterval}) {
		t.Fatalf("short final checkpoint missing set = %v", got)
	}
	if err := headers.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() != int64(len(contents)) {
		t.Fatalf("boundary file = %v, %v", info, err)
	}
}

func TestCheckpointPaddingClearsPartialBytesAtFinalChunkOffset(t *testing.T) {
	zeroHash := string(HashHeader(make([]byte, checkpointInterval*HeaderSize)))
	table := checkpointTableFromHashes(t, zeroHash, zeroHash)
	storage := newMemoryHeaderStorage()
	offset := int64(checkpointInterval * HeaderSize)
	if err := storage.Resize(offset + 3); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.WriteAt([]byte{0xaa, 0xbb, 0xcc}, offset); err != nil {
		t.Fatal(err)
	}
	headers := &Headers{storage: storage, size: checkpointInterval, checkpoints: table}
	if err := headers.ensureCheckpointedSizeLocked(); err != nil {
		t.Fatal(err)
	}
	if headers.size != 2*checkpointInterval || storage.Size() != int64(2*checkpointInterval*HeaderSize) {
		t.Fatalf("padded partial dimensions = %d headers, %d bytes", headers.size, storage.Size())
	}
	cleared := make([]byte, 3)
	if _, err := storage.ReadAt(cleared, offset); err != nil || !bytes.Equal(cleared, []byte{0, 0, 0}) {
		t.Fatalf("partial checkpoint bytes = %x, %v", cleared, err)
	}
}

func TestEmptyCheckpointRepairBoundaryTrustsFirstSuffixAndDropsPredecessorOnFailure(t *testing.T) {
	prefix := make([]byte, (checkpointInterval-1)*HeaderSize)
	trusted := make([]byte, HeaderSize)
	trusted[0] = 1
	broken := make([]byte, HeaderSize)
	broken[0] = 2
	contents := append(prefix, trusted...)
	contents = append(contents, broken...)
	path := filepath.Join(t.TempDir(), "headers")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}

	headers := newCheckpointIndependentHeaders(path)
	if err := headers.Open(); err != nil {
		t.Fatal(err)
	}
	if headers.Len() != checkpointInterval-1 {
		t.Fatalf("failed suffix retained %d headers, want %d", headers.Len(), checkpointInterval-1)
	}
	if err := headers.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != int64(len(contents)) {
		t.Fatalf("repair truncated destination to %d bytes", info.Size())
	}

	trustedOnlyPath := filepath.Join(t.TempDir(), "headers")
	trustedOnly := append(prefix, trusted...)
	if err := os.WriteFile(trustedOnlyPath, trustedOnly, 0o600); err != nil {
		t.Fatal(err)
	}
	trustedHeaders := newCheckpointIndependentHeaders(trustedOnlyPath)
	if err := trustedHeaders.Open(); err != nil {
		t.Fatal(err)
	}
	if trustedHeaders.Len() != checkpointInterval {
		t.Fatalf("sole suffix header was scanned: len %d", trustedHeaders.Len())
	}
	if err := trustedHeaders.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMainnetHeadersOpenSparseCheckpointShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "headers")
	headers := NewHeaders(path)
	if err := headers.Open(); err != nil {
		t.Fatal(err)
	}
	if headers.Len() != mainnetCheckpointCount*checkpointInterval ||
		headers.Height() != mainnetCheckpointCount*checkpointInterval-1 {
		t.Fatalf("mainnet checkpoint shape = len %d, height %d", headers.Len(), headers.Height())
	}
	missing := headers.MissingCheckpointedChunks()
	if len(missing) != mainnetCheckpointCount {
		t.Fatalf("mainnet missing checkpoint count = %d, want %d", len(missing), mainnetCheckpointCount)
	}
	if missing[0] != mainnetCheckpointLastHeight || missing[len(missing)-1] != 0 {
		t.Fatalf("mainnet missing checkpoint endpoints = %d..%d",
			missing[0], missing[len(missing)-1])
	}
	if err := headers.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	wantBytes := int64(mainnetCheckpointCount * checkpointInterval * HeaderSize)
	if info.Size() != wantBytes {
		t.Fatalf("mainnet sparse file size = %d, want %d", info.Size(), wantBytes)
	}
}

func checkpointTableFromHashes(t *testing.T, hashes ...string) checkpointTable {
	t.Helper()
	packed := make([]byte, 0, len(hashes)*checkpointDigestSize)
	for _, hash := range hashes {
		digest, err := hex.DecodeString(hash)
		if err != nil || len(digest) != checkpointDigestSize {
			t.Fatalf("checkpoint hash %q = %x, %v", hash, digest, err)
		}
		packed = append(packed, digest...)
	}
	table, err := newCheckpointTable(string(packed))
	if err != nil {
		t.Fatal(err)
	}
	return table
}
