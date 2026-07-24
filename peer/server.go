package peer

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"lbry/daemon/blob"
	"log"
	"net"
	"sync"
	"time"
)

const (
	MaxRequestSize  = 1200
	IdleTimeout     = 30 * time.Second
	TransferTimeout = 60 * time.Second
)

type PeerServer struct {
	blobManager    *blob.BlobManager
	paymentAddress string

	mu           sync.Mutex
	listener     net.Listener
	connections  map[net.Conn]struct{}
	handlers     sync.WaitGroup
	stopping     bool
	shutdownOnce sync.Once
	shutdownDone chan struct{}
}

func (peerServer *PeerServer) SetPaymentAddress(address string) {
	peerServer.mu.Lock()
	peerServer.paymentAddress = address
	peerServer.mu.Unlock()
}

func CreateServer(blobManager *blob.BlobManager) *PeerServer {
	return &PeerServer{
		blobManager:  blobManager,
		connections:  make(map[net.Conn]struct{}),
		shutdownDone: make(chan struct{}),
	}
}

func (peerServer *PeerServer) StartServer(listener net.Listener) {
	if err := peerServer.Serve(listener); err != nil {
		log.Printf("peer server accept failed: %v", err)
	}
}

func (peerServer *PeerServer) Serve(listener net.Listener) error {
	peerServer.mu.Lock()
	if peerServer.stopping {
		peerServer.mu.Unlock()
		_ = listener.Close()
		return nil
	}
	if peerServer.listener != nil {
		peerServer.mu.Unlock()
		return errors.New("peer server is already serving")
	}
	peerServer.listener = listener
	peerServer.mu.Unlock()

	defer func() {
		peerServer.mu.Lock()
		if peerServer.listener == listener {
			peerServer.listener = nil
		}
		peerServer.mu.Unlock()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			peerServer.mu.Lock()
			stopping := peerServer.stopping
			peerServer.mu.Unlock()
			if stopping || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}

		peerServer.mu.Lock()
		if peerServer.stopping {
			peerServer.mu.Unlock()
			_ = conn.Close()
			return nil
		}
		peerServer.connections[conn] = struct{}{}
		peerServer.handlers.Add(1)
		peerServer.mu.Unlock()

		go peerServer.serveConnection(conn)
	}
}

