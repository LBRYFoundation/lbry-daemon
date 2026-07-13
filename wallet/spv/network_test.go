package spv

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNetworkHandshakeCallAndHeaderNotification(t *testing.T) {
	requests := make(chan hubRequest, 16)
	notificationTrigger := make(chan struct{})
	dialer := &fakeDialer{serve: func(attempt int, connection net.Conn) {
		serveFakeHub(connection, requests, func(request hubRequest) hubReply {
			switch request.Method {
			case "server.version":
				return resultReply([]any{"fake hub", "0.65.0"})
			case "server.features":
				return resultReply(map[string]any{"server_version": "0.199.0"})
			case "server.peers.subscribe":
				return resultReply([]any{})
			case "blockchain.headers.subscribe":
				return resultReply(map[string]any{"height": 321})
			case "blockchain.block.headers":
				return resultReply(map[string]any{"base64": "encoded"})
			case "trigger.notification":
				go func() {
					<-notificationTrigger
					writeHubNotification(connection, "blockchain.headers.subscribe", []any{
						map[string]any{"height": 322},
					})
				}()
				return resultReply(map[string]any{})
			case "server.ping":
				return resultReply(nil)
			default:
				return errorReply(-32601, "unknown method")
			}
		})
	}}
	network := newTestNetwork(t, dialer, []Server{{Host: "hub", Port: 50001}})
	headerNotifications := make(chan any, 2)
	network.SetHeaderNotificationHandler(func(_ context.Context, params any) {
		headerNotifications <- params
	})
	startTestNetwork(t, network)
	waitForConnected(t, network)
	if len(headerNotifications) != 0 {
		t.Fatal("initial header subscription result was delivered as a notification")
	}

	wantHandshake := []struct {
		method        string
		params        any
		paramsPresent bool
	}{
		{method: "server.version", params: []any{DefaultClientName, DefaultProtocolMaximum}, paramsPresent: true},
		{method: "server.features", paramsPresent: false},
		{method: "server.peers.subscribe", paramsPresent: false},
		{method: "blockchain.headers.subscribe", params: []any{true}, paramsPresent: true},
	}
	for _, want := range wantHandshake {
		request := <-requests
		if request.JSONRPC != "2.0" || request.Method != want.method ||
			request.ParamsPresent != want.paramsPresent || !reflect.DeepEqual(request.Params, want.params) {
			t.Fatalf("handshake request = %#v, want %#v", request, want)
		}
	}
	if snapshot := network.Snapshot(); snapshot.RemoteHeight != 321 || snapshot.Server.Host != "hub" ||
		!reflect.DeepEqual(snapshot.Features, map[string]any{"server_version": "0.199.0"}) {
		t.Fatalf("connected snapshot = %#v", snapshot)
	}

	result, err := network.RetriableCall(context.Background(), "blockchain.block.headers", []any{9000, 1000, 0, true}, false)
	if err != nil || !reflect.DeepEqual(result, map[string]any{"base64": "encoded"}) {
		t.Fatalf("header RPC = %#v, %v", result, err)
	}
	headerRequest := <-requests
	if headerRequest.Method != "blockchain.block.headers" ||
		!reflect.DeepEqual(headerRequest.Params, []any{json.Number("9000"), json.Number("1000"), json.Number("0"), true}) {
		t.Fatalf("header request = %#v", headerRequest)
	}
	if _, err := network.RetriableCall(context.Background(), "trigger.notification", nil, true); err != nil {
		t.Fatal(err)
	}
	_ = <-requests
	close(notificationTrigger)
	waitForRemoteHeight(t, network, 322)
	select {
	case params := <-headerNotifications:
		want := []any{map[string]any{"height": json.Number("322")}}
		if !reflect.DeepEqual(params, want) {
			t.Fatalf("ledger header notification = %#v, want %#v", params, want)
		}
	case <-time.After(time.Second):
		t.Fatal("ledger header notification was not delivered")
	}
}

