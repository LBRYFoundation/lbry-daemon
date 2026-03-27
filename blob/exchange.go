package blob

import "bufio"
import "bytes"
import "crypto/sha512"
import "encoding/hex"
import "encoding/json"
import "fmt"
import "io"
import "net"
import "strconv"
import "time"

const (
	MaxBlobSize     = 2 * 1024 * 1024 // 2 MiB
	BlobHashLength  = 96              // SHA-384 hex = 96 chars
	DownloadTimeout = 30 * time.Second
	ConnectTimeout  = 10 * time.Second
)

// BlobRequest is the JSON request sent to a blob exchange peer.
type BlobRequest struct {
	RequestedBlobs []string `json:"requested_blobs"`
	BlobPayRate    float64  `json:"blob_data_payment_rate"`
	RequestedBlob  string   `json:"requested_blob"`
}

// BlobResponse is the JSON response from a blob exchange peer.
type BlobResponse struct {
	AvailableBlobs []string     `json:"available_blobs,omitempty"`
	PaymentRate    string       `json:"blob_data_payment_rate,omitempty"`
	IncomingBlob   *IncomingBlob `json:"incoming_blob,omitempty"`
	Error          string       `json:"error,omitempty"`
}

type IncomingBlob struct {
	BlobHash string `json:"blob_hash"`
	Length   int    `json:"length"`
}

// DownloadBlob downloads a single blob from a peer by TCP.
// Returns the raw (encrypted) blob bytes.
func DownloadBlob(ip net.IP, tcpPort int, blobHash string) ([]byte, error) {
	addr := net.JoinHostPort(ip.String(), strconv.Itoa(tcpPort))
	conn, err := net.DialTimeout("tcp", addr, ConnectTimeout)
	if err != nil {
		return nil, fmt.Errorf("blob: connect %s: %w", addr, err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(DownloadTimeout))

	// Send request
	req := BlobRequest{
		RequestedBlobs: []string{blobHash},
		BlobPayRate:    0.0,
		RequestedBlob:  blobHash,
	}
	reqBytes, _ := json.Marshal(req)
	if _, err := conn.Write(reqBytes); err != nil {
		return nil, fmt.Errorf("blob: write request: %w", err)
	}

	// Read response: first read JSON header, then read exact blob bytes
	reader := bufio.NewReaderSize(conn, 65536)

	// Read until we find the complete JSON response (closing brace)
	var jsonBuf bytes.Buffer
	depth := 0
	inString := false
	escaped := false
	jsonDone := false

	for !jsonDone {
		b, err := reader.ReadByte()
		if err != nil {
			return nil, fmt.Errorf("blob: read response: %w", err)
		}
		jsonBuf.WriteByte(b)

		if escaped {
			escaped = false
			continue
		}
		if b == '\\' && inString {
			escaped = true
			continue
		}
		if b == '"' {
			inString = !inString
			continue
		}
		if !inString {
			if b == '{' {
				depth++
			} else if b == '}' {
				depth--
				if depth == 0 {
					jsonDone = true
				}
			}
		}
	}

	// Parse JSON response
	var resp BlobResponse
	if err := json.Unmarshal(jsonBuf.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("blob: parse response: %w", err)
	}

	if resp.Error != "" {
		return nil, fmt.Errorf("blob: peer error: %s", resp.Error)
	}

	if resp.IncomingBlob == nil {
		return nil, fmt.Errorf("blob: peer has no data for %s", blobHash[:12])
	}

	// Read exact number of blob bytes
	blobData := make([]byte, resp.IncomingBlob.Length)
	_, err = io.ReadFull(reader, blobData)
	if err != nil {
		return nil, fmt.Errorf("blob: read blob data: %w", err)
	}

	// Verify hash
	h := sha512.New384()
	h.Write(blobData)
	actualHash := hex.EncodeToString(h.Sum(nil))
	if actualHash != blobHash {
		return nil, fmt.Errorf("blob: hash mismatch for %s", blobHash[:12])
	}

	return blobData, nil
}

// findJSONEnd finds the index of the closing '}' that ends the JSON object.
// Handles nested braces.
func findJSONEnd(data []byte) int {
	depth := 0
	inString := false
	escaped := false
	for i, b := range data {
		if escaped {
			escaped = false
			continue
		}
		if b == '\\' && inString {
			escaped = true
			continue
		}
		if b == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		if b == '{' {
			depth++
		}
		if b == '}' {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}
