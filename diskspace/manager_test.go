package diskspace

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"lbry/daemon/blob"
	"lbry/daemon/database"
)

type testStore struct {
	usage     database.BlobDiskUsage
	cleans    atomic.Int32
	cleanHook func()
}

func (store *testStore) StoredBlobDiskUsage(context.Context) (database.BlobDiskUsage, error) {
	return store.usage, nil
}

func (store *testStore) CleanManagedBlobs(
	context.Context, *blob.BlobManager, int64, int64,
) (int, error) {
	if store.cleanHook != nil {
		store.cleanHook()
	}
	store.cleans.Add(1)
	return 1, nil
}

type testDownloadController struct {
	paused  atomic.Bool
	pauses  atomic.Int32
	resumes atomic.Int32
}

func (controller *testDownloadController) Pause(context.Context) error {
	controller.pauses.Add(1)
	controller.paused.Store(true)
	return nil
}

func (controller *testDownloadController) Resume() {
	controller.resumes.Add(1)
	controller.paused.Store(false)
}

func TestCleanOwnsDownloadPauseBarrier(t *testing.T) {
	controller := &testDownloadController{}
	store := &testStore{}
	store.cleanHook = func() {
		if !controller.paused.Load() {
			t.Error("cleanup ran without pausing downloads")
		}
	}
	manager := New(store, blob.NewManager(), 1, 1, time.Hour)
	manager.SetDownloadController(controller)
	if deleted, err := manager.Clean(context.Background(), 1, 1); err != nil || deleted != 1 {
		t.Fatalf("clean = %d, %v", deleted, err)
	}
	if controller.pauses.Load() != 1 || controller.resumes.Load() != 1 || controller.paused.Load() {
		t.Fatalf("barrier pauses=%d resumes=%d paused=%t",
			controller.pauses.Load(), controller.resumes.Load(), controller.paused.Load())
	}
}

func TestManagerSleepsBeforeCleaningAndStops(t *testing.T) {
	store := &testStore{}
	manager := New(store, blob.NewManager(), 10, 5, 30*time.Millisecond)
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !manager.Running() || store.cleans.Load() != 0 {
		t.Fatalf("initial running=%t cleans=%d", manager.Running(), store.cleans.Load())
	}
	deadline := time.Now().Add(time.Second)
	for store.cleans.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if store.cleans.Load() == 0 {
		t.Fatal("scheduled cleanup did not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	count := store.cleans.Load()
	time.Sleep(50 * time.Millisecond)
	if manager.Running() || store.cleans.Load() != count {
		t.Fatalf("stopped running=%t cleans=%d->%d", manager.Running(), count, store.cleans.Load())
	}
}

func TestManagerStatusAndFreeSpaceMatchPinnedBuckets(t *testing.T) {
	const mb = int64(1024 * 1024)
	store := &testStore{usage: database.BlobDiskUsage{
		Total: 9*mb + 1, Network: 2*mb + 3, Content: 4*mb + 4, Private: 3*mb + 5,
	}}
	manager := New(store, blob.NewManager(), 10, 5, time.Hour)
	status := manager.Status(context.Background())
	if status["total_used_mb"] != int64(9) || status["seed_blobs_storage_used_mb"] != int64(2) ||
		status["content_blobs_storage_used_mb"] != int64(4) ||
		status["published_blobs_storage_used_mb"] != int64(3) || status["running"] != false {
		t.Fatalf("status = %#v", status)
	}
	if free, err := manager.FreeSpaceMB(context.Background(), false); err != nil || free != 6 {
		t.Fatalf("content free space = %d, %v", free, err)
	}
	if free, err := manager.FreeSpaceMB(context.Background(), true); err != nil || free != 3 {
		t.Fatalf("network free space = %d, %v", free, err)
	}
}
