package blob

import (
	"context"
	"crypto/sha512"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"lbry/daemon/dht"
)

func TestNetworkFetcherUsesFixedPeer(t *testing.T) {
	data := []byte("fixed peer blob")
	digest := sha512.Sum384(data)
	blobHash := hex.EncodeToString(digest[:])
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverErr := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverErr <- acceptErr
			return
		}
		defer connection.Close()
		var request BlobRequest
		if decodeErr := json.NewDecoder(connection).Decode(&request); decodeErr != nil {
			serverErr <- decodeErr
			return
		}
		if request.RequestedBlob != blobHash {
			serverErr <- fmt.Errorf("requested blob = %q", request.RequestedBlob)
			return
		}
		header, _ := json.Marshal(BlobResponse{IncomingBlob: &IncomingBlob{BlobHash: blobHash, Length: len(data)}})
		_, writeErr := connection.Write(append(header, data...))
		serverErr <- writeErr
	}()

	manager := NewManager()
	manager.SetFetcher(NetworkFetcher(NetworkFetcherOptions{FixedPeers: []string{listener.Addr().String()}}))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Ensure(ctx, blobHash); err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestQueryUDPTrackerUsesPinnedWireFormat(t *testing.T) {
	connection, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	var hash [48]byte
	for index := range hash {
		hash[index] = byte(index + 1)
	}
	serverErr := make(chan error, 1)
	go func() {
		buffer := make([]byte, 1024)
		length, client, readErr := connection.ReadFromUDP(buffer)
		if readErr != nil {
			serverErr <- readErr
			return
		}
		if length != 16 || binary.BigEndian.Uint64(buffer[:8]) != trackerProtocolID {
			serverErr <- fmt.Errorf("invalid connect request: %x", buffer[:length])
			return
		}
		transaction := binary.BigEndian.Uint32(buffer[12:16])
		response := make([]byte, 16)
		binary.BigEndian.PutUint32(response[4:8], transaction)
		binary.BigEndian.PutUint64(response[8:16], 77)
		if _, writeErr := connection.WriteToUDP(response, client); writeErr != nil {
			serverErr <- writeErr
			return
		}
		length, client, readErr = connection.ReadFromUDP(buffer)
		if readErr != nil {
			serverErr <- readErr
			return
		}
		if length != 98 || string(buffer[16:36]) != string(hash[:20]) || binary.BigEndian.Uint32(buffer[80:84]) != 1 {
			serverErr <- fmt.Errorf("invalid announce request: %x", buffer[:length])
			return
		}
		transaction = binary.BigEndian.Uint32(buffer[12:16])
		response = make([]byte, 26)
		binary.BigEndian.PutUint32(response[0:4], 1)
		binary.BigEndian.PutUint32(response[4:8], transaction)
		binary.BigEndian.PutUint32(response[8:12], 60)
		copy(response[20:24], net.IPv4(127, 0, 0, 9).To4())
		binary.BigEndian.PutUint16(response[24:26], 5567)
		_, writeErr := connection.WriteToUDP(response, client)
		serverErr <- writeErr
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	peers, err := queryUDPTracker(ctx, connection.LocalAddr().String(), hash, 4444)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 || !peers[0].ip.Equal(net.IPv4(127, 0, 0, 9)) || peers[0].port != 5567 {
		t.Fatalf("tracker peers = %+v", peers)
	}
}

func TestDiscoverFixedPeersHonorsCancellationDuringDelay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := make(chan peerBatch, 1)
	discoverFixedPeers(ctx, []string{"127.0.0.1:5567"}, time.Hour, result)
	if batch := <-result; batch.err != context.Canceled {
		t.Fatalf("error = %v, want context canceled", batch.err)
	}
}

func TestDownloadBlobContextCancelsBlockedResponse(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	requestReceived := make(chan struct{})
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		var request BlobRequest
		if json.NewDecoder(connection).Decode(&request) == nil {
			close(requestReceived)
		}
		buffer := make([]byte, 1)
		_, _ = connection.Read(buffer)
	}()

	address := listener.Addr().(*net.TCPAddr)
	ctx, cancel := context.WithCancel(context.Background())
	downloadDone := make(chan error, 1)
	go func() {
		_, downloadErr := DownloadBlobContext(ctx, address.IP, address.Port, strings.Repeat("0", BlobHashLength))
		downloadDone <- downloadErr
	}()
	select {
	case <-requestReceived:
	case <-time.After(time.Second):
		t.Fatal("peer did not receive blob request")
	}
	started := time.Now()
	cancel()
	select {
	case err = <-downloadDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("download error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled download did not return")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("canceled download returned after %s", elapsed)
	}
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("canceled connection did not unblock peer")
	}
}

func TestNetworkFetcherSkipsFixedPeerDelayWithoutDHTCandidates(t *testing.T) {
	emptyNode, err := dht.NewNode(0)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name         string
		nodeProvider func() *dht.Node
	}{
		{name: "DHT not configured"},
		{name: "DHT unavailable", nodeProvider: func() *dht.Node { return nil }},
		{name: "DHT routing table empty", nodeProvider: func() *dht.Node { return emptyNode }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := []byte("immediate fixed peer blob")
			digest := sha512.Sum384(data)
			blobHash := hex.EncodeToString(digest[:])
			listener, serverErr := startSingleBlobPeer(t, blobHash, data)
			defer listener.Close()

			fetch := NetworkFetcher(NetworkFetcherOptions{
				NodeProvider:   test.nodeProvider,
				FixedPeers:     []string{listener.Addr().String()},
				FixedPeerDelay: time.Hour,
			})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			downloaded, fetchErr := fetch(ctx, blobHash)
			if fetchErr != nil {
				t.Fatal(fetchErr)
			}
			if string(downloaded) != string(data) {
				t.Fatalf("downloaded blob = %q, want %q", downloaded, data)
			}
			if serveErr := <-serverErr; serveErr != nil {
				t.Fatal(serveErr)
			}
		})
	}
}

func startSingleBlobPeer(t *testing.T, blobHash string, data []byte) (net.Listener, <-chan error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverErr := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverErr <- acceptErr
			return
		}
		defer connection.Close()
		var request BlobRequest
		if decodeErr := json.NewDecoder(connection).Decode(&request); decodeErr != nil {
			serverErr <- decodeErr
			return
		}
		if request.RequestedBlob != blobHash {
			serverErr <- fmt.Errorf("requested blob = %q", request.RequestedBlob)
			return
		}
		header, _ := json.Marshal(BlobResponse{IncomingBlob: &IncomingBlob{BlobHash: blobHash, Length: len(data)}})
		_, writeErr := connection.Write(append(header, data...))
		serverErr <- writeErr
	}()
	return listener, serverErr
}
