package reflector

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
	"lbry/daemon/blob"
	"log"
	"math"
	"net"
	"sync"
	"time"
)

type ReflectorServer struct {
	blobManager *blob.BlobManager

	mu           sync.Mutex
	listener     net.Listener
	connections  map[net.Conn]struct{}
	handlers     sync.WaitGroup
	stopping     bool
	shutdownOnce sync.Once
	shutdownDone chan struct{}
}

func CreateServer(blobManager *blob.BlobManager) *ReflectorServer {
	return &ReflectorServer{
		blobManager:  blobManager,
		connections:  make(map[net.Conn]struct{}),
		shutdownDone: make(chan struct{}),
	}
}

func (reflectorServer *ReflectorServer) StartServer(listener net.Listener) {
	if err := reflectorServer.Serve(listener); err != nil {
		log.Printf("reflector server accept failed: %v", err)
	}
}

func (reflectorServer *ReflectorServer) Serve(listener net.Listener) error {
	reflectorServer.mu.Lock()
	if reflectorServer.stopping {
		reflectorServer.mu.Unlock()
		_ = listener.Close()
		return nil
	}
	if reflectorServer.listener != nil {
		reflectorServer.mu.Unlock()
		return errors.New("reflector server is already serving")
	}
	reflectorServer.listener = listener
	reflectorServer.mu.Unlock()

	defer func() {
		reflectorServer.mu.Lock()
		if reflectorServer.listener == listener {
			reflectorServer.listener = nil
		}
		reflectorServer.mu.Unlock()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			reflectorServer.mu.Lock()
			stopping := reflectorServer.stopping
			reflectorServer.mu.Unlock()
			if stopping || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}

		reflectorServer.mu.Lock()
		if reflectorServer.stopping {
			reflectorServer.mu.Unlock()
			_ = conn.Close()
			return nil
		}
		reflectorServer.connections[conn] = struct{}{}
		reflectorServer.handlers.Add(1)
		reflectorServer.mu.Unlock()

		go reflectorServer.serveConnection(conn)
	}
}

