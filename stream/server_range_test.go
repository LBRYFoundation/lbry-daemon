package stream

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"lbry/daemon/blob"
)

func TestStreamServerServesPinnedPartialContentShape(t *testing.T) {
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
	server := CreateServer(manager)

	request := httptest.NewRequest(http.MethodGet, "/stream/"+created.SDHash, nil)
	request.SetPathValue("sd_hash", created.SDHash)
	request.Header.Set("Range", "bytes=2-4")
	response := httptest.NewRecorder()
	server.handleStream(response, request)

	if response.Code != http.StatusPartialContent || response.Body.String() != "llo" {
		t.Fatalf("partial response = %d %q", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Range") != "bytes 2-4/15" ||
		response.Header().Get("Content-Length") != "3" ||
		response.Header().Get("Content-Type") != "video/mp4" ||
		response.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("partial headers = %#v", response.Header())
	}
}

func TestStreamServerWritesBeforeLaterBlobIsAvailable(t *testing.T) {
	content := bytes.Repeat([]byte{0x5a}, blob.MaxBlobSize+31)
	created := createStreamFixture(t, content)
	manager := blob.NewManager()
	if err := manager.Set(created.SDHash, created.DescriptorBytes, true); err != nil {
		t.Fatal(err)
	}
	first := created.Descriptor.Blobs[0].BlobHash
	second := created.Descriptor.Blobs[1].BlobHash
	if err := manager.Set(first, created.Blobs[first], false); err != nil {
		t.Fatal(err)
	}
	fetchStarted := make(chan struct{})
	releaseFetch := make(chan struct{})
	manager.SetFetcher(func(ctx context.Context, hash string) ([]byte, error) {
		if hash != second {
			return nil, errors.New("unexpected blob fetch")
		}
		close(fetchStarted)
		select {
		case <-releaseFetch:
			return created.Blobs[hash], nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	server := CreateServer(manager)
	server.logf = func(string, ...any) {}
	httpServer := httptest.NewServer(server.httpServer.Handler)
	defer httpServer.Close()

	responseResult := make(chan *http.Response, 1)
	errorResult := make(chan error, 1)
	go func() {
		request, _ := http.NewRequest(http.MethodGet, httpServer.URL+"/stream/"+created.SDHash, nil)
		request.Header.Set("Range", "bytes=0-")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			errorResult <- err
			return
		}
		responseResult <- response
	}()

	var response *http.Response
	select {
	case response = <-responseResult:
	case err := <-errorResult:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("response headers did not arrive while the later blob was blocked")
	}
	defer response.Body.Close()
	prefix := make([]byte, 32)
	if _, err := io.ReadFull(response.Body, prefix); err != nil {
		t.Fatalf("read first streamed bytes: %v", err)
	}
	if !bytes.Equal(prefix, content[:len(prefix)]) {
		t.Fatalf("first streamed bytes = %x", prefix)
	}
	select {
	case <-fetchStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("later blob fetch did not start")
	}
	close(releaseFetch)
	remainder, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	got := append(prefix, remainder...)
	if !bytes.Equal(got[:len(content)], content) {
		t.Fatal("incremental response content does not match source")
	}
	for _, value := range got[len(content):] {
		if value != 0 {
			t.Fatal("estimated trailing stream bytes were not zero padded")
		}
	}
}

func TestStreamServerFetchesOnlyBlobsIntersectingRange(t *testing.T) {
	content := bytes.Repeat([]byte{0x4c}, blob.MaxBlobSize+31)
	created := createStreamFixture(t, content)
	manager := blob.NewManager()
	if err := manager.Set(created.SDHash, created.DescriptorBytes, true); err != nil {
		t.Fatal(err)
	}
	var fetched []string
	var fetchedMu sync.Mutex
	manager.SetFetcher(func(_ context.Context, hash string) ([]byte, error) {
		fetchedMu.Lock()
		fetched = append(fetched, hash)
		fetchedMu.Unlock()
		return created.Blobs[hash], nil
	})
	boundary := created.Descriptor.Blobs[0].Length - 1
	request := httptest.NewRequest(http.MethodGet, "/stream/"+created.SDHash, nil)
	request.SetPathValue("sd_hash", created.SDHash)
	request.Header.Set("Range", "bytes="+strconv.Itoa(boundary)+"-"+strconv.Itoa(boundary+3))
	response := httptest.NewRecorder()
	server := CreateServer(manager)
	server.logf = func(string, ...any) {}
	server.handleStream(response, request)

	if response.Code != http.StatusPartialContent || !bytes.Equal(response.Body.Bytes(), content[boundary:boundary+4]) {
		t.Fatalf("range response = %d %x", response.Code, response.Body.Bytes())
	}
	fetchedMu.Lock()
	defer fetchedMu.Unlock()
	if len(fetched) != 1 || fetched[0] != created.Descriptor.Blobs[1].BlobHash {
		t.Fatalf("fetched blobs = %v", fetched)
	}
}

func TestStreamServerCancelsUniqueFetchWhenClientDisconnects(t *testing.T) {
	created := createStreamFixture(t, []byte("cancel me"))
	manager := blob.NewManager()
	if err := manager.Set(created.SDHash, created.DescriptorBytes, true); err != nil {
		t.Fatal(err)
	}
	fetchStarted := make(chan struct{})
	fetchCanceled := make(chan struct{})
	manager.SetFetcher(func(ctx context.Context, _ string) ([]byte, error) {
		close(fetchStarted)
		<-ctx.Done()
		close(fetchCanceled)
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, "/stream/"+created.SDHash, nil).WithContext(ctx)
	request.SetPathValue("sd_hash", created.SDHash)
	response := httptest.NewRecorder()
	server := CreateServer(manager)
	server.logf = func(string, ...any) {}
	done := make(chan struct{})
	go func() {
		server.handleStream(response, request)
		close(done)
	}()
	select {
	case <-fetchStarted:
	case <-time.After(time.Second):
		t.Fatal("content fetch did not start")
	}
	cancel()
	select {
	case <-fetchCanceled:
	case <-time.After(time.Second):
		t.Fatal("content fetch was not canceled")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stream handler did not exit after cancellation")
	}
	if response.Code != http.StatusPartialContent {
		t.Fatalf("response code = %d", response.Code)
	}
}

func TestStreamServerLeavesCommittedResponseTruncatedAfterLateFetchFailure(t *testing.T) {
	content := bytes.Repeat([]byte{0x71}, blob.MaxBlobSize+31)
	created := createStreamFixture(t, content)
	manager := blob.NewManager()
	if err := manager.Set(created.SDHash, created.DescriptorBytes, true); err != nil {
		t.Fatal(err)
	}
	first := created.Descriptor.Blobs[0].BlobHash
	if err := manager.Set(first, created.Blobs[first], false); err != nil {
		t.Fatal(err)
	}
	manager.SetFetcher(func(context.Context, string) ([]byte, error) {
		return nil, errors.New("peer became unavailable")
	})
	request := httptest.NewRequest(http.MethodGet, "/stream/"+created.SDHash, nil)
	request.SetPathValue("sd_hash", created.SDHash)
	response := httptest.NewRecorder()
	server := CreateServer(manager)
	server.logf = func(string, ...any) {}
	server.handleStream(response, request)

	wanted, err := strconv.Atoi(response.Header().Get("Content-Length"))
	if err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusPartialContent || response.Body.Len() != blob.MaxBlobSize-1 || response.Body.Len() >= wanted {
		t.Fatalf("late failure response = %d, %d/%d bytes", response.Code, response.Body.Len(), wanted)
	}
}

func TestStreamServerShutdownCancelsActiveBlobAcquisition(t *testing.T) {
	created := createStreamFixture(t, []byte("shutdown"))
	manager := blob.NewManager()
	if err := manager.Set(created.SDHash, created.DescriptorBytes, true); err != nil {
		t.Fatal(err)
	}
	fetchStarted := make(chan struct{})
	fetchCanceled := make(chan struct{})
	manager.SetFetcher(func(ctx context.Context, _ string) ([]byte, error) {
		close(fetchStarted)
		<-ctx.Done()
		close(fetchCanceled)
		return nil, ctx.Err()
	})
	server := CreateServer(manager)
	server.logf = func(string, ...any) {}
	request := httptest.NewRequest(http.MethodGet, "/stream/"+created.SDHash, nil)
	request.SetPathValue("sd_hash", created.SDHash)
	response := httptest.NewRecorder()
	handlerDone := make(chan struct{})
	go func() {
		server.handleStream(response, request)
		close(handlerDone)
	}()
	select {
	case <-fetchStarted:
	case <-time.After(time.Second):
		t.Fatal("content fetch did not start")
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case <-fetchCanceled:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel content acquisition")
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("active stream did not finish during shutdown")
	}
}

func TestBlockManagedStreamCancelsActiveRequestAndRejectsNewOnes(t *testing.T) {
	created := createStreamFixture(t, []byte("delete"))
	manager := blob.NewManager()
	if err := manager.Set(created.SDHash, created.DescriptorBytes, true); err != nil {
		t.Fatal(err)
	}
	fetchStarted := make(chan struct{})
	fetchCanceled := make(chan struct{})
	manager.SetFetcher(func(ctx context.Context, _ string) ([]byte, error) {
		close(fetchStarted)
		<-ctx.Done()
		close(fetchCanceled)
		return nil, ctx.Err()
	})
	server := CreateServer(manager)
	server.logf = func(string, ...any) {}
	request := httptest.NewRequest(http.MethodGet, "/stream/"+created.SDHash, nil)
	request.SetPathValue("sd_hash", created.SDHash)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		server.handleStream(response, request)
		close(done)
	}()
	select {
	case <-fetchStarted:
	case <-time.After(time.Second):
		t.Fatal("content fetch did not start")
	}
	if err := server.BlockManagedStream(context.Background(), created.SDHash); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fetchCanceled:
	case <-time.After(time.Second):
		t.Fatal("blocking stream did not cancel content fetch")
	}
	<-done

	rejected := httptest.NewRecorder()
	server.handleStream(rejected, request.Clone(context.Background()))
	if rejected.Code != http.StatusNotFound {
		t.Fatalf("blocked stream response = %d", rejected.Code)
	}
}

func TestStreamServerValidatesDescriptorBeforeCommittingHeaders(t *testing.T) {
	created := createStreamFixture(t, []byte("invalid descriptor"))
	created.Descriptor.StreamHash = strings.Repeat("0", blob.BlobHashLength)
	data, err := blob.MarshalDescriptor(created.Descriptor)
	if err != nil {
		t.Fatal(err)
	}
	invalidHash := streamTestHash(data)
	manager := blob.NewManager()
	if err := manager.Set(invalidHash, data, true); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/stream/"+invalidHash, nil)
	request.SetPathValue("sd_hash", invalidHash)
	response := httptest.NewRecorder()
	CreateServer(manager).handleStream(response, request)
	if response.Code != http.StatusInternalServerError || response.Header().Get("Content-Range") != "" {
		t.Fatalf("invalid descriptor response = %d, %#v", response.Code, response.Header())
	}
}

func TestStreamServerRequiresManagedRegistrationWhenConfigured(t *testing.T) {
	created := createStreamFixture(t, []byte("registered"))
	manager := blob.NewManager()
	if err := manager.Set(created.SDHash, created.DescriptorBytes, true); err != nil {
		t.Fatal(err)
	}
	server := CreateServer(manager, WithManagedStreamLookup(func(context.Context, string) (ManagedStreamInfo, bool, error) {
		return ManagedStreamInfo{}, false, nil
	}))
	request := httptest.NewRequest(http.MethodGet, "/stream/"+created.SDHash, nil)
	request.SetPathValue("sd_hash", created.SDHash)
	response := httptest.NewRecorder()
	server.handleStream(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("unmanaged stream response = %d", response.Code)
	}
}

func TestManagedStreamUsesExactClaimSizeAndMIMEType(t *testing.T) {
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
	server := CreateServer(manager, WithManagedStreamLookup(func(context.Context, string) (ManagedStreamInfo, bool, error) {
		return ManagedStreamInfo{Size: 5, MIMEType: "application/x-test"}, true, nil
	}))
	server.logf = func(string, ...any) {}
	request := httptest.NewRequest(http.MethodGet, "/stream/"+created.SDHash, nil)
	request.SetPathValue("sd_hash", created.SDHash)
	response := httptest.NewRecorder()
	server.handleStream(response, request)
	if response.Code != http.StatusPartialContent || response.Body.String() != "hello" ||
		response.Header().Get("Content-Range") != "bytes 0-4/5" ||
		response.Header().Get("Content-Length") != "5" ||
		response.Header().Get("Content-Type") != "application/x-test" {
		t.Fatalf("claim-sized response = %d %q %#v", response.Code, response.Body.String(), response.Header())
	}
}

func TestManagedStreamLifecycleMarksActiveThenIdle(t *testing.T) {
	created := createStreamFixture(t, []byte("lifecycle"))
	manager := blob.NewManager()
	if err := manager.Set(created.SDHash, created.DescriptorBytes, true); err != nil {
		t.Fatal(err)
	}
	for hash, data := range created.Blobs {
		if err := manager.Set(hash, data, false); err != nil {
			t.Fatal(err)
		}
	}
	active := make(chan string, 1)
	idle := make(chan string, 1)
	server := CreateServer(
		manager,
		WithManagedStreamLookup(func(context.Context, string) (ManagedStreamInfo, bool, error) {
			return ManagedStreamInfo{Size: len("lifecycle")}, true, nil
		}),
		WithStreamLifecycle(
			func(_ context.Context, hash string) error { active <- hash; return nil },
			func(_ context.Context, hash string) error { idle <- hash; return nil },
			10*time.Millisecond,
		),
	)
	server.logf = func(string, ...any) {}
	request := httptest.NewRequest(http.MethodGet, "/stream/"+created.SDHash, nil)
	request.SetPathValue("sd_hash", created.SDHash)
	response := httptest.NewRecorder()
	server.handleStream(response, request)
	select {
	case hash := <-active:
		if hash != created.SDHash {
			t.Fatalf("active hash = %q", hash)
		}
	case <-time.After(time.Second):
		t.Fatal("active callback was not called")
	}
	select {
	case hash := <-idle:
		if hash != created.SDHash {
			t.Fatalf("idle hash = %q", hash)
		}
	case <-time.After(time.Second):
		t.Fatal("idle callback was not called")
	}
}

func TestStreamServerHEADReturnsHeadersWithoutFetchingContent(t *testing.T) {
	created := createStreamFixture(t, []byte("probe"))
	manager := blob.NewManager()
	if err := manager.Set(created.SDHash, created.DescriptorBytes, true); err != nil {
		t.Fatal(err)
	}
	manager.SetFetcher(func(context.Context, string) ([]byte, error) {
		t.Fatal("HEAD fetched content")
		return nil, nil
	})
	server := CreateServer(manager, WithManagedStreamLookup(func(context.Context, string) (ManagedStreamInfo, bool, error) {
		return ManagedStreamInfo{Size: 5}, true, nil
	}))
	request := httptest.NewRequest(http.MethodHead, "/stream/"+created.SDHash, nil)
	request.SetPathValue("sd_hash", created.SDHash)
	response := httptest.NewRecorder()
	server.handleStream(response, request)
	if response.Code != http.StatusPartialContent || response.Body.Len() != 0 ||
		response.Header().Get("Content-Range") != "bytes 0-4/5" {
		t.Fatalf("HEAD response = %d %q %#v", response.Code, response.Body.String(), response.Header())
	}
}

func TestPrepareStreamRangeValidation(t *testing.T) {
	descriptor := &blob.StreamDescriptor{Blobs: []blob.BlobInfo{
		{BlobHash: "a", Length: 11}, {BlobHash: "b", Length: 6}, {Length: 0},
	}}
	for _, test := range []struct {
		header string
		want   preparedStreamRange
		ok     bool
	}{
		{header: "", want: preparedStreamRange{start: 0, end: 14, total: 15}, ok: true},
		{header: "bytes=3-", want: preparedStreamRange{start: 3, end: 14, total: 15}, ok: true},
		{header: "bytes=3-7", want: preparedStreamRange{start: 3, end: 7, total: 15}, ok: true},
		{header: "bytes=15-", want: preparedStreamRange{total: 15}},
		{header: "bytes=-4", want: preparedStreamRange{total: 15}},
		{header: "bytes=4-20", want: preparedStreamRange{total: 15}},
	} {
		got, err := prepareStreamRange(test.header, descriptor)
		if (err == nil) != test.ok || got != test.want {
			t.Errorf("prepareStreamRange(%q) = %#v, %v; want %#v, ok=%t",
				test.header, got, err, test.want, test.ok)
		}
	}
}

func TestStreamServerRejectsMalformedDescriptorWithoutPanicking(t *testing.T) {
	manager := blob.NewManager()
	data := []byte(`{"key": 1, "blobs": "wrong"}`)
	badHash := streamTestHash(data)
	if err := manager.Set(badHash, data, true); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/stream/"+badHash, nil)
	request.SetPathValue("sd_hash", badHash)
	response := httptest.NewRecorder()
	CreateServer(manager).handleStream(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("malformed descriptor status = %d", response.Code)
	}
}

func streamTestHash(data []byte) string {
	digest := sha512.Sum384(data)
	return hex.EncodeToString(digest[:])
}

func createStreamFixture(t *testing.T, content []byte) *blob.CreatedStream {
	t.Helper()
	path := filepath.Join(t.TempDir(), "movie.mp4")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := blob.CreateStreamDescriptor(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	return created
}
