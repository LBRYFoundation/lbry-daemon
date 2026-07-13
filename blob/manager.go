package blob

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// BlobManager owns a synchronized blob cache and must not be copied after use.
type BlobManager struct {
	mu       sync.RWMutex
	blobs    map[string][]byte
	fetch    Fetcher
	inFlight map[string]*fetchFlight
	blobDir  string
	persist  bool
	records  CompletionStore
	diskMu   sync.Mutex
}

type Fetcher func(context.Context, string) ([]byte, error)

type CompletionStore interface {
	RecordCompletedBlob(context.Context, string, int, int64, bool) error
}

type fetchFlight struct {
	done    chan struct{}
	err     error
	cancel  context.CancelFunc
	waiters int
}

// NewManager creates an initialized BlobManager for shared pointer ownership.
func NewManager() *BlobManager {
	return &BlobManager{blobs: make(map[string][]byte)}
}

func NewPersistentManager(blobDir string) *BlobManager {
	return &BlobManager{blobs: make(map[string][]byte), blobDir: blobDir, persist: true}
}

func NewConfiguredManager(blobDir string, saveNewBlobs bool) *BlobManager {
	return &BlobManager{blobs: make(map[string][]byte), blobDir: blobDir, persist: saveNewBlobs}
}

// Start loads verified blob files as lazy disk-backed entries.
func (blobManager *BlobManager) Start() (map[string]int64, error) {
	if blobManager == nil || blobManager.blobDir == "" {
		return map[string]int64{}, nil
	}
	if err := os.MkdirAll(blobManager.blobDir, 0o755); err != nil {
		return nil, fmt.Errorf("create blob directory: %w", err)
	}
	entries, err := os.ReadDir(blobManager.blobDir)
	if err != nil {
		return nil, fmt.Errorf("scan blob directory: %w", err)
	}
	loaded := make(map[string]int64)
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !canonicalBlobHash(entry.Name()) {
			continue
		}
		path := filepath.Join(blobManager.blobDir, entry.Name())
		info, infoErr := entry.Info()
		if infoErr != nil || info.Size() <= 0 || info.Size() > MaxBlobSize {
			continue
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil || hashBytes(data) != entry.Name() {
			continue
		}
		loaded[entry.Name()] = int64(len(data))
	}
	blobManager.mu.Lock()
	if blobManager.blobs == nil {
		blobManager.blobs = make(map[string][]byte)
	}
	for hash := range loaded {
		blobManager.blobs[hash] = nil
	}
	blobManager.mu.Unlock()
	return loaded, nil
}

func (blobManager *BlobManager) Stop() {
	if blobManager == nil {
		return
	}
	blobManager.mu.Lock()
	for _, flight := range blobManager.inFlight {
		flight.cancel()
	}
	blobManager.blobs = make(map[string][]byte)
	blobManager.inFlight = nil
	blobManager.mu.Unlock()
}

func (blobManager *BlobManager) SetCompletionStore(store CompletionStore) {
	if blobManager == nil {
		return
	}
	blobManager.mu.Lock()
	blobManager.records = store
	blobManager.mu.Unlock()
}

// CompletedBlobCount returns a point-in-time count without exposing cache data.
func (blobManager *BlobManager) CompletedBlobCount() int {
	if blobManager == nil {
		return 0
	}
	blobManager.mu.RLock()
	defer blobManager.mu.RUnlock()
	return len(blobManager.blobs)
}

func (blobManager *BlobManager) CompletedBlobHashes() []string {
	if blobManager == nil {
		return []string{}
	}
	blobManager.mu.RLock()
	defer blobManager.mu.RUnlock()
	hashes := make([]string, 0, len(blobManager.blobs))
	for hash := range blobManager.blobs {
		hashes = append(hashes, hash)
	}
	return hashes
}

func (blobManager *BlobManager) Has(blobHash string) bool {
	if blobManager == nil {
		return false
	}
	blobManager.mu.RLock()
	defer blobManager.mu.RUnlock()
	_, exists := blobManager.blobs[blobHash]
	return exists
}

func ValidHash(blobHash string) bool {
	_, err := hexToHash(blobHash)
	return err == nil
}

// dhtNode   *dht.Node
// cache     map[string][]byte // blobHash -> decrypted content
// cacheMu   sync.RWMutex
// sdCache   map[string]*StreamDescriptor // sdHash -> descriptor
// sdCacheMu sync.RWMutex
// cache:   make(map[string][]byte),
// sdCache: make(map[string]*StreamDescriptor),

