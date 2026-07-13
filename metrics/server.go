package metrics

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"strings"
)

const (
	BindHost    = "0.0.0.0"
	ContentType = "text/plain; version=0.0.4; charset=utf-8"
)

type Gatherer interface {
	Gather() ([]byte, error)
}

type GatherFunc func() ([]byte, error)

func (gather GatherFunc) Gather() ([]byte, error) {
	return gather()
}

type ServerOption func(*Server)

type Server struct {
	httpServer *http.Server
	gatherer   Gatherer
	logger     *log.Logger
}

func WithGatherer(gatherer Gatherer) ServerOption {
	return func(server *Server) {
		if gatherer != nil {
			server.gatherer = gatherer
		}
	}
}

func WithLogger(logger *log.Logger) ServerOption {
	return func(server *Server) {
		if logger != nil {
			server.logger = logger
		}
	}
}

func CreateServer(options ...ServerOption) *Server {
	server := &Server{gatherer: RuntimeGatherer{}, logger: log.Default()}
	for _, option := range options {
		option(server)
	}
	server.httpServer = &http.Server{Handler: http.HandlerFunc(server.handle)}
	return server
}

func ListenAddress(port int) string {
	return net.JoinHostPort(BindHost, strconv.Itoa(port))
}

func (server *Server) Serve(listener net.Listener) error {
	if listener == nil {
		return fmt.Errorf("metrics: listener is nil")
	}
	return server.httpServer.Serve(listener)
}

func (server *Server) Shutdown(ctx context.Context) error {
	return server.httpServer.Shutdown(ctx)
}

func (server *Server) Close() error {
	return server.httpServer.Close()
}

func (server *Server) handle(w http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/metrics" {
		http.NotFound(w, request)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	payload, err := server.gatherer.Gather()
	if err != nil {
		server.logger.Printf("could not generate prometheus data: %v", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", ContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	w.WriteHeader(http.StatusOK)
	if request.Method == http.MethodGet {
		_, _ = w.Write(payload)
	}
}

// RuntimeGatherer provides useful process metrics without introducing a
// registry dependency whose default metric families differ from Python's.
// Application collectors can replace it through WithGatherer.
type RuntimeGatherer struct{}

func (RuntimeGatherer) Gather() ([]byte, error) {
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	var output strings.Builder
	fmt.Fprintln(&output, "# HELP go_goroutines Number of goroutines that currently exist.")
	fmt.Fprintln(&output, "# TYPE go_goroutines gauge")
	fmt.Fprintf(&output, "go_goroutines %d\n", runtime.NumGoroutine())
	fmt.Fprintln(&output, "# HELP go_memstats_alloc_bytes Number of bytes allocated and still in use.")
	fmt.Fprintln(&output, "# TYPE go_memstats_alloc_bytes gauge")
	fmt.Fprintf(&output, "go_memstats_alloc_bytes %d\n", memory.Alloc)
	fmt.Fprintln(&output, "# HELP go_memstats_sys_bytes Number of bytes obtained from the system.")
	fmt.Fprintln(&output, "# TYPE go_memstats_sys_bytes gauge")
	fmt.Fprintf(&output, "go_memstats_sys_bytes %d\n", memory.Sys)
	return []byte(output.String()), nil
}
