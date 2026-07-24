package diskspace

import (
	"context"
	"errors"
	"sync"
	"time"

	"lbry/daemon/blob"
	"lbry/daemon/database"
)

const DefaultCleaningInterval = 30 * time.Minute

type Store interface {
	StoredBlobDiskUsage(context.Context) (database.BlobDiskUsage, error)
	CleanManagedBlobs(context.Context, *blob.BlobManager, int64, int64) (int, error)
}

type DownloadController interface {
	Pause(context.Context) error
	Resume()
}

type Manager struct {
	store        Store
	blobs        *blob.BlobManager
	contentLimit int64
	networkLimit int64
	interval     time.Duration

	mu        sync.RWMutex
	running   bool
	cancel    context.CancelFunc
	done      chan struct{}
	cached    *database.BlobDiskUsage
	cleanMu   sync.Mutex
	downloads DownloadController
}

func (manager *Manager) SetDownloadController(controller DownloadController) {
	if manager == nil {
		return
	}
	manager.mu.Lock()
	manager.downloads = controller
	manager.mu.Unlock()
}

func New(
	store Store, blobs *blob.BlobManager, contentLimitMB, networkLimitMB int64, interval time.Duration,
) *Manager {
	if interval == 0 {
		interval = DefaultCleaningInterval
	}
	return &Manager{
		store: store, blobs: blobs, contentLimit: contentLimitMB,
		networkLimit: networkLimitMB, interval: interval,
	}
}

func (manager *Manager) Start(ctx context.Context) error {
	if manager == nil || manager.store == nil || manager.blobs == nil {
		return errors.New("disk space manager dependencies are unavailable")
	}
	if ctx == nil {
		return errors.New("disk space manager context is nil")
	}
	manager.mu.Lock()
	if manager.running {
		manager.mu.Unlock()
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	manager.running = true
	manager.cancel = cancel
	manager.done = done
	manager.mu.Unlock()
	go manager.run(runCtx, done)
	return nil
}

func (manager *Manager) run(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(manager.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = manager.Clean(ctx, manager.contentLimit, manager.networkLimit)
		}
	}
}

func (manager *Manager) Stop(ctx context.Context) error {
	if manager == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("disk space manager stop context is nil")
	}
	manager.mu.Lock()
	if !manager.running {
		manager.mu.Unlock()
		return nil
	}
	manager.running = false
	cancel, done := manager.cancel, manager.done
	manager.mu.Unlock()
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (manager *Manager) Running() bool {
	if manager == nil {
		return false
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.running
}

func (manager *Manager) Clean(ctx context.Context, contentLimitMB, networkLimitMB int64) (int, error) {
	if manager == nil || manager.store == nil || manager.blobs == nil {
		return 0, errors.New("disk space manager dependencies are unavailable")
	}
	manager.cleanMu.Lock()
	defer manager.cleanMu.Unlock()
	manager.mu.RLock()
	downloads := manager.downloads
	manager.mu.RUnlock()
	if downloads != nil {
		if err := downloads.Pause(ctx); err != nil {
			return 0, err
		}
		defer downloads.Resume()
	}
	deleted, err := manager.store.CleanManagedBlobs(
		ctx, manager.blobs, contentLimitMB, networkLimitMB,
	)
	manager.mu.Lock()
	manager.cached = nil
	manager.mu.Unlock()
	return deleted, err
}

// CleanManagedBlobs lets blob_clean share the manager's serialization and cache invalidation.
func (manager *Manager) CleanManagedBlobs(
	ctx context.Context, _ *blob.BlobManager, contentLimitMB, networkLimitMB int64,
) (int, error) {
	return manager.Clean(ctx, contentLimitMB, networkLimitMB)
}

func (manager *Manager) Usage(ctx context.Context, cached bool) (database.BlobDiskUsage, error) {
	if manager == nil || manager.store == nil {
		return database.BlobDiskUsage{}, errors.New("disk space manager is unavailable")
	}
	if cached {
		manager.mu.RLock()
		if manager.cached != nil {
			usage := *manager.cached
			manager.mu.RUnlock()
			return usage, nil
		}
		manager.mu.RUnlock()
	}
	usage, err := manager.store.StoredBlobDiskUsage(ctx)
	if err != nil {
		return database.BlobDiskUsage{}, err
	}
	manager.mu.Lock()
	manager.cached = &usage
	manager.mu.Unlock()
	return usage, nil
}

func (manager *Manager) Status(ctx context.Context) map[string]any {
	usage, err := manager.Usage(ctx, true)
	if err != nil {
		return map[string]any{"running": manager.Running()}
	}
	const megabyte = int64(1024 * 1024)
	return map[string]any{
		"total_used_mb":                   usage.Total / megabyte,
		"published_blobs_storage_used_mb": usage.Private / megabyte,
		"content_blobs_storage_used_mb":   usage.Content / megabyte,
		"seed_blobs_storage_used_mb":      usage.Network / megabyte,
		"running":                         manager.Running(),
	}
}

func (manager *Manager) FreeSpaceMB(ctx context.Context, network bool) (int64, error) {
	usage, err := manager.Usage(ctx, false)
	if err != nil {
		return 0, err
	}
	const megabyte = int64(1024 * 1024)
	used, limit := usage.Content/megabyte, manager.contentLimit
	if network {
		used, limit = usage.Network/megabyte, manager.networkLimit
	}
	if available := limit - used; available > 0 {
		return available, nil
	}
	return 0, nil
}