func TestNetworkHeaderNotificationDeduplicatesAndRejectsStaleClient(t *testing.T) {
	network, err := NewNetwork(NetworkConfig{})
	if err != nil {
		t.Fatal(err)
	}
	active := &Client{}
	stale := &Client{}
	network.mu.Lock()
	network.running = true
	network.client = active
	network.mu.Unlock()
	received := make(chan any, 4)
	network.SetHeaderNotificationHandler(func(_ context.Context, params any) {
		received <- params
	})
	first := []any{map[string]any{"height": 10, "hex": "aa"}}
	network.handleNotification(active, context.Background(), "blockchain.headers.subscribe", first)
	network.handleNotification(active, context.Background(), "blockchain.headers.subscribe", []any{
		map[string]any{"height": 10, "hex": "aa"},
	})
	second := []any{map[string]any{"height": 10, "hex": "bb"}}
	network.handleNotification(active, context.Background(), "blockchain.headers.subscribe", second)
	network.handleNotification(stale, context.Background(), "blockchain.headers.subscribe", []any{
		map[string]any{"height": 11, "hex": "cc"},
	})
	if len(received) != 2 {
		t.Fatalf("delivered header notifications = %d, want 2", len(received))
	}
	if got := <-received; !reflect.DeepEqual(got, first) {
		t.Fatalf("first header notification = %#v", got)
	}
	if got := <-received; !reflect.DeepEqual(got, second) {
		t.Fatalf("second header notification = %#v", got)
	}
	if height := network.RemoteHeight(); height != 10 {
		t.Fatalf("remote height after stale notification = %d", height)
	}
}

func TestNetworkSkipsIncompatibleServer(t *testing.T) {
	attempted := make(chan string, 4)
	dialer := &fakeDialer{serveAddress: func(address string, attempt int, connection net.Conn) {
		attempted <- address
		serveFakeHub(connection, nil, func(request hubRequest) hubReply {
			if request.Method == "server.version" && address == "old:1" {
				return resultReply([]any{"old hub", "0.64.9"})
			}
			return standardHandshakeReply(request)
		})
	}}
	network := newTestNetwork(t, dialer, []Server{
		{Host: "old", Port: 1},
		{Host: "current", Port: 2},
	})
	startTestNetwork(t, network)
	waitForConnected(t, network)
	if first, second := <-attempted, <-attempted; first != "old:1" || second != "current:2" {
		t.Fatalf("connection order = %q, %q", first, second)
	}
	if server := network.Snapshot().Server; server != (Server{Host: "current", Port: 2}) {
		t.Fatalf("selected server = %#v", server)
	}
}

func TestNetworkServerSourcePrecedenceAndSelectorOrder(t *testing.T) {
	known := NewMemoryKnownHubs()
	knownServer := Server{Host: "known", Port: 2}
	known.Set(knownServer, HubDetails{})
	explicitServer := Server{Host: "explicit", Port: 3}
	defaultServer := Server{Host: "default", Port: 1}

	explicitNetwork := newTestNetworkWithConfig(t, NetworkConfig{
		Servers:         []Server{defaultServer},
		ExplicitServers: []Server{explicitServer},
		KnownHubs:       known,
	})
	if got := explicitNetwork.selectionServers(); !reflect.DeepEqual(got, []Server{explicitServer}) {
		t.Fatalf("explicit source = %#v", got)
	}
	knownNetwork := newTestNetworkWithConfig(t, NetworkConfig{
		Servers:   []Server{defaultServer},
		KnownHubs: known,
	})
	if got := knownNetwork.selectionServers(); !reflect.DeepEqual(got, []Server{knownServer}) {
		t.Fatalf("known-hub source = %#v", got)
	}
	emptyKnownNetwork := newTestNetworkWithConfig(t, NetworkConfig{
		Servers:   []Server{defaultServer},
		KnownHubs: NewMemoryKnownHubs(),
	})
	if got := emptyKnownNetwork.selectionServers(); !reflect.DeepEqual(got, []Server{defaultServer}) {
		t.Fatalf("default source = %#v", got)
	}

	jurisdiction := "US"
	selector := &recordingCandidateSelector{
		candidates: []Candidate{
			{Server: Server{Host: "wrong-country", Port: 5}, Pong: &Pong{Flags: 1, Country: 118}},
			{Server: Server{Host: "selected", Port: 4}, Pong: &Pong{Flags: 1, Country: 236}},
		},
		requests: make(chan SelectionRequest, 1),
	}
	dialer := &fakeDialer{serveAddress: func(address string, _ int, connection net.Conn) {
		if address != "selected:4" {
			t.Errorf("dialed %q", address)
		}
		serveFakeHub(connection, nil, standardHandshakeReply)
	}}
	selectedNetwork := newTestNetworkWithConfig(t, NetworkConfig{
		Servers:      []Server{defaultServer},
		KnownHubs:    known,
		Selector:     selector,
		Jurisdiction: &jurisdiction,
		Client: ClientConfig{
			Dialer:         dialer,
			RequestTimeout: 250 * time.Millisecond,
		},
		ReconnectDelay: time.Second,
	})
	startTestNetwork(t, selectedNetwork)
	waitForConnected(t, selectedNetwork)
	request := <-selector.requests
	if !reflect.DeepEqual(request.Servers, []Server{knownServer}) || request.KnownHubs != known ||
		request.Jurisdiction == nil || *request.Jurisdiction != jurisdiction {
		t.Fatalf("selection request = %#v", request)
	}
	if server := selectedNetwork.Snapshot().Server; server != (Server{Host: "selected", Port: 4}) {
		t.Fatalf("selector result server = %#v", server)
	}
}

