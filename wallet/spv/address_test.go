package spv

import (
	"context"
	"errors"
	"net"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func TestNetworkRetriableValueAcceptsScalarAndListResults(t *testing.T) {
	dialer := &fakeDialer{serve: func(_ int, connection net.Conn) {
		serveFakeHub(connection, nil, func(request hubRequest) hubReply {
			switch request.Method {
			case "scalar.value":
				return resultReply("status")
			case "list.value":
				return resultReply([]any{"first", nil})
			default:
				return standardHandshakeReply(request)
			}
		})
	}}
	network := newTestNetwork(t, dialer, []Server{{Host: "hub", Port: 1}})
	startTestNetwork(t, network)
	waitForConnected(t, network)

	scalar, err := network.RetriableValue(context.Background(), "scalar.value", nil, true)
	if err != nil || scalar != "status" {
		t.Fatalf("scalar result = %#v, %v", scalar, err)
	}
	list, err := network.RetriableValue(context.Background(), "list.value", nil, false)
	if err != nil || !reflect.DeepEqual(list, []any{"first", nil}) {
		t.Fatalf("list result = %#v, %v", list, err)
	}
	if _, err := network.RetriableCall(context.Background(), "scalar.value", nil, true); !errors.Is(err, ErrUnexpectedRPCResult) {
		t.Fatalf("mapping wrapper error = %v", err)
	}
}

func TestNetworkRetriableValuePreservesOnlyTransactionBatchObjectOrder(t *testing.T) {
	transactionBatch := newOrderedObject()
	transactionBatch.set("second", []any{"02", nil})
	transactionBatch.set("first", []any{"01", nil})
	dialer := &fakeDialer{serve: func(_ int, connection net.Conn) {
		serveFakeHub(connection, nil, func(request hubRequest) hubReply {
			switch request.Method {
			case orderedTransactionBatch, "ordinary.object":
				return resultReply(transactionBatch)
			default:
				return standardHandshakeReply(request)
			}
		})
	}}
	network := newTestNetwork(t, dialer, []Server{{Host: "hub", Port: 1}})
	startTestNetwork(t, network)
	waitForConnected(t, network)

	value, err := network.RetriableValue(
		context.Background(), orderedTransactionBatch, []any{"first", "second"}, true,
	)
	ordered, ok := value.(*OrderedObject)
	if err != nil || !ok || !reflect.DeepEqual(ordered.Keys(), []string{"second", "first"}) {
		t.Fatalf("transaction batch = %T %#v, %v", value, value, err)
	}
	ordinary, err := network.RetriableValue(context.Background(), "ordinary.object", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, ordered := ordinary.(*OrderedObject); ordered {
		t.Fatalf("ordinary RPC unexpectedly returned ordered object: %#v", ordinary)
	}
	if _, mapping := ordinary.(map[string]any); !mapping {
		t.Fatalf("ordinary RPC = %T, want map", ordinary)
	}
}

func TestNetworkSubscribeAddressesUsesFlatParamsAndChecksListResult(t *testing.T) {
	requests := make(chan hubRequest, 8)
	dialer := &fakeDialer{serve: func(_ int, connection net.Conn) {
		serveFakeHub(connection, requests, func(request hubRequest) hubReply {
			if request.Method == "blockchain.address.subscribe" {
				if reflect.DeepEqual(request.Params, []any{"one", "two", "three"}) {
					return resultReply([]any{"a", nil, "c"})
				}
				return resultReply("not-a-list")
			}
			return standardHandshakeReply(request)
		})
	}}
	network := newTestNetwork(t, dialer, []Server{{Host: "hub", Port: 1}})
	startTestNetwork(t, network)
	waitForConnected(t, network)
	for range 4 {
		<-requests
	}

	statuses, err := network.SubscribeAddresses(
		context.Background(), []string{"one", "two", "three"},
	)
	if err != nil || !reflect.DeepEqual(statuses, []any{"a", nil, "c"}) {
		t.Fatalf("subscription statuses = %#v, %v", statuses, err)
	}
	request := <-requests
	if request.Method != "blockchain.address.subscribe" ||
		!reflect.DeepEqual(request.Params, []any{"one", "two", "three"}) {
		t.Fatalf("subscription request = %#v", request)
	}

	_, err = network.SubscribeAddresses(context.Background(), []string{"wrong-result"})
	if !errors.Is(err, ErrUnexpectedRPCResult) {
		t.Fatalf("non-list subscription result error = %v", err)
	}
}

func TestNetworkSubscribeAddressesTimeoutAbortsSessionAndReturnsCancellation(t *testing.T) {
	var addressCalls atomic.Int32
	dialer := &fakeDialer{dial: func(attempt int, _ string) (net.Conn, error) {
		if attempt > 1 {
			return nil, errors.New("offline after subscription timeout")
		}
		client, server := net.Pipe()
		go serveFakeHub(server, nil, func(request hubRequest) hubReply {
			if request.Method == "blockchain.address.subscribe" {
				addressCalls.Add(1)
				return hubReply{ignore: true}
			}
			return standardHandshakeReply(request)
		})
		return client, nil
	}}
	network := newTestNetworkWithConfig(t, NetworkConfig{
		Servers: []Server{{Host: "hub", Port: 1}},
		Client: ClientConfig{
			Dialer:         dialer,
			RequestTimeout: 35 * time.Millisecond,
		},
		ReconnectDelay: 10 * time.Millisecond,
	})
	startTestNetwork(t, network)
	waitForConnected(t, network)

	_, err := network.SubscribeAddresses(context.Background(), []string{"one"})
	var canceled *AddressSubscriptionCanceledError
	if !errors.As(err, &canceled) || !errors.Is(err, context.Canceled) ||
		!errors.Is(err, ErrRequestTimeout) {
		t.Fatalf("subscription timeout = %T %v", err, err)
	}
	if addressCalls.Load() != 1 {
		t.Fatalf("address subscription calls = %d, want 1", addressCalls.Load())
	}
	if network.IsConnected() {
		t.Fatal("timed-out subscription left the failed session published")
	}
}

func TestNetworkSubscribeAddressesReturnsConnectionFailureWithoutInlineRetry(t *testing.T) {
	var addressCalls atomic.Int32
	dialer := &fakeDialer{serve: func(attempt int, connection net.Conn) {
		serveFakeHub(connection, nil, func(request hubRequest) hubReply {
			if request.Method == "blockchain.address.subscribe" {
				addressCalls.Add(1)
				if attempt == 1 {
					return hubReply{close: true}
				}
				return resultReply([]any{"unexpected-retry"})
			}
			return standardHandshakeReply(request)
		})
	}}
	network := newTestNetworkWithConfig(t, NetworkConfig{
		Servers:        []Server{{Host: "hub", Port: 1}},
		Client:         ClientConfig{Dialer: dialer, RequestTimeout: 100 * time.Millisecond},
		ReconnectDelay: 10 * time.Millisecond,
	})
	startTestNetwork(t, network)
	waitForConnected(t, network)

	_, err := network.SubscribeAddresses(context.Background(), []string{"one"})
	if !errors.Is(err, ErrConnection) {
		t.Fatalf("connection failure = %v", err)
	}
	if addressCalls.Load() != 1 {
		t.Fatalf("address subscription calls = %d, want no inline retry", addressCalls.Load())
	}
}

func TestNetworkSubscribeAddressesDoesNotWaitForReplacementSession(t *testing.T) {
	network, err := NewNetwork(NetworkConfig{})
	if err != nil {
		t.Fatal(err)
	}
	network.mu.Lock()
	network.running = true
	network.mu.Unlock()
	started := time.Now()
	_, err = network.SubscribeAddresses(context.Background(), []string{"one"})
	if !errors.Is(err, ErrConnection) || time.Since(started) > 100*time.Millisecond {
		t.Fatalf("unavailable master subscription = %v after %s", err, time.Since(started))
	}
}

func TestNetworkBroadcastTransactionUsesOneDirectScalarRPC(t *testing.T) {
	requests := make(chan hubRequest, 8)
	dialer := &fakeDialer{serve: func(_ int, connection net.Conn) {
		serveFakeHub(connection, requests, func(request hubRequest) hubReply {
			if request.Method == TransactionBroadcastMethod {
				return resultReply("transaction-id")
			}
			return standardHandshakeReply(request)
		})
	}}
	network := newTestNetwork(t, dialer, []Server{{Host: "hub", Port: 1}})
	startTestNetwork(t, network)
	waitForConnected(t, network)
	for range 4 {
		<-requests
	}

	result, err := network.BroadcastTransaction(context.Background(), "00ff")
	if err != nil || result != "transaction-id" {
		t.Fatalf("broadcast result = %#v, %v", result, err)
	}
	request := <-requests
	if request.Method != TransactionBroadcastMethod ||
		!reflect.DeepEqual(request.Params, []any{"00ff"}) {
		t.Fatalf("broadcast request = %#v", request)
	}
}

func TestNetworkBroadcastTransactionPreservesRPCError(t *testing.T) {
	dialer := &fakeDialer{serve: func(_ int, connection net.Conn) {
		serveFakeHub(connection, nil, func(request hubRequest) hubReply {
			if request.Method == TransactionBroadcastMethod {
				return errorReply(1, "rejected transaction")
			}
			return standardHandshakeReply(request)
		})
	}}
	network := newTestNetwork(t, dialer, []Server{{Host: "hub", Port: 1}})
	startTestNetwork(t, network)
	waitForConnected(t, network)

	_, err := network.BroadcastTransaction(context.Background(), "00ff")
	var rpcError *RPCError
	if !errors.As(err, &rpcError) || rpcError.Code != 1 || rpcError.Message != "rejected transaction" {
		t.Fatalf("broadcast RPC error = %T %v", err, err)
	}
}

func TestNetworkBroadcastTransactionTimeoutDoesNotRetryOrAbortSession(t *testing.T) {
	var calls atomic.Int32
	dialer := &fakeDialer{serve: func(_ int, connection net.Conn) {
		serveFakeHub(connection, nil, func(request hubRequest) hubReply {
			if request.Method == TransactionBroadcastMethod {
				calls.Add(1)
				return hubReply{ignore: true}
			}
			return standardHandshakeReply(request)
		})
	}}
	network := newTestNetworkWithConfig(t, NetworkConfig{
		Servers: []Server{{Host: "hub", Port: 1}},
		Client: ClientConfig{
			Dialer: dialer, RequestTimeout: 35 * time.Millisecond,
		},
	})
	startTestNetwork(t, network)
	waitForConnected(t, network)

	_, err := network.BroadcastTransaction(context.Background(), "00ff")
	if !errors.Is(err, ErrRequestTimeout) {
		t.Fatalf("broadcast timeout = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("broadcast calls = %d, want one attempt", calls.Load())
	}
	if !network.IsConnected() {
		t.Fatal("broadcast timeout unexpectedly aborted the master session")
	}
}

func TestNetworkBroadcastTransactionReturnsConnectionFailureWithoutInlineRetry(t *testing.T) {
	var calls atomic.Int32
	dialer := &fakeDialer{serve: func(attempt int, connection net.Conn) {
		serveFakeHub(connection, nil, func(request hubRequest) hubReply {
			if request.Method == TransactionBroadcastMethod {
				calls.Add(1)
				if attempt == 1 {
					return hubReply{close: true}
				}
				return resultReply("unexpected-retry")
			}
			return standardHandshakeReply(request)
		})
	}}
	network := newTestNetworkWithConfig(t, NetworkConfig{
		Servers:        []Server{{Host: "hub", Port: 1}},
		Client:         ClientConfig{Dialer: dialer, RequestTimeout: 100 * time.Millisecond},
		ReconnectDelay: 10 * time.Millisecond,
	})
	startTestNetwork(t, network)
	waitForConnected(t, network)

	_, err := network.BroadcastTransaction(context.Background(), "00ff")
	if !errors.Is(err, ErrConnection) {
		t.Fatalf("broadcast connection failure = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("broadcast calls = %d, want no inline retry", calls.Load())
	}
}

func TestNetworkBroadcastTransactionDoesNotWaitForReplacementSession(t *testing.T) {
	network, err := NewNetwork(NetworkConfig{})
	if err != nil {
		t.Fatal(err)
	}
	network.mu.Lock()
	network.running = true
	network.mu.Unlock()
	started := time.Now()
	_, err = network.BroadcastTransaction(context.Background(), "00ff")
	if !errors.Is(err, ErrConnection) || time.Since(started) > 100*time.Millisecond {
		t.Fatalf("unavailable broadcast = %v after %s", err, time.Since(started))
	}
}

func TestNetworkAddressNotificationDeduplicatesAndRejectsStaleClient(t *testing.T) {
	configured := make(chan any, 4)
	network, err := NewNetwork(NetworkConfig{Client: ClientConfig{
		NotificationHandler: func(_ context.Context, method string, params any) {
			if method == "blockchain.address.subscribe" {
				configured <- params
			}
		},
	}})
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
	network.SetAddressNotificationHandler(func(_ context.Context, params any) {
		received <- params
	})

	first := []any{"address", "status-a"}
	second := []any{"address", "status-b"}
	network.handleNotification(active, context.Background(), "blockchain.address.subscribe", first)
	network.handleNotification(active, context.Background(), "blockchain.address.subscribe", []any{"address", "status-a"})
	network.handleNotification(active, context.Background(), "blockchain.address.subscribe", second)
	network.handleNotification(active, context.Background(), "blockchain.address.subscribe", first)
	network.handleNotification(stale, context.Background(), "blockchain.address.subscribe", []any{"stale", "status"})

	if len(received) != 3 {
		t.Fatalf("deduplicated address notifications = %d, want 3", len(received))
	}
	for index, want := range []any{first, second, first} {
		if got := <-received; !reflect.DeepEqual(got, want) {
			t.Fatalf("address notification %d = %#v, want %#v", index, got, want)
		}
	}
	if len(configured) != 4 {
		t.Fatalf("configured handler notifications = %d, want all 4 active payloads", len(configured))
	}
}

func TestNetworkConnectedHandlerRunsAfterEachSuccessfulPublish(t *testing.T) {
	dialer := &fakeDialer{serve: func(attempt int, connection net.Conn) {
		serveFakeHub(connection, nil, func(request hubRequest) hubReply {
			if attempt == 1 && request.Method == "server.version" {
				return resultReply([]any{"old hub", "0.64.9"})
			}
			return standardHandshakeReply(request)
		})
	}}
	network := newTestNetworkWithConfig(t, NetworkConfig{
		Servers:        []Server{{Host: "hub", Port: 1}},
		Client:         ClientConfig{Dialer: dialer, RequestTimeout: 100 * time.Millisecond},
		ReconnectDelay: 10 * time.Millisecond,
	})
	connected := make(chan struct{}, 4)
	network.SetConnectedHandler(func(_ context.Context) {
		if !network.IsConnected() {
			t.Error("connected callback ran before the client was published")
		}
		connected <- struct{}{}
	})
	startTestNetwork(t, network)
	select {
	case <-connected:
	case <-time.After(time.Second):
		t.Fatal("connected callback did not run after successful publish")
	}
	if attempts := dialer.Attempts(); attempts != 2 {
		t.Fatalf("dial attempts after one failed handshake = %d, want 2", attempts)
	}
	if len(connected) != 0 {
		t.Fatal("failed handshake invoked the connected callback")
	}

	network.RequestReconnect()
	select {
	case <-connected:
	case <-time.After(time.Second):
		t.Fatal("connected callback did not run after reconnect")
	}
	if attempts := dialer.Attempts(); attempts != 3 {
		t.Fatalf("dial attempts after reconnect = %d, want 3", attempts)
	}
}
