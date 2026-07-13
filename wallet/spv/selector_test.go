package spv

import (
	"context"
	"encoding/hex"
	"errors"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestSPVStatusPacketCompatibilityVectors(t *testing.T) {
	ping := MakePing()
	wantPing := "5631193301" + repeatHex("00", 64)
	if len(ping) != SPVPingSize || hex.EncodeToString(ping) != wantPing {
		t.Fatalf("ping = %x, want %s", ping, wantPing)
	}
	permissivePing := append([]byte(nil), ping...)
	permissivePing[4] = 255
	permissivePing[5] = 1
	permissivePing = append(permissivePing, 9, 9)
	decodedPing, err := DecodePing(permissivePing)
	if err != nil || decodedPing.Version != 255 || decodedPing.Padding[0] != 1 {
		t.Fatalf("permissive ping = %#v, %v", decodedPing, err)
	}
	if _, err := DecodePing(ping[:68]); !errors.Is(err, ErrInvalidSPVPing) {
		t.Fatalf("short ping error = %v", err)
	}
	badMagic := append([]byte(nil), ping...)
	badMagic[0] = 0
	if _, err := DecodePing(badMagic); !errors.Is(err, ErrInvalidSPVPing) {
		t.Fatalf("bad magic error = %v", err)
	}

	var tip [32]byte
	for index := range tip {
		tip[index] = byte(index)
	}
	pong := Pong{
		ProtocolVersion: 1,
		Flags:           1,
		Height:          123456,
		Tip:             tip,
		SourceAddress:   [4]byte{203, 0, 113, 7},
		Country:         236,
	}
	wantPong := "01010001e240000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1fcb00710700ec"
	if encoded := hex.EncodeToString(pong.Encode()); encoded != wantPong {
		t.Fatalf("pong = %s, want %s", encoded, wantPong)
	}
	decodedPong, err := DecodePong(append(pong.Encode(), 1, 2, 3))
	if err != nil || decodedPong != pong || !decodedPong.Available() || decodedPong.SourceIP() != "203.0.113.7" {
		t.Fatalf("decoded pong = %#v, %v", decodedPong, err)
	}
	if _, err := DecodePong(pong.Encode()[:43]); !errors.Is(err, ErrInvalidSPVPong) {
		t.Fatalf("short pong error = %v", err)
	}
	decodedPong.ProtocolVersion = 255
	decodedPong.Flags = 2
	if decodedPong.Available() {
		t.Fatal("flags 2 was treated as available")
	}
	decodedPong.Flags = 3
	if !decodedPong.Available() {
		t.Fatal("flags 3 was treated as unavailable")
	}
	for name, code := range map[string]uint16{"FR": 76, "KP": 118, "US": 236, "001": 250} {
		if got, err := CountryName(code); err != nil || got != name {
			t.Fatalf("country %d = %q, %v; want %q", code, got, err, name)
		}
		if got, err := CountryCode(name); err != nil || got != code {
			t.Fatalf("country %q = %d, %v; want %d", name, got, err, code)
		}
	}
	if name, err := CountryName(181); err != nil || name != "E" {
		t.Fatalf("legacy RE country name = %q, %v", name, err)
	}
	if _, err := CountryName(65535); !errors.Is(err, ErrUnknownCountry) {
		t.Fatalf("unknown country error = %v", err)
	}
}

func TestUDPSelectorUsesResponseOrderAndFirstResolvedAlias(t *testing.T) {
	resolver := &scriptedResolver{responses: map[Server]resolverResponse{
		{Host: "slow-alias", Port: 1}: {ip: net.IPv4(192, 0, 2, 1), delay: 20 * time.Millisecond},
		{Host: "fast-alias", Port: 1}: {ip: net.IPv4(192, 0, 2, 1)},
		{Host: "other", Port: 2}:      {ip: net.IPv4(192, 0, 2, 2), delay: 5 * time.Millisecond},
	}}
	connection := newScriptedPacketConn(8)
	connection.queuePong(t, "192.0.2.2", 2, testPong(t, true, 20, "FR"))
	connection.queuePong(t, "192.0.2.1", 1, testPong(t, true, 10, "US"))
	known := NewMemoryKnownHubs()
	selector := &UDPSelector{
		Timeout:  time.Second,
		Resolver: resolver,
		Listen: func(context.Context) (net.PacketConn, error) {
			return connection, nil
		},
	}
	candidates, err := selector.Select(context.Background(), SelectionRequest{
		Servers: []Server{
			{Host: "slow-alias", Port: 1},
			{Host: "fast-alias", Port: 1},
			{Host: "other", Port: 2},
		},
		KnownHubs: known,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []Server{{Host: "other", Port: 2}, {Host: "fast-alias", Port: 1}}
	if got := candidateServers(candidates); !reflect.DeepEqual(got, want) {
		t.Fatalf("response-order candidates = %#v, want %#v", got, want)
	}
	if writes := connection.Writes(); len(writes) != 2 ||
		hex.EncodeToString(writes[0].packet) != hex.EncodeToString(MakePing()) ||
		hex.EncodeToString(writes[1].packet) != hex.EncodeToString(MakePing()) {
		t.Fatalf("UDP writes = %#v", writes)
	}
	if snapshot := known.Snapshot(); len(snapshot) != 2 ||
		snapshot[0].Server.Host != "other" || snapshot[0].Details["country"] != "FR" ||
		snapshot[1].Server.Host != "fast-alias" || snapshot[1].Details["country"] != "US" {
		t.Fatalf("probe known hubs = %#v", snapshot)
	}
}

func TestUDPSelectorUnavailablePongWaitsAndJurisdictionDoesNotFallback(t *testing.T) {
	resolver := &scriptedResolver{responses: map[Server]resolverResponse{
		{Host: "unavailable", Port: 1}: {ip: net.IPv4(198, 51, 100, 1)},
		{Host: "available", Port: 2}:   {ip: net.IPv4(198, 51, 100, 2)},
	}}
	connection := newScriptedPacketConn(4)
	connection.queuePong(t, "198.51.100.1", 1, testPong(t, false, 10, "KP"))
	connection.queuePong(t, "198.51.100.2", 2, testPong(t, true, 20, "US"))
	known := NewMemoryKnownHubs()
	jurisdiction := "KP"
	selector := &UDPSelector{
		Timeout:  35 * time.Millisecond,
		Resolver: resolver,
		Listen: func(context.Context) (net.PacketConn, error) {
			return connection, nil
		},
		RandomIndex: func(int) int {
			t.Fatal("available wrong-jurisdiction pong triggered random fallback")
			return 0
		},
	}
	started := time.Now()
	candidates, err := selector.Select(context.Background(), SelectionRequest{
		Servers:   []Server{{Host: "unavailable", Port: 1}, {Host: "available", Port: 2}},
		KnownHubs: known, Jurisdiction: &jurisdiction,
	})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 25*time.Millisecond {
		t.Fatalf("unavailable pong returned before overall timeout: %s", elapsed)
	}
	if len(candidates) != 0 {
		t.Fatalf("wrong-jurisdiction candidates = %#v", candidates)
	}
	snapshot := known.Snapshot()
	if len(snapshot) != 2 || snapshot[0].Details["country"] != "KP" || snapshot[1].Details["country"] != "US" {
		t.Fatalf("unavailable/available metadata = %#v", snapshot)
	}
}

func TestUDPSelectorNoPongUsesNumericFallbackAndBypassesJurisdiction(t *testing.T) {
	resolver := &scriptedResolver{responses: map[Server]resolverResponse{
		{Host: "first", Port: 1}:  {ip: net.IPv4(203, 0, 113, 1)},
		{Host: "second", Port: 2}: {ip: net.IPv4(203, 0, 113, 2), delay: time.Millisecond},
	}}
	connection := newScriptedPacketConn(2)
	known := NewMemoryKnownHubs()
	jurisdiction := "KP"
	selector := &UDPSelector{
		Timeout:  10 * time.Millisecond,
		Resolver: resolver,
		Listen: func(context.Context) (net.PacketConn, error) {
			return connection, nil
		},
		RandomIndex: func(length int) int {
			if length != 2 {
				t.Fatalf("fallback population = %d, want 2", length)
			}
			return 1
		},
	}
	candidates, err := selector.Select(context.Background(), SelectionRequest{
		Servers:   []Server{{Host: "first", Port: 1}, {Host: "second", Port: 2}},
		KnownHubs: known, Jurisdiction: &jurisdiction,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []Candidate{{Server: Server{Host: "203.0.113.2", Port: 2}}}
	if !reflect.DeepEqual(candidates, want) {
		t.Fatalf("fallback candidates = %#v, want %#v", candidates, want)
	}
	if snapshot := known.Snapshot(); len(snapshot) != 1 || snapshot[0].Server != want[0].Server {
		t.Fatalf("fallback known hubs = %#v", snapshot)
	}
}

func TestUDPSelectorIgnoresUnknownAndInvalidDatagrams(t *testing.T) {
	server := Server{Host: "expected", Port: 1}
	resolver := &scriptedResolver{responses: map[Server]resolverResponse{
		server: {ip: net.IPv4(192, 0, 2, 1)},
	}}
	connection := newScriptedPacketConn(4)
	connection.queuePong(t, "192.0.2.99", 9, testPong(t, true, 1, "US"))
	connection.queueRead([]byte{1, 2}, &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 1})
	invalidCountry := testPong(t, true, 1, "US")
	invalidCountry.Country = 65535
	connection.queuePong(t, "192.0.2.1", 1, invalidCountry)
	selector := &UDPSelector{
		Timeout: 10 * time.Millisecond, Resolver: resolver,
		Listen:      func(context.Context) (net.PacketConn, error) { return connection, nil },
		RandomIndex: func(int) int { return 0 },
	}
	candidates, err := selector.Select(context.Background(), SelectionRequest{Servers: []Server{server}})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Server.Host != "192.0.2.1" || candidates[0].Pong != nil {
		t.Fatalf("safe malformed fallback = %#v", candidates)
	}
}

func TestUDPSelectorAllDNSFailuresReturnsEmptyAfterBinding(t *testing.T) {
	server := Server{Host: "missing", Port: 1}
	connection := newScriptedPacketConn(0)
	binds := 0
	selector := &UDPSelector{
		Resolver: &scriptedResolver{responses: map[Server]resolverResponse{server: {err: errors.New("DNS failure")}}},
		Listen: func(context.Context) (net.PacketConn, error) {
			binds++
			return connection, nil
		},
	}
	candidates, err := selector.Select(context.Background(), SelectionRequest{Servers: []Server{server}})
	if err != nil || len(candidates) != 0 || binds != 1 || len(connection.Writes()) != 0 {
		t.Fatalf("failed DNS candidates = %#v, %v; binds %d", candidates, err, binds)
	}
}

func TestUDPSelectorReturnsCancellationAfterDNS(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	selector := &UDPSelector{
		Resolver: resolverFunc(func(context.Context, Server) (net.IP, error) {
			cancel()
			return net.IPv4(192, 0, 2, 1), nil
		}),
		Listen: func(context.Context) (net.PacketConn, error) {
			t.Fatal("canceled selection opened a UDP socket")
			return nil, nil
		},
	}
	_, err := selector.Select(ctx, SelectionRequest{Servers: []Server{{Host: "hub", Port: 1}}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled DNS selection error = %v", err)
	}
}

func TestUDPSelectorCancellationWinsOverSocketErrors(t *testing.T) {
	server := Server{Host: "hub", Port: 1}
	resolver := &scriptedResolver{responses: map[Server]resolverResponse{
		server: {ip: net.IPv4(192, 0, 2, 1)},
	}}
	t.Run("listen", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		selector := &UDPSelector{
			Resolver: resolver,
			Listen: func(context.Context) (net.PacketConn, error) {
				cancel()
				return nil, net.ErrClosed
			},
		}
		_, err := selector.Select(ctx, SelectionRequest{Servers: []Server{server}})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled listen error = %v", err)
		}
	})
	t.Run("write", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		connection := &cancelingWritePacketConn{
			scriptedPacketConn: newScriptedPacketConn(0), cancel: cancel,
		}
		selector := &UDPSelector{
			Resolver: resolver,
			Listen:   func(context.Context) (net.PacketConn, error) { return connection, nil },
		}
		_, err := selector.Select(ctx, SelectionRequest{Servers: []Server{server}})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled write error = %v", err)
		}
	})
}

func repeatHex(value string, count int) string {
	result := ""
	for range count {
		result += value
	}
	return result
}

func testPong(t *testing.T, available bool, height uint32, country string) Pong {
	t.Helper()
	code, err := CountryCode(country)
	if err != nil {
		t.Fatal(err)
	}
	flags := byte(0)
	if available {
		flags = 1
	}
	return Pong{ProtocolVersion: 1, Flags: flags, Height: height, Country: code}
}

func candidateServers(candidates []Candidate) []Server {
	servers := make([]Server, len(candidates))
	for index, candidate := range candidates {
		servers[index] = candidate.Server
	}
	return servers
}

type resolverResponse struct {
	ip    net.IP
	err   error
	delay time.Duration
}

type scriptedResolver struct {
	responses map[Server]resolverResponse
}

type resolverFunc func(context.Context, Server) (net.IP, error)

func (function resolverFunc) ResolveIPv4(ctx context.Context, server Server) (net.IP, error) {
	return function(ctx, server)
}

func (resolver *scriptedResolver) ResolveIPv4(ctx context.Context, server Server) (net.IP, error) {
	response, exists := resolver.responses[server]
	if !exists {
		return nil, errors.New("unexpected server")
	}
	if response.delay > 0 {
		timer := time.NewTimer(response.delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return append(net.IP(nil), response.ip...), response.err
}

type packetWrite struct {
	packet []byte
	remote net.Addr
}

type packetRead struct {
	packet []byte
	remote net.Addr
}

type scriptedPacketConn struct {
	mu       sync.Mutex
	reads    chan packetRead
	writes   []packetWrite
	deadline time.Time
	closed   chan struct{}
	close    sync.Once
}

type cancelingWritePacketConn struct {
	*scriptedPacketConn
	cancel context.CancelFunc
}

func (connection *cancelingWritePacketConn) WriteTo([]byte, net.Addr) (int, error) {
	connection.cancel()
	return 0, net.ErrClosed
}

func newScriptedPacketConn(capacity int) *scriptedPacketConn {
	return &scriptedPacketConn{reads: make(chan packetRead, capacity), closed: make(chan struct{})}
}

func (connection *scriptedPacketConn) queuePong(t *testing.T, host string, port int, pong Pong) {
	t.Helper()
	connection.queueRead(pong.Encode(), &net.UDPAddr{IP: net.ParseIP(host), Port: port})
}

func (connection *scriptedPacketConn) queueRead(packet []byte, remote net.Addr) {
	connection.reads <- packetRead{packet: append([]byte(nil), packet...), remote: remote}
}

func (connection *scriptedPacketConn) ReadFrom(destination []byte) (int, net.Addr, error) {
	connection.mu.Lock()
	deadline := connection.deadline
	connection.mu.Unlock()
	wait := time.Until(deadline)
	if deadline.IsZero() {
		wait = time.Hour
	}
	timer := time.NewTimer(max(wait, 0))
	defer timer.Stop()
	select {
	case read := <-connection.reads:
		return copy(destination, read.packet), read.remote, nil
	case <-connection.closed:
		return 0, nil, net.ErrClosed
	case <-timer.C:
		return 0, nil, packetTimeoutError{}
	}
}

func (connection *scriptedPacketConn) WriteTo(packet []byte, remote net.Addr) (int, error) {
	connection.mu.Lock()
	connection.writes = append(connection.writes, packetWrite{
		packet: append([]byte(nil), packet...), remote: remote,
	})
	connection.mu.Unlock()
	return len(packet), nil
}

func (connection *scriptedPacketConn) Close() error {
	connection.close.Do(func() { close(connection.closed) })
	return nil
}

func (*scriptedPacketConn) LocalAddr() net.Addr { return &net.UDPAddr{} }

func (connection *scriptedPacketConn) SetDeadline(deadline time.Time) error {
	return connection.SetReadDeadline(deadline)
}

func (connection *scriptedPacketConn) SetReadDeadline(deadline time.Time) error {
	connection.mu.Lock()
	connection.deadline = deadline
	connection.mu.Unlock()
	return nil
}

func (*scriptedPacketConn) SetWriteDeadline(time.Time) error { return nil }

func (connection *scriptedPacketConn) Writes() []packetWrite {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	return append([]packetWrite(nil), connection.writes...)
}

type packetTimeoutError struct{}

func (packetTimeoutError) Error() string   { return "packet read timed out" }
func (packetTimeoutError) Timeout() bool   { return true }
func (packetTimeoutError) Temporary() bool { return true }

var _ IPv4Resolver = (*scriptedResolver)(nil)
var _ net.PacketConn = (*scriptedPacketConn)(nil)
