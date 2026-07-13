package filemanager

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"lbry/daemon/blob"
	"lbry/daemon/database"
	reflectorpkg "lbry/daemon/reflector"
)

type managerTestStore struct {
	mu          sync.Mutex
	rows        []database.ManagedFileRow
	descriptor  *blob.StreamDescriptor
	reconciled  bool
	finished    []string
	finishedSet map[string]struct{}
	markHook    func(string)
	reflected   []string
	reflectDone chan string
}

func (store *managerTestStore) ListManagedFiles(context.Context) ([]database.ManagedFileRow, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]database.ManagedFileRow(nil), store.rows...), nil
}

func (store *managerTestStore) ReconcileManagedFilePaths(context.Context) error {
	store.mu.Lock()
	store.reconciled = true
	store.mu.Unlock()
	return nil
}

func (store *managerTestStore) RecoverManagedDescriptor(context.Context, string) (*blob.StreamDescriptor, error) {
	return store.descriptor, nil
}

func (store *managerTestStore) FinalizeManagedDescriptorRecovery(
	context.Context, string, string, string,
) error {
	return nil
}

func (store *managerTestStore) MarkManagedBlobsFinished(_ context.Context, hashes []string) error {
	for _, hash := range hashes {
		if store.markHook != nil {
			store.markHook(hash)
		}
	}
	store.mu.Lock()
	if store.finishedSet == nil {
		store.finishedSet = make(map[string]struct{})
	}
	store.finished = append(store.finished, hashes...)
	for _, hash := range hashes {
		if _, exists := store.finishedSet[hash]; exists {
			continue
		}
		store.finishedSet[hash] = struct{}{}
		isContent := false
		if store.descriptor != nil {
			for _, info := range store.descriptor.ContentBlobs() {
				isContent = isContent || info.BlobHash == hash
			}
		}
		if isContent {
			for index := range store.rows {
				store.rows[index].BlobsCompleted++
			}
		}
	}
	store.mu.Unlock()
	return nil
}

func (store *managerTestStore) SetManagedFileSaved(_ context.Context, streamHash string, saved bool) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	for index := range store.rows {
		if store.rows[index].StreamHash == streamHash {
			store.rows[index].SavedFile = saved
		}
	}
	return nil
}

func (store *managerTestStore) ChangeManagedFileStatus(_ context.Context, streamHash, status string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	for index := range store.rows {
		if store.rows[index].StreamHash == streamHash {
			store.rows[index].Status = status
		}
	}
	return nil
}

func (store *managerTestStore) ChangeManagedFilePath(
	_ context.Context, streamHash string, name, directory *string,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	for index := range store.rows {
		if store.rows[index].StreamHash == streamHash {
			store.rows[index].FileName = name
			store.rows[index].DownloadDirectory = directory
		}
	}
	return nil
}

func (store *managerTestStore) StopAllManagedFiles(context.Context) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	for index := range store.rows {
		store.rows[index].Status = "stopped"
	}
	return nil
}

func (store *managerTestStore) MarkStreamReflected(_ context.Context, sdHash, _ string) error {
	store.mu.Lock()
	store.reflected = append(store.reflected, sdHash)
	notify := store.reflectDone
	store.mu.Unlock()
	if notify != nil {
		select {
		case notify <- sdHash:
		default:
		}
	}
	return nil
}

func (store *managerTestStore) CompleteManagedFileSave(_ context.Context, streamHash string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	for index := range store.rows {
		if store.rows[index].StreamHash == streamHash {
			store.rows[index].SavedFile = true
			store.rows[index].Status = "finished"
		}
	}
	return nil
}

