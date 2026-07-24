package dht

import (
	"context"
	"sync"
	"time"
)

type AnnouncementStore interface {
	BlobsToAnnounce(context.Context, bool, int, int64) ([]string, error)
	MarkBlobsAnnounced(context.Context, []string, int64) error
}

type BlobAnnouncer struct {
	store       AnnouncementStore
	announce    func(string) (int, error)
	headOnly    bool
	concurrency int
	interval    time.Duration
	now         func() time.Time
	cancel      context.CancelFunc
	done        chan struct{}
	startOnce   sync.Once
	stopOnce    sync.Once
}

func NewBlobAnnouncer(node *Node, store AnnouncementStore, headOnly bool, concurrency int) *BlobAnnouncer {
	if concurrency < 1 {
		concurrency = 1
	}
	return &BlobAnnouncer{
		store: store, headOnly: headOnly, concurrency: concurrency,
		interval: time.Minute, now: time.Now, done: make(chan struct{}),
		announce: func(hash string) (int, error) {
			peers, err := node.AnnounceBlob(hash)
			return len(peers), err
		},
	}
}

func (announcer *BlobAnnouncer) Start() {
	if announcer == nil || announcer.store == nil || announcer.announce == nil {
		return
	}
	announcer.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		announcer.cancel = cancel
		go announcer.run(ctx)
	})
}

func (announcer *BlobAnnouncer) Stop() {
	if announcer == nil || announcer.cancel == nil {
		return
	}
	announcer.stopOnce.Do(func() { announcer.cancel() })
	<-announcer.done
}

func (announcer *BlobAnnouncer) run(ctx context.Context) {
	defer close(announcer.done)
	ticker := time.NewTicker(announcer.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			announcer.round(ctx)
		}
	}
}

func (announcer *BlobAnnouncer) round(ctx context.Context) {
	hashes, err := announcer.store.BlobsToAnnounce(
		ctx, announcer.headOnly, announcer.concurrency*10, announcer.now().Unix(),
	)
	if err != nil || len(hashes) == 0 {
		return
	}
	jobs := make(chan string)
	successes := make(chan string, len(hashes))
	var workers sync.WaitGroup
	for range min(announcer.concurrency, len(hashes)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for hash := range jobs {
				peers, announceErr := announcer.announce(hash)
				if announceErr == nil && peers > 4 {
					successes <- hash
				}
			}
		}()
	}
	for _, hash := range hashes {
		select {
		case jobs <- hash:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return
		}
	}
	close(jobs)
	workers.Wait()
	close(successes)
	announced := make([]string, 0, len(successes))
	for hash := range successes {
		announced = append(announced, hash)
	}
	if len(announced) > 0 {
		_ = announcer.store.MarkBlobsAnnounced(ctx, announced, announcer.now().Unix())
	}
}
