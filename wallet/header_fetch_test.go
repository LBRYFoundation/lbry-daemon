package wallet

import (
	"bytes"
	"compress/flate"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCheckpointFetchViaContextReadWritesAndClearsMissing(t *testing.T) {
	chunk := checkpointFetchFixture(7)
	headers := checkpointFetchHeaders(t, chunk)
	encoded := checkpointFetchEncoded(t, chunk, nil)
	var calls atomic.Int32
	var starts []int
	var startsMu sync.Mutex
	headers.SetChunkGetter(func(_ context.Context, start int) (HeaderChunkResponse, error) {
		calls.Add(1)
		startsMu.Lock()
		starts = append(starts, start)
		startsMu.Unlock()
		return HeaderChunkResponse{Base64: encoded}, nil
	})
	if !headers.ChunkGetterConfigured() {
		t.Fatal("configured chunk getter was not reported")
	}

	height := 321
	raw, err := headers.GetRawContext(context.Background(), height)
	if err != nil {
		t.Fatal(err)
	}
	want := chunk[height*HeaderSize : (height+1)*HeaderSize]
	if !bytes.Equal(raw, want) {
		t.Fatalf("fetched header %d = %x, want %x", height, raw, want)
	}
	if calls.Load() != 1 || !equalInts(starts, []int{0}) {
		t.Fatalf("getter calls = %d starts %v", calls.Load(), starts)
	}
	if missing := headers.MissingCheckpointedChunks(); len(missing) != 0 {
		t.Fatalf("fetched checkpoint remained missing: %v", missing)
	}
	if has, err := headers.HasHeader(999); err != nil || !has {
		t.Fatalf("fetched checkpoint presence = %t, %v", has, err)
	}
	if _, err := headers.GetRaw(999); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("present checkpoint fetched again: %d calls", calls.Load())
	}
	headers.SetChunkGetter(nil)
	if headers.ChunkGetterConfigured() {
		t.Fatal("nil getter remained configured")
	}
}

func TestCheckpointEnsureCoalescesConcurrentReads(t *testing.T) {
	chunk := checkpointFetchFixture(11)
	headers := checkpointFetchHeaders(t, chunk)
	encoded := checkpointFetchEncoded(t, chunk, nil)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	var once sync.Once
	headers.SetChunkGetter(func(_ context.Context, _ int) (HeaderChunkResponse, error) {
		calls.Add(1)
		once.Do(func() { close(started) })
		<-release
		return HeaderChunkResponse{Base64: encoded}, nil
	})

	const readers = 24
	errorsByReader := make(chan error, readers)
	for index := range readers {
		index := index
		go func() {
			_, err := headers.GetRawContext(context.Background(), index%CheckpointChunkHeaders)
			errorsByReader <- err
		}()
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("checkpoint getter did not start")
	}
	close(release)
	for range readers {
		if err := <-errorsByReader; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("concurrent reads made %d getter calls, want 1", calls.Load())
	}
}

