package blob

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateStreamDescriptorMatchesPinnedPythonVector(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := CreateStreamDescriptor(path, bytes.NewReader(sequenceBytes(48)))
	if err != nil {
		t.Fatal(err)
	}
	const blobHash = "89793d0862e833e989099e69a53720d56ff914d7d18ef494693fbbd9ed2a37d950996eb47cb2f7620f87cf102f34e152"
	const streamHash = "acdb2734b9ddc9ddb4fbcbff296eab38a37e4be80be39d9a7614979dd7a416cffd2cca7613b8af1a82e9d1a91e40e67c"
	const sdHash = "858e706e7eae161cd5a2841c68cc4e75980edb623854831ec50787ab47bc3b0d431cb9d183570d0eb3254b65d7796cbc"
	if created.SDHash != sdHash || created.Descriptor.StreamHash != streamHash ||
		len(created.Descriptor.Blobs) != 2 || created.Descriptor.Blobs[0].BlobHash != blobHash ||
		created.Descriptor.Blobs[0].Length != 16 || len(created.Blobs[blobHash]) != 16 {
		t.Fatalf("created stream = %#v", created)
	}
	parsed, err := ParseDescriptor(created.DescriptorBytes)
	if err != nil || parsed.StreamHash != streamHash || parsed.Blobs[1].Length != 0 {
		t.Fatalf("parsed descriptor = %#v, %v", parsed, err)
	}
}

func TestCreateStreamDescriptorRoundTripsMultipleBlobs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.bin")
	content := bytes.Repeat([]byte{0x5a}, MaxBlobSize+17)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	random := bytes.NewReader(bytes.Repeat([]byte{0x11}, 64))
	created, err := CreateStreamDescriptor(path, random)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager()
	if err := manager.Set(created.SDHash, created.DescriptorBytes, true); err != nil {
		t.Fatal(err)
	}
	for hash, data := range created.Blobs {
		if err := manager.Set(hash, data, false); err != nil {
			t.Fatal(err)
		}
	}
	_, decoded, err := manager.ReadStream(created.SDHash)
	if err != nil || !bytes.Equal(decoded, content) || len(created.Descriptor.ContentBlobs()) != 2 {
		t.Fatalf("round trip = %d/%d bytes, blobs %d, %v", len(decoded), len(content), len(created.Descriptor.Blobs), err)
	}
}

func TestValidateDescriptorRejectsPinnedStructuralFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "validate.bin")
	if err := os.WriteFile(path, []byte("validate"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := CreateStreamDescriptor(path, bytes.NewReader(sequenceBytes(48)))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateDescriptor(created.Descriptor); err != nil {
		t.Fatalf("valid descriptor rejected: %v", err)
	}
	for name, mutate := range map[string]func(*StreamDescriptor){
		"nonzero terminator": func(descriptor *StreamDescriptor) {
			descriptor.Blobs[len(descriptor.Blobs)-1].Length = 1
		},
		"hashed terminator": func(descriptor *StreamDescriptor) {
			descriptor.Blobs[len(descriptor.Blobs)-1].BlobHash = strings.Repeat("0", BlobHashLength)
		},
		"out of order":    func(descriptor *StreamDescriptor) { descriptor.Blobs[0].BlobNum = 2 },
		"missing iv":      func(descriptor *StreamDescriptor) { descriptor.Blobs[0].IV = "" },
		"bad stream hash": func(descriptor *StreamDescriptor) { descriptor.StreamHash = strings.Repeat("0", BlobHashLength) },
	} {
		t.Run(name, func(t *testing.T) {
			copyDescriptor := *created.Descriptor
			copyDescriptor.Blobs = append([]BlobInfo(nil), created.Descriptor.Blobs...)
			mutate(&copyDescriptor)
			if err := ValidateDescriptor(&copyDescriptor); err == nil {
				t.Fatal("invalid descriptor was accepted")
			}
		})
	}
}

func TestWalkStreamYieldsDecryptedBlobsInOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "walk.bin")
	content := bytes.Repeat([]byte{0x4a}, MaxBlobSize+17)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := CreateStreamDescriptor(path, bytes.NewReader(bytes.Repeat([]byte{0x41}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager()
	manager.SetFetcher(func(_ context.Context, hash string) ([]byte, error) {
		return created.Blobs[hash], nil
	})
	var got []byte
	var positions []int
	if err := WalkStream(context.Background(), manager, created.Descriptor, func(info BlobInfo, data []byte) error {
		positions = append(positions, info.BlobNum)
		got = append(got, data...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) || len(positions) != 2 || positions[0] != 0 || positions[1] != 1 {
		t.Fatalf("walk = %d/%d bytes at %v", len(got), len(content), positions)
	}
}

func sequenceBytes(length int) []byte {
	result := make([]byte, length)
	for index := range result {
		result[index] = byte(index)
	}
	return result
}