func (blobManager *BlobManager) Get(blobHash string) ([]byte, bool) {
	blobData, ok := blobManager.get(blobHash)
	if !ok {
		if err := blobManager.Ensure(context.Background(), blobHash); err != nil {
			return nil, false
		}
		return blobManager.get(blobHash)
	}
	return blobData, ok
}

// GetLocal returns only data already held by this manager. Peer serving uses
// it to avoid recursively acquiring a blob while answering availability.
func (blobManager *BlobManager) GetLocal(blobHash string) ([]byte, bool) {
	if blobManager == nil {
		return nil, false
	}
	return blobManager.get(blobHash)
}

func (blobManager *BlobManager) SetFetcher(fetcher Fetcher) {
	if blobManager == nil {
		return
	}
	blobManager.mu.Lock()
	blobManager.fetch = fetcher
	blobManager.mu.Unlock()
}

func (blobManager *BlobManager) Ensure(ctx context.Context, blobHash string) error {
	if blobManager == nil {
		return errors.New("blob manager is nil")
	}
	if _, err := hexToHash(blobHash); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	blobManager.mu.Lock()
	if _, exists := blobManager.blobs[blobHash]; exists {
		blobManager.mu.Unlock()
		return nil
	}
	if flight, exists := blobManager.inFlight[blobHash]; exists {
		if flight.waiters > 0 {
			flight.waiters++
			blobManager.mu.Unlock()
			return blobManager.waitForFlight(ctx, blobHash, flight)
		}
		delete(blobManager.inFlight, blobHash)
	}
	fetcher := blobManager.fetch
	if fetcher == nil {
		blobManager.mu.Unlock()
		return fmt.Errorf("no fetcher is configured for blob %s", blobHash)
	}
	if blobManager.inFlight == nil {
		blobManager.inFlight = make(map[string]*fetchFlight)
	}
	fetchContext, cancel := context.WithCancel(context.Background())
	flight := &fetchFlight{done: make(chan struct{}), cancel: cancel, waiters: 1}
	blobManager.inFlight[blobHash] = flight
	blobManager.mu.Unlock()
	go func() {
		flight.err = blobManager.fetchAndStore(fetchContext, fetcher, blobHash)
		blobManager.mu.Lock()
		if blobManager.inFlight[blobHash] == flight {
			delete(blobManager.inFlight, blobHash)
		}
		flight.cancel()
		close(flight.done)
		blobManager.mu.Unlock()
	}()
	return blobManager.waitForFlight(ctx, blobHash, flight)
}

func (blobManager *BlobManager) waitForFlight(ctx context.Context, blobHash string, flight *fetchFlight) error {
	select {
	case <-flight.done:
		return flight.err
	case <-ctx.Done():
		blobManager.mu.Lock()
		if blobManager.inFlight[blobHash] == flight {
			flight.waiters--
			if flight.waiters == 0 {
				flight.cancel()
			}
		}
		blobManager.mu.Unlock()
		return ctx.Err()
	}
}

func (blobManager *BlobManager) fetchAndStore(ctx context.Context, fetcher Fetcher, blobHash string) error {
	data, err := fetcher(ctx, blobHash)
	if err != nil {
		return err
	}
	digest := sha512.Sum384(data)
	if actual := hex.EncodeToString(digest[:]); actual != blobHash {
		return fmt.Errorf("blob: hash mismatch for %s", blobHash[:12])
	}
	if err := blobManager.Set(blobHash, data, false); err != nil {
		return err
	}
	blobManager.mu.RLock()
	records := blobManager.records
	blobManager.mu.RUnlock()
	if records != nil {
		if err := records.RecordCompletedBlob(ctx, blobHash, len(data), time.Now().Unix(), false); err != nil {
			cleanupErr := blobManager.Delete(blobHash)
			if cleanupErr != nil {
				cleanupErr = fmt.Errorf("roll back unrecorded blob: %w", cleanupErr)
			}
			return errors.Join(err, cleanupErr)
		}
	}
	return nil
}

