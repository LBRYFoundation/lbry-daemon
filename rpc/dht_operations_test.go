package rpc

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	dhtpkg "lbry/daemon/dht"
)

func TestRoutingTableGetEncodesBucketsAndPrefixNeighbors(t *testing.T) {
	var nodeID [dhtpkg.HashSize]byte
	nodeID[0] = 0x11
	node, err := dhtpkg.NewNodeWithID(0, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	firstID := nodeID
	firstID[dhtpkg.HashSize-1] = 1
	secondID := nodeID
	secondID[0] = 0x91
	node.ObservePeer(dhtpkg.Peer{ID: firstID, IP: net.IPv4(192, 0, 2, 1), UDPPort: 4444, TCPPort: 5555})
	node.ObservePeer(dhtpkg.Peer{ID: secondID, IP: net.IPv4(192, 0, 2, 2), UDPPort: 4445, TCPPort: 5556})
	server := CreateServer(WithDHTNodeProvider(func() *dhtpkg.Node { return node }))
	result := fileMutationRPCResult(t, server, "routing_table_get", map[string]any{}).(map[string]any)
	buckets := result["buckets"].(map[string]any)
	if result["node_id"] != hex.EncodeToString(nodeID[:]) ||
		result["prefix_neighbors_count"] != json.Number("1") || len(buckets) != dhtpkg.HashSize*8 {
		t.Fatalf("routing_table_get = %#v", result)
	}
	found := 0
	for _, value := range buckets {
		for _, item := range value.([]any) {
			peer := item.(map[string]any)
			if peer["address"] == "192.0.2.1" && peer["tcp_port"] == json.Number("5555") {
				found++
			}
		}
	}
	if found != 1 {
		t.Fatalf("routing peers = %#v", buckets)
	}
}

func TestPeerPingAndListValidatePeerInputs(t *testing.T) {
	node, err := dhtpkg.NewNode(0)
	if err != nil {
		t.Fatal(err)
	}
	server := CreateServer(WithDHTNodeProvider(func() *dhtpkg.Node { return node }))
	ping := fileMutationRPCResult(t, server, "peer_ping", map[string]any{
		"node_id": "bad", "address": "127.0.0.1", "port": 4444,
	}).(map[string]any)
	if ping["error"] != "peer not found" {
		t.Fatalf("peer_ping = %#v", ping)
	}
	response := dhtRPCEnvelope(t, server, "peer_list", map[string]any{
		"blob_hash": hex.EncodeToString(bytes.Repeat([]byte{1}, dhtpkg.HashSize-1)),
	})
	if response["error"] == nil {
		t.Fatalf("invalid peer_list = %#v", response)
	}
}

func dhtRPCEnvelope(t *testing.T, server *RPCServer, method string, params map[string]any) map[string]any {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	decoder := json.NewDecoder(response.Body)
	decoder.UseNumber()
	var envelope map[string]any
	if err := decoder.Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	return envelope
}
