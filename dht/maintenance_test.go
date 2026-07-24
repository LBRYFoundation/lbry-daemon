package dht

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestHandleMessageRejectsMalformedEnvelopesWithoutPanicking(t *testing.T) {
	node, err := NewNode(0)
	if err != nil {
		t.Fatal(err)
	}
	from := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4444}
	var remoteID [HashSize]byte
	remoteID[0] = 1
	rpcID := make([]byte, RPCIDSize)

	cases := []map[int]any{
		{0: TYPE_REQUEST, 1: rpcID, 2: remoteID[:]},
		{0: TYPE_REQUEST, 1: rpcID[:RPCIDSize-1], 2: remoteID[:], 3: []byte(METHOD_PING)},
		{0: TYPE_REQUEST, 1: rpcID, 2: remoteID[:HashSize-1], 3: []byte(METHOD_PING)},
		{0: 9, 1: rpcID, 2: remoteID[:], 3: []byte(METHOD_PING)},
	}
	for _, envelope := range cases {
		encoded, encodeErr := BencodeEncode(envelope)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		node.handleMessage(encoded, from)
	}
	valid, err := BencodeEncode(map[int]any{
		0: TYPE_REQUEST, 1: rpcID, 2: remoteID[:], 3: []byte(METHOD_PING),
	})
	if err != nil {
		t.Fatal(err)
	}
	node.handleMessage(append(valid, 'x'), from)
	if node.RoutingPeerCount() != 0 {
		t.Fatal("unverified request sender entered routing table")
	}
}

func TestPendingResponseRequiresExpectedSource(t *testing.T) {
	node, err := NewNode(0)
	if err != nil {
		t.Fatal(err)
	}
	rpcID := make([]byte, RPCIDSize)
	var remoteID [HashSize]byte
	remoteID[0] = 2
	expected := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4444}
	responses := make(chan map[string]any, 1)
	node.pending[string(rpcID)] = pendingRPC{response: responses, address: expected}
	encoded, err := BencodeEncode(map[int]any{
		0: TYPE_RESPONSE, 1: rpcID, 2: remoteID[:], 3: []byte("pong"),
	})
	if err != nil {
		t.Fatal(err)
	}
	node.handleMessage(encoded, &net.UDPAddr{IP: expected.IP, Port: expected.Port + 1})
	select {
	case <-responses:
		t.Fatal("accepted response from unexpected UDP endpoint")
	default:
	}
	if node.RoutingPeerCount() != 0 {
		t.Fatal("unexpected response source entered routing table")
	}
	node.handleMessage(encoded, expected)
	select {
	case <-responses:
	case <-time.After(time.Second):
		t.Fatal("expected response was not delivered")
	}
	if node.RoutingPeerCount() != 1 {
		t.Fatalf("routing peers = %d, want 1", node.RoutingPeerCount())
	}
}

func TestRoutingTableMaintainsLRUAndPrunesRepeatedFailures(t *testing.T) {
	var self [HashSize]byte
	routing := NewRoutingTable(self)
	peers := make([]Peer, K+1)
	for index := range peers {
		peers[index].ID[0] = 0x80
		peers[index].ID[HashSize-1] = byte(index + 1)
		peers[index].IP = net.IPv4(192, 0, 2, byte(index+1))
		peers[index].UDPPort = 4000 + index
	}
	for index := 0; index < K; index++ {
		if !routing.AddPeer(peers[index]) {
			t.Fatalf("failed adding peer %d", index)
		}
	}
	routing.AddPeer(peers[0])
	bucket := routing.BucketsSnapshot()[0]
	if bucket[len(bucket)-1].ID != peers[0].ID {
		t.Fatal("refreshed peer did not move to the LRU tail")
	}
	if routing.AddPeer(peers[K]) {
		t.Fatal("full bucket accepted an unverified replacement")
	}
	if routing.MarkFailure(peers[1].ID) {
		t.Fatal("single failure removed a questionable peer")
	}
	if !routing.MarkFailure(peers[1].ID) {
		t.Fatal("second consecutive failure did not remove bad peer")
	}
	if !routing.AddPeer(peers[K]) || routing.PeerCount() != K {
		t.Fatalf("replacement failed, peer count = %d", routing.PeerCount())
	}
}