func (blobManager *BlobManager) Set(blobHash string, blobData []byte, isStreamDescriptor bool) error {
	storedData := cloneBytes(blobData)
	if len(storedData) == 0 || len(storedData) > MaxBlobSize {
		return fmt.Errorf("blob: size %d exceeds the resource limit of %d bytes", len(storedData), MaxBlobSize)
	}
	if !canonicalBlobHash(blobHash) || hashBytes(storedData) != blobHash {
		return fmt.Errorf("blob: hash mismatch for blob %q", blobHash)
	}
	if blobManager.persist {
		blobManager.diskMu.Lock()
		defer blobManager.diskMu.Unlock()
		if err := blobManager.writePersistent(blobHash, storedData); err != nil {
			return err
		}
	}

	blobManager.mu.Lock()
	defer blobManager.mu.Unlock()

	if blobManager.blobs == nil {
		blobManager.blobs = make(map[string][]byte)
	}

	if isStreamDescriptor {
		// TODO Process SD blob data
		if blobManager.persist {
			blobManager.blobs[blobHash] = nil
		} else {
			blobManager.blobs[blobHash] = storedData
		}
		//fmt.Printf("SD BLOB (%s) = %+v\n", blobHash, string(blobData))
		return nil
	}
	// TODO Process blob data
	if blobManager.persist {
		blobManager.blobs[blobHash] = nil
	} else {
		blobManager.blobs[blobHash] = storedData
	}
	//fmt.Printf("BLOB (%s) = %+v\n", blobHash, blobData)
	return nil
}

// Delete removes completed blobs from the in-memory manager.
func (blobManager *BlobManager) Delete(blobHashes ...string) error {
	if blobManager == nil {
		return nil
	}
	var deleteErr error
	for _, blobHash := range blobHashes {
		if blobManager.blobDir != "" && canonicalBlobHash(blobHash) {
			blobManager.diskMu.Lock()
			if err := os.Remove(filepath.Join(blobManager.blobDir, blobHash)); err != nil && !errors.Is(err, os.ErrNotExist) {
				deleteErr = errors.Join(deleteErr, err)
				blobManager.diskMu.Unlock()
				continue
			}
			blobManager.mu.Lock()
			delete(blobManager.blobs, blobHash)
			blobManager.mu.Unlock()
			blobManager.diskMu.Unlock()
			continue
		}
		blobManager.mu.Lock()
		delete(blobManager.blobs, blobHash)
		blobManager.mu.Unlock()
	}
	return deleteErr
}

// ReadStream assembles the decrypted bytes of a locally available stream.
func (blobManager *BlobManager) ReadStream(sdHash string) (*StreamDescriptor, []byte, error) {
	if blobManager == nil {
		return nil, nil, errors.New("blob manager is nil")
	}
	descriptorBytes, ok := blobManager.get(sdHash)
	if !ok {
		return nil, nil, fmt.Errorf("stream descriptor %s is unavailable", sdHash)
	}
	descriptor, err := DecodeDescriptor(sdHash, descriptorBytes)
	if err != nil {
		return nil, nil, err
	}
	content := make([]byte, 0)
	for _, blobInfo := range descriptor.ContentBlobs() {
		encrypted, found := blobManager.get(blobInfo.BlobHash)
		if !found {
			return nil, nil, fmt.Errorf("stream blob %s is unavailable", blobInfo.BlobHash)
		}
		decrypted, decryptErr := DecryptBlob(encrypted, descriptor.Key, blobInfo.IV)
		if decryptErr != nil {
			return nil, nil, decryptErr
		}
		content = append(content, decrypted...)
	}
	return descriptor, content, nil
}

func (blobManager *BlobManager) get(blobHash string) ([]byte, bool) {
	blobManager.mu.RLock()
	blobData, ok := blobManager.blobs[blobHash]
	blobDir := blobManager.blobDir
	blobManager.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if blobDir != "" && blobData == nil {
		data, err := os.ReadFile(filepath.Join(blobDir, blobHash))
		if err != nil || hashBytes(data) != blobHash {
			return nil, false
		}
		return data, true
	}
	return cloneBytes(blobData), true
}

func (blobManager *BlobManager) writePersistent(blobHash string, data []byte) error {
	temporary, err := os.CreateTemp(blobManager.blobDir, ".blob-*")
	if err != nil {
		return fmt.Errorf("create temporary blob: %w", err)
	}
	temporaryPath := temporary.Name()
	cleanup := func() { _ = os.Remove(temporaryPath) }
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		cleanup()
		return fmt.Errorf("write temporary blob: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		cleanup()
		return fmt.Errorf("sync temporary blob: %w", err)
	}
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		cleanup()
		return fmt.Errorf("set blob permissions: %w", err)
	}
	if err := temporary.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temporary blob: %w", err)
	}
	if err := os.Rename(temporaryPath, filepath.Join(blobManager.blobDir, blobHash)); err != nil {
		cleanup()
		return fmt.Errorf("publish blob: %w", err)
	}
	return nil
}

