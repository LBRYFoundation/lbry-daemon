package blob

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

const (
	MaxBlobSize               = 2 * 1024 * 1024 // 2 MiB
	MaxStreamDescriptorSize   = MaxBlobSize
	MaxStreamDescriptorBlobs  = 12_000
	MaxBlobResponseHeaderSize = 64 * 1024
	BlobHashLength            = 96 // SHA-384 hex = 96 chars
	DownloadTimeout           = 30 * time.Second
	ConnectTimeout            = 10 * time.Second
)

// BlobRequest is the JSON request sent to a blob exchange peer.
type BlobRequest struct {
	RequestedBlobs []string `json:"requested_blobs"`
	BlobPayRate    float64  `json:"blob_data_payment_rate"`
	RequestedBlob  string   `json:"requested_blob"`
}

// BlobResponse is the JSON response from a blob exchange peer.
type BlobResponse struct {
	AvailableBlobs []string      `json:"available_blobs,omitempty"`
	PaymentRate    string        `json:"blob_data_payment_rate,omitempty"`
	IncomingBlob   *IncomingBlob `json:"incoming_blob,omitempty"`
	Error          string        `json:"error,omitempty"`
}

type IncomingBlob struct {
	BlobHash string `json:"blob_hash"`
	Length   int    `json:"length"`
}

// DownloadBlob downloads a single blob from a peer by TCP.
// Returns the raw (encrypted) blob bytes.
func DownloadBlob(ip net.IP, tcpPort int, blobHash string) ([]byte, error) {
	return DownloadBlobContext(context.Background(), ip, tcpPort, blobHash)
}

// DownloadBlobContext downloads a single blob from a peer by TCP and closes
// the connection promptly when the context is canceled.
func DownloadBlobContext(ctx context.Context, ip net.IP, tcpPort int, blobHash string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	addr := net.JoinHostPort(ip.String(), strconv.Itoa(tcpPort))
	dialer := net.Dialer{Timeout: ConnectTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("blob: connect %s: %w", addr, err)
	}
	defer conn.Close()
	stopCancellation := context.AfterFunc(ctx, func() {
		_ = conn.SetDeadline(time.Now())
		_ = conn.Close()
	})
	defer stopCancellation()
	deadline := time.Now().Add(DownloadTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("blob: set connection deadline: %w", err)
	}

	// Send request
	req := BlobRequest{
		RequestedBlobs: []string{blobHash},
		BlobPayRate:    0.0,
		RequestedBlob:  blobHash,
	}
	reqBytes, _ := json.Marshal(req)
	if _, err := conn.Write(reqBytes); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
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
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, fmt.Errorf("blob: read response: %w", err)
		}
		jsonBuf.WriteByte(b)
		if jsonBuf.Len() > MaxBlobResponseHeaderSize {
			return nil, fmt.Errorf("blob: response header exceeds %d bytes", MaxBlobResponseHeaderSize)
		}

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
	if err := validateIncomingBlob(resp.IncomingBlob, blobHash); err != nil {
		return nil, err
	}

	// Read exact number of blob bytes
	blobData := make([]byte, resp.IncomingBlob.Length)
	_, err = io.ReadFull(reader, blobData)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("blob: read blob data: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
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

func validateIncomingBlob(incoming *IncomingBlob, requestedHash string) error {
	if incoming == nil {
		return errors.New("blob: response has no incoming blob")
	}
	if incoming.BlobHash != requestedHash {
		return fmt.Errorf("blob: peer offered unexpected blob %q", incoming.BlobHash)
	}
	if incoming.Length <= 0 || incoming.Length > MaxBlobSize {
		return fmt.Errorf("blob: peer length %d exceeds the resource limit", incoming.Length)
	}
	return nil
}