func TestFindValueResponseUsesStableProviderPages(t *testing.T) {
	node, err := NewNode(0)
	if err != nil {
		t.Fatal(err)
	}
	var hash [HashSize]byte
	hash[0] = 7
	now := time.Unix(1_700_000_000, 0)
	node.now = func() time.Time { return now }
	node.announcements[hash] = make(map[[HashSize]byte]Peer)
	node.announcementTimes[hash] = make(map[[HashSize]byte]time.Time)
	for index := 0; index < 20; index++ {
		var publisher [HashSize]byte
		publisher[0], publisher[HashSize-1] = 9, byte(index)
		node.announcements[hash][publisher] = Peer{
			ID: publisher, IP: net.IPv4(198, 51, 100, byte(index+1)), TCPPort: 5000 + index,
		}
		node.announcementTimes[hash][publisher] = now
	}
	from := &net.UDPAddr{IP: net.IPv4(203, 0, 113, 1), Port: 4444}
	wantCounts := []int{8, 8, 4, 0}
	for page, wantCount := range wantCounts {
		result, _, callErr := node.handleFindValue(from, []any{hash[:], map[string]any{"p": int64(page)}})
		if callErr != nil {
			t.Fatal(callErr)
		}
		payload := result.(map[string]any)
		providers, _ := payload[string(hash[:])].([]any)
		if len(providers) != wantCount {
			t.Fatalf("page %d providers = %d, want %d", page, len(providers), wantCount)
		}
		if got := toBencInt(payload["p"]); got != 3 {
			t.Fatalf("page count = %d, want 3", got)
		}
		_, hasContacts := payload["contacts"]
		if hasContacts != (page == 0) {
			t.Fatalf("page %d contacts present = %t", page, hasContacts)
		}
		encoded, encodeErr := BencodeEncode(map[int]any{
			0: TYPE_RESPONSE, 1: make([]byte, RPCIDSize), 2: node.ID[:], 3: payload,
		})
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		if len(encoded) > MsgSizeLimit {
			t.Fatalf("page %d datagram = %d bytes", page, len(encoded))
		}
	}
}

func TestBootstrapLoopReseedsAfterRoutingTableEmpties(t *testing.T) {
	node, err := NewNode(0)
	if err != nil {
		t.Fatal(err)
	}
	node.bootstrapInterval = 5 * time.Millisecond
	var peerID [HashSize]byte
	peerID[0] = 3
	peer := Peer{ID: peerID, IP: net.IPv4(192, 0, 2, 10), UDPPort: 4444}
	var calls atomic.Int32
	node.bootstrapFn = func() error {
		calls.Add(1)
		node.routing.AddPeer(peer)
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	node.loopWG.Add(1)
	go node.bootstrapLoop(ctx)
	waitForDHTCondition(t, func() bool { return calls.Load() >= 1 && node.RoutingPeerCount() == 1 })
	node.routing.RemovePeer(peerID)
	waitForDHTCondition(t, func() bool { return calls.Load() >= 2 && node.RoutingPeerCount() == 1 })
	cancel()
	node.loopWG.Wait()
}

func TestFindValueTraversesAllProviderPages(t *testing.T) {
	requester, provider := startDHTPair(t)
	address := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: provider.UDPPort}
	if err := requester.Ping(address); err != nil {
		t.Fatal(err)
	}
	var hash [HashSize]byte
	hash[0] = 11
	provider.mu.Lock()
	provider.announcements[hash] = make(map[[HashSize]byte]Peer)
	provider.announcementTimes[hash] = make(map[[HashSize]byte]time.Time)
	for index := 0; index < 20; index++ {
		var publisher [HashSize]byte
		publisher[0], publisher[HashSize-1] = 12, byte(index)
		provider.announcements[hash][publisher] = Peer{
			ID: publisher, IP: net.IPv4(127, 0, 0, 1), TCPPort: 6000 + index,
		}
		provider.announcementTimes[hash][publisher] = provider.now()
	}
	provider.mu.Unlock()
	peers, _ := requester.FindValue(hash)
	if len(peers) != 20 {
		t.Fatalf("discovered providers = %d, want 20", len(peers))
	}
}

func waitForDHTCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}
