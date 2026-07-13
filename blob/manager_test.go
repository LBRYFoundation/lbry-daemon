package blob

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type completionStoreFunc func(context.Context, string, int, int64, bool) error

func (function completionStoreFunc) RecordCompletedBlob(
	ctx context.Context, hash string, length int, addedOn int64, isMine bool,
) error {
	return function(ctx, hash, length, addedOn, isMine)
}

func TestBlobManagerSetAndGetCopyData(t *testing.T) {
	manager := NewManager()
	input := []byte("original")
	hash := hashBytes(input)

	if err := manager.Set(hash, input, false); err != nil {
		t.Fatalf("Set returned an error: %v", err)
	}
	input[0] = 'X'

	got, ok := manager.Get(hash)
	if !ok {
		t.Fatal("Get did not find the stored blob")
	}
	if want := []byte("original"); !bytes.Equal(got, want) {
		t.Fatalf("Get returned %q, want %q", got, want)
	}

	got[1] = 'X'
	gotAgain, ok := manager.Get(hash)
	if !ok {
		t.Fatal("second Get did not find the stored blob")
	}
	if want := []byte("original"); !bytes.Equal(gotAgain, want) {
		t.Fatalf("mutating a Get result changed stored data: got %q, want %q", gotAgain, want)
	}
}

func TestPersistentBlobManagerSurvivesRestart(t *testing.T) {
	directory := t.TempDir()
	data := []byte("persistent encrypted blob")
	digest := sha512.Sum384(data)
	hash := hex.EncodeToString(digest[:])
	manager := NewPersistentManager(directory)
	if loaded, err := manager.Start(); err != nil || len(loaded) != 0 {
		t.Fatalf("initial Start = %v, %v", loaded, err)
	}
	if err := manager.Set(hash, data, false); err != nil {
		t.Fatal(err)
	}
	if stored, err := os.ReadFile(filepath.Join(directory, hash)); err != nil || !bytes.Equal(stored, data) {
		t.Fatalf("stored blob = %q, %v", stored, err)
	}
	manager.Stop()

	restarted := NewPersistentManager(directory)
	loaded, err := restarted.Start()
	if err != nil || loaded[hash] != int64(len(data)) {
		t.Fatalf("restart loaded = %v, %v", loaded, err)
	}
	got, ok := restarted.GetLocal(hash)
	if !ok || !bytes.Equal(got, data) {
		t.Fatalf("restarted blob = %q, %t", got, ok)
	}
	if err := restarted.Delete(hash); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, hash)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted blob stat error = %v", err)
	}
}

func TestPersistentBlobManagerIgnoresCorruptAndUnrelatedFiles(t *testing.T) {
	directory := t.TempDir()
	validData := []byte("valid")
	validDigest := sha512.Sum384(validData)
	validHash := hex.EncodeToString(validDigest[:])
	corruptHash := strings.Repeat("0", BlobHashLength)
	if err := os.WriteFile(filepath.Join(directory, validHash), validData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, corruptHash), []byte("wrong"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "unrelated"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewPersistentManager(directory)
	loaded, err := manager.Start()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[validHash] != int64(len(validData)) {
		t.Fatalf("loaded files = %v", loaded)
	}
	if _, ok := manager.GetLocal(corruptHash); ok {
		t.Fatal("corrupt blob was loaded")
	}
	if err := manager.Set(corruptHash, validData, false); err == nil {
		t.Fatal("hash-mismatched persistent Set succeeded")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".blob-") {
			t.Fatalf("temporary blob leaked: %s", entry.Name())
		}
	}
}

func TestConfiguredManagerReadsExistingDiskWithoutSavingNewBlobs(t *testing.T) {
	directory := t.TempDir()
	existing := []byte("existing")
	existingDigest := sha512.Sum384(existing)
	existingHash := hex.EncodeToString(existingDigest[:])
	if err := os.WriteFile(filepath.Join(directory, existingHash), existing, 0o644); err != nil {
		t.Fatal(err)
	}
	manager := NewConfiguredManager(directory, false)
	if _, err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	if got, ok := manager.GetLocal(existingHash); !ok || !bytes.Equal(got, existing) {
		t.Fatalf("existing disk blob = %q, %t", got, ok)
	}
	newData := []byte("memory only")
	newDigest := sha512.Sum384(newData)
	newHash := hex.EncodeToString(newDigest[:])
	if err := manager.Set(newHash, newData, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, newHash)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("save_blobs=false created a file: %v", err)
	}
	manager.Stop()
	restarted := NewConfiguredManager(directory, false)
	loaded, err := restarted.Start()
	if err != nil || len(loaded) != 1 || loaded[existingHash] != int64(len(existing)) {
		t.Fatalf("restart loaded = %v, %v", loaded, err)
	}
	if _, ok := restarted.GetLocal(newHash); ok {
		t.Fatal("memory-only blob survived restart")
	}
}