// Shutdown stops accepting connections, closes active connections, and waits
// for their handlers to exit or for ctx to expire.
func (reflectorServer *ReflectorServer) Shutdown(ctx context.Context) error {
	reflectorServer.mu.Lock()
	reflectorServer.stopping = true
	listener := reflectorServer.listener
	connections := make([]net.Conn, 0, len(reflectorServer.connections))
	for conn := range reflectorServer.connections {
		connections = append(connections, conn)
	}
	reflectorServer.mu.Unlock()

	if listener != nil {
		_ = listener.Close()
	}
	for _, conn := range connections {
		_ = conn.Close()
	}

	reflectorServer.shutdownOnce.Do(func() {
		go func() {
			reflectorServer.handlers.Wait()
			close(reflectorServer.shutdownDone)
		}()
	})

	select {
	case <-reflectorServer.shutdownDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (reflectorServer *ReflectorServer) serveConnection(conn net.Conn) {
	defer func() {
		_ = conn.Close()
		reflectorServer.mu.Lock()
		delete(reflectorServer.connections, conn)
		reflectorServer.mu.Unlock()
		reflectorServer.handlers.Done()
	}()
	reflectorServer.handleConnection(conn)
}

func (reflectorServer *ReflectorServer) handleConnection(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return
	}

	reader := bufio.NewReader(conn)
	jsonEncoder := json.NewEncoder(conn)

	version := -1
	expectedBlobs := make(map[string]int)

	for {
		var data map[string]any

		if err := readControlMessage(reader, &data); err != nil {
			return
		}

		versionValue, hasVersion := data["version"]
		if version == -1 {
			if hasVersion {
				versionNumber, ok := versionValue.(float64)
				if !ok {
					return
				}
				parsedVersion, valid := exactInteger(versionNumber, 0, math.MaxInt32)
				if !valid {
					return
				}
				version = parsedVersion
				if err := jsonEncoder.Encode(map[string]any{
					"version": version,
				}); err != nil {
					return
				}
				continue
			}
			return
		}

		blobHashValue, hasBlobHash := data["blob_hash"]
		blobSizeValue, hasBlobSize := data["blob_size"]

		sdBlobHashValue, hasSDBlobHash := data["sd_blob_hash"]
		sdBlobSizeValue, hasSDBlobSize := data["sd_blob_size"]

		if version >= 0 && hasBlobHash && hasBlobSize {
			blobHash, hashOK := blobHashValue.(string)
			blobSizeNumber, sizeOK := blobSizeValue.(float64)
			if !hashOK || !sizeOK {
				return
			}
			blobSize, validSize := exactInteger(blobSizeNumber, 1, blob.MaxBlobSize)
			expectedSize, expected := expectedBlobs[blobHash]
			if !blob.ValidHash(blobHash) || !validSize || !expected || expectedSize != blobSize {
				return
			}
			if existing, ok := reflectorServer.blobManager.GetLocal(blobHash); ok && len(existing) == blobSize {
				if err := jsonEncoder.Encode(map[string]any{"send_blob": false}); err != nil {
					return
				}
				continue
			}

			if err := jsonEncoder.Encode(map[string]any{
				"send_blob": true, // TODO: Improve response
			}); err != nil {
				return
			}

			blobData := make([]byte, blobSize)
			_, err := io.ReadFull(reader, blobData)
			if err != nil {
				return
			}

			err = storeVerifiedBlob(reflectorServer.blobManager, blobHash, blobData, false)

			if encodeErr := jsonEncoder.Encode(map[string]any{
				"received_blob": err == nil,
			}); encodeErr != nil {
				return
			}
			if err != nil {
				return
			}
			continue
		}

		if version >= 1 && hasSDBlobHash && hasSDBlobSize {
			sdBlobHash, hashOK := sdBlobHashValue.(string)
			sdBlobSizeNumber, sizeOK := sdBlobSizeValue.(float64)
			if !hashOK || !sizeOK {
				return
			}
			sdBlobSize, validSize := exactInteger(sdBlobSizeNumber, 1, blob.MaxStreamDescriptorSize)

			if !blob.ValidHash(sdBlobHash) || !validSize {
				return
			}
			if existing, ok := reflectorServer.blobManager.GetLocal(sdBlobHash); ok && len(existing) == sdBlobSize {
				descriptor, decodeErr := blob.DecodeDescriptor(sdBlobHash, existing)
				if decodeErr != nil {
					return
				}
				needed := make([]string, 0, len(descriptor.ContentBlobs()))
				for _, item := range descriptor.ContentBlobs() {
					expectedBlobs[item.BlobHash] = item.Length
					if !reflectorServer.blobManager.Has(item.BlobHash) {
						needed = append(needed, item.BlobHash)
					}
				}
				if err := jsonEncoder.Encode(map[string]any{
					"send_sd_blob": false, "needed_blobs": needed,
				}); err != nil {
					return
				}
				continue
			}

			if err := jsonEncoder.Encode(map[string]any{
				"send_sd_blob": true, // TODO: Improve response
			}); err != nil {
				return
			}

			sdBlobData := make([]byte, sdBlobSize)
			_, err := io.ReadFull(reader, sdBlobData)
			if err != nil {
				return
			}

			descriptor, decodeErr := blob.DecodeDescriptor(sdBlobHash, sdBlobData)
			if decodeErr == nil {
				for _, item := range descriptor.ContentBlobs() {
					expectedBlobs[item.BlobHash] = item.Length
				}
				decodeErr = storeVerifiedBlob(reflectorServer.blobManager, sdBlobHash, sdBlobData, true)
			}
			err = decodeErr

			if encodeErr := jsonEncoder.Encode(map[string]any{
				"received_sd_blob": err == nil,
			}); encodeErr != nil {
				return
			}
			if err != nil {
				return
			}
			continue
		}

		return
	}
}

func readControlMessage(reader *bufio.Reader, target any) error {
	return readJSONMessage(reader, target, blob.MaxBlobResponseHeaderSize)
}

func readJSONMessage(reader *bufio.Reader, target any, maximum int) error {
	var frame bytes.Buffer
	stack := make([]byte, 0, 8)
	started := false
	inString := false
	escaped := false
	for {
		value, err := reader.ReadByte()
		if err != nil {
			return err
		}
		if frame.Len()+1 > maximum {
			return fmt.Errorf("reflector: JSON message exceeds %d bytes", maximum)
		}
		if !started {
			if value == ' ' || value == '\t' || value == '\r' || value == '\n' {
				frame.WriteByte(value)
				continue
			}
			if value != '{' {
				return errors.New("reflector: control message must be a JSON object")
			}
			started = true
			stack = append(stack, value)
			frame.WriteByte(value)
			continue
		}
		frame.WriteByte(value)
		if escaped {
			escaped = false
			continue
		}
		if inString && value == '\\' {
			escaped = true
			continue
		}
		if value == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch value {
		case '{', '[':
			stack = append(stack, value)
		case '}', ']':
			want := byte('{')
			if value == ']' {
				want = '['
			}
			if len(stack) == 0 || stack[len(stack)-1] != want {
				return errors.New("reflector: mismatched JSON delimiter")
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				if err := json.Unmarshal(bytes.TrimSpace(frame.Bytes()), target); err != nil {
					return fmt.Errorf("reflector: invalid control message: %w", err)
				}
				return nil
			}
		}
	}
}

func exactInteger(value float64, minimum, maximum int) (int, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) ||
		value < float64(minimum) || value > float64(maximum) {
		return 0, false
	}
	return int(value), true
}

func storeVerifiedBlob(manager *blob.BlobManager, blobHash string, data []byte, descriptor bool) error {
	digest := sha512.Sum384(data)
	if hex.EncodeToString(digest[:]) != blobHash {
		return errors.New("reflector: blob hash mismatch")
	}
	return manager.Set(blobHash, data, descriptor)
}