func TestNetworkPeerDiscoveryPersistsInitialAndNotifiedHubs(t *testing.T) {
	known, err := OpenKnownHubs(filepath.Join(t.TempDir(), KnownHubsFilename))
	if err != nil {
		t.Fatal(err)
	}
	connectedServer := Server{Host: "hub", Port: 1}
	known.UpdateCountry(connectedServer, "US")
	notify := make(chan struct{})
	dialer := &fakeDialer{serve: func(_ int, connection net.Conn) {
		serveFakeHub(connection, nil, func(request hubRequest) hubReply {
			switch request.Method {
			case "server.peers.subscribe":
				return resultReply([]any{"discovered:50002"})
			case "trigger.peers":
				go func() {
					<-notify
					writeHubNotification(connection, "blockchain.peers.subscribe", []any{"notified:50003"})
				}()
				return resultReply(map[string]any{})
			default:
				return standardHandshakeReply(request)
			}
		})
	}}
	network := newTestNetworkWithConfig(t, NetworkConfig{
		Servers:   []Server{{Host: "default", Port: 9}},
		KnownHubs: known,
		Client: ClientConfig{
			Dialer:         dialer,
			RequestTimeout: 250 * time.Millisecond,
		},
		ReconnectDelay: time.Second,
	})
	startTestNetwork(t, network)
	waitForConnected(t, network)
	waitForKnownHubs(t, known, 2)
	assertKnownHubsFile(t, known.Path(), "discovered:50002: {}\nhub:1:\n  country: US\n")

	if _, err := network.RetriableCall(context.Background(), "trigger.peers", nil, false); err != nil {
		t.Fatal(err)
	}
	close(notify)
	waitForKnownHubs(t, known, 3)
	assertKnownHubsFile(t, known.Path(), "discovered:50002: {}\nhub:1:\n  country: US\nnotified:50003: {}\n")
}

func TestNetworkPeerDiscoveryKeepsPartialAddButDoesNotSaveFailedBatch(t *testing.T) {
	known, err := OpenKnownHubs(filepath.Join(t.TempDir(), KnownHubsFilename))
	if err != nil {
		t.Fatal(err)
	}
	network := newTestNetworkWithConfig(t, NetworkConfig{KnownHubs: known})
	network.updateKnownHubs([]any{"partial:50001", "broken:not-a-port"})
	if known.Len() != 1 || known.Exists() {
		t.Fatalf("failed batch state = %#v, exists %v", known.Snapshot(), known.Exists())
	}
	network.updateKnownHubs([]any{"later:50002"})
	assertKnownHubsFile(t, known.Path(), "later:50002: {}\npartial:50001: {}\n")

	falsyKnown, err := OpenKnownHubs(filepath.Join(t.TempDir(), KnownHubsFilename))
	if err != nil {
		t.Fatal(err)
	}
	falsyNetwork := newTestNetworkWithConfig(t, NetworkConfig{KnownHubs: falsyKnown})
	falsyNetwork.updateKnownHubs([]any{
		nil, false, json.Number("0"), []any{}, map[string]any{}, "good:50003",
	})
	assertKnownHubsFile(t, falsyKnown.Path(), "good:50003: {}\n")
}