func TestCheckpointEnsureWaitHonorsContextCancellation(t *testing.T) {
	chunk := checkpointFetchFixture(13)
	headers := checkpointFetchHeaders(t, chunk)
	encoded := checkpointFetchEncoded(t, chunk, nil)
	started := make(chan struct{})
	release := make(chan struct{})
	headers.SetChunkGetter(func(_ context.Context, _ int) (HeaderChunkResponse, error) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		return HeaderChunkResponse{Base64: encoded}, nil
	})
	first := make(chan error, 1)
	go func() { first <- headers.EnsureChunkAt(context.Background(), 0) }()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := headers.EnsureChunkAt(ctx, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting fetch error = %v, want deadline exceeded", err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

func TestFillMissingCheckpointsUsesNewestFirstOrder(t *testing.T) {
	chunks := [][]byte{
		checkpointFetchFixture(41),
		checkpointFetchFixture(43),
		checkpointFetchFixture(47),
	}
	table := checkpointTableFromHashes(t,
		string(HashHeader(chunks[0])), string(HashHeader(chunks[1])), string(HashHeader(chunks[2])),
	)
	headers := NewHeaders(":memory:", withHeaderCheckpoints(table))
	if err := headers.Open(); err != nil {
		t.Fatal(err)
	}
	encoded := map[int]string{
		0:                          checkpointFetchEncoded(t, chunks[0], nil),
		CheckpointChunkHeaders:     checkpointFetchEncoded(t, chunks[1], nil),
		2 * CheckpointChunkHeaders: checkpointFetchEncoded(t, chunks[2], nil),
	}
	var starts []int
	headers.SetChunkGetter(func(_ context.Context, start int) (HeaderChunkResponse, error) {
		starts = append(starts, start)
		return HeaderChunkResponse{Base64: encoded[start]}, nil
	})
	if err := headers.FillMissingCheckpoints(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []int{2 * CheckpointChunkHeaders, CheckpointChunkHeaders, 0}
	if !equalInts(starts, want) {
		t.Fatalf("checkpoint fill order = %v, want %v", starts, want)
	}
	if missing := headers.MissingCheckpointedChunks(); len(missing) != 0 {
		t.Fatalf("filled checkpoints remain missing: %v", missing)
	}
}

func TestCheckpointFetchMismatchRetainsMissingAndData(t *testing.T) {
	wantChunk := checkpointFetchFixture(17)
	wrongChunk := checkpointFetchFixture(19)
	headers := checkpointFetchHeaders(t, wantChunk)
	headers.SetChunkGetter(func(context.Context, int) (HeaderChunkResponse, error) {
		return HeaderChunkResponse{Base64: checkpointFetchEncoded(t, wrongChunk, nil)}, nil
	})
	err := headers.EnsureChunkAt(context.Background(), 432)
	var mismatch *CheckpointMismatchError
	if !errors.Is(err, ErrCheckpointMismatch) || !errors.As(err, &mismatch) || mismatch.Height != 0 ||
		mismatch.Expected != string(HashHeader(wantChunk)) ||
		mismatch.Actual != string(HashHeader(wrongChunk)) {
		t.Fatalf("mismatch error = %#v, %v", mismatch, err)
	}
	wantMessage := fmt.Sprintf(
		"Checkpoint mismatch at height 0. Expected %s, but got %s instead.",
		HashHeader(wantChunk), HashHeader(wrongChunk),
	)
	if err.Error() != wantMessage {
		t.Fatalf("mismatch message = %q, want %q", err, wantMessage)
	}
	if missing := headers.MissingCheckpointedChunks(); !equalInts(missing, []int{0}) {
		t.Fatalf("mismatch missing set = %v", missing)
	}
	headers.SetChunkGetter(nil)
	raw, err := headers.GetRaw(432)
	if err != nil || !bytes.Equal(raw, make([]byte, HeaderSize)) {
		t.Fatalf("mismatch wrote header data: %x, %v", raw, err)
	}
}

func TestCheckpointFetchPropagatesGetterErrorWithoutMutation(t *testing.T) {
	chunk := checkpointFetchFixture(23)
	headers := checkpointFetchHeaders(t, chunk)
	wantErr := errors.New("hub unavailable")
	headers.SetChunkGetter(func(context.Context, int) (HeaderChunkResponse, error) {
		return HeaderChunkResponse{}, wantErr
	})
	if err := headers.EnsureChunkAt(context.Background(), 0); !errors.Is(err, wantErr) {
		t.Fatalf("getter error = %v, want %v", err, wantErr)
	}
	if missing := headers.MissingCheckpointedChunks(); !equalInts(missing, []int{0}) {
		t.Fatalf("getter failure missing set = %v", missing)
	}
	headers.SetChunkGetter(nil)
	if err := headers.FetchChunk(context.Background(), 0); !errors.Is(err, ErrHeaderChunkGetterUnavailable) {
		t.Fatalf("missing getter error = %v", err)
	}
}

func TestCheckpointFetchDiscardsValidNoncheckpointChunk(t *testing.T) {
	headers := newCheckpointIndependentHeaders(":memory:")
	if err := headers.Open(); err != nil {
		t.Fatal(err)
	}
	chunk := checkpointFetchFixture(29)
	encoded := checkpointFetchEncoded(t, chunk, nil)
	var starts []int
	headers.SetChunkGetter(func(_ context.Context, start int) (HeaderChunkResponse, error) {
		starts = append(starts, start)
		return HeaderChunkResponse{Base64: encoded}, nil
	})
	if err := headers.FetchChunk(context.Background(), 1_777); err != nil {
		t.Fatal(err)
	}
	if headers.Len() != 0 || !equalInts(starts, []int{1_000}) {
		t.Fatalf("noncheckpoint fetch = len %d, starts %v", headers.Len(), starts)
	}
	if _, err := headers.GetRawContext(context.Background(), 1_777); !errors.Is(err, ErrHeaderOutOfBounds) {
		t.Fatalf("noncheckpoint context read error = %v", err)
	}
	if !equalInts(starts, []int{1_000, 1_000}) {
		t.Fatalf("discarded noncheckpoint was cached: %v", starts)
	}
}

func TestConnectContextFetchesMissingCheckpointPredecessors(t *testing.T) {
	chunk := checkpointFetchFixture(37)
	table := checkpointTableFromHashes(t, string(HashHeader(chunk)))
	headers := NewHeaders(":memory:", WithHeaderValidation(false), withHeaderCheckpoints(table))
	if err := headers.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := headers.Close(); err != nil {
			t.Error(err)
		}
	})
	encoded := checkpointFetchEncoded(t, chunk, nil)
	var calls atomic.Int32
	headers.SetChunkGetter(func(_ context.Context, start int) (HeaderChunkResponse, error) {
		calls.Add(1)
		if start != 0 {
			return HeaderChunkResponse{}, fmt.Errorf("unexpected checkpoint start %d", start)
		}
		return HeaderChunkResponse{Base64: encoded}, nil
	})
	previous := HashHeader(chunk[len(chunk)-HeaderSize:])
	next, err := SerializeHeader(BlockHeader{
		Version:       1,
		PreviousHash:  previous,
		MerkleRoot:    bytes.Repeat([]byte{'0'}, 64),
		ClaimTrieRoot: bytes.Repeat([]byte{'0'}, 64),
		Timestamp:     1,
		Bits:          1,
		Nonce:         1,
	})
	if err != nil {
		t.Fatal(err)
	}
	added, err := headers.ConnectContext(context.Background(), CheckpointChunkHeaders, next)
	if err != nil || added != 1 {
		t.Fatalf("ConnectContext() = %d, %v", added, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("predecessor validation made %d fetches, want 1", calls.Load())
	}
	if headers.Height() != CheckpointChunkHeaders {
		t.Fatalf("connected height = %d, want %d", headers.Height(), CheckpointChunkHeaders)
	}
	stored, err := headers.GetRaw(CheckpointChunkHeaders)
	if err != nil || !bytes.Equal(stored, next) {
		t.Fatalf("connected header = %x, %v", stored, err)
	}
}

func TestConnectEmptyChunkDoesNotGrowStorage(t *testing.T) {
	headers := newCheckpointIndependentHeaders(":memory:")
	if err := headers.Open(); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	headers.SetChunkGetter(func(context.Context, int) (HeaderChunkResponse, error) {
		calls.Add(1)
		return HeaderChunkResponse{}, errors.New("empty connect fetched a checkpoint")
	})
	if added, err := headers.Connect(17, nil); err != nil || added != 0 {
		t.Fatalf("empty Connect() = %d, %v", added, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("empty Connect() made %d checkpoint calls", calls.Load())
	}
	if headers.Len() != 0 || headers.storage.Size() != 0 {
		t.Fatalf("empty Connect() grew to %d headers/%d bytes", headers.Len(), headers.storage.Size())
	}
}

func TestCheckpointDecoderValidCompatibilityCases(t *testing.T) {
	chunk := checkpointFetchFixture(31)
	compressed := checkpointFetchCompressed(t, chunk)
	encoded := base64.StdEncoding.EncodeToString(compressed)
	noisy := encoded[:17] + " \n!@#$%^&*()?" + encoded[17:]
	withTrailingDeflateData := base64.StdEncoding.EncodeToString(append(compressed, []byte("ignored trailing data")...))
	for name, input := range map[string]string{
		"ordinary":              encoded,
		"permissive base64":     noisy,
		"trailing deflate data": withTrailingDeflateData,
	} {
		t.Run(name, func(t *testing.T) {
			decoded, err := decodeCheckpointChunk(input)
			if err != nil || !bytes.Equal(decoded, chunk) {
				t.Fatalf("decoded = %x, %v", decoded, err)
			}
		})
	}
}

func TestCheckpointDecoderResourceLimitsAndMalformedInputs(t *testing.T) {
	short := checkpointFetchEncoded(t, make([]byte, CheckpointChunkBytes-1), nil)
	oversized := checkpointFetchEncoded(t, make([]byte, CheckpointChunkBytes+1), nil)
	compressedOversized := base64.StdEncoding.EncodeToString(
		make([]byte, MaxCheckpointCompressedBytes+1),
	)
	tests := []struct {
		name    string
		encoded string
		want    error
	}{
		{name: "encoded cap", encoded: strings.Repeat("A", MaxCheckpointBase64Bytes+1), want: ErrCheckpointBase64TooLarge},
		{name: "compressed cap", encoded: compressedOversized, want: ErrCheckpointCompressedTooLarge},
		{name: "output cap", encoded: oversized, want: ErrCheckpointOutputTooLarge},
		{name: "short output", encoded: short, want: ErrInvalidCheckpointChunkLength},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeCheckpointChunk(test.encoded); !errors.Is(err, test.want) {
				t.Fatalf("decode error = %v, want %v", err, test.want)
			}
		})
	}
	for name, encoded := range map[string]string{
		"bad base64 quantum": "A",
		"invalid deflate":    base64.StdEncoding.EncodeToString([]byte("not deflate")),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeCheckpointChunk(encoded); err == nil {
				t.Fatal("malformed checkpoint decoded without an error")
			}
		})
	}
}

