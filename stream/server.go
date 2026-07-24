package stream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"lbry/daemon/blob"
	"log"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Option func(*StreamServer)

type ManagedStreamInfo struct {
	Size     int
	MIMEType string
}

type ManagedStreamLookup func(context.Context, string) (ManagedStreamInfo, bool, error)

type StreamGet func(context.Context, string) (string, error)
type StreamLifecycle func(context.Context, string) error

func WithManagedStreamLookup(lookup ManagedStreamLookup) Option {
	return func(server *StreamServer) {
		server.managedStream = lookup
	}
}

func WithStreamGet(get StreamGet, enabled func() bool) Option {
	return func(server *StreamServer) {
		server.streamGet = get
		server.streamingGetEnabled = enabled
	}
}

func WithStreamLifecycle(active, idle StreamLifecycle, idleDelay time.Duration) Option {
	return func(server *StreamServer) {
		server.streamActive = active
		server.streamIdle = idle
		if idleDelay > 0 {
			server.idleDelay = idleDelay
		}
	}
}

type StreamServer struct {
	blobManager         *blob.BlobManager
	managedStream       ManagedStreamLookup
	streamGet           StreamGet
	streamingGetEnabled func() bool
	streamActive        StreamLifecycle
	streamIdle          StreamLifecycle
	idleDelay           time.Duration
	httpServer          *http.Server
	logf                func(string, ...any)
	shutdownCtx         context.Context
	cancelStreams       context.CancelFunc
	activeMu            sync.Mutex
	lifecycleMu         sync.Mutex
	active              map[string]map[uint64]*activeStream
	blocked             map[string]bool
	idleTimers          map[string]*time.Timer
	nextStreamID        uint64
}

type activeStream struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func CreateServer(blobManager *blob.BlobManager, options ...Option) *StreamServer {
	contentServeMux := http.NewServeMux()

	shutdownCtx, cancelStreams := context.WithCancel(context.Background())
	server := &StreamServer{
		blobManager:   blobManager,
		httpServer:    &http.Server{Handler: contentServeMux},
		logf:          log.Printf,
		shutdownCtx:   shutdownCtx,
		cancelStreams: cancelStreams,
		active:        make(map[string]map[uint64]*activeStream),
		blocked:       make(map[string]bool),
		idleTimers:    make(map[string]*time.Timer),
		idleDelay:     2 * time.Second,
	}
	for _, option := range options {
		option(server)
	}

	contentServeMux.HandleFunc("/stream/{sd_hash}", server.handleStream)
	contentServeMux.HandleFunc("/get/{claim_name}", server.handleGet)
	contentServeMux.HandleFunc("/get/{claim_name}/{claim_id}", server.handleGet)

	return server
}

func (contentServer *StreamServer) handleGet(w http.ResponseWriter, req *http.Request) {
	if !strings.EqualFold(req.Method, http.MethodGet) && !strings.EqualFold(req.Method, http.MethodHead) {
		w.Header().Set("Allow", "GET,HEAD")
		writeAiohttpText(w, req, http.StatusMethodNotAllowed, "405: Method Not Allowed")
		return
	}
	if contentServer.streamingGetEnabled == nil || !contentServer.streamingGetEnabled() {
		contentServer.logf("Streaming: /get request rejected because streaming_get is disabled.")
		writeAiohttpText(w, req, http.StatusForbidden, "403: Forbidden")
		return
	}
	ctx, cancel := context.WithCancel(req.Context())
	stopShutdownCancel := context.AfterFunc(contentServer.shutdownCtx, cancel)
	defer func() {
		stopShutdownCancel()
		cancel()
	}()
	_, nameAndClaimID, found := strings.Cut(req.URL.Path, "/get/")
	if !found {
		writeAiohttpText(w, req, http.StatusInternalServerError, "invalid streaming get path")
		return
	}
	parts := strings.Split(nameAndClaimID, "/")
	uri := ""
	switch len(parts) {
	case 1:
		uri = "lbry://" + parts[0]
	case 2:
		uri = "lbry://" + parts[0] + "#" + parts[1]
	default:
		writeAiohttpText(w, req, http.StatusInternalServerError, "invalid streaming get path")
		return
	}
	if contentServer.streamGet == nil {
		writeAiohttpText(w, req, http.StatusInternalServerError, "managed get is unavailable")
		return
	}
	contentServer.logf("Streaming: preparing %s for browser playback.", uri)
	sdHash, err := contentServer.streamGet(ctx, uri)
	if err != nil {
		if ctx.Err() == nil {
			contentServer.logf("Streaming: unable to prepare %s: %v", uri, err)
			writeAiohttpText(w, req, http.StatusInternalServerError, err.Error())
		}
		return
	}
	location := "/stream/" + sdHash
	w.Header().Set("Location", location)
	contentServer.logf("Streaming: %s is ready; redirecting to %s.", uri, location)
	writeAiohttpText(w, req, http.StatusFound, "302: Found")
}