func hashBytes(data []byte) string {
	digest := sha512.Sum384(data)
	return hex.EncodeToString(digest[:])
}

func canonicalBlobHash(value string) bool {
	return len(value) == BlobHashLength && value == strings.ToLower(value) && ValidHash(value)
}

func cloneBytes(data []byte) []byte {
	if data == nil {
		return nil
	}
	cloned := make([]byte, len(data))
	copy(cloned, data)
	return cloned
}

func hexToHash(s string) ([48]byte, error) {
	var h [48]byte
	b, err := decodeHex(s)
	if err != nil {
		return h, err
	}
	if len(b) != len(h) {
		return h, fmt.Errorf("hash must be %d bytes", len(h))
	}
	copy(h[:], b)
	return h, nil
}

func decodeHex(s string) ([]byte, error) {
	// Hex-encoded stream/file names
	b := make([]byte, len(s)/2)
	_, err := hexDecode(b, []byte(s))
	if err != nil {
		return nil, err
	}
	return b, nil
}

func hexDecode(dst, src []byte) (int, error) {
	if len(src)%2 != 0 {
		return 0, fmt.Errorf("odd hex length")
	}
	for i := 0; i < len(src)/2; i++ {
		a := unhex(src[i*2])
		b := unhex(src[i*2+1])
		if a > 15 || b > 15 {
			return 0, fmt.Errorf("invalid hex byte")
		}
		dst[i] = (a << 4) | b
	}
	return len(src) / 2, nil
}

func unhex(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	default:
		return 255
	}
}

// GuessMIME returns the legacy stream MIME type inferred from descriptor names.
func GuessMIME(suggestedName, streamName string) string {
	name := suggestedName
	if name == "" {
		name = streamName
	}
	// Decode hex-encoded name
	if decoded, err := decodeHex(name); err == nil {
		name = string(decoded)
	}

	extension := strings.ToLower(filepath.Ext(name))
	if mimeType, ok := legacyMIMETypes[extension]; ok {
		return mimeType
	}
	if len(extension) > 1 {
		return "application/x-ext-" + extension[1:]
	}
	return "application/octet-stream"
}

var legacyMIMETypes = map[string]string{
	".avi":      "video/x-msvideo",
	".bmp":      "image/bmp",
	".cbr":      "application/vnd.comicbook-rar",
	".cbz":      "application/vnd.comicbook+zip",
	".css":      "text/css",
	".csv":      "text/csv",
	".doc":      "application/msword",
	".epub":     "application/epub+zip",
	".flac":     "audio/flac",
	".gif":      "image/gif",
	".htm":      "text/html",
	".html":     "text/html",
	".ico":      "image/vnd.microsoft.icon",
	".jpe":      "image/jpeg",
	".jpeg":     "image/jpeg",
	".jpg":      "image/jpeg",
	".js":       "application/javascript",
	".json":     "application/json",
	".m3u8":     "application/x-mpegurl",
	".m4a":      "audio/mp4",
	".m4v":      "video/m4v",
	".markdown": "text/markdown",
	".md":       "text/markdown",
	".mid":      "audio/midi",
	".midi":     "audio/midi",
	".mkv":      "video/x-matroska",
	".mov":      "video/quicktime",
	".mp2":      "audio/mpeg",
	".mp3":      "audio/mpeg",
	".mp4":      "video/mp4",
	".mpe":      "video/mpeg",
	".mpeg":     "video/mpeg",
	".mpg":      "video/mpeg",
	".oga":      "audio/ogg",
	".ogg":      "video/ogg",
	".ogv":      "video/ogg",
	".pdf":      "application/pdf",
	".png":      "image/png",
	".ppt":      "application/vnd.ms-powerpoint",
	".rtf":      "application/rtf",
	".svg":      "image/svg+xml",
	".tar":      "application/x-tar",
	".tif":      "image/tiff",
	".tiff":     "image/tiff",
	".ts":       "video/mp2t",
	".tsv":      "text/tab-separated-values",
	".txt":      "text/plain",
	".vtt":      "text/vtt",
	".wav":      "audio/x-wav",
	".webm":     "video/webm",
	".webp":     "image/webp",
	".wmv":      "video/x-ms-wmv",
	".xls":      "application/vnd.ms-excel",
	".xml":      "text/xml",
	".zip":      "application/zip",
}
