package blob

import "fmt"
import "lbry/daemon/dht"
import "net"
import "strings"

type BlobManager struct {
	Blobs      map[string][]byte
}

// dhtNode   *dht.Node
// cache     map[string][]byte // blobHash -> decrypted content
// cacheMu   sync.RWMutex
// sdCache   map[string]*StreamDescriptor // sdHash -> descriptor
// sdCacheMu sync.RWMutex
// cache:   make(map[string][]byte),
// sdCache: make(map[string]*StreamDescriptor),

func (blobManager *BlobManager) Get(blobHash string) ([]byte, bool) {
	blobData, ok := blobManager.Blobs[blobHash]
	if !ok {
		// Temporary: Ensure blob
		blobData, _ = downloadBlob(blobHash)
		blobManager.Set(blobHash, blobData, false)
		_, ok = blobManager.Blobs[blobHash]
	}
	return blobData, ok
}

func (blobManager *BlobManager) Set(blobHash string, blobData []byte, isStreamDescriptor bool) error {
	if isStreamDescriptor {
		// TODO Process SD blob data
		blobManager.Blobs[blobHash] = blobData
		//fmt.Printf("SD BLOB (%s) = %+v\n", blobHash, string(blobData))
		return nil
	}
	// TODO Process blob data
	blobManager.Blobs[blobHash] = blobData
	//fmt.Printf("BLOB (%s) = %+v\n", blobHash, blobData)
	return nil
}

////////////////////////////////

// downloadBlob finds peers via DHT and downloads the blob.
func downloadBlob(blobHash string) ([]byte, error) {
	hashBytes, err := hexToHash(blobHash)
	if err != nil {
		return nil, err
	}

	// Find peers that have this blob
	//blobPeers, _ = m.dhtNode.FindValue(hashBytes)
	blobPeers := []dht.Peer{}

	// Static peers
	blobPeers = append(blobPeers, dht.Peer{
		ID:      hashBytes, //[48]byte{},
		IP:      net.ParseIP("51.210.220.149"),
		TCPPort: 5567,
	})

	if len(blobPeers) == 0 {
		return nil, fmt.Errorf("no peers found for blob %s", blobHash[:12])
	}

	// Try each peer until one works
	var lastErr error
	for _, peer := range blobPeers {
		if peer.TCPPort <= 0 {
			continue
		}
		data, err := DownloadBlob(peer.IP, peer.TCPPort, blobHash)
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
