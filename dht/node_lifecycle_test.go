package dht

import (
	"encoding/hex"
	"net"
	"testing"
	"time"
)

func TestNodeStatusAccessorsReturnSnapshots(t *testing.T) {
	var nodeID [HashSize]byte
	for index := range nodeID {
		nodeID[index] = byte(index + 1)
	}
	node, err := NewNodeWithID(0, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := node.NodeIDHex(), hex.EncodeToString(nodeID[:]); got != want {
		t.Fatalf("node ID = %q, want %q", got, want)
	}

	var firstID, secondID [HashSize]byte
	firstID[HashSize-1] = 1
	secondID[HashSize-1] = 2
	node.routing.AddPeer(Peer{ID: firstID, IP: net.IPv4(192, 0, 2, 1), UDPPort: 4444})
	node.routing.AddPeer(Peer{ID: secondID, IP: net.IPv4(192, 0, 2, 2), UDPPort: 4444})
	node.routing.AddPeer(Peer{ID: firstID, IP: net.IPv4(192, 0, 2, 3), UDPPort: 5555})
	if got := node.RoutingPeerCount(); got != 2 {
		t.Fatalf("routing peer count = %d, want 2", got)
	}

	var nilNode *Node
	if nilNode.NodeIDHex() != "" || nilNode.RoutingPeerCount() != 0 {
		t.Fatal("nil node accessors did not return zero values")
	}
}

func TestNodeStopReturnsPromptlyAndIsIdempotent(t *testing.T) {
	node, err := NewNode(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := node.Start(); err != nil {
		t.Fatal(err)
	}
	port := node.UDPPort

	stopped := make(chan struct{})
	go func() {
		node.Stop()
		node.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not interrupt DHT loops")
	}

	address := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}
	connection, err := net.DialUDP("udp", nil, address)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 1)
	if _, err := connection.Read(buffer); err == nil {
		t.Fatal("stopped DHT socket unexpectedly responded")
	}
	if err := node.Start(); err == nil || err.Error() != "dht: node is already running" {
		t.Fatalf("restart error = %v", err)
	}
}

func TestNodeRejectsSecondStartWhileRunning(t *testing.T) {
	node, err := NewNode(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := node.Start(); err != nil {
		t.Fatal(err)
	}
	defer node.Stop()
	if err := node.Start(); err == nil || err.Error() != "dht: node is already running" {
		t.Fatalf("second Start error = %v", err)
	}
}

func TestNodeBindsConfiguredInterface(t *testing.T) {
	node, err := NewNode(0)
	if err != nil {
		t.Fatal(err)
	}
	node.BindIP = net.IPv4(127, 0, 0, 1)
	if err := node.Start(); err != nil {
		t.Fatal(err)
	}
	defer node.Stop()
	address := node.conn.LocalAddr().(*net.UDPAddr)
	if !address.IP.IsLoopback() {
		t.Fatalf("DHT bound to %s, want loopback", address)
	}
}
