package stream

import (
	"context"
	"errors"
	"lbry/daemon/blob"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

type shutdownListener struct {
	acceptStarted chan struct{}
	closed        chan struct{}
	startOnce     sync.Once
	closeOnce     sync.Once
}

func newShutdownListener() *shutdownListener {
	return &shutdownListener{
		acceptStarted: make(chan struct{}),
		closed:        make(chan struct{}),
	}
}

func (listener *shutdownListener) Accept() (net.Conn, error) {
	listener.startOnce.Do(func() { close(listener.acceptStarted) })
	<-listener.closed
	return nil, net.ErrClosed
}

func (listener *shutdownListener) Close() error {
	listener.closeOnce.Do(func() { close(listener.closed) })
	return nil
}

func (*shutdownListener) Addr() net.Addr { return lifecycleAddr("stream") }

type lifecycleAddr string

func (address lifecycleAddr) Network() string { return string(address) }

func (address lifecycleAddr) String() string { return string(address) }

func TestServeAndShutdown(t *testing.T) {
	listener := newShutdownListener()

	server := CreateServer(blob.NewManager())
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(listener)
	}()

	select {
	case <-listener.acceptStarted:
	case <-time.After(time.Second):
		t.Fatal("Serve() did not start accepting connections")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	select {
	case err := <-serveResult:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("Serve() error = %v, want http.ErrServerClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve() did not return after Shutdown()")
	}
}
