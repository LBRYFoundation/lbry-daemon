package wallet

import (
	"bytes"
	"compress/flate"
	"context"
	"errors"
	"fmt"
	"io"
)

const (
	CheckpointChunkHeaders       = checkpointInterval
	CheckpointChunkBytes         = CheckpointChunkHeaders * HeaderSize
	MaxCheckpointBase64Bytes     = 512 * 1024
	MaxCheckpointCompressedBytes = 256 * 1024
)

var (
	ErrHeaderChunkGetterUnavailable = errors.New("header checkpoint chunk getter is unavailable")
	ErrCheckpointBase64TooLarge     = errors.New("checkpoint Base64 response exceeds the resource limit")
	ErrCheckpointCompressedTooLarge = errors.New("checkpoint compressed response exceeds the resource limit")
	ErrCheckpointOutputTooLarge     = errors.New("checkpoint decompressed response exceeds the resource limit")
	ErrInvalidCheckpointChunkLength = errors.New("invalid checkpoint chunk length")
	ErrCheckpointMismatch           = errors.New("checkpoint mismatch")
)

type HeaderChunkResponse struct {
	Base64 string
}

type HeaderChunkGetter func(context.Context, int) (HeaderChunkResponse, error)

type CheckpointMismatchError struct {
	Height   int
	Expected string
	Actual   string
}

func (err *CheckpointMismatchError) Error() string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf(
		"Checkpoint mismatch at height %d. Expected %s, but got %s instead.",
		err.Height, err.Expected, err.Actual,
	)
}

func (*CheckpointMismatchError) Unwrap() error { return ErrCheckpointMismatch }

func (headers *Headers) SetChunkGetter(getter HeaderChunkGetter) {
	if headers == nil {
		return
	}
	headers.mu.Lock()
	headers.chunkGetter = getter
	headers.mu.Unlock()
}

func (headers *Headers) ChunkGetterConfigured() bool {
	if headers == nil {
		return false
	}
	headers.mu.RLock()
	defer headers.mu.RUnlock()
	return headers.chunkGetter != nil
}

// EnsureChunkAt serializes all checkpoint fetches, rechecks presence after
// acquiring the gate, and then fetches only when the header is still missing.
func (headers *Headers) EnsureChunkAt(ctx context.Context, height int) error {
	if headers == nil {
		return errors.New("headers are nil")
	}
	if ctx == nil {
		return errors.New("checkpoint fetch context is nil")
	}
	if headers.chunkFetchLock == nil {
		return errors.New("checkpoint fetch lock is uninitialized")
	}
	select {
	case headers.chunkFetchLock <- struct{}{}:
		defer func() { <-headers.chunkFetchLock }()
	case <-ctx.Done():
		return ctx.Err()
	}
	hasHeader, err := headers.HasHeader(height)
	if err != nil {
		return err
	}
	if hasHeader {
		return nil
	}
	return headers.FetchChunk(ctx, height)
}

// FillMissingCheckpoints fetches the current missing snapshot newest first,
// matching Ledger.initial_headers_sync's background loop. EnsureChunkAt still
// rechecks each entry in case another read filled it after the snapshot.
func (headers *Headers) FillMissingCheckpoints(ctx context.Context) error {
	if headers == nil {
		return errors.New("headers are nil")
	}
	if ctx == nil {
		return errors.New("checkpoint fill context is nil")
	}
	for _, height := range headers.MissingCheckpointedChunks() {
		if err := headers.EnsureChunkAt(ctx, height); err != nil {
			return err
		}
	}
	return nil
}

// FetchChunk performs one raw-DEFLATE checkpoint request without taking the
// fetch gate. Callers normally use EnsureChunkAt so concurrent reads coalesce.
func (headers *Headers) FetchChunk(ctx context.Context, height int) error {
	if headers == nil {
		return errors.New("headers are nil")
	}
	if ctx == nil {
		return errors.New("checkpoint fetch context is nil")
	}
	if height < 0 {
		return fmt.Errorf("%w: %d", ErrHeaderOutOfBounds, height)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	headers.mu.RLock()
	getter := headers.chunkGetter
	headers.mu.RUnlock()
	if getter == nil {
		return ErrHeaderChunkGetterUnavailable
	}
	start := (height / CheckpointChunkHeaders) * CheckpointChunkHeaders
	response, err := getter(ctx, start)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	chunk, err := decodeCheckpointChunk(response.Base64)
	if err != nil {
		return err
	}
	actual := string(HashHeader(chunk))

	headers.mu.Lock()
	defer headers.mu.Unlock()
	if !headers.opened {
		return ErrHeadersNotOpen
	}
	expected, checkpointed := headers.checkpoints.lookup(start)
	if !checkpointed {
		return nil
	}
	if actual != expected {
		return &CheckpointMismatchError{Height: start, Expected: expected, Actual: actual}
	}
	if _, err := headers.writeLocked(start, chunk); err != nil {
		return err
	}
	delete(headers.missingCheckpoints, start)
	return nil
}

func decodeCheckpointChunk(encoded string) ([]byte, error) {
	if len(encoded) > MaxCheckpointBase64Bytes {
		return nil, fmt.Errorf("%w: got %d bytes, limit %d",
			ErrCheckpointBase64TooLarge, len(encoded), MaxCheckpointBase64Bytes)
	}
	compressed, err := decodePythonBase64(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode checkpoint Base64: %w", err)
	}
	if len(compressed) > MaxCheckpointCompressedBytes {
		return nil, fmt.Errorf("%w: got %d bytes, limit %d",
			ErrCheckpointCompressedTooLarge, len(compressed), MaxCheckpointCompressedBytes)
	}
	decompressor := flate.NewReader(bytes.NewReader(compressed))
	chunk, readErr := io.ReadAll(io.LimitReader(decompressor, CheckpointChunkBytes+1))
	closeErr := decompressor.Close()
	if readErr != nil {
		return nil, fmt.Errorf("decompress checkpoint chunk: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close checkpoint decompressor: %w", closeErr)
	}
	if len(chunk) > CheckpointChunkBytes {
		return nil, fmt.Errorf("%w: got at least %d bytes, limit %d",
			ErrCheckpointOutputTooLarge, len(chunk), CheckpointChunkBytes)
	}
	if len(chunk) != CheckpointChunkBytes {
		return nil, fmt.Errorf("%w: got %d bytes, want %d",
			ErrInvalidCheckpointChunkLength, len(chunk), CheckpointChunkBytes)
	}
	return chunk, nil
}
