package stream

import (
	"fmt"
	"log"
	"lbry/daemon/blob"
	"lbry/daemon/dht"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Manager handles P2P blob downloading and HTTP streaming.
type Manager struct {
	dhtNode    *dht.Node
	cache      map[string][]byte // blobHash -> decrypted content
	cacheMu    sync.RWMutex
	sdCache    map[string]*blob.StreamDescriptor // sdHash -> descriptor
	sdCacheMu  sync.RWMutex
}

func CreateServer(m *Manager) *http.Server {
	contentServeMux := http.NewServeMux()
	contentServeMux.HandleFunc("/", m.handleStream)

	return &http.Server{Handler: contentServeMux}
}

func NewManager(dhtNode *dht.Node) *Manager {
	return &Manager{
		dhtNode: dhtNode,
		cache:   make(map[string][]byte),
		sdCache: make(map[string]*blob.StreamDescriptor),
	}
}

// GetStreamingURL returns the local streaming URL for a given SD hash.
func (m *Manager) GetStreamingURL(sdHash string, port int) string {
	return fmt.Sprintf("http://localhost:%d/stream/%s", port, sdHash)
}

func (m *Manager) handleStream(w http.ResponseWriter, r *http.Request) {
	sdHash := strings.TrimPrefix(r.URL.Path, "/stream/")
	if sdHash == "" || len(sdHash) != blob.BlobHashLength {
		http.Error(w, "invalid sd_hash", http.StatusBadRequest)
		return
	}

	// CORS for frontend
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Range")
	w.Header().Set("Access-Control-Expose-Headers", "Content-Range, Content-Length, Accept-Ranges")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Get or download stream descriptor
	sd, err := m.getDescriptor(sdHash)
	if err != nil {
		log.Printf("P2P STREAM: failed to get descriptor %s: %v", sdHash[:12], err)
		http.Error(w, "failed to load stream", http.StatusBadGateway)
		return
	}
	log.Printf("P2P STREAM: serving %s (%s)", sdHash[:12], r.Header.Get("Range"))

	contentBlobs := sd.ContentBlobs()
	if len(contentBlobs) == 0 {
		http.Error(w, "empty stream", http.StatusNotFound)
		return
	}

	// Calculate total size (encrypted sizes; decrypted will be slightly less due to padding)
	// For range requests, we need approximate total. Use encrypted size as estimate.
	var totalSize int64
	for _, bi := range contentBlobs {
		totalSize += int64(bi.Length)
	}

	// Determine MIME type from file extension
	mimeType := guessMIME(sd.SuggestedFileName, sd.StreamName)
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Accept-Ranges", "bytes")

	// Parse range header
	rangeHeader := r.Header.Get("Range")
	var start, end int64
	if rangeHeader != "" && strings.HasPrefix(rangeHeader, "bytes=") {
		rangeParts := strings.TrimPrefix(rangeHeader, "bytes=")
		parts := strings.SplitN(rangeParts, "-", 2)
		start, _ = strconv.ParseInt(parts[0], 10, 64)
		if parts[1] != "" {
			end, _ = strconv.ParseInt(parts[1], 10, 64)
		} else {
			end = totalSize - 1
		}
		if end >= totalSize {
			end = totalSize - 1
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, totalSize))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		start = 0
		end = totalSize - 1
		w.Header().Set("Content-Length", strconv.FormatInt(totalSize, 10))
	}

	// Stream blobs sequentially, decrypting on the fly
	var offset int64
	for _, bi := range contentBlobs {
		blobEnd := offset + int64(bi.Length)

		// Skip blobs before the requested range
		if blobEnd <= start {
			offset = blobEnd
			continue
		}
		// Stop if past the requested range
		if offset > end {
			break
		}

		// Download and decrypt this blob
		decrypted, err := m.getDecryptedBlob(bi.BlobHash, sd.Key, bi.IV)
		if err != nil {
			log.Printf("P2P STREAM: blob %s failed: %v", bi.BlobHash[:12], err)
			return // connection will be broken
		}
		log.Printf("P2P STREAM: blob #%d %s... → %d bytes decrypted", bi.BlobNum, bi.BlobHash[:12], len(decrypted))

		// Calculate which portion of this blob to send
		blobStart := int64(0)
		if start > offset {
			blobStart = start - offset
		}
		blobSendEnd := int64(len(decrypted))
		if end < blobEnd-1 {
			blobSendEnd = end - offset + 1
		}

		if blobStart < int64(len(decrypted)) && blobSendEnd > blobStart {
			w.Write(decrypted[blobStart:blobSendEnd])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}

		offset = blobEnd
	}
}

