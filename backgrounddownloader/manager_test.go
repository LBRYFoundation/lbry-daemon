package backgrounddownloader

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"lbry/daemon/blob"
	"lbry/daemon/dht"
)

type testNode struct {
	id     [dht.HashSize]byte
	hashes [][dht.HashSize]byte
}

func (node *testNode) NodeID() [dht.HashSize]byte { return node.id }
func (node *testNode) StoredBlobHashes() [][dht.HashSize]byte {
	return append([][dht.HashSize]byte(nil), node.hashes...)
}

type testSpace struct {
	mu        sync.Mutex
	available int64
	calls     int
}

func (space *testSpace) FreeSpaceMB(context.Context, bool) (int64, error) {
	space.mu.Lock()
	defer space.mu.Unlock()
	space.calls++
	return space.available, nil
}

func (space *testSpace) Calls() int {
	space.mu.Lock()
	defer space.mu.Unlock()
	return space.calls
}

func TestManagerDownloadsOneMatchingAnnouncedStream(t *testing.T) {
	path := filepath.Join(t.TempDir(), "background.bin")
	if err := os.WriteFile(path, bytes.Repeat([]byte("seed"), 32), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := blob.CreateStreamDescriptor(path, bytes.NewReader(bytes.Repeat([]byte{0x21}, 48)))
	if err != nil {
		t.Fatal(err)
	}
	sdHash := mustHash(t, created.SDHash)
	completedHash := sha512.Sum384([]byte("complete"))
	nonmatchingHash := sha512.Sum384([]byte("does not match"))
	node := &testNode{hashes: [][dht.HashSize]byte{completedHash, nonmatchingHash, sdHash}}
	node.id[0] = sdHash[0]
	nonmatchingHash[0] = sdHash[0] ^ 0xff
	node.hashes[1] = nonmatchingHash

	managerBlobs := blob.NewManager()
	if err := managerBlobs.Set(hex.EncodeToString(completedHash[:]), []byte("complete"), false); err != nil {
		t.Fatal(err)
	}
	data := map[string][]byte{created.SDHash: created.DescriptorBytes}
	for hash, contents := range created.Blobs {
		data[hash] = contents
	}
	var fetchMu sync.Mutex
	var fetched []string
	managerBlobs.SetFetcher(func(_ context.Context, hash string) ([]byte, error) {
		fetchMu.Lock()
		fetched = append(fetched, hash)
		fetchMu.Unlock()
		contents, ok := data[hash]
		if !ok {
			return nil, fmt.Errorf("unexpected hash %s", hash)
		}
		return append([]byte(nil), contents...), nil
	})
	var logMu sync.Mutex
	var logs []string
	manager := New(node, managerBlobs, &testSpace{available: 11},
		WithPollInterval(time.Hour),
		WithLogger(func(format string, values ...any) {
			logMu.Lock()
			logs = append(logs, fmt.Sprintf(format, values...))
			logMu.Unlock()
		}),
	)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = manager.Stop(ctx)
	})
	for hash := range created.Blobs {
		waitFor(t, time.Second, func() bool { return managerBlobs.Has(hash) })
	}
	waitFor(t, time.Second, func() bool { return !manager.OngoingDownload() })

	fetchMu.Lock()
	gotFetched := append([]string(nil), fetched...)
	fetchMu.Unlock()
	wantFetched := []string{created.SDHash}
	for _, item := range created.Descriptor.Blobs[:len(created.Descriptor.Blobs)-1] {
		wantFetched = append(wantFetched, item.BlobHash)
	}
	if !reflect.DeepEqual(gotFetched, wantFetched) {
		t.Fatalf("fetched hashes = %v, want %v", gotFetched, wantFetched)
	}
	logMu.Lock()
	joinedLogs := strings.Join(logs, "\n")
	logMu.Unlock()
	for _, want := range []string{"started", "caching stream " + created.SDHash[:12], "cached stream " + created.SDHash[:12]} {
		if !strings.Contains(joinedLogs, want) {
			t.Fatalf("logs missing %q:\n%s", want, joinedLogs)
		}
	}
	status := manager.Status()
	if status["running"] != true || status["available_free_space_mb"] != int64(11) || status["ongoing_download"] != false {
		t.Fatalf("status = %#v", status)
	}
}

func TestManagerRequiresStrictlyMoreThanTenMegabytes(t *testing.T) {
	var hash [dht.HashSize]byte
	hash[0] = 7
	node := &testNode{id: hash, hashes: [][dht.HashSize]byte{hash}}
	blobs := blob.NewManager()
	fetched := make(chan struct{}, 1)
	blobs.SetFetcher(func(context.Context, string) ([]byte, error) {
		fetched <- struct{}{}
		return nil, fmt.Errorf("unexpected download")
	})
	var logMu sync.Mutex
	var logs []string
	manager := New(node, blobs, &testSpace{available: 10}, WithPollInterval(time.Hour),
		WithLogger(func(format string, values ...any) {
			logMu.Lock()
			logs = append(logs, fmt.Sprintf(format, values...))
			logMu.Unlock()
		}))
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fetched:
		t.Fatal("download started with only 10 MB available")
	default:
	}
	logMu.Lock()
	joined := strings.Join(logs, "\n")
	logMu.Unlock()
	if !strings.Contains(joined, "paused with 10 MB") {
		t.Fatalf("low-space log missing:\n%s", joined)
	}
}

func TestManagerKeepsPollingWhileDownloadIsOngoingAndStopsIt(t *testing.T) {
	var hash [dht.HashSize]byte
	hash[0] = 9
	node := &testNode{id: hash, hashes: [][dht.HashSize]byte{hash}}
	space := &testSpace{available: 100}
	blobs := blob.NewManager()
	started := make(chan struct{})
	blobs.SetFetcher(func(ctx context.Context, _ string) ([]byte, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	manager := New(node, blobs, space, WithPollInterval(5*time.Millisecond), WithLogger(func(string, ...any) {}))
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-started
	waitFor(t, time.Second, func() bool { return space.Calls() >= 3 })
	if !manager.OngoingDownload() {
		t.Fatal("download was not reported as ongoing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if manager.Running() || manager.OngoingDownload() {
		t.Fatalf("manager remained active: %#v", manager.Status())
	}
}

func TestManagerWithoutDHTIsInert(t *testing.T) {
	manager := New(nil, blob.NewManager(), &testSpace{available: 100}, WithLogger(func(string, ...any) {}))
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"running": false, "available_free_space_mb": nil, "ongoing_download": false,
	}
	if got := manager.Status(); !reflect.DeepEqual(got, want) {
		t.Fatalf("status = %#v, want %#v", got, want)
	}
}

func mustHash(t *testing.T, value string) [dht.HashSize]byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != dht.HashSize {
		t.Fatalf("invalid hash %q: %v", value, err)
	}
	var result [dht.HashSize]byte
	copy(result[:], decoded)
	return result
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not satisfied before timeout")
}
