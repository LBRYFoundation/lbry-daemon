package rpc

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"

	dhtpkg "lbry/daemon/dht"
)

func (rpcServer *RPCServer) handlePeerPing(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	node := rpcServer.dhtNodeProvider()
	if node == nil {
		panic(errors.New("DHT node is unavailable"))
	}
	nodeID, _ := normalized.named["node_id"].(string)
	decoded, err := hex.DecodeString(nodeID)
	address, _ := normalized.named["address"].(string)
	port, portErr := strconv.Atoi(fmt.Sprint(normalized.named["port"]))
	if err != nil || len(decoded) != dhtpkg.HashSize || address == "" || portErr != nil || port == 0 {
		sendResultResponse(response, map[string]any{"error": "peer not found"})
		return
	}
	if err := node.Ping(&net.UDPAddr{IP: net.ParseIP(address), Port: port}); err != nil {
		if timeout, ok := err.(net.Error); ok && timeout.Timeout() || err.Error() == "rpc timeout" {
			sendResultResponse(response, map[string]any{"error": "timeout"})
			return
		}
		panic(err)
	}
	sendResultResponse(response, "pong")
}

func (rpcServer *RPCServer) handlePeerList(response http.ResponseWriter, params any) {
	normalized := params.(normalizedRPCParams)
	node := rpcServer.dhtNodeProvider()
	if node == nil {
		panic(errors.New("DHT node is unavailable"))
	}
	blobHash, _ := normalized.named["blob_hash"].(string)
	decoded, err := hex.DecodeString(blobHash)
	if err != nil || len(decoded) != dhtpkg.HashSize {
		panic(errors.New("invalid blob hash"))
	}
	var key [dhtpkg.HashSize]byte
	copy(key[:], decoded)
	peers, _ := node.FindValue(key)
	deduplicated := make(map[string]dhtpkg.Peer)
	order := make([]string, 0, len(peers))
	for _, peer := range peers {
		identity := peer.IP.String() + ":" + strconv.Itoa(peer.TCPPort)
		if _, exists := deduplicated[identity]; !exists {
			order = append(order, identity)
		}
		deduplicated[identity] = peer
	}
	items := make([]any, len(order))
	for index, identity := range order {
		items[index] = dhtPeerJSON(deduplicated[identity])
	}
	page := walletListPositiveInteger(normalized.named["page"], 1)
	pageSize := walletListPositiveInteger(normalized.named["page_size"], 20)
	total, start := len(items), pageSize*(page-1)
	end := min(start+pageSize, total)
	paged := make([]any, 0)
	if start <= total {
		paged = append(paged, items[start:end]...)
	}
	sendResultResponse(response, map[string]any{
		"items": paged, "total_pages": (total + pageSize - 1) / pageSize,
		"total_items": total, "page": page, "page_size": pageSize,
	})
}

func (rpcServer *RPCServer) handleRoutingTableGet(response http.ResponseWriter, _ any) {
	node := rpcServer.dhtNodeProvider()
	if node == nil {
		panic(errors.New("DHT node is unavailable"))
	}
	buckets := make(map[int]any)
	prefixNeighbors := 0
	for index, peers := range node.RoutingBucketsSnapshot() {
		encoded := make([]any, len(peers))
		for peerIndex, peer := range peers {
			encoded[peerIndex] = dhtPeerJSON(peer)
			if peer.ID[0] == node.ID[0] {
				prefixNeighbors++
			}
		}
		buckets[index] = encoded
	}
	sendResultResponse(response, map[string]any{
		"buckets": buckets, "prefix_neighbors_count": prefixNeighbors,
		"node_id": node.NodeIDHex(),
	})
}

func dhtPeerJSON(peer dhtpkg.Peer) map[string]any {
	var nodeID any
	zero := [dhtpkg.HashSize]byte{}
	if peer.ID != zero {
		nodeID = hex.EncodeToString(peer.ID[:])
	}
	return map[string]any{
		"node_id": nodeID, "address": peer.IP.String(),
		"udp_port": peer.UDPPort, "tcp_port": peer.TCPPort,
	}
}