func (m *Manager) getDescriptor(sdHash string) (*blob.StreamDescriptor, error) {
	m.sdCacheMu.RLock()
	if sd, ok := m.sdCache[sdHash]; ok {
		m.sdCacheMu.RUnlock()
		return sd, nil
	}
	m.sdCacheMu.RUnlock()

	// Download SD blob from peers
	sdData, err := m.downloadBlob(sdHash)
	if err != nil {
		return nil, fmt.Errorf("download SD blob: %w", err)
	}

	sd, err := blob.ParseDescriptor(sdData)
	if err != nil {
		return nil, err
	}

	m.sdCacheMu.Lock()
	m.sdCache[sdHash] = sd
	m.sdCacheMu.Unlock()
	return sd, nil
}

func (m *Manager) getDecryptedBlob(blobHash, keyHex, ivHex string) ([]byte, error) {
	// Check cache
	cacheKey := blobHash + ":" + ivHex
	m.cacheMu.RLock()
	if data, ok := m.cache[cacheKey]; ok {
		m.cacheMu.RUnlock()
		return data, nil
	}
	m.cacheMu.RUnlock()

	// Download encrypted blob
	encrypted, err := m.downloadBlob(blobHash)
	if err != nil {
		return nil, err
	}

	// Decrypt
	decrypted, err := blob.DecryptBlob(encrypted, keyHex, ivHex)
	if err != nil {
		return nil, err
	}

	// Cache (limit: keep last 20 blobs ≈ 40MB)
	m.cacheMu.Lock()
	if len(m.cache) > 20 {
		// Evict oldest entries (simple: clear all)
		m.cache = make(map[string][]byte)
	}
	m.cache[cacheKey] = decrypted
	m.cacheMu.Unlock()

	return decrypted, nil
}

// downloadBlob finds peers via DHT and downloads the blob.
func (m *Manager) downloadBlob(blobHash string) ([]byte, error) {
	hashBytes, err := hexToHash(blobHash)
	if err != nil {
		return nil, err
	}

	// Find peers that have this blob
	blobPeers, _ := m.dhtNode.FindValue(hashBytes)

	if len(blobPeers) == 0 {
		return nil, fmt.Errorf("no peers found for blob %s", blobHash[:12])
	}

	// Try each peer until one works
	var lastErr error
	for _, peer := range blobPeers {
		if peer.TCPPort <= 0 {
			continue
		}
		data, err := blob.DownloadBlob(peer.IP, peer.TCPPort, blobHash)
		if err != nil {
			lastErr = err
			continue
		}
		return data, nil
	}

	return nil, fmt.Errorf("all peers failed for blob %s: %v", blobHash[:12], lastErr)
}

func hexToHash(s string) ([dht.HashSize]byte, error) {
	var h [dht.HashSize]byte
	b, err := decodeHex(s)
	if err != nil {
		return h, err
	}
	if len(b) != dht.HashSize {
		return h, fmt.Errorf("hash must be %d bytes", dht.HashSize)
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

func guessMIME(suggestedName, streamName string) string {
	name := suggestedName
	if name == "" {
		name = streamName
	}
	// Decode hex-encoded name
	if decoded, err := decodeHex(name); err == nil {
		name = string(decoded)
	}

	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".mp4"):
		return "video/mp4"
	case strings.HasSuffix(lower, ".webm"):
		return "video/webm"
	case strings.HasSuffix(lower, ".mkv"):
		return "video/x-matroska"
	case strings.HasSuffix(lower, ".mp3"):
		return "audio/mpeg"
	case strings.HasSuffix(lower, ".flac"):
		return "audio/flac"
	case strings.HasSuffix(lower, ".ogg"):
		return "audio/ogg"
	case strings.HasSuffix(lower, ".m4a"):
		return "audio/mp4"
	case strings.HasSuffix(lower, ".png"):
		return "image/png"
	case strings.HasSuffix(lower, ".jpg"), strings.HasSuffix(lower, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(lower, ".gif"):
		return "image/gif"
	case strings.HasSuffix(lower, ".webp"):
		return "image/webp"
	case strings.HasSuffix(lower, ".pdf"):
		return "application/pdf"
	default:
		return "application/octet-stream"
	}
}

// idle cleanup goroutine
func (m *Manager) startCacheCleanup() {
	go func() {
		for {
			time.Sleep(5 * time.Minute)
			m.cacheMu.Lock()
			if len(m.cache) > 10 {
				m.cache = make(map[string][]byte)
			}
			m.cacheMu.Unlock()
		}
	}()
}