func writeAiohttpText(w http.ResponseWriter, req *http.Request, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	if req == nil || !strings.EqualFold(req.Method, http.MethodHead) {
		_, _ = io.WriteString(w, body)
	}
}

func (contentServer *StreamServer) StartServer(listener net.Listener) {
	err := contentServer.Serve(listener)
	if err != nil && err != http.ErrServerClosed {
		log.Printf("error starting stream server: %v", err)
	}
}

func (contentServer *StreamServer) Serve(listener net.Listener) error {
	return contentServer.httpServer.Serve(listener)
}

func (contentServer *StreamServer) Shutdown(ctx context.Context) error {
	contentServer.cancelStreams()
	contentServer.activeMu.Lock()
	for hash, timer := range contentServer.idleTimers {
		timer.Stop()
		delete(contentServer.idleTimers, hash)
	}
	contentServer.activeMu.Unlock()
	err := contentServer.httpServer.Shutdown(ctx)
	if err != nil {
		_ = contentServer.httpServer.Close()
	}
	return err
}

func (contentServer *StreamServer) handleStream(w http.ResponseWriter, req *http.Request) {
	info, _ := debug.ReadBuildInfo()

	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Access-Control-Allow-Headers", "Range")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Expose-Headers", "Accept-Ranges, Content-Length, Content-Range")
	//w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Server", "LBRYd/"+info.Main.Version)

	if strings.EqualFold(req.Method, "OPTIONS") {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if strings.EqualFold(req.Method, "GET") || strings.EqualFold(req.Method, "HEAD") {
		sdHash := req.PathValue("sd_hash")

		contentServer.handleSDHash(w, req, sdHash)
		return
	}

	http.Error(w, "HTTP method not allowed.", http.StatusMethodNotAllowed)
}

