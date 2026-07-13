package rpc

import (
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"testing"

	blobpkg "lbry/daemon/blob"
	dhtpkg "lbry/daemon/dht"
)

func TestBlobAnnounceValidationAndCompletedBlobResponse(t *testing.T) {
	manager := blobpkg.NewManager()
	data := []byte("announced rpc blob")
	digest := sha512.Sum384(data)
	hash := hex.EncodeToString(digest[:])
	if err := manager.Set(hash, data, false); err != nil {
		t.Fatal(err)
	}
	node, err := dhtpkg.NewNode(0)
	if err != nil {
		t.Fatal(err)
	}
	server := CreateServer(WithBlobManager(manager), WithDHTNodeProvider(func() *dhtpkg.Node { return node }))
	if result := fileMutationRPCResult(t, server, "blob_announce", map[string]any{"blob_hash": hash}); result != true {
		t.Fatalf("blob_announce = %#v", result)
	}

	tests := []struct {
		name    string
		params  map[string]any
		message string
	}{
		{"missing", map[string]any{}, "single argument must be specified"},
		{"two stream selectors", map[string]any{"stream_hash": "stream", "sd_hash": "sd"}, "either the sd hash or the stream hash should be provided, not both"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := performRequest(server, "POST", "/", rpcRequestBody(t, "blob_announce", test.params), nil)
			assertRPCError(t, response, json.Number("-32500"), test.message)
		})
	}
}

func rpcRequestBody(t *testing.T, method string, params map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}