func testDescriptor(t *testing.T) (*blob.StreamDescriptor, []byte, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "name.bin")
	if err := os.WriteFile(path, []byte("managed"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := blob.CreateStreamDescriptor(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	return created.Descriptor, created.DescriptorBytes, created.SDHash
}

func TestManagerLoadsAndRecoversManagedDescriptor(t *testing.T) {
	descriptor, data, sdHash := testDescriptor(t)
	store := &managerTestStore{
		descriptor: descriptor,
		rows:       []database.ManagedFileRow{{StreamHash: descriptor.StreamHash, SDHash: sdHash, Status: "stopped"}},
	}
	blobs := blob.NewManager()
	manager := New(store, blobs, "")
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = manager.Stop(context.Background()) }()
	if !manager.Running() || manager.ManagedFileCount() != 1 || !store.reconciled {
		t.Fatalf("running=%t files=%d reconciled=%t", manager.Running(), manager.ManagedFileCount(), store.reconciled)
	}
	got, ok := blobs.GetLocal(sdHash)
	if !ok || string(got) != string(data) {
		t.Fatalf("recovered descriptor = %q, %t", got, ok)
	}
	if len(store.finished) != 1 || store.finished[0] != sdHash {
		t.Fatalf("finished hashes = %v", store.finished)
	}
}

func TestManagerRecoversLegacyOldSortDescriptor(t *testing.T) {
	descriptor, _, _ := testDescriptor(t)
	data, err := blob.MarshalOldSortDescriptor(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha512.Sum384(data)
	sdHash := hex.EncodeToString(digest[:])
	store := &managerTestStore{
		descriptor: descriptor,
		rows: []database.ManagedFileRow{{
			StreamHash: descriptor.StreamHash, SDHash: sdHash, Status: "stopped",
		}},
	}
	blobs := blob.NewManager()
	manager := New(store, blobs, "")
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = manager.Stop(context.Background()) }()
	got, ok := blobs.GetLocal(sdHash)
	if !ok || !bytes.Equal(got, data) {
		t.Fatalf("legacy recovered descriptor = %q, %t", got, ok)
	}
}

func TestManagedStreamLookupUsesOnlyValidatedRestoredRows(t *testing.T) {
	descriptor, _, sdHash := testDescriptor(t)
	invalid := *descriptor
	invalid.StreamHash = strings.Repeat("0", blob.BlobHashLength)
	store := &managerTestStore{
		descriptor: &invalid,
		rows: []database.ManagedFileRow{{
			StreamHash: descriptor.StreamHash, SDHash: sdHash, Status: "stopped",
		}},
	}
	manager := New(store, blob.NewManager(), "")
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = manager.Stop(context.Background()) }()
	if _, found, err := manager.LookupManagedStream(context.Background(), sdHash); err != nil || found {
		t.Fatalf("invalid restored lookup = %t, %v", found, err)
	}
}

