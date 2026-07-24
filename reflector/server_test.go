package reflector

import (
	"context"
	"errors"
	"lbry/daemon/blob"
	"net"
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
