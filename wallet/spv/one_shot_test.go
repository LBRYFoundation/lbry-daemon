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

func TestNetworkOneShotValueDisconnectedReturnsImmediatelyAndWakesReconnect(t *testing.T) {
	network, err := NewNetwork(NetworkConfig{})
	if err != nil {
		t.Fatal(err)
	}
	network.mu.Lock()
	network.running = true
	network.mu.Unlock()

	started := time.Now()
	result, err := network.OneShotValue(
		context.Background(), "blockchain.transaction.info", []any{"txid"}, true,
	)
	if result != nil || !errors.Is(err, ErrConnection) {
		t.Fatalf("disconnected one-shot result = %#v, %v", result, err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("disconnected one-shot waited %s", elapsed)
	}
	select {
	case <-network.wake:
	default:
		t.Fatal("disconnected one-shot did not wake reconnect")
	}
}

func TestNetworkOneShotValueSendsOneRequestWithoutTimeoutRetry(t *testing.T) {
	var infoRequests atomic.Int64
	requestSeen := make(chan hubRequest, 1)
	dialer := &fakeDialer{serve: func(_ int, connection net.Conn) {
		serveFakeHub(connection, nil, func(request hubRequest) hubReply {
			if request.Method == "blockchain.transaction.info" {
				infoRequests.Add(1)
				select {
				case requestSeen <- request:
				default:
				}
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

	result, err := network.OneShotValue(
		context.Background(), "blockchain.transaction.info", []any{"txid"}, true,
	)
	if result != nil || !errors.Is(err, ErrRequestTimeout) {
		t.Fatalf("timed-out one-shot result = %#v, %v", result, err)
	}
	request := <-requestSeen
	if !reflect.DeepEqual(request.Params, []any{"txid"}) {
		t.Fatalf("one-shot params = %#v", request.Params)
	}
	time.Sleep(50 * time.Millisecond)
	if calls := infoRequests.Load(); calls != 1 {
		t.Fatalf("one-shot transaction info requests = %d, want 1", calls)
	}
}