func (contentServer *StreamServer) handleSDHash(w http.ResponseWriter, req *http.Request, sdHash string) {
	ctx, cancel := context.WithCancel(req.Context())
	stopShutdownCancel := context.AfterFunc(contentServer.shutdownCtx, cancel)
	release, allowed := contentServer.registerStream(sdHash, cancel)
	defer func() {
		release()
		stopShutdownCancel()
		cancel()
	}()
	if !allowed {
		http.Error(w, "Stream not found.", http.StatusNotFound)
		return
	}
	managedInfo := ManagedStreamInfo{}
	if contentServer.managedStream != nil {
		info, managed, err := contentServer.managedStream(ctx, sdHash)
		managedInfo = info
		if err != nil {
			if ctx.Err() == nil {
				http.Error(w, "Unable to inspect managed stream.", http.StatusInternalServerError)
			}
			return
		}
		if !managed {
			http.Error(w, "Stream not found.", http.StatusNotFound)
			return
		}
	}
	if contentServer.streamActive != nil {
		contentServer.lifecycleMu.Lock()
		err := contentServer.streamActive(ctx, sdHash)
		contentServer.lifecycleMu.Unlock()
		if err != nil {
			if ctx.Err() == nil {
				http.Error(w, "Unable to start managed stream.", http.StatusInternalServerError)
			}
			return
		}
	}
	blobData, ok := contentServer.blobManager.GetLocal(sdHash)
	if !ok {
		if err := contentServer.blobManager.Ensure(ctx, sdHash); err != nil {
			if ctx.Err() == nil {
				http.Error(w, "Blob not found.", http.StatusNotFound)
			}
			return
		}
		blobData, ok = contentServer.blobManager.GetLocal(sdHash)
	}
	if !ok {
		http.Error(w, "Blob not found.", http.StatusNotFound)
		return
	}

	descriptor, err := blob.DecodeDescriptor(sdHash, blobData)
	if err != nil {
		http.Error(w, "Malformed stream descriptor.", http.StatusInternalServerError)
		return
	}
	streamRange, err := prepareStreamRangeWithClaimSize(req.Header.Get("Range"), descriptor, managedInfo.Size)
	if err != nil {
		if errors.Is(err, errStreamRangeNotSatisfiable) {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", streamRange.total))
			http.Error(w, http.StatusText(http.StatusRequestedRangeNotSatisfiable), http.StatusRequestedRangeNotSatisfiable)
			return
		}
		http.Error(w, "Malformed range header.", http.StatusInternalServerError)
		return
	}

	plan, err := planStreamBlobs(descriptor, streamRange)
	if err != nil {
		http.Error(w, "Malformed stream descriptor.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Range", fmt.Sprintf(
		"bytes %d-%d/%d", streamRange.start, streamRange.end, streamRange.total,
	))
	w.Header().Set("Content-Length", strconv.Itoa(streamRange.end-streamRange.start+1))
	mimeType := managedInfo.MIMEType
	if mimeType == "" {
		mimeType = blob.GuessMIME(descriptor.SuggestedFileName, descriptor.StreamName)
	}
	w.Header().Set("Content-Type", mimeType)
	w.WriteHeader(http.StatusPartialContent)
	if strings.EqualFold(req.Method, "HEAD") {
		return
	}

	started := time.Now()
	wanted := streamRange.end - streamRange.start + 1
	written := 0
	lastProgress := 0
	contentServer.logf(
		"Streaming: serving %s bytes %d-%d/%d (%d blobs).",
		shortHash(sdHash), streamRange.start, streamRange.end, streamRange.total, len(plan),
	)
	err = blob.WalkStreamRange(
		ctx, contentServer.blobManager, descriptor, plan[0].index, plan[len(plan)-1].index+1,
		func(info blob.BlobInfo, data []byte) error {
			entry := plan[info.BlobNum-plan[0].index]
			count, writeErr := writePlannedBlob(w, data, entry)
			written += count
			if writeErr != nil {
				return writeErr
			}
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			progress := int(float64(written) / float64(wanted) * 100)
			milestone := progress / 25 * 25
			if milestone > lastProgress && milestone < 100 {
				lastProgress = milestone
				contentServer.logf(
					"Streaming: %s is %d%% complete (%d/%d bytes).",
					shortHash(sdHash), milestone, written, wanted,
				)
			}
			return nil
		},
	)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			contentServer.logf("Streaming: client disconnected from %s after %d/%d bytes.", shortHash(sdHash), written, wanted)
		} else {
			contentServer.logf("Streaming: stream %s stopped after %d/%d bytes: %v", shortHash(sdHash), written, wanted, err)
		}
		return
	}
	contentServer.logf(
		"Streaming: completed %s (%d bytes in %s).",
		shortHash(sdHash), written, time.Since(started).Round(time.Millisecond),
	)
}