func TestBlobManagerEnsureUsesConfiguredFetcherAndVerifiesHash(t *testing.T) {
	manager := NewManager()
	data := []byte("downloaded")
	digest := sha512.Sum384(data)
	blobHash := hex.EncodeToString(digest[:])
	calls := 0
	manager.SetFetcher(func(_ context.Context, requested string) ([]byte, error) {
		calls++
		if requested != blobHash {
			t.Fatalf("requested hash = %q", requested)
		}
		return append([]byte(nil), data...), nil
	})
	if err := manager.Ensure(context.Background(), blobHash); err != nil {
		t.Fatal(err)
	}
	if err := manager.Ensure(context.Background(), blobHash); err != nil {
		t.Fatal(err)
	}
	got, ok := manager.Get(blobHash)
	if !ok || !bytes.Equal(got, data) || calls != 1 {
		t.Fatalf("ensured blob = %q, %t, calls %d", got, ok, calls)
	}

	bad := NewManager()
	bad.SetFetcher(func(context.Context, string) ([]byte, error) { return []byte("wrong"), nil })
	if err := bad.Ensure(context.Background(), blobHash); err == nil {
		t.Fatal("hash-mismatched fetched blob was accepted")
	}
	if got := bad.CompletedBlobCount(); got != 0 {
		t.Fatalf("bad fetch stored %d blobs", got)
	}
}

func TestEnsureRollsBackBlobWhenCompletionRecordFails(t *testing.T) {
	data := []byte("recorded after retry")
	digest := sha512.Sum384(data)
	hash := hex.EncodeToString(digest[:])
	manager := NewPersistentManager(t.TempDir())
	if _, err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	fetches := 0
	manager.SetFetcher(func(context.Context, string) ([]byte, error) {
		fetches++
		return data, nil
	})
	records := 0
	manager.SetCompletionStore(completionStoreFunc(func(
		context.Context, string, int, int64, bool,
	) error {
		records++
		if records == 1 {
			return errors.New("database unavailable")
		}
		return nil
	}))
	if err := manager.Ensure(context.Background(), hash); err == nil || !strings.Contains(err.Error(), "database unavailable") {
		t.Fatalf("first Ensure error = %v", err)
	}
	if manager.Has(hash) {
		t.Fatal("unrecorded blob remained completed")
	}
	if _, err := os.Stat(filepath.Join(manager.blobDir, hash)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unrecorded blob file remained: %v", err)
	}
	if err := manager.Ensure(context.Background(), hash); err != nil {
		t.Fatal(err)
	}
	if !manager.Has(hash) || fetches != 2 || records != 2 {
		t.Fatalf("retry state: completed=%t fetches=%d records=%d", manager.Has(hash), fetches, records)
	}
}

func TestEnsureDeduplicatesConcurrentFetches(t *testing.T) {
	data := []byte("one network request")
	digest := sha512.Sum384(data)
	hash := hex.EncodeToString(digest[:])
	manager := NewManager()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	manager.SetFetcher(func(context.Context, string) ([]byte, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return data, nil
	})

	const callers = 12
	errorsByCaller := make(chan error, callers)
	var waitGroup sync.WaitGroup
	for range callers {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			errorsByCaller <- manager.Ensure(context.Background(), hash)
		}()
	}
	<-started
	time.Sleep(10 * time.Millisecond)
	close(release)
	waitGroup.Wait()
	close(errorsByCaller)
	for err := range errorsByCaller {
		if err != nil {
			t.Fatalf("Ensure() error = %v", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("fetch calls = %d, want 1", got)
	}
}

func TestEnsureKeepsSharedFetchAliveForRemainingWaiter(t *testing.T) {
	data := []byte("foreground survives background timeout")
	digest := sha512.Sum384(data)
	hash := hex.EncodeToString(digest[:])
	manager := NewManager()
	started := make(chan struct{})
	release := make(chan struct{})
	manager.SetFetcher(func(ctx context.Context, _ string) ([]byte, error) {
		close(started)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return data, nil
		}
	})
	backgroundContext, cancelBackground := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelBackground()
	backgroundResult := make(chan error, 1)
	go func() { backgroundResult <- manager.Ensure(backgroundContext, hash) }()
	<-started
	foregroundResult := make(chan error, 1)
	go func() { foregroundResult <- manager.Ensure(context.Background(), hash) }()
	if err := <-backgroundResult; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("background error = %v", err)
	}
	close(release)
	if err := <-foregroundResult; err != nil {
		t.Fatalf("foreground inherited background cancellation: %v", err)
	}
	if !manager.Has(hash) {
		t.Fatal("shared fetch did not complete")
	}
}

