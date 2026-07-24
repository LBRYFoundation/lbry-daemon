package trackerannouncer

import (
	"context"
	"encoding/binary"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"lbry/daemon/database"
)

type trackerStore struct {
	rows []database.ManagedFileRow
}

func (store *trackerStore) ListManagedFiles(context.Context) ([]database.ManagedFileRow, error) {
	return append([]database.ManagedFileRow(nil), store.rows...), nil
}

func TestManagerAnnouncesImmediatelyAndStops(t *testing.T) {
	connection, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	received := make(chan []byte, 1)
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		buffer := make([]byte, 1024)
		length, remote, readErr := connection.ReadFromUDP(buffer)
		if readErr != nil || length != 16 {
			return
		}
		transaction := binary.BigEndian.Uint32(buffer[12:16])
		connect := make([]byte, 16)
		binary.BigEndian.PutUint32(connect[4:8], transaction)
		binary.BigEndian.PutUint64(connect[8:16], 1234)
		if _, writeErr := connection.WriteToUDP(connect, remote); writeErr != nil {
			return
		}
		length, remote, readErr = connection.ReadFromUDP(buffer)
		if readErr != nil || length != 98 {
			return
		}
		request := append([]byte(nil), buffer[:length]...)
		received <- request
		transaction = binary.BigEndian.Uint32(request[12:16])
		response := make([]byte, 20)
		binary.BigEndian.PutUint32(response[0:4], 1)
		binary.BigEndian.PutUint32(response[4:8], transaction)
		_, _ = connection.WriteToUDP(response, remote)
	}()

	hash := strings.Repeat("01", 48)
	manager := New(&trackerStore{rows: []database.ManagedFileRow{{SDHash: hash}}},
		[]string{connection.LocalAddr().String()}, 4444, WithInterval(time.Hour))
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case request := <-received:
		if string(request[16:36]) != string([]byte(strings.Repeat("\x01", 20))) {
			t.Fatalf("announced hash prefix = %x", request[16:36])
		}
		if port := binary.BigEndian.Uint16(request[96:98]); port != 4444 {
			t.Fatalf("announced port = %d", port)
		}
	case <-time.After(time.Second):
		t.Fatal("tracker announcement was not received")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	<-serverDone
	if manager.Running() {
		t.Fatal("tracker announcer remained running")
	}
}

func TestManagerStartIsIdempotent(t *testing.T) {
	manager := New(&trackerStore{}, nil, 0, WithInterval(time.Hour))
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	firstDone := manager.done
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if manager.done != firstDone {
		t.Fatal("second start replaced the active lifecycle")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentStopIsSafe(t *testing.T) {
	manager := New(&trackerStore{}, nil, 0, WithInterval(time.Hour))
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := manager.Stop(ctx); err != nil {
				t.Errorf("Stop: %v", err)
			}
		}()
	}
	wait.Wait()
}