func TestNetworkRetriableCallReconnectsAfterConnectionLoss(t *testing.T) {
	var headerCalls atomic.Int32
	dialer := &fakeDialer{serve: func(attempt int, connection net.Conn) {
		serveFakeHub(connection, nil, func(request hubRequest) hubReply {
			if request.Method == "blockchain.block.headers" {
				headerCalls.Add(1)
				if attempt == 1 {
					return hubReply{close: true}
				}
				return resultReply(map[string]any{"base64": "after-reconnect"})
			}
			return standardHandshakeReply(request)
		})
	}}
	network := newTestNetwork(t, dialer, []Server{{Host: "hub", Port: 1}})
	startTestNetwork(t, network)
	waitForConnected(t, network)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := network.RetriableCall(ctx, "blockchain.block.headers", []any{0, 1000, 0, true}, true)
	if err != nil || result["base64"] != "after-reconnect" {
		t.Fatalf("retriable response = %#v, %v", result, err)
	}
	if dialer.Attempts() != 2 || headerCalls.Load() != 2 {
		t.Fatalf("retry attempts = dials %d, calls %d", dialer.Attempts(), headerCalls.Load())
	}
}

func TestNetworkRetriableCallRetriesTimeoutOnSameClient(t *testing.T) {
	var headerCalls atomic.Int32
	dialer := &fakeDialer{serve: func(_ int, connection net.Conn) {
		serveFakeHub(connection, nil, func(request hubRequest) hubReply {
			if request.Method == "blockchain.block.headers" {
				if headerCalls.Add(1) == 1 {
					return hubReply{ignore: true}
				}
				return resultReply(map[string]any{"base64": "retry"})
			}
			return standardHandshakeReply(request)
		})
	}}
	network := newTestNetworkWithConfig(t, NetworkConfig{
		Servers: []Server{{Host: "hub", Port: 1}},
		Client: ClientConfig{
			Dialer:         dialer,
			RequestTimeout: 40 * time.Millisecond,
		},
		ReconnectDelay: time.Second,
	})
	startTestNetwork(t, network)
	waitForConnected(t, network)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := network.RetriableCall(ctx, "blockchain.block.headers", nil, false)
	if err != nil || result["base64"] != "retry" {
		t.Fatalf("timeout retry = %#v, %v", result, err)
	}
	if dialer.Attempts() != 1 || headerCalls.Load() != 2 {
		t.Fatalf("timeout retry = dials %d, calls %d", dialer.Attempts(), headerCalls.Load())
	}
}

func TestNetworkRetriableCallDoesNotRetryRPCError(t *testing.T) {
	var headerCalls atomic.Int32
	dialer := &fakeDialer{serve: func(_ int, connection net.Conn) {
		serveFakeHub(connection, nil, func(request hubRequest) hubReply {
			if request.Method == "blockchain.block.headers" {
				headerCalls.Add(1)
				return errorReply(-1, "checkpoint unavailable")
			}
			return standardHandshakeReply(request)
		})
	}}
	network := newTestNetwork(t, dialer, []Server{{Host: "hub", Port: 1}})
	startTestNetwork(t, network)
	waitForConnected(t, network)
	_, err := network.RetriableCall(context.Background(), "blockchain.block.headers", nil, true)
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != -1 || headerCalls.Load() != 1 || dialer.Attempts() != 1 {
		t.Fatalf("RPC failure = %#v, %v; calls %d dials %d", rpcErr, err, headerCalls.Load(), dialer.Attempts())
	}
}

