package rpc

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"testing"

	blobpkg "lbry/daemon/blob"
)

func TestBlobGetReadDeleteAndValidation(t *testing.T) {
	manager := blobpkg.NewManager()
	data := []byte("blob text")
	digest := sha512.Sum384(data)
	hash := hex.EncodeToString(digest[:])
	manager.SetFetcher(func(_ context.Context, requested string) ([]byte, error) {
		if requested != hash {
			t.Fatalf("requested hash = %q", requested)
		}
		return data, nil
	})
	server := CreateServer(WithBlobManager(manager))
	if result := fileMutationRPCResult(t, server, "blob_get", map[string]any{
		"blob_hash": hash, "read": true,
	}); result != "blob text" {
		t.Fatalf("blob_get read = %#v", result)
	}
	if result := fileMutationRPCResult(t, server, "blob_delete", map[string]any{
		"blob_hash": "bad",
	}); result != "Invalid blob hash to delete 'bad'" {
		t.Fatalf("invalid blob_delete = %#v", result)
	}
	if result := fileMutationRPCResult(t, server, "blob_delete", map[string]any{
		"blob_hash": hash,
	}); result != "Deleted "+hash || manager.Has(hash) {
		t.Fatalf("blob_delete = %#v, exists=%v", result, manager.Has(hash))
	}
}

func TestBlobListCompletedAndDescriptorFilters(t *testing.T) {
	manager := blobpkg.NewManager()
	finishedData := bytes.Repeat([]byte("f"), 16)
	finishedDigest := sha512.Sum384(finishedData)
	finishedHash := hex.EncodeToString(finishedDigest[:])
	missingDigest := sha512.Sum384(bytes.Repeat([]byte("m"), 16))
	missingHash := hex.EncodeToString(missingDigest[:])
	descriptor := blobpkg.StreamDescriptor{
		StreamName: hex.EncodeToString([]byte("stream")), SuggestedFileName: hex.EncodeToString([]byte("file.bin")), StreamType: "lbryfile",
		Key: "00000000000000000000000000000000",
		Blobs: []blobpkg.BlobInfo{
			{BlobHash: finishedHash, BlobNum: 0, IV: "00000000000000000000000000000000", Length: 16},
			{BlobHash: missingHash, BlobNum: 1, IV: "00000000000000000000000000000001", Length: 16},
			{BlobNum: 2, IV: "00000000000000000000000000000002"},
		},
	}
	descriptor.StreamHash = blobpkg.CalculateStreamHash(&descriptor)
	descriptorData, err := blobpkg.MarshalDescriptor(&descriptor)
	if err != nil {
		t.Fatal(err)
	}
	descriptorDigest := sha512.Sum384(descriptorData)
	sdHash := hex.EncodeToString(descriptorDigest[:])
	if err := manager.Set(sdHash, descriptorData, true); err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(finishedHash, finishedData, false); err != nil {
		t.Fatal(err)
	}
	server := CreateServer(WithBlobManager(manager))
	needed := fileMutationRPCResult(t, server, "blob_list", map[string]any{
		"sd_hash": sdHash, "needed": true,
	}).(map[string]any)
	neededItems := needed["items"].([]any)
	if len(neededItems) != 1 || neededItems[0] != missingHash {
		t.Fatalf("needed blobs = %#v", needed)
	}
	finished := fileMutationRPCResult(t, server, "blob_list", map[string]any{
		"sd_hash": sdHash, "finished": true,
	}).(map[string]any)
	finishedItems := finished["items"].([]any)
	if len(finishedItems) != 2 || finishedItems[0] != sdHash || finishedItems[1] != finishedHash {
		t.Fatalf("finished blobs = %#v", finished)
	}
}

func TestBlobListURIResolvesManagedDescriptor(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	if result := fileMutationRPCResult(t, fixture.server, "get", map[string]any{
		"uri": "lbry://paid", "save_file": false,
	}); result == nil {
		t.Fatal("get did not create a managed stream")
	}
	result := fileMutationRPCResult(t, fixture.server, "blob_list", map[string]any{
		"uri": "lbry://paid",
	}).(map[string]any)
	items := result["items"].([]any)
	if len(items) != 1 || items[0] != fixture.sdHash {
		t.Fatalf("URI blob_list = %#v", result)
	}
}

func TestBlobHashSelectionKeepsUnresolvedURIFilterEmpty(t *testing.T) {
	manager := blobpkg.NewManager()
	data := []byte("global inventory")
	digest := sha512.Sum384(data)
	hash := hex.EncodeToString(digest[:])
	if err := manager.Set(hash, data, false); err != nil {
		t.Fatal(err)
	}
	server := CreateServer(WithBlobManager(manager))
	hashes, err := server.selectedBlobHashes(normalizedRPCParams{named: map[string]any{
		"uri": "lbry://missing", "sd_hash": "",
	}})
	if err != nil || len(hashes) != 0 {
		t.Fatalf("unresolved URI hashes = %#v, %v", hashes, err)
	}
}