func TestRegisterManagedFileMakesNewDownloadStreamable(t *testing.T) {
	store := &managerTestStore{}
	blobs := blob.NewManager()
	manager := New(store, blobs, "")
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = manager.Stop(context.Background()) }()
	path := filepath.Join(t.TempDir(), "new.bin")
	if err := os.WriteFile(path, []byte("new managed stream"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := blob.CreateStreamDescriptor(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := blobs.Set(created.SDHash, created.DescriptorBytes, true); err != nil {
		t.Fatal(err)
	}
	row := database.ManagedFileRow{StreamHash: created.Descriptor.StreamHash, SDHash: created.SDHash}
	store.rows = []database.ManagedFileRow{row}
	if err := manager.RegisterManagedFile(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	got, found, err := manager.LookupManagedStream(context.Background(), created.SDHash)
	if err != nil || !found || got.StreamHash != row.StreamHash {
		t.Fatalf("registered lookup = %#v, %t, %v", got, found, err)
	}
}

func TestRegisterManagedFileQueuesImmediateReflection(t *testing.T) {
	store := &managerTestStore{reflectDone: make(chan string, 1)}
	source := blob.NewManager()
	destination := blob.NewManager()
	reflectorServer := reflectorpkg.CreateServer(destination)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go reflectorServer.Serve(listener)
	t.Cleanup(func() { _ = reflectorServer.Shutdown(context.Background()) })

	manager := New(store, source, "", WithReflection(
		true, []string{listener.Addr().String()}, 1, time.Hour,
	))
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = manager.Stop(context.Background()) }()

	path := filepath.Join(t.TempDir(), "immediate-reflection.bin")
	if err := os.WriteFile(path, []byte("reflect without waiting for the periodic pass"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := blob.CreateStreamDescriptor(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Set(created.SDHash, created.DescriptorBytes, true); err != nil {
		t.Fatal(err)
	}
	for hash, data := range created.Blobs {
		if err := source.Set(hash, data, false); err != nil {
			t.Fatal(err)
		}
	}
	row := database.ManagedFileRow{
		StreamHash: created.Descriptor.StreamHash,
		SDHash:     created.SDHash,
		Status:     "finished",
	}
	store.mu.Lock()
	store.rows = []database.ManagedFileRow{row}
	store.mu.Unlock()
	if err := manager.RegisterManagedFile(context.Background(), row); err != nil {
		t.Fatal(err)
	}

	select {
	case reflected := <-store.reflectDone:
		if reflected != created.SDHash {
			t.Fatalf("reflected stream = %s", reflected)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("new managed stream was not reflected immediately")
	}
	if !destination.Has(created.SDHash) {
		t.Fatal("reflector did not receive the stream descriptor")
	}
	for hash := range created.Blobs {
		if !destination.Has(hash) {
			t.Fatalf("reflector did not receive content blob %s", hash)
		}
	}
}

func TestRegisterManagedFileRejectsRowDeletedBeforeRegistration(t *testing.T) {
	store := &managerTestStore{}
	blobs := blob.NewManager()
	manager := New(store, blobs, "")
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = manager.Stop(context.Background()) }()
	path := filepath.Join(t.TempDir(), "deleted.bin")
	if err := os.WriteFile(path, []byte("deleted before registration"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := blob.CreateStreamDescriptor(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := blobs.Set(created.SDHash, created.DescriptorBytes, true); err != nil {
		t.Fatal(err)
	}
	row := database.ManagedFileRow{StreamHash: created.Descriptor.StreamHash, SDHash: created.SDHash}
	if err := manager.RegisterManagedFile(context.Background(), row); err == nil ||
		!strings.Contains(err.Error(), "no longer persisted") {
		t.Fatalf("stale registration error = %v", err)
	}
	if _, found, err := manager.LookupManagedStream(context.Background(), created.SDHash); err != nil || found {
		t.Fatalf("stale registered lookup = %t, %v", found, err)
	}
}

func TestWaitReadyBlocksUntilManagerStartupCompletes(t *testing.T) {
	manager := New(&managerTestStore{}, blob.NewManager(), "")
	ready := make(chan error, 1)
	go func() { ready <- manager.WaitReady(context.Background()) }()
	select {
	case err := <-ready:
		t.Fatalf("WaitReady returned before startup: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = manager.Stop(context.Background()) }()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("WaitReady did not return after startup")
	}
}

func TestStreamingOnlyManagedStatusTransitionsActiveAndIdle(t *testing.T) {
	descriptor, _, sdHash := testDescriptor(t)
	store := &managerTestStore{
		descriptor: descriptor,
		rows: []database.ManagedFileRow{{
			StreamHash: descriptor.StreamHash, SDHash: sdHash, Status: "running",
		}},
	}
	manager := New(store, blob.NewManager(), "")
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = manager.Stop(context.Background()) }()
	if err := manager.StopManagedStreamIfIdle(context.Background(), sdHash); err != nil {
		t.Fatal(err)
	}
	row, found, err := manager.LookupManagedStream(context.Background(), sdHash)
	if err != nil || !found || row.Status != "stopped" {
		t.Fatalf("idle row = %#v, %t, %v", row, found, err)
	}
	if err := manager.MarkManagedStreamActive(context.Background(), sdHash); err != nil {
		t.Fatal(err)
	}
	row, found, err = manager.LookupManagedStream(context.Background(), sdHash)
	if err != nil || !found || row.Status != "running" {
		t.Fatalf("active row = %#v, %t, %v", row, found, err)
	}
}

func TestIdleTransitionDoesNotStopDownloadPausedForCleanup(t *testing.T) {
	descriptor, _, sdHash := testDescriptor(t)
	store := &managerTestStore{
		descriptor: descriptor,
		rows: []database.ManagedFileRow{{
			StreamHash: descriptor.StreamHash, SDHash: sdHash, Status: "running",
		}},
	}
	manager := New(store, blob.NewManager(), "")
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = manager.Stop(context.Background()) }()
	manager.mu.Lock()
	manager.paused = true
	manager.mu.Unlock()
	if err := manager.StopManagedStreamIfIdle(context.Background(), sdHash); err != nil {
		t.Fatal(err)
	}
	row, found, err := manager.LookupManagedStream(context.Background(), sdHash)
	if err != nil || !found || row.Status != "running" {
		t.Fatalf("paused idle row = %#v, %t, %v", row, found, err)
	}
}

func TestManagerResumesRunningUnsavedFile(t *testing.T) {
	sourceDirectory := t.TempDir()
	sourcePath := filepath.Join(sourceDirectory, "source.bin")
	content := []byte("resumed managed file content")
	if err := os.WriteFile(sourcePath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := blob.CreateStreamDescriptor(sourcePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	blobs := blob.NewManager()
	if err := blobs.Set(created.SDHash, created.DescriptorBytes, true); err != nil {
		t.Fatal(err)
	}
	for hash, data := range created.Blobs {
		if err := blobs.Set(hash, data, false); err != nil {
			t.Fatal(err)
		}
	}
	destination := t.TempDir()
	name := "restored.bin"
	store := &managerTestStore{
		descriptor: created.Descriptor,
		rows: []database.ManagedFileRow{{
			StreamHash: created.Descriptor.StreamHash, SDHash: created.SDHash,
			FileName: &name, DownloadDirectory: &destination, Status: "running", SavedFile: false,
		}},
	}
	manager := New(store, blobs, "")
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = manager.Stop(context.Background()) }()
	path := filepath.Join(destination, name)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if restored, err := os.ReadFile(path); err == nil {
			if string(restored) != string(content) {
				t.Fatalf("restored content = %q", restored)
			}
			store.mu.Lock()
			saved, status := store.rows[0].SavedFile, store.rows[0].Status
			store.mu.Unlock()
			if saved && status == "finished" {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("managed file was not resumed")
}

func TestSaveReturnsAfterFirstWriteAndSurvivesRequestCancellation(t *testing.T) {
	content := bytes.Repeat([]byte{0x5a}, blob.MaxBlobSize+128)
	source := filepath.Join(t.TempDir(), "async.bin")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := blob.CreateStreamDescriptor(source, nil)
	if err != nil {
		t.Fatal(err)
	}
	dataBlobs := created.Descriptor.ContentBlobs()
	if len(dataBlobs) != 2 {
		t.Fatalf("data blobs = %d, want 2", len(dataBlobs))
	}
	row := database.ManagedFileRow{
		StreamHash: created.Descriptor.StreamHash, SDHash: created.SDHash,
		Status: "stopped", SuggestedFileName: "async.bin", BlobsInStream: 2,
	}
	store := &managerTestStore{descriptor: created.Descriptor, rows: []database.ManagedFileRow{row}}
	blobs := blob.NewManager()
	if err := blobs.Set(created.SDHash, created.DescriptorBytes, true); err != nil {
		t.Fatal(err)
	}
	secondStarted, releaseSecond := make(chan struct{}), make(chan struct{})
	blobs.SetFetcher(func(ctx context.Context, hash string) ([]byte, error) {
		if hash == dataBlobs[1].BlobHash {
			close(secondStarted)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-releaseSecond:
			}
		}
		return created.Blobs[hash], nil
	})
	destination := t.TempDir()
	manager := New(store, blobs, destination, WithDownloadTimeout(time.Second))
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = manager.Stop(context.Background()) }()

	requestContext, cancelRequest := context.WithCancel(context.Background())
	returned, err := manager.SaveManagedFile(requestContext, row, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("second blob acquisition did not begin")
	}
	if returned.Status != "running" || returned.SavedFile || returned.FileName == nil {
		t.Fatalf("early returned row = %#v", returned)
	}
	path := filepath.Join(destination, *returned.FileName)
	info, err := os.Stat(path)
	if err != nil || info.Size() <= 0 || info.Size() >= int64(len(content)) {
		t.Fatalf("partial output size = %v, %v", info, err)
	}
	cancelRequest()
	close(releaseSecond)
	waitForManagedFile(t, store, time.Second, func(row database.ManagedFileRow) bool {
		return row.SavedFile && row.Status == "finished"
	})
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("completed output = %d/%d bytes, %v", len(got), len(content), err)
	}
}

func TestStartManagedFileUsesDefaultPath(t *testing.T) {
	content := []byte("pathless managed resume")
	source := filepath.Join(t.TempDir(), "default-name.bin")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := blob.CreateStreamDescriptor(source, nil)
	if err != nil {
		t.Fatal(err)
	}
	row := database.ManagedFileRow{
		StreamHash: created.Descriptor.StreamHash, SDHash: created.SDHash,
		Status: "stopped", SuggestedFileName: "default-name.bin", BlobsInStream: 1,
	}
	store := &managerTestStore{descriptor: created.Descriptor, rows: []database.ManagedFileRow{row}}
	blobs := blob.NewManager()
	if err := blobs.Set(created.SDHash, created.DescriptorBytes, true); err != nil {
		t.Fatal(err)
	}
	for hash, data := range created.Blobs {
		if err := blobs.Set(hash, data, false); err != nil {
			t.Fatal(err)
		}
	}
	destination := t.TempDir()
	manager := New(store, blobs, destination, WithDownloadTimeout(time.Second))
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = manager.Stop(context.Background()) }()
	if err := manager.StartManagedFile(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	waitForManagedFile(t, store, time.Second, func(row database.ManagedFileRow) bool { return row.SavedFile })
	store.mu.Lock()
	updated := store.rows[0]
	store.mu.Unlock()
	if updated.FileName == nil || updated.DownloadDirectory == nil || *updated.DownloadDirectory != destination {
		t.Fatalf("pathless resume row = %#v", updated)
	}
	got, err := os.ReadFile(filepath.Join(destination, *updated.FileName))
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("pathless output = %q, %v", got, err)
	}
}

func TestManagedSaveLogsReadableLifecycle(t *testing.T) {
	var output bytes.Buffer
	previousOutput, previousFlags := log.Writer(), log.Flags()
	log.SetOutput(&output)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(previousOutput)
		log.SetFlags(previousFlags)
	})
	content := []byte("logged managed save")
	source := filepath.Join(t.TempDir(), "logged.bin")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := blob.CreateStreamDescriptor(source, nil)
	if err != nil {
		t.Fatal(err)
	}
	row := database.ManagedFileRow{
		StreamHash: created.Descriptor.StreamHash, SDHash: created.SDHash,
		Status: "stopped", SuggestedFileName: "logged.bin", BlobsInStream: 1,
	}
	store := &managerTestStore{descriptor: created.Descriptor, rows: []database.ManagedFileRow{row}}
	blobs := blob.NewManager()
	if err := blobs.Set(created.SDHash, created.DescriptorBytes, true); err != nil {
		t.Fatal(err)
	}
	for hash, data := range created.Blobs {
		if err := blobs.Set(hash, data, false); err != nil {
			t.Fatal(err)
		}
	}
	manager := New(store, blobs, t.TempDir(), WithDownloadTimeout(time.Second))
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = manager.Stop(context.Background()) }()
	if _, err := manager.SaveManagedFile(context.Background(), row, nil, nil); err != nil {
		t.Fatal(err)
	}
	waitForManagedFile(t, store, time.Second, func(row database.ManagedFileRow) bool { return row.SavedFile })
	deadline := time.Now().Add(time.Second)
	active := 1
	for time.Now().Before(deadline) {
		manager.mu.RLock()
		active = len(manager.active)
		manager.mu.RUnlock()
		if active == 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if active != 0 {
		t.Fatal("save worker did not finish")
	}
	text := output.String()
	for _, want := range []string{"saving stream", "progress: 1/1 blobs (100%)", "saved stream"} {
		if !strings.Contains(text, want) {
			t.Fatalf("save log missing %q:\n%s", want, text)
		}
	}
	if count := strings.Count(text, " progress:"); count != 1 {
		t.Fatalf("progress log count = %d:\n%s", count, text)
	}
}

func TestCleanupPauseRestartsRunningDownload(t *testing.T) {
	content := bytes.Repeat([]byte{0x33}, blob.MaxBlobSize+64)
	source := filepath.Join(t.TempDir(), "cleanup.bin")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := blob.CreateStreamDescriptor(source, nil)
	if err != nil {
		t.Fatal(err)
	}
	dataBlobs := created.Descriptor.ContentBlobs()
	row := database.ManagedFileRow{
		StreamHash: created.Descriptor.StreamHash, SDHash: created.SDHash,
		Status: "stopped", SuggestedFileName: "cleanup.bin", BlobsInStream: int64(len(dataBlobs)),
	}
	store := &managerTestStore{descriptor: created.Descriptor, rows: []database.ManagedFileRow{row}}
	blobs := blob.NewManager()
	if err := blobs.Set(created.SDHash, created.DescriptorBytes, true); err != nil {
		t.Fatal(err)
	}
	var fetchMu sync.Mutex
	secondCalls := 0
	firstSecondStarted := make(chan struct{})
	blobs.SetFetcher(func(ctx context.Context, hash string) ([]byte, error) {
		if hash == dataBlobs[1].BlobHash {
			fetchMu.Lock()
			secondCalls++
			call := secondCalls
			fetchMu.Unlock()
			if call == 1 {
				close(firstSecondStarted)
				<-ctx.Done()
				return nil, ctx.Err()
			}
		}
		return created.Blobs[hash], nil
	})
	destination := t.TempDir()
	manager := New(store, blobs, destination, WithDownloadTimeout(time.Second))
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = manager.Stop(context.Background()) }()
	returned, err := manager.SaveManagedFile(context.Background(), row, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	<-firstSecondStarted
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Pause(ctx); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(destination, *returned.FileName)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("paused partial output remains: %v", err)
	}
	store.mu.Lock()
	status := store.rows[0].Status
	store.mu.Unlock()
	if status != "running" {
		t.Fatalf("cleanup pause persisted status %q", status)
	}
	manager.Resume()
	waitForManagedFile(t, store, 2*time.Second, func(row database.ManagedFileRow) bool { return row.SavedFile })
	store.mu.Lock()
	updated := store.rows[0]
	store.mu.Unlock()
	got, err := os.ReadFile(filepath.Join(*updated.DownloadDirectory, *updated.FileName))
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("resumed cleanup output = %d/%d bytes, %v", len(got), len(content), err)
	}
}

func TestTimedOutPauseEventuallyRestartsDownload(t *testing.T) {
	content := []byte("pause timeout recovery")
	source := filepath.Join(t.TempDir(), "pause-timeout.bin")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := blob.CreateStreamDescriptor(source, nil)
	if err != nil {
		t.Fatal(err)
	}
	contentHash := created.Descriptor.ContentBlobs()[0].BlobHash
	row := database.ManagedFileRow{
		StreamHash: created.Descriptor.StreamHash, SDHash: created.SDHash,
		Status: "stopped", SuggestedFileName: "pause-timeout.bin", BlobsInStream: 1,
	}
	hookStarted, releaseHook := make(chan struct{}), make(chan struct{})
	var hookMu sync.Mutex
	hookCalls := 0
	store := &managerTestStore{descriptor: created.Descriptor, rows: []database.ManagedFileRow{row}}
	store.markHook = func(hash string) {
		if hash != contentHash {
			return
		}
		hookMu.Lock()
		hookCalls++
		call := hookCalls
		hookMu.Unlock()
		if call == 1 {
			close(hookStarted)
			<-releaseHook
		}
	}
	blobs := blob.NewManager()
	if err := blobs.Set(created.SDHash, created.DescriptorBytes, true); err != nil {
		t.Fatal(err)
	}
	for hash, data := range created.Blobs {
		if err := blobs.Set(hash, data, false); err != nil {
			t.Fatal(err)
		}
	}
	manager := New(store, blobs, t.TempDir(), WithDownloadTimeout(time.Second))
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = manager.Stop(context.Background()) }()
	saveDone := make(chan struct{})
	go func() {
		_, _ = manager.SaveManagedFile(context.Background(), row, nil, nil)
		close(saveDone)
	}()
	<-hookStarted
	pauseContext, cancelPause := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancelPause()
	if err := manager.Pause(pauseContext); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Pause error = %v", err)
	}
	close(releaseHook)
	select {
	case <-saveDone:
	case <-time.After(time.Second):
		t.Fatal("canceled save call did not return")
	}
	waitForManagedFile(t, store, 2*time.Second, func(row database.ManagedFileRow) bool { return row.SavedFile })
	manager.mu.RLock()
	paused := manager.paused
	manager.mu.RUnlock()
	if paused {
		t.Fatal("manager remained paused after timed-out cleanup drained")
	}
}