func TestNetworkUsesFixedReconnectDelayWithoutWaiter(t *testing.T) {
	var firstAttempt time.Time
	dialer := &fakeDialer{dial: func(attempt int, address string) (net.Conn, error) {
		if attempt == 1 {
			firstAttempt = time.Now()
			return nil, errors.New("offline")
		}
		client, server := net.Pipe()
		go serveFakeHub(server, nil, standardHandshakeReply)
		return client, nil
	}}
	network := newTestNetworkWithConfig(t, NetworkConfig{
		Servers:        []Server{{Host: "hub", Port: 1}},
		Client:         ClientConfig{Dialer: dialer},
		ReconnectDelay: 80 * time.Millisecond,
	})
	startTestNetwork(t, network)
	waitForConnected(t, network)
	if elapsed := time.Since(firstAttempt); elapsed < 65*time.Millisecond {
		t.Fatalf("failed connection retried after %s, want fixed delay", elapsed)
	}
	if dialer.Attempts() != 2 {
		t.Fatalf("dial attempts = %d, want 2", dialer.Attempts())
	}
}

func TestNetworkStopUnblocksDisconnectedRetriableCall(t *testing.T) {
	network := newTestNetwork(t, &fakeDialer{}, nil)
	startTestNetwork(t, network)
	result := make(chan error, 1)
	go func() {
		_, err := network.RetriableCall(context.Background(), "blockchain.block.headers", nil, true)
		result <- err
	}()
	time.Sleep(20 * time.Millisecond)
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := network.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrNetworkStopped) {
			t.Fatalf("stopped call error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stopped network did not release retriable call")
	}
}

func TestNetworkIgnoresStaleSessionNotification(t *testing.T) {
	network := newTestNetwork(t, &fakeDialer{}, nil)
	oldClient := &Client{}
	currentClient := &Client{}
	network.mu.Lock()
	network.running = true
	network.client = currentClient
	network.remoteHeight = 50
	network.mu.Unlock()
	network.handleNotification(oldClient, context.Background(), "blockchain.headers.subscribe", []any{
		map[string]any{"height": json.Number("999")},
	})
	if height := network.RemoteHeight(); height != 50 {
		t.Fatalf("stale session changed height to %d", height)
	}
	network.mu.Lock()
	network.running = false
	network.client = nil
	network.mu.Unlock()
}

func TestProtocolVersionComparisonMatchesPythonTupleOrdering(t *testing.T) {
	for _, test := range []struct {
		version string
		want    bool
	}{
		{version: "0.64.999", want: false},
		{version: "0.65", want: false},
		{version: "0.65.0", want: true},
		{version: "0.65.0.1", want: true},
		{version: "1.0.0", want: true},
	} {
		got, err := protocolAtLeast(test.version, DefaultProtocolMinimum)
		if err != nil || got != test.want {
			t.Fatalf("protocolAtLeast(%q) = %t, %v; want %t", test.version, got, err, test.want)
		}
	}
}

type hubRequest struct {
	JSONRPC       string
	Method        string
	ID            int64
	Params        any
	ParamsPresent bool
}

type hubReply struct {
	result any
	rpcErr *RPCError
	close  bool
	ignore bool
}

type fakeDialer struct {
	mu           sync.Mutex
	attempts     int
	dial         func(int, string) (net.Conn, error)
	serve        func(int, net.Conn)
	serveAddress func(string, int, net.Conn)
}

func (dialer *fakeDialer) DialContext(ctx context.Context, _, address string) (net.Conn, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	dialer.mu.Lock()
	dialer.attempts++
	attempt := dialer.attempts
	dial := dialer.dial
	serve := dialer.serve
	serveAddress := dialer.serveAddress
	dialer.mu.Unlock()
	if dial != nil {
		return dial(attempt, address)
	}
	client, server := net.Pipe()
	if serveAddress != nil {
		go serveAddress(address, attempt, server)
	} else if serve != nil {
		go serve(attempt, server)
	} else {
		_ = server.Close()
	}
	return client, nil
}

func (dialer *fakeDialer) Attempts() int {
	dialer.mu.Lock()
	defer dialer.mu.Unlock()
	return dialer.attempts
}

func serveFakeHub(connection net.Conn, requests chan<- hubRequest, reply func(hubRequest) hubReply) {
	defer connection.Close()
	reader := bufio.NewReader(connection)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		request, err := decodeHubRequest(line)
		if err != nil {
			return
		}
		if requests != nil {
			requests <- request
		}
		response := reply(request)
		if response.close {
			return
		}
		if response.ignore {
			continue
		}
		var payload any
		if response.rpcErr != nil {
			payload = map[string]any{
				"jsonrpc": "2.0",
				"error": map[string]any{
					"code": response.rpcErr.Code, "message": response.rpcErr.Message,
				},
				"id": request.ID,
			}
		} else {
			payload = map[string]any{"jsonrpc": "2.0", "result": response.result, "id": request.ID}
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return
		}
		if _, err := connection.Write(append(encoded, '\n')); err != nil {
			return
		}
	}
}