func TestEnsureRetriesAfterFailedFlight(t *testing.T) {
	data := []byte("retry succeeds")
	digest := sha512.Sum384(data)
	hash := hex.EncodeToString(digest[:])
	manager := NewManager()
	var calls atomic.Int32
	manager.SetFetcher(func(context.Context, string) ([]byte, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("temporary failure")
		}
		return data, nil
	})

	if err := manager.Ensure(context.Background(), hash); err == nil {
		t.Fatal("first Ensure() unexpectedly succeeded")
	}
	if err := manager.Ensure(context.Background(), hash); err != nil {
		t.Fatalf("second Ensure() error = %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("fetch calls = %d, want 2", got)
	}
}

func TestBlobManagerSetInitializesNilMap(t *testing.T) {
	manager := &BlobManager{}
	data := []byte("data")
	hash := hashBytes(data)

	if err := manager.Set(hash, data, true); err != nil {
		t.Fatalf("Set returned an error: %v", err)
	}
	got, ok := manager.Get(hash)
	if !ok || !bytes.Equal(got, []byte("data")) {
		t.Fatalf("Get returned (%q, %v), want (%q, true)", got, ok, []byte("data"))
	}
}

func TestBlobManagerCompletedBlobCountIsReadOnly(t *testing.T) {
	manager := NewManager()
	if got := manager.CompletedBlobCount(); got != 0 {
		t.Fatalf("initial completed blob count = %d, want 0", got)
	}
	one, two := []byte("one"), []byte("two")
	first, second := hashBytes(one), hashBytes(two)
	if err := manager.Set(first, one, false); err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(second, two, true); err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(first, one, false); err != nil {
		t.Fatal(err)
	}
	if got := manager.CompletedBlobCount(); got != 2 {
		t.Fatalf("completed blob count = %d, want 2", got)
	}
	var nilManager *BlobManager
	if got := nilManager.CompletedBlobCount(); got != 0 {
		t.Fatalf("nil manager completed blob count = %d, want 0", got)
	}
}

func TestGuessMIMEMatchesPinnedCommonAndFallbackTypes(t *testing.T) {
	for name, want := range map[string]string{
		"sound.wav": "audio/x-wav", "movie.ogg": "video/ogg", "movie.ogv": "video/ogg",
		"movie.m4v": "video/m4v", "movie.ts": "video/mp2t", "image.svg": "image/svg+xml",
		"notes.txt": "text/plain", "archive.unknown": "application/x-ext-unknown",
		"extensionless": "application/octet-stream",
	} {
		if got := GuessMIME(hex.EncodeToString([]byte(name)), ""); got != want {
			t.Errorf("GuessMIME(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestBlobManagerSupportsConcurrentAccessThroughOneOwner(t *testing.T) {
	manager := NewManager()
	const keyCount = 8
	initialData := []byte("initial")
	initialHash := hashBytes(initialData)
	for i := 0; i < keyCount; i++ {
		if err := manager.Set(initialHash, initialData, false); err != nil {
			t.Fatalf("initial Set returned an error: %v", err)
		}
	}

	var wg sync.WaitGroup
	for worker := 0; worker < 32; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			for iteration := 0; iteration < 500; iteration++ {
				if worker%2 == 0 {
					input := []byte(fmt.Sprintf("%d-%d", worker, iteration))
					if err := manager.Set(hashBytes(input), input, false); err != nil {
						t.Errorf("Set returned an error: %v", err)
						return
					}
					input[0] ^= 0xff
					continue
				}

				got, ok := manager.Get(initialHash)
				if !ok {
					t.Error("Get did not find pre-populated blob")
					return
				}
				if len(got) > 0 {
					got[0] ^= 0xff
				}
			}
		}()
	}
	wg.Wait()

	for i := 0; i < keyCount; i++ {
		if _, ok := manager.Get(initialHash); !ok {
			t.Fatal("manager lost initial blob")
		}
	}
}
