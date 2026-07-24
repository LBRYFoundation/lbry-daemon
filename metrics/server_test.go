package metrics

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestServerServesPrometheusMetricsAndShutsDown(t *testing.T) {
	const payload = "# HELP fixture A fixture metric.\n# TYPE fixture gauge\nfixture 7\n"
	server := CreateServer(WithGatherer(GatherFunc(func() ([]byte, error) {
		return []byte(payload), nil
	})))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()

	response, err := http.Get("http://" + listener.Addr().String() + "/metrics")
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || string(body) != payload {
		t.Fatalf("metrics response status=%d body=%q", response.StatusCode, body)
	}
	if got := response.Header.Get("Content-Type"); got != ContentType {
		t.Fatalf("content type = %q, want %q", got, ContentType)
	}
	if got := response.Header.Get("Content-Length"); got != strconv.Itoa(len(payload)) {
		t.Fatalf("content length = %q", got)
	}

	request, err := http.NewRequest(http.MethodHead, "http://"+listener.Addr().String()+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	headResponse, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	headBody, err := io.ReadAll(headResponse.Body)
	headResponse.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if headResponse.StatusCode != http.StatusOK || len(headBody) != 0 ||
		headResponse.Header.Get("Content-Length") != strconv.Itoa(len(payload)) {
		t.Fatalf("HEAD status=%d length=%q body=%q", headResponse.StatusCode, headResponse.Header.Get("Content-Length"), headBody)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-serveResult; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("Serve returned %v, want http.ErrServerClosed", err)
	}
}

func TestListenAddressUsesLegacyAllInterfacesBind(t *testing.T) {
	if got, want := ListenAddress(2112), "0.0.0.0:2112"; got != want {
		t.Fatalf("listen address = %q, want %q", got, want)
	}
}

func TestServerRejectsOtherPathsAndMethods(t *testing.T) {
	server := CreateServer(WithGatherer(GatherFunc(func() ([]byte, error) {
		return []byte("fixture 1\n"), nil
	})))

	notFound := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(notFound, httptest.NewRequest(http.MethodGet, "/metrics/", nil))
	if notFound.Code != http.StatusNotFound {
		t.Fatalf("unexpected path status = %d", notFound.Code)
	}

	methodNotAllowed := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(methodNotAllowed, httptest.NewRequest(http.MethodPost, "/metrics", nil))
	if methodNotAllowed.Code != http.StatusMethodNotAllowed || methodNotAllowed.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("POST status=%d allow=%q", methodNotAllowed.Code, methodNotAllowed.Header().Get("Allow"))
	}
}

func TestServerReturnsInternalErrorWhenGatheringFails(t *testing.T) {
	wantErr := errors.New("gather failed")
	server := CreateServer(
		WithGatherer(GatherFunc(func() ([]byte, error) { return nil, wantErr })),
		WithLogger(log.New(io.Discard, "", 0)),
	)
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("gather failure status = %d", response.Code)
	}
}

func TestRuntimeGathererProducesValidMetricFamilies(t *testing.T) {
	payload, err := (RuntimeGatherer{}).Gather()
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, fragment := range []string{
		"# TYPE go_goroutines gauge\n",
		"go_goroutines ",
		"# TYPE go_memstats_alloc_bytes gauge\n",
		"# TYPE go_memstats_sys_bytes gauge\n",
	} {
		if !strings.Contains(text, fragment) {
			t.Errorf("runtime metrics are missing %q:\n%s", fragment, text)
		}
	}
	if !strings.HasSuffix(text, "\n") {
		t.Fatalf("runtime metrics do not end with a newline: %q", text)
	}
}
