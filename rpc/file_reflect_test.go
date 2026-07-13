package rpc

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	blobpkg "lbry/daemon/blob"
	databasepkg "lbry/daemon/database"
	reflectorpkg "lbry/daemon/reflector"
	walletpkg "lbry/daemon/wallet"
)

type reflectorTestStore struct {
	*fileListTestStore
	reflected [][2]string
}

func (store *reflectorTestStore) MarkStreamReflected(_ context.Context, sdHash, address string) error {
	store.reflected = append(store.reflected, [2]string{sdHash, address})
	return nil
}

func TestFileReflectFiltersAndReturnsUploadedHashes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reflect.bin")
	if err := os.WriteFile(path, bytes.Repeat([]byte("rpc reflect"), 100), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := blobpkg.CreateStreamDescriptor(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	source := blobpkg.NewManager()
	if err := source.Set(created.SDHash, created.DescriptorBytes, true); err != nil {
		t.Fatal(err)
	}
	for hash, data := range created.Blobs {
		if err := source.Set(hash, data, false); err != nil {
			t.Fatal(err)
		}
	}
	destination := blobpkg.NewManager()
	reflectorServer := reflectorpkg.CreateServer(destination)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go reflectorServer.Serve(listener)
	t.Cleanup(func() { _ = reflectorServer.Shutdown(context.Background()) })
	host, portText, _ := net.SplitHostPort(listener.Addr().String())

	store := &reflectorTestStore{fileListTestStore: &fileListTestStore{rows: []databasepkg.ManagedFileRow{
		{RowID: 1, StreamHash: created.Descriptor.StreamHash, SDHash: created.SDHash, SuggestedFileName: "reflect.bin"},
		{RowID: 2, StreamHash: "other", SDHash: "missing", SuggestedFileName: "other.bin"},
	}}}
	server := CreateServer(
		WithWalletManagerProvider(func() *walletpkg.WalletManager { return nil }),
		WithManagedFileLister(store), WithBlobManager(source),
	)
	result := fileMutationRPCResult(t, server, "file_reflect", map[string]any{
		"rowid": 1, "server": host, "port": portText,
	}).([]any)
	if len(result) != len(created.Blobs)+1 || !destination.Has(created.SDHash) {
		t.Fatalf("file_reflect = %#v", result)
	}
	if len(store.reflected) != 1 || store.reflected[0] != [2]string{created.SDHash, listener.Addr().String()} {
		t.Fatalf("persisted reflections = %#v", store.reflected)
	}
}