func TestStopManagedFileWinsAgainstActiveResume(t *testing.T) {
	source := filepath.Join(t.TempDir(), "cancel-source.bin")
	if err := os.WriteFile(source, []byte("missing encrypted content"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := blob.CreateStreamDescriptor(source, nil)
	if err != nil {
		t.Fatal(err)
	}
	destination, name := t.TempDir(), "cancelled.bin"
	row := database.ManagedFileRow{
		StreamHash: created.Descriptor.StreamHash, SDHash: created.SDHash, FileName: &name,
		DownloadDirectory: &destination, Status: "running",
	}
	store := &managerTestStore{descriptor: created.Descriptor, rows: []database.ManagedFileRow{row}}
	blobs := blob.NewManager()
	if err := blobs.Set(created.SDHash, created.DescriptorBytes, true); err != nil {
		t.Fatal(err)
	}
	fetchStarted := make(chan struct{})
	blobs.SetFetcher(func(ctx context.Context, _ string) ([]byte, error) {
		select {
		case <-fetchStarted:
		default:
			close(fetchStarted)
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	manager := New(store, blobs, "")
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fetchStarted:
	case <-time.After(time.Second):
		t.Fatal("resume did not begin fetching")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.StopManagedFile(ctx, row); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	status := store.rows[0].Status
	store.mu.Unlock()
	if status != "stopped" {
		t.Fatalf("status after stop = %q", status)
	}
	if _, err := os.Stat(filepath.Join(destination, name)); !os.IsNotExist(err) {
		t.Fatalf("canceled output exists: %v", err)
	}
	if err := manager.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func waitForManagedFile(
	t *testing.T, store *managerTestStore, timeout time.Duration,
	condition func(database.ManagedFileRow) bool,
) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		store.mu.Lock()
		row := store.rows[0]
		store.mu.Unlock()
		if condition(row) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("managed file condition was not satisfied before timeout")
}
