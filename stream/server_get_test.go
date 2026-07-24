package stream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"lbry/daemon/blob"
)

func TestStreamGetRoutesBuildLegacyURIsAndRedirect(t *testing.T) {
	for _, test := range []struct {
		path string
		uri  string
	}{
		{path: "/get/video", uri: "lbry://video"},
		{path: "/get/video/abc123", uri: "lbry://video#abc123"},
		{path: "/get/caf%C3%A9?ignored=yes", uri: "lbry://café"},
		{path: "/get/video%2Fabc123", uri: "lbry://video#abc123"},
	} {
		t.Run(test.path, func(t *testing.T) {
			var gotURI string
			server := CreateServer(blob.NewManager(), WithStreamGet(func(_ context.Context, uri string) (string, error) {
				gotURI = uri
				return "descriptor", nil
			}, func() bool { return true }))
			server.logf = func(string, ...any) {}
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			response := httptest.NewRecorder()
			server.httpServer.Handler.ServeHTTP(response, request)
			if gotURI != test.uri || response.Code != http.StatusFound ||
				response.Header().Get("Location") != "/stream/descriptor" ||
				response.Header().Get("Content-Type") != "text/plain; charset=utf-8" ||
				response.Body.String() != "302: Found" {
				t.Fatalf("route = uri %q, response %d %q %#v", gotURI, response.Code, response.Body.String(), response.Header())
			}
		})
	}
}

func TestStreamGetDisabledAndFailureResponsesMatchAiohttp(t *testing.T) {
	called := false
	disabled := CreateServer(blob.NewManager(), WithStreamGet(func(context.Context, string) (string, error) {
		called = true
		return "", nil
	}, func() bool { return false }))
	disabled.logf = func(string, ...any) {}
	request := httptest.NewRequest(http.MethodGet, "/get/video", nil)
	response := httptest.NewRecorder()
	disabled.httpServer.Handler.ServeHTTP(response, request)
	if called || response.Code != http.StatusForbidden || response.Body.String() != "403: Forbidden" {
		t.Fatalf("disabled response = called %t, %d %q", called, response.Code, response.Body.String())
	}

	failed := CreateServer(blob.NewManager(), WithStreamGet(func(context.Context, string) (string, error) {
		return "", errors.New("download failed")
	}, func() bool { return true }))
	failed.logf = func(string, ...any) {}
	response = httptest.NewRecorder()
	failed.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || response.Body.String() != "download failed" {
		t.Fatalf("failed response = %d %q", response.Code, response.Body.String())
	}
}

func TestStreamGetRejectsUnsupportedMethod(t *testing.T) {
	server := CreateServer(blob.NewManager(), WithStreamGet(func(context.Context, string) (string, error) {
		t.Fatal("POST invoked managed get")
		return "", nil
	}, func() bool { return true }))
	request := httptest.NewRequest(http.MethodPost, "/get/video", nil)
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET,HEAD" ||
		response.Body.String() != "405: Method Not Allowed" {
		t.Fatalf("POST response = %d %q %#v", response.Code, response.Body.String(), response.Header())
	}
}

func TestStreamGetHEADReturnsRedirectHeadersWithoutBody(t *testing.T) {
	server := CreateServer(blob.NewManager(), WithStreamGet(func(context.Context, string) (string, error) {
		return "descriptor", nil
	}, func() bool { return true }))
	server.logf = func(string, ...any) {}
	request := httptest.NewRequest(http.MethodHead, "/get/video", nil)
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusFound || response.Header().Get("Location") != "/stream/descriptor" ||
		response.Header().Get("Content-Length") != "10" || response.Body.Len() != 0 {
		t.Fatalf("HEAD redirect = %d %q %#v", response.Code, response.Body.String(), response.Header())
	}
}

func TestStreamGetShutdownCancelsBlockedPreparation(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	server := CreateServer(blob.NewManager(), WithStreamGet(func(ctx context.Context, _ string) (string, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		return "", ctx.Err()
	}, func() bool { return true }))
	server.logf = func(string, ...any) {}
	request := httptest.NewRequest(http.MethodGet, "/get/video", nil)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		server.httpServer.Handler.ServeHTTP(response, request)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("stream preparation did not start")
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel stream preparation")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stream get handler did not return after shutdown")
	}
}

func TestStreamGetRedirectFollowsIntoManagedIncrementalStream(t *testing.T) {
	created := createStreamFixture(t, []byte("hello"))
	manager := blob.NewManager()
	if err := manager.Set(created.SDHash, created.DescriptorBytes, true); err != nil {
		t.Fatal(err)
	}
	for hash, data := range created.Blobs {
		if err := manager.Set(hash, data, false); err != nil {
			t.Fatal(err)
		}
	}
	server := CreateServer(
		manager,
		WithStreamGet(func(context.Context, string) (string, error) { return created.SDHash, nil }, func() bool { return true }),
		WithManagedStreamLookup(func(context.Context, string) (ManagedStreamInfo, bool, error) {
			return ManagedStreamInfo{Size: 5, MIMEType: "text/plain"}, true, nil
		}),
	)
	server.logf = func(string, ...any) {}
	httpServer := httptest.NewServer(server.httpServer.Handler)
	defer httpServer.Close()
	response, err := http.Get(httpServer.URL + "/get/video")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusPartialContent || string(body) != "hello" ||
		response.Request.URL.Path != "/stream/"+created.SDHash {
		t.Fatalf("followed response = %d %q at %s", response.StatusCode, body, response.Request.URL.Path)
	}
}
