package reflector

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"lbry/daemon/blob"
)

func TestReflectStreamUploadsDescriptorAndContent(t *testing.T) {
	content := bytes.Repeat([]byte("reflect me"), 200)
	path := filepath.Join(t.TempDir(), "source.bin")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := blob.CreateStreamDescriptor(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	source := blob.NewManager()
	if err := source.Set(created.SDHash, created.DescriptorBytes, true); err != nil {
		t.Fatal(err)
	}
	for hash, data := range created.Blobs {
		if err := source.Set(hash, data, false); err != nil {
			t.Fatal(err)
		}
	}

	destination := blob.NewManager()
	server := CreateServer(destination)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })

	reflected, err := ReflectStream(context.Background(), listener.Addr().String(), source, created.SDHash)
	if err != nil {
		t.Fatal(err)
	}
	if len(reflected) != len(created.Blobs)+1 || !destination.Has(created.SDHash) {
		t.Fatalf("reflected = %#v", reflected)
	}
	for hash := range created.Blobs {
		if !destination.Has(hash) {
			t.Fatalf("destination is missing %s", hash)
		}
	}
}