func checkpointFetchHeaders(t *testing.T, chunk []byte) *Headers {
	t.Helper()
	table := checkpointTableFromHashes(t, string(HashHeader(chunk)))
	headers := NewHeaders(":memory:", withHeaderCheckpoints(table))
	if err := headers.Open(); err != nil {
		t.Fatal(err)
	}
	if missing := headers.MissingCheckpointedChunks(); !equalInts(missing, []int{0}) {
		t.Fatalf("initial missing checkpoints = %v", missing)
	}
	t.Cleanup(func() {
		if err := headers.Close(); err != nil {
			t.Error(err)
		}
	})
	return headers
}

func checkpointFetchFixture(seed byte) []byte {
	chunk := make([]byte, CheckpointChunkBytes)
	for index := range chunk {
		chunk[index] = byte((int(seed) + index*37) % 251)
	}
	return chunk
}

func checkpointFetchEncoded(t *testing.T, chunk, trailing []byte) string {
	t.Helper()
	compressed := checkpointFetchCompressed(t, chunk)
	compressed = append(compressed, trailing...)
	return base64.StdEncoding.EncodeToString(compressed)
}

func checkpointFetchCompressed(t *testing.T, chunk []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	writer, err := flate.NewWriter(&compressed, flate.BestSpeed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(chunk); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}

func equalInts(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
