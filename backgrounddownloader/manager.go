package backgrounddownloader

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math/bits"
	"sync"
	"time"

	"lbry/daemon/blob"
	"lbry/daemon/dht"
)

const (
	DefaultPollInterval            = 60 * time.Second
	DefaultDescriptorTimeout       = 30 * time.Second
	DefaultStreamBlobTimeout       = 300 * time.Second
	MinimumFreeSpaceMB       int64 = 10
	MinimumPrefixBits              = 8
)

type Node interface {
	NodeID() [dht.HashSize]byte
	StoredBlobHashes() [][dht.HashSize]byte
}

type BlobStore interface {
	Has(string) bool
	Ensure(context.Context, string) error
	GetLocal(string) ([]byte, bool)
}

type SpaceManager interface {
	FreeSpaceMB(context.Context, bool) (int64, error)
}

type Logger func(string, ...any)

type Option func(*Manager)

func WithPollInterval(interval time.Duration) Option {
	return func(manager *Manager) {
		if interval > 0 {
			manager.pollInterval = interval
		}
	}
}

func WithDownloadTimeouts(descriptor, streamBlob time.Duration) Option {
	return func(manager *Manager) {
		if descriptor > 0 {
			manager.descriptorTimeout = descriptor
		}
		if streamBlob > 0 {
			manager.streamBlobTimeout = streamBlob
		}
	}
}

func WithLogger(logger Logger) Option {
	return func(manager *Manager) {
		if logger != nil {
			manager.logf = logger
		}
	}
}

type Manager struct {
	node  Node
	blobs BlobStore
	space SpaceManager

	pollInterval      time.Duration
	descriptorTimeout time.Duration
	streamBlobTimeout time.Duration
	logf              Logger

	mu             sync.RWMutex
	running        bool
	ongoing        bool
	spaceAvailable *int64
	cancel         context.CancelFunc
	done           chan struct{}
	workerCancel   context.CancelFunc
	lowSpace       bool
	lowSpaceKnown  bool
}

type downloadResult struct {
	hash     string
	blobs    int
	bytes    int64
	duration time.Duration
	err      error
}

func New(node Node, blobs BlobStore, space SpaceManager, options ...Option) *Manager {
	manager := &Manager{
		node: node, blobs: blobs, space: space,
		pollInterval: DefaultPollInterval, descriptorTimeout: DefaultDescriptorTimeout,
		streamBlobTimeout: DefaultStreamBlobTimeout, logf: log.Printf,
	}
	for _, option := range options {
		option(manager)
	}
	return manager
}

func (manager *Manager) Start(ctx context.Context) error {
	if manager == nil || manager.blobs == nil || manager.space == nil {
		return errors.New("background downloader dependencies are unavailable")
	}
	if ctx == nil {
		return errors.New("background downloader context is nil")
	}
	manager.mu.Lock()
	if manager.running {
		manager.mu.Unlock()
		return nil
	}
	if manager.node == nil {
		manager.mu.Unlock()
		manager.logf("Background downloader: inactive because DHT is disabled.")
		return nil
	}
	runContext, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	manager.running = true
	manager.cancel = cancel
	manager.done = done
	manager.mu.Unlock()
	manager.logf("Background downloader: started; checking network storage every %s.", manager.pollInterval)
	go manager.run(runContext, done)
	return nil
}

func (manager *Manager) run(ctx context.Context, done chan struct{}) {
	defer func() {
		manager.mu.Lock()
		manager.running = false
		manager.ongoing = false
		manager.workerCancel = nil
		manager.mu.Unlock()
		close(done)
	}()

	ticker := time.NewTicker(manager.pollInterval)
	defer ticker.Stop()
	results := make(chan downloadResult, 1)
	if err := manager.poll(ctx, results); err != nil {
		manager.logPollFailure(err)
		return
	}
	for {
		select {
		case <-ctx.Done():
			manager.cancelWorker()
			if manager.OngoingDownload() {
				<-results
			}
			manager.logf("Background downloader: stopped.")
			return
		case result := <-results:
			manager.finishDownload(result)
		case <-ticker.C:
			if err := manager.poll(ctx, results); err != nil {
				manager.cancelWorker()
				if manager.OngoingDownload() {
					<-results
				}
				manager.logPollFailure(err)
				return
			}
		}
	}
}

func (manager *Manager) logPollFailure(err error) {
	if errors.Is(err, context.Canceled) {
		manager.logf("Background downloader: stopped.")
		return
	}
	manager.logf("Background downloader: stopped because network storage could not be checked: %v", err)
}

