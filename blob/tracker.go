package blob

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

const trackerProtocolID uint64 = 0x41727101980

func queryUDPTracker(ctx context.Context, address string, hash [48]byte, announcePort int) ([]discoveredPeer, error) {
	remote, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return nil, fmt.Errorf("resolve tracker %s: %w", address, err)
	}
	connection, err := net.DialUDP("udp", nil, remote)
	if err != nil {
		return nil, fmt.Errorf("connect tracker %s: %w", address, err)
	}
	defer connection.Close()
	stopCancellation := context.AfterFunc(ctx, func() {
		_ = connection.SetDeadline(time.Now())
	})
	defer stopCancellation()
	deadline := time.Now().Add(10 * time.Second)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return nil, err
	}

	connectTransaction, err := randomUint32()
	if err != nil {
		return nil, err
	}
	connectRequest := make([]byte, 16)
	binary.BigEndian.PutUint64(connectRequest[0:8], trackerProtocolID)
	binary.BigEndian.PutUint32(connectRequest[8:12], 0)
	binary.BigEndian.PutUint32(connectRequest[12:16], connectTransaction)
	connectResponse, err := trackerRoundTrip(ctx, connection, connectRequest)
	if err != nil {
		return nil, fmt.Errorf("tracker %s connect: %w", address, err)
	}
	if len(connectResponse) < 16 || binary.BigEndian.Uint32(connectResponse[0:4]) != 0 ||
		binary.BigEndian.Uint32(connectResponse[4:8]) != connectTransaction {
		return nil, fmt.Errorf("tracker %s returned an invalid connect response", address)
	}
	connectionID := binary.BigEndian.Uint64(connectResponse[8:16])

	announceTransaction, err := randomUint32()
	if err != nil {
		return nil, err
	}
	key, err := randomUint32()
	if err != nil {
		return nil, err
	}
	peerID := make([]byte, 20)
	if _, err := io.ReadFull(rand.Reader, peerID); err != nil {
		return nil, err
	}
	copy(peerID, []byte("LB-0-113-0-"))
	request := make([]byte, 98)
	binary.BigEndian.PutUint64(request[0:8], connectionID)
	binary.BigEndian.PutUint32(request[8:12], 1)
	binary.BigEndian.PutUint32(request[12:16], announceTransaction)
	copy(request[16:36], hash[:20])
	copy(request[36:56], peerID)
	binary.BigEndian.PutUint32(request[80:84], 1)
	binary.BigEndian.PutUint32(request[88:92], key)
	binary.BigEndian.PutUint32(request[92:96], ^uint32(0))
	if announcePort > 0 && announcePort <= 65535 {
		binary.BigEndian.PutUint16(request[96:98], uint16(announcePort))
	}
	response, err := trackerRoundTrip(ctx, connection, request)
	if err != nil {
		return nil, fmt.Errorf("tracker %s announce: %w", address, err)
	}
	if len(response) < 20 || binary.BigEndian.Uint32(response[0:4]) != 1 ||
		binary.BigEndian.Uint32(response[4:8]) != announceTransaction {
		return nil, fmt.Errorf("tracker %s returned an invalid announce response", address)
	}
	if (len(response)-20)%6 != 0 {
		return nil, fmt.Errorf("tracker %s returned malformed compact peers", address)
	}
	peers := make([]discoveredPeer, 0, (len(response)-20)/6)
	for offset := 20; offset < len(response); offset += 6 {
		peers = append(peers, discoveredPeer{
			ip:   net.IPv4(response[offset], response[offset+1], response[offset+2], response[offset+3]),
			port: int(binary.BigEndian.Uint16(response[offset+4 : offset+6])),
		})
	}
	return peers, nil
}

// AnnounceUDPTracker announces a stream descriptor hash to one legacy UDP
// tracker. The tracker protocol truncates the SHA-384 hash to its 20-byte
// info-hash field, matching the pinned Python SDK's struct packing.
func AnnounceUDPTracker(ctx context.Context, address, blobHash string, announcePort int) error {
	hash, err := hexToHash(blobHash)
	if err != nil {
		return err
	}
	_, err = queryUDPTracker(ctx, address, hash, announcePort)
	return err
}

func trackerRoundTrip(ctx context.Context, connection *net.UDPConn, request []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := connection.Write(request); err != nil {
		return nil, err
	}
	response := make([]byte, 64*1024)
	length, err := connection.Read(response)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	if length >= 8 && binary.BigEndian.Uint32(response[0:4]) == 3 {
		return nil, errors.New(string(response[8:length]))
	}
	return response[:length], nil
}

func randomUint32() (uint32, error) {
	var data [4]byte
	if _, err := io.ReadFull(rand.Reader, data[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(data[:]), nil
}