func decodeHubRequest(message []byte) (hubRequest, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(message, &raw); err != nil {
		return hubRequest{}, err
	}
	var request hubRequest
	if err := json.Unmarshal(raw["jsonrpc"], &request.JSONRPC); err != nil {
		return hubRequest{}, err
	}
	if err := json.Unmarshal(raw["method"], &request.Method); err != nil {
		return hubRequest{}, err
	}
	var id json.Number
	if err := json.Unmarshal(raw["id"], &id); err != nil {
		return hubRequest{}, err
	}
	parsed, err := id.Int64()
	if err != nil {
		return hubRequest{}, err
	}
	request.ID = parsed
	if params, exists := raw["params"]; exists {
		request.ParamsPresent = true
		request.Params, err = decodeJSONValue(params)
		if err != nil {
			return hubRequest{}, err
		}
	}
	return request, nil
}

func standardHandshakeReply(request hubRequest) hubReply {
	switch request.Method {
	case "server.version":
		return resultReply([]any{"fake hub", "0.65.0"})
	case "server.features":
		return resultReply(map[string]any{"server_version": "fake"})
	case "server.peers.subscribe":
		return resultReply([]any{})
	case "blockchain.headers.subscribe":
		return resultReply(map[string]any{"height": 100})
	case "server.ping":
		return resultReply(nil)
	default:
		return errorReply(-32601, "unknown method")
	}
}

func resultReply(result any) hubReply { return hubReply{result: result} }

func errorReply(code int64, message string) hubReply {
	return hubReply{rpcErr: &RPCError{Code: code, Message: message}}
}

func writeHubNotification(connection net.Conn, method string, params any) {
	payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	_, _ = connection.Write(append(payload, '\n'))
}

type recordingCandidateSelector struct {
	candidates []Candidate
	requests   chan SelectionRequest
}

func (selector *recordingCandidateSelector) Select(
	ctx context.Context, request SelectionRequest,
) ([]Candidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	request.Servers = append([]Server(nil), request.Servers...)
	selector.requests <- request
	return append([]Candidate(nil), selector.candidates...), nil
}

func waitForKnownHubs(t *testing.T, known *KnownHubs, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for known.Len() != count {
		if time.Now().After(deadline) {
			t.Fatalf("known hubs = %#v, want %d entries", known.Snapshot(), count)
		}
		time.Sleep(time.Millisecond)
	}
}

func assertKnownHubsFile(t *testing.T, path, want string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != want {
		t.Fatalf("known hubs YAML = %q, want %q", contents, want)
	}
}

func newTestNetwork(t *testing.T, dialer Dialer, servers []Server) *Network {
	t.Helper()
	return newTestNetworkWithConfig(t, NetworkConfig{
		Servers: servers,
		Client: ClientConfig{
			Dialer:         dialer,
			RequestTimeout: 250 * time.Millisecond,
		},
		ReconnectDelay: time.Second,
		KeepaliveIdle:  time.Hour,
	})
}

func newTestNetworkWithConfig(t *testing.T, config NetworkConfig) *Network {
	t.Helper()
	if config.KeepaliveIdle == 0 {
		config.KeepaliveIdle = time.Hour
	}
	network, err := NewNetwork(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := network.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	return network
}

func startTestNetwork(t *testing.T, network *Network) {
	t.Helper()
	if err := network.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func waitForConnected(t *testing.T, network *Network) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !network.IsConnected() {
		if time.Now().After(deadline) {
			t.Fatalf("network did not connect: %#v", network.Snapshot())
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForRemoteHeight(t *testing.T, network *Network, height int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for network.RemoteHeight() != height {
		if time.Now().After(deadline) {
			t.Fatalf("remote height = %d, want %d", network.RemoteHeight(), height)
		}
		time.Sleep(time.Millisecond)
	}
}

var _ Dialer = (*fakeDialer)(nil)
