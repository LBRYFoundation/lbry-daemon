package rpc

import (
	"context"
	"testing"

	blobpkg "lbry/daemon/blob"
)

type blobCleanerTestStore struct {
	*fileMutationTestStore
	contentLimit int64
	networkLimit int64
}

func (store *blobCleanerTestStore) CleanManagedBlobs(
	_ context.Context, _ *blobpkg.BlobManager, contentLimit, networkLimit int64,
) (int, error) {
	store.contentLimit, store.networkLimit = contentLimit, networkLimit
	return 2, nil
}

func TestBlobCleanUsesConfiguredLimitsAndReturnsNull(t *testing.T) {
	store := &blobCleanerTestStore{fileMutationTestStore: &fileMutationTestStore{}}
	server := CreateServer(WithManagedFileLister(store), WithBlobManager(blobpkg.NewManager()))
	if _, err := server.settings.Set("blob_storage_limit", 12); err != nil {
		t.Fatal(err)
	}
	if _, err := server.settings.Set("network_storage_limit", 3); err != nil {
		t.Fatal(err)
	}
	if result := fileMutationRPCResult(t, server, "blob_clean", map[string]any{}); result != nil {
		t.Fatalf("blob_clean = %#v", result)
	}
	if store.contentLimit != 12 || store.networkLimit != 3 {
		t.Fatalf("cleanup limits = %d/%d", store.contentLimit, store.networkLimit)
	}
}
