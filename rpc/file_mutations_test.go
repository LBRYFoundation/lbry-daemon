package rpc

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"lbry/daemon/blob"
	databasepkg "lbry/daemon/database"
	walletpkg "lbry/daemon/wallet"
)

type fileMutationTestStore struct {
	rows       []databasepkg.ManagedFileRow
	statuses   []string
	paths      [][2]*string
	saved      []bool
	deleted    []string
	deleteHash []string
}

type fileControllerTest struct {
	started        []string
	stopped        []string
	registered     []string
	deleteFinished []bool
}

func (controller *fileControllerTest) RegisterManagedFile(
	_ context.Context, row databasepkg.ManagedFileRow,
) error {
	controller.registered = append(controller.registered, row.SDHash)
	return nil
}

func (controller *fileControllerTest) PrepareManagedFileDelete(
	ctx context.Context, row databasepkg.ManagedFileRow,
) error {
	return controller.StopManagedFile(ctx, row)
}

func (controller *fileControllerTest) FinishManagedFileDelete(_ databasepkg.ManagedFileRow, deleted bool) {
	controller.deleteFinished = append(controller.deleteFinished, deleted)
}

func (controller *fileControllerTest) StartManagedFile(_ context.Context, row databasepkg.ManagedFileRow) error {
	controller.started = append(controller.started, row.StreamHash)
	return nil
}

func (controller *fileControllerTest) StopManagedFile(_ context.Context, row databasepkg.ManagedFileRow) error {
	controller.stopped = append(controller.stopped, row.StreamHash)
	return nil
}

func (controller *fileControllerTest) SaveManagedFile(
	_ context.Context, row databasepkg.ManagedFileRow, _, _ *string,
) (databasepkg.ManagedFileRow, error) {
	return row, nil
}

func (store *fileMutationTestStore) ListManagedFiles(context.Context) ([]databasepkg.ManagedFileRow, error) {
	return append([]databasepkg.ManagedFileRow(nil), store.rows...), nil
}

func (store *fileMutationTestStore) ChangeManagedFileStatus(_ context.Context, streamHash, status string) error {
	store.statuses = append(store.statuses, status)
	for index := range store.rows {
		if store.rows[index].StreamHash == streamHash {
			store.rows[index].Status = status
		}
	}
	return nil
}

func (store *fileMutationTestStore) ChangeManagedFilePath(
	_ context.Context, streamHash string, fileName, directory *string,
) error {
	store.paths = append(store.paths, [2]*string{fileName, directory})
	for index := range store.rows {
		if store.rows[index].StreamHash == streamHash {
			store.rows[index].FileName, store.rows[index].DownloadDirectory = fileName, directory
		}
	}
	return nil
}

func (store *fileMutationTestStore) SetManagedFileSaved(_ context.Context, streamHash string, saved bool) error {
	store.saved = append(store.saved, saved)
	for index := range store.rows {
		if store.rows[index].StreamHash == streamHash {
			store.rows[index].SavedFile = saved
		}
	}
	return nil
}

func (store *fileMutationTestStore) DeleteManagedStream(_ context.Context, streamHash string) ([]string, error) {
	store.deleted = append(store.deleted, streamHash)
	return append([]string(nil), store.deleteHash...), nil
}

func TestFileSaveWritesLocalStreamAndUsesNextAvailableName(t *testing.T) {
	manager, _ := transactionListOracleManager(t)
	blobs := blob.NewManager()
	sdHash, contentHash := seedFileMutationBlobs(t, blobs, []byte("hello"))
	store := &fileMutationTestStore{rows: []databasepkg.ManagedFileRow{
		fileMutationTestRow("stream", sdHash, contentHash),
	}}
	server := fileMutationTestServer(manager, store, blobs)
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "movie.mp4"), []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := fileMutationRPCResult(t, server, "file_save", map[string]any{
		"stream_hash": "stream", "file_name": "movie.mp4", "download_directory": directory,
	})
	encoded := result.(map[string]any)
	if encoded["file_name"] != "movie_1.mp4" || encoded["status"] != "finished" {
		t.Fatalf("file_save result = %#v", encoded)
	}
	data, err := os.ReadFile(filepath.Join(directory, "movie_1.mp4"))
	if err != nil || !bytes.Equal(data, []byte("hello")) {
		t.Fatalf("saved bytes = %q, %v", data, err)
	}
	if len(store.paths) != 1 || len(store.statuses) != 2 ||
		store.statuses[0] != "running" || store.statuses[1] != "finished" ||
		len(store.saved) != 1 || !store.saved[0] {
		t.Fatalf("save mutations = paths %#v statuses %v saved %v", store.paths, store.statuses, store.saved)
	}
}

