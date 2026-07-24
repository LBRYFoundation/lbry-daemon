package trackerannouncer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"lbry/daemon/blob"
	"lbry/daemon/database"
)

const defaultInterval = time.Minute

type Store interface {
	ListManagedFiles(context.Context) ([]database.ManagedFileRow, error)
}

type Option func(*Manager)

func WithInterval(interval time.Duration) Option {
	return func(manager *Manager) {
		if interval > 0 {
			manager.interval = interval
		}
	}
}

type Manager struct {
	store    Store
	servers  []string
	port     int
	interval time.Duration

	mu      sync.RWMutex
	running bool
	cancel  context.CancelFunc
	done    chan struct{}
}

func New(store Store, servers []string, port int, options ...Option) *Manager {
	manager := &Manager{
		store: store, servers: append([]string(nil), servers...),
		port: port, interval: defaultInterval,
	}
	for _, option := range options {
		option(manager)
	}
	return manager
}

func (manager *Manager) Start(ctx context.Context) error {
	if manager == nil || manager.store == nil {
		return errors.New("tracker announcer store is unavailable")
	}
	if ctx == nil {
		return errors.New("tracker announcer context is nil")
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
	log.Printf("Tracker announcer: started with %d server(s).", len(manager.servers))
	go manager.run(runCtx, done)
	return nil
}

func (manager *Manager) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	defer func() {
		manager.mu.Lock()
		manager.running = false
		manager.cancel = nil
		manager.mu.Unlock()
		log.Printf("Tracker announcer: stopped.")
	}()
	manager.announce(ctx)
	ticker := time.NewTicker(manager.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			manager.announce(ctx)
		}
	}
}

func (manager *Manager) announce(ctx context.Context) {
	rows, err := manager.store.ListManagedFiles(ctx)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("Tracker announcer: could not load managed streams: %v", err)
		}
		return
	}
	if len(rows) == 0 || len(manager.servers) == 0 {
		return
	}
	var wait sync.WaitGroup
	errorsByServer := make(chan error, len(rows)*len(manager.servers))
	for _, row := range rows {
		if row.SDHash == "" {
			continue
		}
		for _, server := range manager.servers {
			wait.Add(1)
			go func(server, hash string) {
				defer wait.Done()
				if err := blob.AnnounceUDPTracker(ctx, server, hash, manager.port); err != nil {
					errorsByServer <- fmt.Errorf("%s: %w", server, err)
				}
			}(server, row.SDHash)
		}
	}
	wait.Wait()
	close(errorsByServer)
	failures := 0
	var first error
	for err := range errorsByServer {
		failures++
		if first == nil {
			first = err
		}
	}
	if ctx.Err() != nil {
		return
	}
	attempts := len(rows) * len(manager.servers)
	if failures == 0 {
		log.Printf("Tracker announcer: announced %d stream(s) to %d server(s).", len(rows), len(manager.servers))
	} else {
		log.Printf("Tracker announcer: %d of %d announcements failed (first error: %v).", failures, attempts, first)
	}
}

func (manager *Manager) Stop(ctx context.Context) error {
	if manager == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("tracker announcer stop context is nil")
	}
	manager.mu.RLock()
	cancel, done := manager.cancel, manager.done
	manager.mu.RUnlock()
	if cancel == nil || done == nil {
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