// Shutdown stops accepting connections, closes active connections, and waits
// for their handlers to exit or for ctx to expire.
func (peerServer *PeerServer) Shutdown(ctx context.Context) error {
	peerServer.mu.Lock()
	peerServer.stopping = true
	listener := peerServer.listener
	connections := make([]net.Conn, 0, len(peerServer.connections))
	for conn := range peerServer.connections {
		connections = append(connections, conn)
	}
	peerServer.mu.Unlock()

	if listener != nil {
		_ = listener.Close()
	}
	for _, conn := range connections {
		_ = conn.Close()
	}

	peerServer.shutdownOnce.Do(func() {
		go func() {
			peerServer.handlers.Wait()
			close(peerServer.shutdownDone)
		}()
	})

	select {
	case <-peerServer.shutdownDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (peerServer *PeerServer) serveConnection(conn net.Conn) {
	defer func() {
		_ = conn.Close()
		peerServer.mu.Lock()
		delete(peerServer.connections, conn)
		peerServer.mu.Unlock()
		peerServer.handlers.Done()
	}()
	peerServer.handleConnection(conn)
}

func (peerServer *PeerServer) handleConnection(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	for {
		if err := conn.SetReadDeadline(time.Now().Add(IdleTimeout)); err != nil {
			return
		}
		var data map[string]any
		request, err := readJSONRequest(reader, MaxRequestSize)
		if err != nil || json.Unmarshal(request, &data) != nil {
			return
		}

		responseData := map[string]any{}
		recognized := false
		if _, requested := data["lbrycrd_address"]; requested {
			recognized = true
			peerServer.mu.Lock()
			responseData["lbrycrd_address"] = peerServer.paymentAddress
			peerServer.mu.Unlock()
		}

		requestedBlobsValue, hasRequestedBlobs := data["requested_blobs"]
		if hasRequestedBlobs {
			recognized = true
			requestedBlobs, ok := stringsFromJSON(requestedBlobsValue)
			if !ok {
				return
			}
			responseData["available_blobs"] = peerServer.getAvailableBlobs(requestedBlobs)
		}

		blobDataPaymentRateValue, hasBlobDataPaymentRate := data["blob_data_payment_rate"]
		if hasBlobDataPaymentRate {
			recognized = true
			blobDataPaymentRate, ok := blobDataPaymentRateValue.(float64)
			if !ok {
				return
			}
			responseData["blob_data_payment_rate"] = getBlobDataPaymentRate(blobDataPaymentRate)
		}

		var incomingBlob map[string]any
		var blobData []byte

		requestedBlobValue, hasRequestedBlob := data["requested_blob"]
		if hasRequestedBlob {
			recognized = true
			requestedBlob, ok := requestedBlobValue.(string)
			if !ok {
				return
			}
			incomingBlob, blobData = peerServer.getRequestedBlob(requestedBlob)
			if incomingBlob != nil {
				responseData["incoming_blob"] = incomingBlob
			}
		}
		if !recognized {
			return
		}

		encoded, err := json.Marshal(responseData)
		if err != nil {
			return
		}
		if err := writeAll(conn, encoded); err != nil {
			return
		}
		if blobData != nil {
			if err := conn.SetWriteDeadline(time.Now().Add(TransferTimeout)); err != nil {
				return
			}
			if err := writeAll(conn, blobData); err != nil {
				return
			}
		}
	}
}

func readJSONRequest(reader *bufio.Reader, limit int) ([]byte, error) {
	request := make([]byte, 0, 256)
	depth := 0
	arrayDepth := 0
	inString := false
	escaped := false
	started := false
	for len(request) < limit {
		value, err := reader.ReadByte()
		if err != nil {
			return nil, err
		}
		request = append(request, value)
		if len(request) >= limit {
			return nil, fmt.Errorf("peer request exceeds %d bytes", limit-1)
		}
		if !started {
			if value == ' ' || value == '\t' || value == '\r' || value == '\n' {
				continue
			}
			if value != '{' {
				return nil, errors.New("peer request is not a JSON object")
			}
			started = true
			depth = 1
			continue
		}
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
		case '{':
			depth++
		case '}':
			if depth == 1 && arrayDepth != 0 {
				return nil, errors.New("peer request has mismatched JSON brackets")
			}
			depth--
			if depth == 0 {
				return request, nil
			}
		case '[':
			arrayDepth++
		case ']':
			if arrayDepth == 0 {
				return nil, errors.New("peer request has mismatched JSON brackets")
			}
			arrayDepth--
		}
	}
	return nil, fmt.Errorf("peer request exceeds %d bytes", limit-1)
}

func stringsFromJSON(value any) ([]string, bool) {
	items, ok := value.([]any)
	if !ok {
		return nil, false
	}
	strings := make([]string, len(items))
	for index, item := range items {
		strings[index], ok = item.(string)
		if !ok {
			return nil, false
		}
	}
	return strings, true
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func (peerServer *PeerServer) getAvailableBlobs(requestedBlobs []string) []string {
	var availableBlobs []string

	for _, requestedBlob := range requestedBlobs {
		_, ok := peerServer.blobManager.GetLocal(requestedBlob)
		if ok {
			availableBlobs = append(availableBlobs, requestedBlob)
		}
	}

	return availableBlobs
}

func getBlobDataPaymentRate(blobDataPaymentRate float64) string {
	return "RATE_ACCEPTED"
}

func (peerServer *PeerServer) getRequestedBlob(requestedBlob string) (map[string]any, []byte) {
	blobData, ok := peerServer.blobManager.GetLocal(requestedBlob)
	if ok {
		return map[string]any{
			"blob_hash": requestedBlob,
			"length":    len(blobData),
		}, blobData
	}

	return nil, nil
}
