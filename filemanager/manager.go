package filemanager

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"lbry/daemon/blob"
	"lbry/daemon/database"
	"lbry/daemon/reflector"
)

type Store interface {
	ListManagedFiles(context.Context) ([]database.ManagedFileRow, error)
	ReconcileManagedFilePaths(context.Context) error
	RecoverManagedDescriptor(context.Context, string) (*blob.StreamDescriptor, error)
	FinalizeManagedDescriptorRecovery(context.Context, string, string, string) error
	MarkManagedBlobsFinished(context.Context, []string) error
	CompleteManagedFileSave(context.Context, string) error
	ChangeManagedFileStatus(context.Context, string, string) error
	ChangeManagedFilePath(context.Context, string, *string, *string) error
	StopAllManagedFiles(context.Context) error
	MarkStreamReflected(context.Context, string, string) error
}

type Option func(*Manager)

type activeSave struct {
	ctx          context.Context
	cancel       context.CancelFunc
	done         chan struct{}
	firstWritten chan struct{}
	result       chan saveResult
	streamHash   string
}

type saveResult struct {
	bytes int64
	err   error
}

func WithReflection(enabled bool, servers []string, concurrency int, interval time.Duration) Option {
	return func(manager *Manager) {
		manager.reflectEnabled = enabled
		manager.reflectServers = append([]string(nil), servers...)
		if concurrency > 0 {
			manager.reflectConcurrency = concurrency
		}
		if interval > 0 {
			manager.reflectInterval = interval
		}
	}
}

func WithDownloadTimeout(timeout time.Duration) Option {
	return func(manager *Manager) {
		if timeout > 0 {
			manager.downloadTimeout = timeout
		}
	}
}

type Manager struct {
	store             Store
	blobs             *blob.BlobManager
	downloadDirectory string
	downloadTimeout   time.Duration

	mu                 sync.RWMutex
	startMu            sync.Mutex
	running            bool
	starting           bool
	cancel             context.CancelFunc
	runCtx             context.Context
	rows               map[string]database.ManagedFileRow
	saveTasks          sync.WaitGroup
	active             map[string]*activeSave
	paused             bool
	stopping           bool
	stopDone           chan struct{}
	ready              chan struct{}
	readyOnce          sync.Once
	reflectEnabled     bool
	reflectServers     []string
	reflectConcurrency int
	reflectInterval    time.Duration
	reflecting         map[string]struct{}
	reflectDone        chan struct{}
	reflectWake        chan struct{}
}

func New(
	store Store, blobs *blob.BlobManager, downloadDirectory string, options ...Option,
) *Manager {
	manager := &Manager{
		store: store, blobs: blobs, downloadDirectory: downloadDirectory,
		rows: make(map[string]database.ManagedFileRow), active: make(map[string]*activeSave),
		ready:              make(chan struct{}),
		downloadTimeout:    30 * time.Second,
		reflectConcurrency: 1, reflectInterval: 5 * time.Minute,
		reflecting:  make(map[string]struct{}),
		reflectWake: make(chan struct{}, 1),
	}
	for _, option := range options {
		option(manager)
	}
	return manager
}

func (manager *Manager) Start(ctx context.Context) error {
	if manager == nil || manager.store == nil || manager.blobs == nil {
		return errors.New("file manager dependencies are unavailable")
	}
	if ctx == nil {
		return errors.New("file manager context is nil")
	}
	manager.mu.Lock()
	if manager.running || manager.starting {
		manager.mu.Unlock()
		return nil
	}
	manager.starting = true
	manager.mu.Unlock()
	started := false
	defer func() {
		if !started {
			manager.mu.Lock()
			manager.starting = false
			manager.mu.Unlock()
		}
	}()
	if err := manager.store.ReconcileManagedFilePaths(ctx); err != nil {
		return fmt.Errorf("reconcile managed file paths: %w", err)
	}
	rows, err := manager.store.ListManagedFiles(ctx)
	if err != nil {
		return fmt.Errorf("load managed files: %w", err)
	}
	loaded := make(map[string]database.ManagedFileRow, len(rows))
	resume := make([]database.ManagedFileRow, 0)
	for _, row := range rows {
		if _, err := manager.loadDescriptor(ctx, row); err != nil {
			log.Printf("File manager: failed to restore stream %s: %v", row.SDHash, err)
			continue
		}
		loaded[row.SDHash] = row
		if row.FileName != nil && row.DownloadDirectory != nil && !row.SavedFile && row.Status == "running" {
			resume = append(resume, row)
		}
	}
	runCtx, cancel := context.WithCancel(ctx)
	manager.mu.Lock()
	manager.rows = loaded
	manager.starting = false
	manager.running = true
	manager.paused = false
	manager.stopDone = nil
	manager.cancel = cancel
	manager.runCtx = runCtx
	reflectDone := make(chan struct{})
	manager.reflectDone = reflectDone
	manager.mu.Unlock()
	manager.readyOnce.Do(func() { close(manager.ready) })
	started = true
	restoredCount := len(loaded)
	log.Printf("File manager: restored %d managed files.", restoredCount)
	for _, row := range resume {
		_ = manager.startSave(runCtx, row)
	}
	go func() {
		defer close(reflectDone)
		manager.reflectLoop(runCtx)
	}()
	return nil
}

