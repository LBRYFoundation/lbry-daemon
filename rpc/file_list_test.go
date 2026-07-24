package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	databasepkg "lbry/daemon/database"
	walletpkg "lbry/daemon/wallet"
)

type fileListTestStore struct {
	rows []databasepkg.ManagedFileRow
	err  error
}

func (store *fileListTestStore) ListManagedFiles(context.Context) ([]databasepkg.ManagedFileRow, error) {
	return append([]databasepkg.ManagedFileRow(nil), store.rows...), store.err
}

func TestFileListPublicFilteringPaginationAndEncoding(t *testing.T) {
	manager, _ := transactionListOracleManager(t)
	directory := t.TempDir()
	firstName := "first.mp4"
	if err := os.WriteFile(filepath.Join(directory, firstName), bytes.Repeat([]byte{1}, 20), 0o600); err != nil {
		t.Fatal(err)
	}
	firstDirectory := directory
	rows := []databasepkg.ManagedFileRow{
		{
			RowID: 2, AddedOn: 20, StreamHash: "stream-b", Status: "stopped",
			SDHash: "sd-b", Key: "key-b", StreamName: "second",
			SuggestedFileName: "second.webm", ClaimOutpoint: "tx-b:1",
			ClaimID: "claim-b", ClaimName: "beta", ClaimHeight: 0,
			SerializedMetadataHex: "0a00", Address: "address-b", ClaimSequence: -1,
			BlobsCompleted: 0, BlobsInStream: 2,
			TotalBytesLowerBound: 30, TotalBytesUpperBound: 46,
		},
		{
			RowID: 1, AddedOn: 10, StreamHash: "stream-a",
			FileName: &firstName, DownloadDirectory: &firstDirectory, Status: "running",
			SDHash: "sd-a", Key: "key-a", StreamName: "first",
			SuggestedFileName: "first.mp4", ClaimOutpoint: "tx-a:0",
			ClaimID: "claim-a", ClaimName: "alpha", ClaimHeight: 0,
			SerializedMetadataHex: "0a00", Address: "address-a", ClaimSequence: -1,
			FullyReflected: true, BlobsCompleted: 2, BlobsInStream: 2,
			TotalBytesLowerBound: 15, TotalBytesUpperBound: 31,
		},
	}
	server := CreateServer(
		WithWalletManagerProvider(func() *walletpkg.WalletManager { return manager }),
		WithManagedFileLister(&fileListTestStore{rows: rows}),
	)

	result := fileListTestRequest(t, server, map[string]any{"page_size": 1})
	if result["page"] != json.Number("1") || result["page_size"] != json.Number("1") ||
		result["total_items"] != json.Number("2") || result["total_pages"] != json.Number("2") {
		t.Fatalf("file list pagination = %#v", result)
	}
	items := result["items"].([]any)
	first := items[0].(map[string]any)
	if first["stream_hash"] != "stream-a" || first["completed"] != true ||
		first["written_bytes"] != json.Number("20") || first["file_name"] != firstName ||
		first["download_path"] != directory+"/"+firstName ||
		first["streaming_url"] != "http://localhost:5280/stream/sd-a" ||
		first["mime_type"] != "video/mp4" || first["blobs_remaining"] != json.Number("0") ||
		first["is_fully_reflected"] != true {
		t.Fatalf("encoded first file = %#v", first)
	}
	if metadata, ok := first["metadata"].(map[string]any); !ok || metadata["source"] != nil {
		t.Fatalf("encoded metadata = %#v", first["metadata"])
	}

	result = fileListTestRequest(t, server, map[string]any{
		"status": "stopped", "sort": "added_on", "comparison": "eq", "reverse": true,
	})
	items = result["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["stream_hash"] != "stream-b" {
		t.Fatalf("filtered files = %#v", items)
	}
}

func fileListTestRequest(t *testing.T, server *RPCServer, params map[string]any) map[string]any {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"method": "file_list", "params": params})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("file_list HTTP status = %d", response.Code)
	}
	decoder := json.NewDecoder(response.Body)
	decoder.UseNumber()
	var envelope map[string]any
	if err := decoder.Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if rpcError := envelope["error"]; rpcError != nil {
		t.Fatalf("file_list error = %#v", rpcError)
	}
	return envelope["result"].(map[string]any)
}