func (contentServer *StreamServer) registerStream(
	sdHash string, cancel context.CancelFunc,
) (func(), bool) {
	contentServer.activeMu.Lock()
	defer contentServer.activeMu.Unlock()
	if contentServer.blocked[sdHash] {
		return func() {}, false
	}
	if timer := contentServer.idleTimers[sdHash]; timer != nil {
		timer.Stop()
		delete(contentServer.idleTimers, sdHash)
	}
	contentServer.nextStreamID++
	id := contentServer.nextStreamID
	if contentServer.active[sdHash] == nil {
		contentServer.active[sdHash] = make(map[uint64]*activeStream)
	}
	stream := &activeStream{cancel: cancel, done: make(chan struct{})}
	contentServer.active[sdHash][id] = stream
	return func() {
		contentServer.activeMu.Lock()
		delete(contentServer.active[sdHash], id)
		if len(contentServer.active[sdHash]) == 0 {
			delete(contentServer.active, sdHash)
			contentServer.scheduleIdleLocked(sdHash)
		}
		contentServer.activeMu.Unlock()
		close(stream.done)
	}, true
}

func (contentServer *StreamServer) scheduleIdleLocked(sdHash string) {
	if contentServer.streamIdle == nil || contentServer.blocked[sdHash] || contentServer.shutdownCtx.Err() != nil {
		return
	}
	var timer *time.Timer
	timer = time.AfterFunc(contentServer.idleDelay, func() {
		ctx, cancel := context.WithTimeout(contentServer.shutdownCtx, 5*time.Second)
		defer cancel()
		contentServer.lifecycleMu.Lock()
		defer contentServer.lifecycleMu.Unlock()
		contentServer.activeMu.Lock()
		if contentServer.idleTimers[sdHash] != timer || len(contentServer.active[sdHash]) != 0 ||
			contentServer.blocked[sdHash] || ctx.Err() != nil {
			contentServer.activeMu.Unlock()
			return
		}
		delete(contentServer.idleTimers, sdHash)
		contentServer.activeMu.Unlock()
		if err := contentServer.streamIdle(ctx, sdHash); err != nil && ctx.Err() == nil {
			contentServer.logf("Streaming: unable to stop inactive stream %s: %v", shortHash(sdHash), err)
		}
	})
	contentServer.idleTimers[sdHash] = timer
}

func (contentServer *StreamServer) CancelManagedStream(ctx context.Context, sdHash string) error {
	return contentServer.cancelManagedStream(ctx, sdHash, false)
}

func (contentServer *StreamServer) BlockManagedStream(ctx context.Context, sdHash string) error {
	return contentServer.cancelManagedStream(ctx, sdHash, true)
}

func (contentServer *StreamServer) AllowManagedStream(sdHash string) {
	contentServer.activeMu.Lock()
	delete(contentServer.blocked, sdHash)
	contentServer.activeMu.Unlock()
}

func (contentServer *StreamServer) ScheduleManagedStreamIdle(sdHash string) {
	contentServer.activeMu.Lock()
	defer contentServer.activeMu.Unlock()
	if len(contentServer.active[sdHash]) == 0 {
		if timer := contentServer.idleTimers[sdHash]; timer != nil {
			timer.Stop()
		}
		contentServer.scheduleIdleLocked(sdHash)
	}
}