func (manager *Manager) poll(ctx context.Context, results chan<- downloadResult) error {
	available, err := manager.space.FreeSpaceMB(ctx, true)
	if err != nil {
		return err
	}
	manager.mu.Lock()
	value := available
	manager.spaceAvailable = &value
	wasLow, knewSpace := manager.lowSpace, manager.lowSpaceKnown
	manager.lowSpace = available <= MinimumFreeSpaceMB
	manager.lowSpaceKnown = true
	busy := manager.ongoing
	manager.mu.Unlock()

	if available <= MinimumFreeSpaceMB {
		if !knewSpace || !wasLow {
			manager.logf(
				"Background downloader: paused with %d MB of network storage available; more than %d MB is required.",
				available, MinimumFreeSpaceMB,
			)
		}
		return nil
	}
	if knewSpace && wasLow {
		manager.logf("Background downloader: resumed with %d MB of network storage available.", available)
	}
	if busy {
		return nil
	}

	nodeID := manager.node.NodeID()
	for _, hash := range manager.node.StoredBlobHashes() {
		hashText := hex.EncodeToString(hash[:])
		if manager.blobs.Has(hashText) {
			continue
		}
		prefixBits := CollidingPrefixBits(nodeID, hash)
		if prefixBits < MinimumPrefixBits {
			continue
		}
		workerContext, cancel := context.WithCancel(ctx)
		manager.mu.Lock()
		if manager.ongoing {
			manager.mu.Unlock()
			cancel()
			return nil
		}
		manager.ongoing = true
		manager.workerCancel = cancel
		manager.mu.Unlock()
		manager.logf(
			"Background downloader: caching stream %s for network seeding (%d matching prefix bits, %d MB free).",
			shortHash(hashText), prefixBits, available,
		)
		go func() {
			started := time.Now()
			count, size, downloadErr := manager.downloadStream(workerContext, hashText)
			results <- downloadResult{
				hash: hashText, blobs: count, bytes: size,
				duration: time.Since(started), err: downloadErr,
			}
		}()
		return nil
	}
	return nil
}

func (manager *Manager) downloadStream(ctx context.Context, sdHash string) (int, int64, error) {
	descriptorContext, cancel := context.WithTimeout(ctx, manager.descriptorTimeout)
	err := manager.blobs.Ensure(descriptorContext, sdHash)
	cancel()
	if err != nil {
		return 0, 0, fmt.Errorf("download stream descriptor: %w", err)
	}
	descriptorData, ok := manager.blobs.GetLocal(sdHash)
	if !ok {
		return 0, 0, errors.New("downloaded stream descriptor is unavailable")
	}
	descriptor, err := blob.DecodeDescriptor(sdHash, descriptorData)
	if err != nil {
		return 1, int64(len(descriptorData)), err
	}
	count, size := 1, int64(len(descriptorData))
	for _, item := range descriptor.Blobs[:len(descriptor.Blobs)-1] {
		blobContext, blobCancel := context.WithTimeout(ctx, manager.streamBlobTimeout)
		err = manager.blobs.Ensure(blobContext, item.BlobHash)
		blobCancel()
		if err != nil {
			return count, size, fmt.Errorf("download stream blob %d: %w", item.BlobNum, err)
		}
		count++
		size += int64(item.Length)
	}
	return count, size, nil
}

func (manager *Manager) finishDownload(result downloadResult) {
	manager.mu.Lock()
	manager.ongoing = false
	manager.workerCancel = nil
	manager.mu.Unlock()
	if result.err != nil {
		if errors.Is(result.err, context.Canceled) {
			return
		}
		manager.logf("Background downloader: stream %s failed: %v", shortHash(result.hash), result.err)
		return
	}
	manager.logf(
		"Background downloader: cached stream %s (%d blobs, %d bytes) in %s.",
		shortHash(result.hash), result.blobs, result.bytes, result.duration.Round(time.Millisecond),
	)
}

func (manager *Manager) cancelWorker() {
	manager.mu.RLock()
	cancel := manager.workerCancel
	manager.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
}

func (manager *Manager) Stop(ctx context.Context) error {
	if manager == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("background downloader stop context is nil")
	}
	manager.mu.RLock()
	cancel, done, running := manager.cancel, manager.done, manager.running
	manager.mu.RUnlock()
	if !running {
		return nil
	}
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

func (manager *Manager) OngoingDownload() bool {
	if manager == nil {
		return false
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.ongoing
}

func (manager *Manager) Status() map[string]any {
	if manager == nil {
		return map[string]any{
			"running": false, "available_free_space_mb": nil, "ongoing_download": false,
		}
	}
	manager.mu.RLock()
	var available any
	if manager.spaceAvailable != nil {
		available = *manager.spaceAvailable
	}
	status := map[string]any{
		"running": manager.running, "available_free_space_mb": available,
		"ongoing_download": manager.ongoing,
	}
	manager.mu.RUnlock()
	return status
}

func CollidingPrefixBits(first, second [dht.HashSize]byte) int {
	matching := 0
	for index := range first {
		difference := first[index] ^ second[index]
		if difference == 0 {
			matching += 8
			continue
		}
		return matching + bits.LeadingZeros8(difference)
	}
	return len(first) * 8
}

func shortHash(hash string) string {
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}
