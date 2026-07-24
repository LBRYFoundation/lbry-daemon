package dht

import (
	"bytes"
	"crypto/sha512"
	"encoding/hex"
	"net"
	"testing"
	"time"
)

func TestAnnounceBlobPublishesDiscoverableTCPPeer(t *testing.T) {
	publisher, err := NewNode(0)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := NewNode(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range []*Node{publisher, receiver} {
		node.BindIP = net.IPv4(127, 0, 0, 1)
		node.BootstrapNodes = nil
		if startErr := node.Start(); startErr != nil {
			t.Fatal(startErr)
		}
		defer node.Stop()
	}
	publisher.TCPPort = 3333
	publisher.ObservePeer(Peer{
		ID: receiver.ID, IP: net.IPv4(127, 0, 0, 1), UDPPort: receiver.UDPPort,
	})

	hash := sha512.Sum384([]byte("announced blob"))
	stored, err := publisher.AnnounceBlob(hex.EncodeToString(hash[:]))
	if err != nil || len(stored) != 1 || stored[0] != receiver.ID {
		t.Fatalf("AnnounceBlob = %x, %v", stored, err)
	}
	peers, _ := publisher.FindValue(hash)
	if len(peers) != 1 || peers[0].ID != publisher.ID || peers[0].TCPPort != 3333 {
		t.Fatalf("FindValue after announcement = %#v", peers)
	}

	receiver.mu.RLock()
	announced := receiver.announcements[hash][publisher.ID]
	receiver.mu.RUnlock()
	if announced.TCPPort != 3333 || !announced.IP.IsLoopback() {
		t.Fatalf("stored announcement = %#v", announced)
	}
}

func TestStoreRejectsInvalidTokenWithoutWaitingForTimeout(t *testing.T) {
	publisher, receiver := startDHTPair(t)
	publisher.TCPPort = 3333
	_, err := publisher.sendRPC(
		&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: receiver.UDPPort},
		METHOD_STORE, []any{make([]byte, HashSize), make([]byte, TokenSize), 3333, publisher.ID[:], 0},
	)
	if err == nil || err.Error() != "Invalid token" {
		t.Fatalf("store error = %v", err)
	}
}

func TestStoreUsesAuthenticatedEnvelopePublisher(t *testing.T) {
	actual := bytes.Repeat([]byte{1}, HashSize)
	forged := bytes.Repeat([]byte{2}, HashSize)
	message := DHTMessage{
		Payload: []byte(METHOD_STORE), NodeID: actual,
		Arguments: []any{make([]byte, HashSize), make([]byte, TokenSize), 3333, forged, 0},
	}
	arguments := authenticatedRequestArguments(message)
	if !bytes.Equal(toBytes(arguments[3]), actual) {
		t.Fatalf("publisher = %x, want authenticated %x", toBytes(arguments[3]), actual)
	}
	if !bytes.Equal(toBytes(message.Arguments[3]), forged) {
		t.Fatal("authentication rewrite mutated the decoded request")
	}
}

func TestStoredBlobHashesPreservesFirstAnnouncementOrderAndOwnership(t *testing.T) {
	node, err := NewNode(0)
	if err != nil {
		t.Fatal(err)
	}
	from := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4444}
	var first, second, publisher [HashSize]byte
	first[0], second[0], publisher[0] = 1, 2, 3
	store := func(hash [HashSize]byte) {
		t.Helper()
		_, _, storeErr := node.handleStore(from, []any{
			hash[:], node.makeToken(from.IP), 3333, publisher[:], 0,
		})
		if storeErr != nil {
			t.Fatal(storeErr)
		}
	}
	store(first)
	store(second)
	store(first)

	hashes := node.StoredBlobHashes()
	if len(hashes) != 2 || hashes[0] != first || hashes[1] != second {
		t.Fatalf("stored hashes = %x", hashes)
	}
	hashes[0] = [HashSize]byte{}
	again := node.StoredBlobHashes()
	if len(again) != 2 || again[0] != first {
		t.Fatalf("snapshot aliases node state: %x", again)
	}
	if node.NodeID() != node.ID {
		t.Fatal("NodeID does not return the configured identity")
	}
}

func TestAnnouncementsExpireAfterPinnedRetentionWindow(t *testing.T) {
	node, err := NewNode(0)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	node.now = func() time.Time { return now }
	from := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4444}
	var hash, publisher [HashSize]byte
	hash[0], publisher[0] = 4, 5
	store := func() {
		t.Helper()
		_, _, storeErr := node.handleStore(from, []any{
			hash[:], node.makeToken(from.IP), 3333, publisher[:], 0,
		})
		if storeErr != nil {
			t.Fatal(storeErr)
		}
	}
	store()
	now = now.Add(AnnouncementExpiration)
	node.expireAnnouncements(now)
	if hashes := node.StoredBlobHashes(); len(hashes) != 1 || hashes[0] != hash {
		t.Fatalf("announcement expired at exact boundary: %x", hashes)
	}
	response, _, err := node.handleFindValue(from, []any{hash[:]})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := response.(map[string]any)[string(hash[:])]; exists {
		t.Fatal("exact-boundary announcement was still served")
	}

	store()
	now = now.Add(AnnouncementExpiration + time.Nanosecond)
	node.expireAnnouncements(now)
	if hashes := node.StoredBlobHashes(); len(hashes) != 0 {
		t.Fatalf("expired announcement remained: %x", hashes)
	}
	store()
	if hashes := node.StoredBlobHashes(); len(hashes) != 1 || hashes[0] != hash {
		t.Fatalf("re-announced hash order/state = %x", hashes)
	}
}

func startDHTPair(t *testing.T) (*Node, *Node) {
	t.Helper()
	first, _ := NewNode(0)
	second, _ := NewNode(0)
	for _, node := range []*Node{first, second} {
		node.BindIP = net.IPv4(127, 0, 0, 1)
		node.BootstrapNodes = nil
		if err := node.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(node.Stop)
	}
	return first, second
}