func (contentServer *StreamServer) cancelManagedStream(ctx context.Context, sdHash string, block bool) error {
	if ctx == nil {
		return errors.New("stream cancellation context is nil")
	}
	contentServer.activeMu.Lock()
	if block {
		contentServer.blocked[sdHash] = true
	}
	if timer := contentServer.idleTimers[sdHash]; timer != nil {
		timer.Stop()
		delete(contentServer.idleTimers, sdHash)
	}
	streams := make([]*activeStream, 0, len(contentServer.active[sdHash]))
	for _, stream := range contentServer.active[sdHash] {
		streams = append(streams, stream)
	}
	contentServer.activeMu.Unlock()
	for _, stream := range streams {
		stream.cancel()
	}
	for _, stream := range streams {
		select {
		case <-stream.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

type plannedStreamBlob struct {
	index int
	start int
	end   int
}

func planStreamBlobs(descriptor *blob.StreamDescriptor, streamRange preparedStreamRange) ([]plannedStreamBlob, error) {
	if descriptor == nil {
		return nil, errors.New("stream descriptor is nil")
	}
	position := 0
	plan := make([]plannedStreamBlob, 0)
	for index, info := range descriptor.Blobs[:len(descriptor.Blobs)-1] {
		length := info.Length - 1
		blobEnd := position + length
		if streamRange.start < blobEnd && streamRange.end >= position {
			plan = append(plan, plannedStreamBlob{
				index: index,
				start: max(streamRange.start-position, 0),
				end:   min(streamRange.end-position+1, length),
			})
		}
		position = blobEnd
	}
	if len(plan) == 0 {
		return nil, errStreamRangeNotSatisfiable
	}
	return plan, nil
}

func writePlannedBlob(w io.Writer, data []byte, plan plannedStreamBlob) (int, error) {
	written := 0
	dataEnd := min(plan.end, len(data))
	if plan.start < dataEnd {
		count, err := w.Write(data[plan.start:dataEnd])
		written += count
		if err != nil {
			return written, err
		}
		if count != dataEnd-plan.start {
			return written, io.ErrShortWrite
		}
	}
	padding := plan.end - max(plan.start, len(data))
	if padding <= 0 {
		return written, nil
	}
	var zeroes [32 * 1024]byte
	for padding > 0 {
		chunk := min(padding, len(zeroes))
		count, err := w.Write(zeroes[:chunk])
		written += count
		padding -= count
		if err != nil {
			return written, err
		}
		if count != chunk {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}

func shortHash(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}

var errStreamRangeNotSatisfiable = errors.New("stream range is not satisfiable")

type preparedStreamRange struct {
	start int
	end   int
	total int
}

func prepareStreamRange(header string, descriptor *blob.StreamDescriptor) (preparedStreamRange, error) {
	return prepareStreamRangeWithClaimSize(header, descriptor, 0)
}

func prepareStreamRangeWithClaimSize(
	header string, descriptor *blob.StreamDescriptor, claimSize int,
) (preparedStreamRange, error) {
	result := preparedStreamRange{}
	if descriptor == nil {
		return result, errors.New("stream descriptor is nil")
	}
	for _, blobInfo := range descriptor.ContentBlobs() {
		if blobInfo.Length <= 0 || result.total > int(^uint(0)>>1)-(blobInfo.Length-1) {
			return result, errors.New("invalid stream size")
		}
		result.total += blobInfo.Length - 1
	}
	if claimSize > 0 {
		if claimSize > result.total || result.total-claimSize > 16 {
			return result, errors.New("claim contains implausible stream size")
		}
		result.total = claimSize
	}
	if result.total <= 0 {
		return result, errStreamRangeNotSatisfiable
	}
	if header == "" {
		header = "bytes=0-"
	}
	if equal := strings.IndexByte(header, '='); equal >= 0 {
		header = header[equal+1:]
	}
	startText, endText, found := strings.Cut(header, "-")
	if !found || strings.Contains(endText, "-") {
		return result, errors.New("invalid byte range")
	}
	start, err := strconv.Atoi(startText)
	if err != nil || start < 0 || start >= result.total {
		return result, errStreamRangeNotSatisfiable
	}
	end := result.total - 1
	if endText != "" {
		end, err = strconv.Atoi(endText)
		if err != nil {
			return result, errors.New("invalid byte range")
		}
	}
	if end < start || end >= result.total {
		return result, errStreamRangeNotSatisfiable
	}
	result.start, result.end = start, end
	return result, nil
}