func (manager *Manager) LookupManagedStream(
	ctx context.Context, sdHash string,
) (database.ManagedFileRow, bool, error) {
	if err := manager.WaitReady(ctx); err != nil {
		return database.ManagedFileRow{}, false, err
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if !manager.running {
		return database.ManagedFileRow{}, false, nil
	}
	row, ok := manager.rows[sdHash]
	return row, ok, nil
}

func (manager *Manager) WaitReady(ctx context.Context) error {
	if manager == nil || ctx == nil {
		return errors.New("file manager readiness is unavailable")
	}
	select {
	case <-manager.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (manager *Manager) RegisterManagedFile(ctx context.Context, row database.ManagedFileRow) error {
	if manager == nil || ctx == nil {
		return errors.New("file manager is unavailable")
	}
	if err := manager.WaitReady(ctx); err != nil {
		return err
	}
	manager.startMu.Lock()
	defer manager.startMu.Unlock()
	data, ok := manager.blobs.GetLocal(row.SDHash)
	if !ok {
		return fmt.Errorf("stream descriptor %s is unavailable", row.SDHash)
	}
	descriptor, err := blob.DecodeDescriptor(row.SDHash, data)
	if err != nil {
		return err
	}
	if descriptor.StreamHash != row.StreamHash {
		return errors.New("managed stream hash does not match descriptor")
	}
	persisted, err := manager.store.ListManagedFiles(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, candidate := range persisted {
		if candidate.SDHash == row.SDHash && candidate.StreamHash == row.StreamHash {
			found = true
			row = candidate
			break
		}
	}
	if !found {
		return errors.New("managed stream is no longer persisted")
	}
	manager.mu.Lock()
	if !manager.running {
		manager.mu.Unlock()
		return errors.New("file manager is not running")
	}
	manager.rows[row.SDHash] = row
	manager.mu.Unlock()
	manager.queueReflection()
	return nil
}

func (manager *Manager) ForgetManagedFile(sdHash string) {
	if manager == nil {
		return
	}
	manager.mu.Lock()
	delete(manager.rows, sdHash)
	manager.mu.Unlock()
}

func (manager *Manager) MarkManagedStreamActive(ctx context.Context, sdHash string) error {
	if manager == nil || ctx == nil {
		return errors.New("managed stream activation is unavailable")
	}
	manager.startMu.Lock()
	defer manager.startMu.Unlock()
	manager.mu.RLock()
	row, found := manager.rows[sdHash]
	manager.mu.RUnlock()
	if !found || row.Status != "stopped" {
		return nil
	}
	if err := manager.store.ChangeManagedFileStatus(ctx, row.StreamHash, "running"); err != nil {
		return err
	}
	manager.updateRowStatus(sdHash, "running")
	return nil
}

func (manager *Manager) StopManagedStreamIfIdle(ctx context.Context, sdHash string) error {
	if manager == nil || ctx == nil {
		return errors.New("managed stream idle transition is unavailable")
	}
	manager.startMu.Lock()
	defer manager.startMu.Unlock()
	manager.mu.RLock()
	row, found := manager.rows[sdHash]
	_, saving := manager.active[row.StreamHash]
	paused := manager.paused || manager.stopping || !manager.running
	manager.mu.RUnlock()
	if !found || saving || paused || row.Status != "running" {
		return nil
	}
	if err := manager.store.ChangeManagedFileStatus(ctx, row.StreamHash, "stopped"); err != nil {
		return err
	}
	manager.updateRowStatus(sdHash, "stopped")
	log.Printf("File manager: stopped inactive stream %s.", shortHash(sdHash))
	return nil
}

func (manager *Manager) Stop(ctx context.Context) error {
	if manager == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("file manager stop context is nil")
	}
	manager.startMu.Lock()
	manager.mu.Lock()
	if !manager.running {
		done := manager.stopDone
		manager.mu.Unlock()
		manager.startMu.Unlock()
		if done != nil {
			select {
			case <-done:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	manager.running = false
	manager.stopping = true
	done := make(chan struct{})
	manager.stopDone = done
	cancel := manager.cancel
	manager.mu.Unlock()
	manager.startMu.Unlock()
	if cancel != nil {
		cancel()
	}
	go func() {
		_ = manager.pause(context.Background(), false)
		manager.mu.RLock()
		reflectDone := manager.reflectDone
		manager.mu.RUnlock()
		if reflectDone != nil {
			<-reflectDone
		}
		manager.mu.Lock()
		manager.rows = make(map[string]database.ManagedFileRow)
		manager.stopping = false
		close(done)
		manager.mu.Unlock()
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (manager *Manager) StartManagedFile(ctx context.Context, row database.ManagedFileRow) error {
	_, err := manager.SaveManagedFile(ctx, row, nil, nil)
	return err
}

func (manager *Manager) StopManagedFile(ctx context.Context, row database.ManagedFileRow) error {
	manager.startMu.Lock()
	defer manager.startMu.Unlock()
	manager.mu.RLock()
	active, exists := manager.active[row.StreamHash]
	manager.mu.RUnlock()
	if exists {
		active.cancel()
		select {
		case <-active.done:
		case <-ctx.Done():
			_ = manager.store.ChangeManagedFileStatus(context.Background(), row.StreamHash, "stopped")
			manager.updateRowStatus(row.SDHash, "stopped")
			return ctx.Err()
		}
	}
	if current := manager.currentRow(row); current.Status == "finished" {
		return nil
	}
	if err := manager.store.ChangeManagedFileStatus(ctx, row.StreamHash, "stopped"); err != nil {
		return err
	}
	manager.updateRowStatus(row.SDHash, "stopped")
	log.Printf("File manager: stopped download %s.", shortHash(row.SDHash))
	return nil
}

func (manager *Manager) SaveManagedFile(
	ctx context.Context, row database.ManagedFileRow, fileName, downloadDirectory *string,
) (database.ManagedFileRow, error) {
	if ctx == nil {
		return row, errors.New("managed file context is nil")
	}
	manager.startMu.Lock()
	op, prepared, err := func() (*activeSave, database.ManagedFileRow, error) {
		manager.mu.RLock()
		runCtx := manager.runCtx
		accepting := manager.running && !manager.paused
		manager.mu.RUnlock()
		if runCtx == nil || !accepting {
			return nil, row, errors.New("file manager is not running")
		}
		if err := manager.cancelActive(ctx, row.StreamHash); err != nil {
			return nil, row, err
		}
		descriptorContext, cancelDescriptor := context.WithTimeout(ctx, manager.downloadTimeout)
		_, descriptorErr := manager.ensureDescriptor(descriptorContext, row)
		cancelDescriptor()
		if descriptorErr != nil {
			return nil, row, descriptorErr
		}
		prepared, prepareErr := manager.prepareSave(ctx, row, fileName, downloadDirectory)
		if prepareErr != nil {
			return nil, prepared, prepareErr
		}
		operation, beginErr := manager.beginSave(runCtx, prepared)
		if beginErr != nil {
			return nil, prepared, beginErr
		}
		manager.launchSave(operation, prepared)
		return operation, prepared, nil
	}()
	manager.startMu.Unlock()
	if err != nil {
		return prepared, err
	}
	row = prepared

	timer := time.NewTimer(manager.downloadTimeout)
	defer timer.Stop()
	select {
	case <-op.firstWritten:
		return manager.currentRow(row), nil
	case result := <-op.result:
		updated := manager.currentRow(row)
		if result.err != nil {
			return updated, nil
		}
		return updated, nil
	case <-timer.C:
		select {
		case <-op.firstWritten:
			return manager.currentRow(row), nil
		default:
		}
		op.cancel()
		<-op.done
		result := <-op.result
		updated := manager.currentRow(row)
		if result.err == nil || updated.Status == "finished" {
			return updated, nil
		}
		if err := manager.store.ChangeManagedFileStatus(context.Background(), row.StreamHash, "stopped"); err != nil {
			return row, err
		}
		manager.updateRowStatus(row.SDHash, "stopped")
		log.Printf("File manager: timed out waiting for stream %s to begin writing; download stopped.", shortHash(row.SDHash))
		return manager.currentRow(row), nil
	case <-ctx.Done():
		return row, ctx.Err()
	}
}

func (manager *Manager) Pause(ctx context.Context) error {
	return manager.pause(ctx, true)
}

func (manager *Manager) pause(ctx context.Context, stopFiles bool) error {
	manager.startMu.Lock()
	manager.mu.Lock()
	manager.paused = true
	cancels := make([]context.CancelFunc, 0, len(manager.active))
	for _, active := range manager.active {
		cancels = append(cancels, active.cancel)
	}
	manager.mu.Unlock()
	manager.startMu.Unlock()
	if stopFiles && len(cancels) > 0 {
		log.Printf("File manager: pausing %d active downloads for disk cleanup.", len(cancels))
	}
	for _, cancel := range cancels {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		manager.saveTasks.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		go func() {
			<-done
			manager.Resume()
		}()
		return ctx.Err()
	}
}

func (manager *Manager) Resume() {
	if manager == nil {
		return
	}
	manager.mu.Lock()
	if !manager.running || manager.stopping {
		manager.mu.Unlock()
		return
	}
	manager.paused = false
	runCtx := manager.runCtx
	manager.mu.Unlock()
	rows, err := manager.store.ListManagedFiles(context.Background())
	if err != nil {
		log.Printf("File manager: unable to resume downloads after disk cleanup: %v", err)
		return
	}
	resumed := 0
	for _, row := range rows {
		if row.Status != "running" || row.SavedFile || row.FileName == nil || row.DownloadDirectory == nil {
			continue
		}
		if err := manager.startSave(runCtx, row); err == nil {
			resumed++
		}
	}
	if resumed > 0 {
		log.Printf("File manager: resumed %d downloads after disk cleanup.", resumed)
	}
}

func (manager *Manager) startSave(ctx context.Context, row database.ManagedFileRow) error {
	manager.startMu.Lock()
	defer manager.startMu.Unlock()
	manager.mu.RLock()
	active := manager.active[row.StreamHash] != nil
	manager.mu.RUnlock()
	if active {
		return errors.New("managed file operation is already active")
	}
	prepared, err := manager.prepareSave(ctx, row, nil, nil)
	if err != nil {
		return err
	}
	op, err := manager.beginSave(ctx, prepared)
	if err != nil {
		return err
	}
	manager.launchSave(op, prepared)
	return nil
}

func (manager *Manager) beginSave(
	ctx context.Context, row database.ManagedFileRow,
) (*activeSave, error) {
	manager.mu.Lock()
	if !manager.running || manager.paused {
		manager.mu.Unlock()
		return nil, errors.New("file manager is not accepting downloads")
	}
	if _, exists := manager.active[row.StreamHash]; exists {
		manager.mu.Unlock()
		return nil, errors.New("managed file operation is already active")
	}
	saveCtx, cancel := context.WithCancel(ctx)
	op := &activeSave{
		ctx: saveCtx, cancel: cancel, done: make(chan struct{}), firstWritten: make(chan struct{}),
		result: make(chan saveResult, 1), streamHash: row.StreamHash,
	}
	manager.active[row.StreamHash] = op
	manager.saveTasks.Add(1)
	manager.mu.Unlock()
	return op, nil
}

func (manager *Manager) finishSave(op *activeSave) {
	manager.mu.Lock()
	if manager.active[op.streamHash] == op {
		delete(manager.active, op.streamHash)
	}
	manager.mu.Unlock()
	close(op.done)
	manager.saveTasks.Done()
}

func (manager *Manager) cancelActive(ctx context.Context, streamHash string) error {
	manager.mu.RLock()
	active := manager.active[streamHash]
	manager.mu.RUnlock()
	if active == nil {
		return nil
	}
	active.cancel()
	select {
	case <-active.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (manager *Manager) prepareSave(
	ctx context.Context, row database.ManagedFileRow, fileName, downloadDirectory *string,
) (database.ManagedFileRow, error) {
	directory := manager.downloadDirectory
	if row.DownloadDirectory != nil {
		directory = *row.DownloadDirectory
	}
	if downloadDirectory != nil {
		directory = *downloadDirectory
	}
	if directory == "" {
		return row, errors.New("no directory to download to")
	}
	name := row.SuggestedFileName
	if row.FileName != nil {
		name = *row.FileName
	}
	if fileName != nil {
		name = *fileName
	}
	if name == "" {
		return row, errors.New("no file name to download to")
	}
	if err := os.MkdirAll(directory, 0o777); err != nil {
		return row, err
	}
	name = nextAvailableName(directory, name)
	if err := manager.store.ChangeManagedFilePath(ctx, row.StreamHash, &name, &directory); err != nil {
		return row, err
	}
	if err := manager.store.ChangeManagedFileStatus(ctx, row.StreamHash, "running"); err != nil {
		return row, err
	}
	row.FileName, row.DownloadDirectory, row.Status = &name, &directory, "running"
	manager.mu.Lock()
	manager.rows[row.SDHash] = row
	manager.mu.Unlock()
	return row, nil
}

func (manager *Manager) launchSave(op *activeSave, row database.ManagedFileRow) {
	go func() {
		started := time.Now()
		path := filepath.Join(*row.DownloadDirectory, filepath.Base(*row.FileName))
		log.Printf("File manager: saving stream %s to %s.", shortHash(row.SDHash), path)
		written, err := manager.resumeSave(op.ctx, row, op.firstWritten)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			_ = manager.store.ChangeManagedFileStatus(context.Background(), row.StreamHash, "stopped")
			manager.updateRowStatus(row.SDHash, "stopped")
			log.Printf("File manager: stream %s failed: %v", shortHash(row.SDHash), err)
		} else if err == nil {
			log.Printf(
				"File manager: saved stream %s (%d bytes) in %s.",
				shortHash(row.SDHash), written, time.Since(started).Round(time.Millisecond),
			)
		}
		op.result <- saveResult{bytes: written, err: err}
		manager.finishSave(op)
	}()
}

func (manager *Manager) ensureDescriptor(
	ctx context.Context, row database.ManagedFileRow,
) (*blob.StreamDescriptor, error) {
	data, ok := manager.blobs.GetLocal(row.SDHash)
	if !ok {
		if err := manager.blobs.Ensure(ctx, row.SDHash); err != nil {
			return nil, err
		}
		data, ok = manager.blobs.GetLocal(row.SDHash)
	}
	if !ok {
		return nil, fmt.Errorf("descriptor %s is unavailable", row.SDHash)
	}
	descriptor, err := blob.DecodeDescriptor(row.SDHash, data)
	if err != nil {
		return nil, err
	}
	if row.StreamHash != "" && descriptor.StreamHash != row.StreamHash {
		return nil, fmt.Errorf("descriptor stream hash %s does not match %s", descriptor.StreamHash, row.StreamHash)
	}
	if err := manager.store.MarkManagedBlobsFinished(ctx, []string{row.SDHash}); err != nil {
		return nil, err
	}
	return descriptor, nil
}

func (manager *Manager) currentRow(fallback database.ManagedFileRow) database.ManagedFileRow {
	rows, err := manager.store.ListManagedFiles(context.Background())
	if err == nil {
		for _, row := range rows {
			if row.StreamHash == fallback.StreamHash {
				manager.mu.Lock()
				manager.rows[row.SDHash] = row
				manager.mu.Unlock()
				return row
			}
		}
	}
	manager.mu.RLock()
	row, ok := manager.rows[fallback.SDHash]
	manager.mu.RUnlock()
	if ok {
		return row
	}
	return fallback
}

func (manager *Manager) updateRowStatus(sdHash, status string) {
	manager.mu.Lock()
	row, ok := manager.rows[sdHash]
	if ok {
		row.Status = status
		manager.rows[sdHash] = row
	}
	manager.mu.Unlock()
}

func (manager *Manager) reflectLoop(ctx context.Context) {
	manager.reflectPass(ctx)
	ticker := time.NewTicker(manager.reflectInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-manager.reflectWake:
			manager.reflectPass(ctx)
		case <-ticker.C:
			manager.reflectPass(ctx)
		}
	}
}

func (manager *Manager) queueReflection() {
	if !manager.reflectEnabled || len(manager.reflectServers) == 0 {
		return
	}
	select {
	case manager.reflectWake <- struct{}{}:
	default:
	}
}

func (manager *Manager) reflectPass(ctx context.Context) {
	if !manager.reflectEnabled || len(manager.reflectServers) == 0 {
		return
	}
	rows, err := manager.store.ListManagedFiles(ctx)
	if err != nil {
		log.Printf("File manager: failed to select streams for reflection: %v", err)
		return
	}
	semaphore := make(chan struct{}, manager.reflectConcurrency)
	var batch sync.WaitGroup
	for index, row := range rows {
		if row.FullyReflected || !manager.reflectionEligible(row) {
			continue
		}
		manager.mu.Lock()
		if _, active := manager.reflecting[row.SDHash]; active {
			manager.mu.Unlock()
			continue
		}
		manager.reflecting[row.SDHash] = struct{}{}
		manager.mu.Unlock()
		server := manager.reflectServers[index%len(manager.reflectServers)]
		semaphore <- struct{}{}
		batch.Add(1)
		go func(row database.ManagedFileRow, server string) {
			defer batch.Done()
			defer func() { <-semaphore }()
			defer func() {
				manager.mu.Lock()
				delete(manager.reflecting, row.SDHash)
				manager.mu.Unlock()
			}()
			hashes, reflectErr := reflector.ReflectStream(ctx, server, manager.blobs, row.SDHash)
			if reflectErr != nil {
				return
			}
			if manager.reflectionComplete(row.SDHash, hashes) {
				_ = manager.store.MarkStreamReflected(ctx, row.SDHash, server)
			}
		}(row, server)
	}
	batch.Wait()
}

func (manager *Manager) reflectionEligible(row database.ManagedFileRow) bool {
	data, ok := manager.blobs.GetLocal(row.SDHash)
	if !ok {
		return false
	}
	descriptor, err := blob.DecodeDescriptor(row.SDHash, data)
	if err != nil {
		return false
	}
	for _, info := range descriptor.ContentBlobs() {
		if manager.blobs.Has(info.BlobHash) {
			return true
		}
	}
	return false
}

func (manager *Manager) reflectionComplete(sdHash string, reflected []string) bool {
	if len(reflected) == 0 {
		return true
	}
	data, ok := manager.blobs.GetLocal(sdHash)
	if !ok {
		return false
	}
	descriptor, err := blob.DecodeDescriptor(sdHash, data)
	return err == nil && len(reflected) == len(descriptor.ContentBlobs())+1
}

func (manager *Manager) Running() bool {
	if manager == nil {
		return false
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.running
}

func (manager *Manager) ManagedFileCount() int {
	if manager == nil {
		return 0
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return len(manager.rows)
}

func (manager *Manager) Status() map[string]any {
	if !manager.Running() {
		return nil
	}
	count := manager.ManagedFileCount()
	if rows, err := manager.store.ListManagedFiles(context.Background()); err == nil {
		count = 0
		manager.mu.RLock()
		for _, row := range rows {
			_, restored := manager.rows[row.SDHash]
			if !restored {
				if data, ok := manager.blobs.GetLocal(row.SDHash); ok {
					descriptor, parseErr := blob.DecodeDescriptor(row.SDHash, data)
					restored = parseErr == nil && descriptor.StreamHash == row.StreamHash
				}
			}
			if restored {
				count++
			}
		}
		manager.mu.RUnlock()
	}
	return map[string]any{"managed_files": count}
}

func (manager *Manager) loadDescriptor(
	ctx context.Context, row database.ManagedFileRow,
) (*blob.StreamDescriptor, error) {
	if data, ok := manager.blobs.GetLocal(row.SDHash); ok {
		descriptor, err := blob.DecodeDescriptor(row.SDHash, data)
		if err == nil && descriptor.StreamHash == row.StreamHash {
			return descriptor, nil
		}
	}
	descriptor, err := manager.store.RecoverManagedDescriptor(ctx, row.StreamHash)
	if err != nil {
		return nil, err
	}
	if err := blob.ValidateDescriptor(descriptor); err != nil {
		return nil, err
	}
	data, err := blob.MarshalDescriptor(descriptor)
	if err != nil {
		return nil, err
	}
	digest := sha512.Sum384(data)
	if actual := hex.EncodeToString(digest[:]); actual != row.SDHash {
		data, err = blob.MarshalOldSortDescriptor(descriptor)
		if err != nil {
			return nil, err
		}
		digest = sha512.Sum384(data)
		if oldActual := hex.EncodeToString(digest[:]); oldActual != row.SDHash {
			return nil, fmt.Errorf(
				"recovered descriptor hashes %s (canonical) and %s (legacy) do not match %s",
				actual, oldActual, row.SDHash,
			)
		}
	}
	if _, err := blob.DecodeDescriptor(row.SDHash, data); err != nil {
		return nil, err
	}
	if err := manager.blobs.Set(row.SDHash, data, true); err != nil {
		return nil, err
	}
	if err := manager.store.FinalizeManagedDescriptorRecovery(
		ctx, row.StreamHash, descriptor.SuggestedFileName, manager.downloadDirectory,
	); err != nil {
		return nil, err
	}
	finished := []string{row.SDHash}
	for _, info := range descriptor.ContentBlobs() {
		if manager.blobs.Has(info.BlobHash) {
			finished = append(finished, info.BlobHash)
		}
	}
	if err := manager.store.MarkManagedBlobsFinished(ctx, finished); err != nil {
		return nil, err
	}
	return descriptor, nil
}

func (manager *Manager) resumeSave(
	ctx context.Context, row database.ManagedFileRow, firstWritten chan struct{},
) (written int64, resultErr error) {
	descriptor, err := manager.ensureDescriptor(ctx, row)
	if err != nil {
		return 0, err
	}
	if row.FileName == nil || row.DownloadDirectory == nil {
		return 0, errors.New("managed file output path is unavailable")
	}
	if err := os.MkdirAll(*row.DownloadDirectory, 0o777); err != nil {
		return 0, err
	}
	path := filepath.Join(*row.DownloadDirectory, filepath.Base(*row.FileName))
	output, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, err
	}
	completed, total, nextProgress := 0, len(descriptor.Blobs)-1, 25
	defer func() {
		if output != nil {
			closeErr := output.Close()
			if resultErr == nil && closeErr != nil {
				resultErr = closeErr
			}
		}
		if resultErr != nil {
			_ = os.Remove(path)
		}
	}()
	err = blob.WalkStream(ctx, manager.blobs, descriptor, func(info blob.BlobInfo, data []byte) error {
		if err := manager.store.MarkManagedBlobsFinished(ctx, []string{info.BlobHash}); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		count, writeErr := output.Write(data)
		written += int64(count)
		if writeErr != nil {
			return writeErr
		}
		if count != len(data) {
			return io.ErrShortWrite
		}
		completed++
		if completed == 1 {
			close(firstWritten)
		}
		percent := completed * 100 / max(total, 1)
		if completed == 1 || completed == total || percent >= nextProgress {
			log.Printf(
				"File manager: stream %s progress: %d/%d blobs (%d%%).",
				shortHash(row.SDHash), completed, total, percent,
			)
			for nextProgress <= percent {
				nextProgress += 25
			}
		}
		return nil
	})
	if err != nil {
		return written, err
	}
	if err := output.Sync(); err != nil {
		return written, err
	}
	if err := output.Close(); err != nil {
		return written, err
	}
	output = nil
	if err := manager.store.CompleteManagedFileSave(ctx, row.StreamHash); err != nil {
		return written, err
	}
	row.SavedFile, row.Status = true, "finished"
	manager.mu.Lock()
	manager.rows[row.SDHash] = row
	manager.mu.Unlock()
	return written, nil
}

func nextAvailableName(directory, fileName string) string {
	fileName = filepath.Base(fileName)
	extension := filepath.Ext(fileName)
	base := fileName[:len(fileName)-len(extension)]
	for index, candidate := 0, fileName; ; index++ {
		if info, err := os.Stat(filepath.Join(directory, candidate)); err != nil || info.IsDir() {
			return candidate
		}
		candidate = fmt.Sprintf("%s_%d%s", base, index+1, extension)
	}
}

func shortHash(hash string) string {
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}
