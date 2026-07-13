package dht

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type announcerTestStore struct {
	hashes []string
	marked []string
	now    int64
}

func (store *announcerTestStore) BlobsToAnnounce(
	context.Context, bool, int, int64,
) ([]string, error) {
	return append([]string(nil), store.hashes...), nil
}

func (store *announcerTestStore) MarkBlobsAnnounced(_ context.Context, hashes []string, now int64) error {
	store.marked, store.now = append([]string(nil), hashes...), now
	return nil
}

func TestBlobAnnouncerMarksOnlyPublicationsStoredToFivePeers(t *testing.T) {
	store := &announcerTestStore{hashes: []string{"enough", "few", "failed"}}
	announcer := &BlobAnnouncer{
		store: store, headOnly: true, concurrency: 2,
		now: func() time.Time { return time.Unix(1234, 0) },
		announce: func(hash string) (int, error) {
			switch hash {
			case "enough":
				return 5, nil
			case "failed":
				return 0, errors.New("failed")
			default:
				return 4, nil
			}
		},
	}
	announcer.round(context.Background())
	if !reflect.DeepEqual(store.marked, []string{"enough"}) || store.now != 1234 {
		t.Fatalf("marked announcements = %v at %d", store.marked, store.now)
	}
}
