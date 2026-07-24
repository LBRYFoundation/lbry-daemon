package peer

import (
	"bufio"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"lbry/daemon/blob"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type failingListener struct {
	accepts atomic.Int32
	err     error
}

func (listener *failingListener) Accept() (net.Conn, error) {
	listener.accepts.Add(1)
	return nil, listener.err
}

func (*failingListener) Close() error { return nil }

func (*failingListener) Addr() net.Addr { return testAddr("listener") }

type testAddr string

func (address testAddr) Network() string { return string(address) }

func (address testAddr) String() string { return string(address) }

func TestServeStopsWhenListenerIsClosed(t *testing.T) {
	listener := &failingListener{err: errors.Join(errors.New("accept failed"), net.ErrClosed)}
	server := CreateServer(blob.NewManager())

	if err := server.Serve(listener); err != nil {
		t.Fatalf("Serve() error = %v, want nil", err)
	}
	if accepts := listener.accepts.Load(); accepts != 1 {
		t.Fatalf("Accept() called %d times, want 1", accepts)
	}
}

func TestServeReturnsUnexpectedAcceptError(t *testing.T) {
	want := errors.New("accept failed")
	listener := &failingListener{err: want}
	server := CreateServer(blob.NewManager())

	if err := server.Serve(listener); !errors.Is(err, want) {
		t.Fatalf("Serve() error = %v, want %v", err, want)
	}
	if accepts := listener.accepts.Load(); accepts != 1 {
		t.Fatalf("Accept() called %d times, want 1", accepts)
	}
}

func TestMalformedRequestTerminatesConnectionHandler(t *testing.T) {
	serverConnection, clientConnection := net.Pipe()
	defer func() { _ = clientConnection.Close() }()

	server := CreateServer(blob.NewManager())
	done := make(chan struct{})
	go func() {
		server.handleConnection(serverConnection)
		close(done)
	}()

	if _, err := clientConnection.Write([]byte("{]\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("connection handler did not stop after JSON decode error")
	}
}

func TestPeerProtocolAcceptsRateAndReturnsPaymentAddress(t *testing.T) {
	serverConnection, clientConnection := net.Pipe()
	defer func() { _ = clientConnection.Close() }()
	server := CreateServer(blob.NewManager())
	server.SetPaymentAddress("bGoPaymentAddress")
	done := make(chan struct{})
	go func() {
		server.handleConnection(serverConnection)
		close(done)
	}()

	if _, err := clientConnection.Write([]byte(`{"blob_data_payment_rate":0,"lbrycrd_address":true}`)); err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := json.NewDecoder(clientConnection).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response["blob_data_payment_rate"] != "RATE_ACCEPTED" ||
		response["lbrycrd_address"] != "bGoPaymentAddress" {
		t.Fatalf("peer response = %#v", response)
	}
	_ = clientConnection.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("peer handler did not stop")
	}
}

func TestReadJSONRequestEnforcesPinnedSizeLimit(t *testing.T) {
	oversized := `{"padding":"` + strings.Repeat("x", MaxRequestSize) + `"}`
	_, err := readJSONRequest(bufio.NewReader(strings.NewReader(oversized)), MaxRequestSize)
	if err == nil || !strings.Contains(err.Error(), "exceeds 1199 bytes") {
		t.Fatalf("oversized request error = %v", err)
	}
}

func TestPeerServerRoundTripsBlobWithoutHeaderDelimiter(t *testing.T) {
	data := []byte("peer protocol round trip")
	digest := sha512.Sum384(data)
	hash := hex.EncodeToString(digest[:])
	manager := blob.NewManager()
	if err := manager.Set(hash, data, false); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := CreateServer(manager)
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()

	address := listener.Addr().(*net.TCPAddr)
	downloaded, err := blob.DownloadBlob(address.IP, address.Port, hash)
	if err != nil {
		t.Fatal(err)
	}
	if string(downloaded) != string(data) {
		t.Fatalf("downloaded blob = %q, want %q", downloaded, data)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-serveDone; err != nil {
		t.Fatal(err)
	}
}

func TestAvailabilityDoesNotFetchMissingBlob(t *testing.T) {
	manager := blob.NewManager()
	var fetches atomic.Int32
	manager.SetFetcher(func(context.Context, string) ([]byte, error) {
		fetches.Add(1)
		return nil, errors.New("must not fetch")
	})
	server := CreateServer(manager)
	missing := strings.Repeat("0", blob.BlobHashLength)
	if available := server.getAvailableBlobs([]string{missing}); len(available) != 0 {
		t.Fatalf("available blobs = %v", available)
	}
	if fetches.Load() != 0 {
		t.Fatalf("availability triggered %d fetches", fetches.Load())
	}
}

type singleConnListener struct {
	conn      net.Conn
	accepted  chan struct{}
	closed    chan struct{}
	acceptOne sync.Once
	closeOne  sync.Once
}

func newSingleConnListener(conn net.Conn) *singleConnListener {
	return &singleConnListener{
		conn:     conn,
		accepted: make(chan struct{}),
		closed:   make(chan struct{}),
	}
}

func (listener *singleConnListener) Accept() (net.Conn, error) {
	accepted := false
	listener.acceptOne.Do(func() {
		accepted = true
		close(listener.accepted)
	})
	if accepted {
		return listener.conn, nil
	}
	<-listener.closed
	return nil, net.ErrClosed
}

func (listener *singleConnListener) Close() error {
	listener.closeOne.Do(func() { close(listener.closed) })
	return nil
}

func (*singleConnListener) Addr() net.Addr { return testAddr("single-connection") }

type readSignalConn struct {
	net.Conn
	started chan struct{}
	once    sync.Once
}

func (conn *readSignalConn) Read(data []byte) (int, error) {
	conn.once.Do(func() { close(conn.started) })
	return conn.Conn.Read(data)
}

type closeIgnoringConn struct{ net.Conn }

func (*closeIgnoringConn) Close() error { return nil }

func TestShutdownClosesActiveConnectionAndWaitsForHandler(t *testing.T) {
	serverConnection, clientConnection := net.Pipe()
	defer func() { _ = clientConnection.Close() }()

	readStarted := make(chan struct{})
	listener := newSingleConnListener(&readSignalConn{
		Conn:    serverConnection,
		started: readStarted,
	})
	server := CreateServer(blob.NewManager())
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()

	select {
	case <-readStarted:
	case <-time.After(time.Second):
		t.Fatal("connection handler did not begin reading")
	}
	if err := clientConnection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve() did not stop after Shutdown()")
	}

	if _, err := clientConnection.Read(make([]byte, 1)); err == nil {
		t.Fatal("client connection remained open after Shutdown()")
	}

	server.mu.Lock()
	activeConnections := len(server.connections)
	server.mu.Unlock()
	if activeConnections != 0 {
		t.Fatalf("active connections = %d, want 0", activeConnections)
	}
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown() error = %v", err)
	}
}

func TestShutdownHonorsContextWhileHandlerIsBlocked(t *testing.T) {
	serverConnection, clientConnection := net.Pipe()
	defer func() { _ = clientConnection.Close() }()

	readStarted := make(chan struct{})
	listener := newSingleConnListener(&readSignalConn{
		Conn:    &closeIgnoringConn{Conn: serverConnection},
		started: readStarted,
	})
	server := CreateServer(blob.NewManager())
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()

	select {
	case <-readStarted:
	case <-time.After(time.Second):
		t.Fatal("connection handler did not begin reading")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := server.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown() error = %v, want context deadline exceeded", err)
	}

	if err := serverConnection.Close(); err != nil {
		t.Fatal(err)
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Second)
	defer cleanupCancel()
	if err := server.Shutdown(cleanupCtx); err != nil {
		t.Fatalf("Shutdown() after releasing handler error = %v", err)
	}
	if err := <-serveDone; err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
}
