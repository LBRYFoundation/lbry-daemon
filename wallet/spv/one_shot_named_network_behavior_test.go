package spv

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

const oneShotNamedTestMethod = "blockchain.claimtrie.search"

func TestNetworkOneShotNamedValueStoppedAndDisconnectedBehavior(t *testing.T) {
	stopped, err := NewNetwork(NetworkConfig{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := stopped.OneShotNamedValue(
		context.Background(), oneShotNamedTestMethod, map[string]any{"text": "cats"}, true,
	)
	if result != nil || !errors.Is(err, ErrNetworkStopped) {
		t.Fatalf("stopped named one-shot = %#v, %v", result, err)
	}

	stopped.mu.Lock()
	stopped.running = true
	stopped.mu.Unlock()
	started := time.Now()
	result, err = stopped.OneShotNamedValue(
		context.Background(), oneShotNamedTestMethod, map[string]any{"text": "cats"}, false,
	)
	if result != nil || !errors.Is(err, ErrConnection) {
		t.Fatalf("disconnected named one-shot = %#v, %v", result, err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("disconnected named one-shot waited %s", elapsed)
	}
	select {
	case <-stopped.wake:
	default:
		t.Fatal("disconnected named one-shot did not wake reconnect")
	}
}

func TestNetworkOneShotNamedValueSuccessUsesObjectParamsExactlyOnce(t *testing.T) {
	var calls atomic.Int64
	requests := make(chan hubRequest, 1)
	dialer := &fakeDialer{serve: func(_ int, connection net.Conn) {
		serveFakeHub(connection, nil, func(request hubRequest) hubReply {
			if request.Method == oneShotNamedTestMethod {
				calls.Add(1)
				requests <- request
				return resultReply(map[string]any{"txid": "abc"})
			}
			return standardHandshakeReply(request)
		})
	}}
	network := newTestNetwork(t, dialer, []Server{{Host: "hub", Port: 1}})
	startTestNetwork(t, network)
	waitForConnected(t, network)

	result, err := network.OneShotNamedValue(context.Background(), oneShotNamedTestMethod, map[string]any{
		"page": 2,
		"text": "cats",
	}, true)
	if err != nil || !reflect.DeepEqual(result, map[string]any{"txid": "abc"}) {
		t.Fatalf("successful named one-shot = %#v, %v", result, err)
	}
	request := <-requests
	if !request.ParamsPresent || !reflect.DeepEqual(request.Params, map[string]any{
		"page": json.Number("2"),
		"text": "cats",
	}) {
		t.Fatalf("named one-shot params = %#v, present = %t", request.Params, request.ParamsPresent)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("successful named one-shot calls = %d, want 1", got)
	}
}

func TestNetworkOneShotNamedValueFailuresAreNeverRetried(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		var calls atomic.Int64
		dialer := &fakeDialer{serve: func(_ int, connection net.Conn) {
			serveFakeHub(connection, nil, func(request hubRequest) hubReply {
				if request.Method == oneShotNamedTestMethod {
					calls.Add(1)
					return hubReply{ignore: true}
				}
				return standardHandshakeReply(request)
			})
		}}
		network := newTestNetworkWithConfig(t, NetworkConfig{
			Servers:        []Server{{Host: "hub", Port: 1}},
			Client:         ClientConfig{Dialer: dialer, RequestTimeout: 20 * time.Millisecond},
			ReconnectDelay: 10 * time.Millisecond,
			KeepaliveIdle:  time.Hour,
		})
		startTestNetwork(t, network)
		waitForConnected(t, network)

		result, err := network.OneShotNamedValue(
			context.Background(), oneShotNamedTestMethod, map[string]any{"text": "cats"}, true,
		)
		if result != nil || !errors.Is(err, ErrRequestTimeout) {
			t.Fatalf("timed-out named one-shot = %#v, %v", result, err)
		}
		time.Sleep(50 * time.Millisecond)
		if got := calls.Load(); got != 1 {
			t.Fatalf("timed-out named one-shot calls = %d, want 1", got)
		}
	})

	t.Run("connection", func(t *testing.T) {
		var calls atomic.Int64
		dialer := &fakeDialer{serve: func(_ int, connection net.Conn) {
			serveFakeHub(connection, nil, func(request hubRequest) hubReply {
				if request.Method == oneShotNamedTestMethod {
					calls.Add(1)
					return hubReply{close: true}
				}
				return standardHandshakeReply(request)
			})
		}}
		network := newTestNetworkWithConfig(t, NetworkConfig{
			Servers:        []Server{{Host: "hub", Port: 1}},
			Client:         ClientConfig{Dialer: dialer, RequestTimeout: 100 * time.Millisecond},
			ReconnectDelay: 10 * time.Millisecond,
			KeepaliveIdle:  time.Hour,
		})
		startTestNetwork(t, network)
		waitForConnected(t, network)

		result, err := network.OneShotNamedValue(
			context.Background(), oneShotNamedTestMethod, map[string]any{"text": "cats"}, false,
		)
		if result != nil || !errors.Is(err, ErrConnection) {
			t.Fatalf("connection-failed named one-shot = %#v, %v", result, err)
		}
		time.Sleep(50 * time.Millisecond)
		if got := calls.Load(); got != 1 {
			t.Fatalf("connection-failed named one-shot calls = %d, want 1", got)
		}
	})

	t.Run("rpc", func(t *testing.T) {
		var calls atomic.Int64
		dialer := &fakeDialer{serve: func(_ int, connection net.Conn) {
			serveFakeHub(connection, nil, func(request hubRequest) hubReply {
				if request.Method == oneShotNamedTestMethod {
					calls.Add(1)
					return errorReply(-32602, "bad claim constraints")
				}
				return standardHandshakeReply(request)
			})
		}}
		network := newTestNetwork(t, dialer, []Server{{Host: "hub", Port: 1}})
		startTestNetwork(t, network)
		waitForConnected(t, network)

		result, err := network.OneShotNamedValue(
			context.Background(), oneShotNamedTestMethod, map[string]any{"text": "cats"}, true,
		)
		var rpcErr *RPCError
		if result != nil || !errors.As(err, &rpcErr) || rpcErr.Code != -32602 ||
			rpcErr.Message != "bad claim constraints" {
			t.Fatalf("RPC-failed named one-shot = %#v, %#v, %v", result, rpcErr, err)
		}
		if got := calls.Load(); got != 1 {
			t.Fatalf("RPC-failed named one-shot calls = %d, want 1", got)
		}
	})
}

func TestNetworkOneShotNamedValueContextBehavior(t *testing.T) {
	var calls atomic.Int64
	requestSeen := make(chan struct{}, 1)
	dialer := &fakeDialer{serve: func(_ int, connection net.Conn) {
		serveFakeHub(connection, nil, func(request hubRequest) hubReply {
			if request.Method == oneShotNamedTestMethod {
				calls.Add(1)
				requestSeen <- struct{}{}
				return hubReply{ignore: true}
			}
			return standardHandshakeReply(request)
		})
	}}
	network := newTestNetworkWithConfig(t, NetworkConfig{
		Servers:       []Server{{Host: "hub", Port: 1}},
		Client:        ClientConfig{Dialer: dialer, RequestTimeout: time.Second},
		KeepaliveIdle: time.Hour,
	})
	startTestNetwork(t, network)
	waitForConnected(t, network)

	result, err := network.OneShotNamedValue(nil, oneShotNamedTestMethod, nil, true)
	if result != nil || err == nil || err.Error() != "SPV one-shot-call context is nil" {
		t.Fatalf("nil-context named one-shot = %#v, %v", result, err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("nil-context named one-shot calls = %d, want 0", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	resultReady := make(chan callResult, 1)
	go func() {
		value, callErr := network.OneShotNamedValue(
			ctx, oneShotNamedTestMethod, map[string]any{"text": "cats"}, false,
		)
		resultReady <- callResult{value: value, err: callErr}
	}()
	<-requestSeen
	cancel()
	response := <-resultReady
	if response.value != nil || !errors.Is(response.err, context.Canceled) {
		t.Fatalf("canceled named one-shot = %#v, %v", response.value, response.err)
	}
	time.Sleep(20 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("canceled named one-shot calls = %d, want 1", got)
	}
}