func TestFileSetStatusAndDeleteGuards(t *testing.T) {
	manager, _ := transactionListOracleManager(t)
	blobs := blob.NewManager()
	deleteData := []byte("data")
	deleteDigest := sha512.Sum384(deleteData)
	deleteHash := hex.EncodeToString(deleteDigest[:])
	if err := blobs.Set(deleteHash, deleteData, false); err != nil {
		t.Fatal(err)
	}
	running := fileMutationTestRow("running", "sd-running", "blob-running")
	running.Status = "running"
	fileName, directory := "delete.mp4", t.TempDir()
	running.FileName, running.DownloadDirectory = &fileName, &directory
	if err := os.WriteFile(filepath.Join(directory, fileName), []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	second := fileMutationTestRow("second", "sd-second", "blob-second")
	store := &fileMutationTestStore{rows: []databasepkg.ManagedFileRow{running, second}, deleteHash: []string{deleteHash}}
	server := fileMutationTestServer(manager, store, blobs)

	if got := fileMutationRPCResult(t, server, "file_set_status", map[string]any{
		"status": "stop", "stream_hash": "running",
	}); got != "Stopped download" || len(store.statuses) != 1 || store.statuses[0] != "stopped" {
		t.Fatalf("file_set_status = %#v, statuses %v", got, store.statuses)
	}
	if got := fileMutationRPCResult(t, server, "file_delete", map[string]any{}); got != false || len(store.deleted) != 0 {
		t.Fatalf("guarded file_delete = %#v, deleted %v", got, store.deleted)
	}
	if got := fileMutationRPCResult(t, server, "file_delete", map[string]any{
		"stream_hash": "running", "delete_from_download_dir": true,
	}); got != true || len(store.deleted) != 1 {
		t.Fatalf("file_delete = %#v, deleted %v", got, store.deleted)
	}
	if _, err := os.Stat(filepath.Join(directory, fileName)); !os.IsNotExist(err) {
		t.Fatalf("deleted output still exists: %v", err)
	}
	if got := blobs.CompletedBlobCount(); got != 0 {
		t.Fatalf("blob cache count after delete = %d", got)
	}
}

func TestFileSetStatusRoutesThroughLifecycleController(t *testing.T) {
	manager, _ := transactionListOracleManager(t)
	row := fileMutationTestRow("controlled", "sd", "blob")
	row.Status = "running"
	store := &fileMutationTestStore{rows: []databasepkg.ManagedFileRow{row}}
	controller := &fileControllerTest{}
	server := CreateServer(
		WithWalletManagerProvider(func() *walletpkg.WalletManager { return manager }),
		WithManagedFileLister(store), WithBlobManager(blob.NewManager()),
		WithManagedFileController(controller),
	)
	if got := fileMutationRPCResult(t, server, "file_set_status", map[string]any{
		"status": "stop", "stream_hash": "controlled",
	}); got != "Stopped download" || len(controller.stopped) != 1 || len(store.statuses) != 0 {
		t.Fatalf("controlled stop = %#v, controller=%v store=%v", got, controller.stopped, store.statuses)
	}
	store.rows[0].Status = "stopped"
	if got := fileMutationRPCResult(t, server, "file_set_status", map[string]any{
		"status": "start", "stream_hash": "controlled",
	}); got != "Resumed download" || len(controller.started) != 1 {
		t.Fatalf("controlled start = %#v, controller=%v", got, controller.started)
	}
}

func TestFileDeleteStopsManagedWorkerBeforeDeleting(t *testing.T) {
	manager, _ := transactionListOracleManager(t)
	row := fileMutationTestRow("controlled", "sd", "blob")
	row.Status = "running"
	store := &fileMutationTestStore{rows: []databasepkg.ManagedFileRow{row}}
	controller := &fileControllerTest{}
	server := CreateServer(
		WithWalletManagerProvider(func() *walletpkg.WalletManager { return manager }),
		WithManagedFileLister(store), WithBlobManager(blob.NewManager()),
		WithManagedFileController(controller),
	)
	if got := fileMutationRPCResult(t, server, "file_delete", map[string]any{
		"stream_hash": "controlled",
	}); got != true || len(controller.stopped) != 1 || len(store.deleted) != 1 ||
		len(controller.deleteFinished) != 1 || !controller.deleteFinished[0] {
		t.Fatalf("controlled delete = %#v, stopped=%v deleted=%v finished=%v",
			got, controller.stopped, store.deleted, controller.deleteFinished)
	}
}

func fileMutationTestServer(
	manager *walletpkg.WalletManager, store *fileMutationTestStore, blobs *blob.BlobManager,
) *RPCServer {
	return CreateServer(
		WithWalletManagerProvider(func() *walletpkg.WalletManager { return manager }),
		WithManagedFileLister(store), WithBlobManager(blobs),
	)
}

func fileMutationRPCResult(t *testing.T, server *RPCServer, method string, params map[string]any) any {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	decoder := json.NewDecoder(response.Body)
	decoder.UseNumber()
	var envelope map[string]any
	if err := decoder.Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["error"] != nil {
		t.Fatalf("%s error = %#v", method, envelope["error"])
	}
	return envelope["result"]
}

func fileMutationTestRow(streamHash, sdHash, _ string) databasepkg.ManagedFileRow {
	return databasepkg.ManagedFileRow{
		RowID: 1, StreamHash: streamHash, Status: "stopped", SDHash: sdHash,
		Key: "key", StreamName: "movie", SuggestedFileName: "movie.mp4",
		ClaimOutpoint: "tx:0", ClaimID: "claim", ClaimName: "movie",
		SerializedMetadataHex: "0a00", ClaimSequence: -1,
		BlobsInStream: 1, BlobsCompleted: 1,
		TotalBytesLowerBound: 0, TotalBytesUpperBound: 16,
	}
}

func seedFileMutationBlobs(t *testing.T, manager *blob.BlobManager, plaintext []byte) (string, string) {
	t.Helper()
	key, iv := bytes.Repeat([]byte{0x11}, 16), bytes.Repeat([]byte{0x22}, 16)
	padding := aes.BlockSize - len(plaintext)%aes.BlockSize
	padded := append(append([]byte(nil), plaintext...), bytes.Repeat([]byte{byte(padding)}, padding)...)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	encrypted := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(encrypted, padded)
	contentDigest := sha512.Sum384(encrypted)
	contentHash := hex.EncodeToString(contentDigest[:])
	descriptor := blob.StreamDescriptor{
		StreamName: hex.EncodeToString([]byte("movie")), Key: hex.EncodeToString(key),
		SuggestedFileName: hex.EncodeToString([]byte("movie.mp4")), StreamType: "lbryfile",
		Blobs: []blob.BlobInfo{
			{BlobHash: contentHash, BlobNum: 0, IV: hex.EncodeToString(iv), Length: len(encrypted)},
			{BlobNum: 1, IV: hex.EncodeToString(iv), Length: 0},
		},
	}
	descriptor.StreamHash = blob.CalculateStreamHash(&descriptor)
	sd, err := blob.MarshalDescriptor(&descriptor)
	if err != nil {
		t.Fatal(err)
	}
	sdDigest := sha512.Sum384(sd)
	sdHash := hex.EncodeToString(sdDigest[:])
	if err := manager.Set(sdHash, sd, true); err != nil {
		t.Fatal(err)
	}
	if err := manager.Set(contentHash, encrypted, false); err != nil {
		t.Fatal(err)
	}
	return sdHash, contentHash
}
