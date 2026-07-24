package torrent

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
)

const DefaultPort = 10889

// Session owns the TCP and uTP/UDP listen endpoints created by the pinned
// SDK's libtorrent component. Torrent download management is layered above it.
type Session struct {
	interfaceAddress string
	port             int

	mu      sync.RWMutex
	tcp     net.Listener
	udp     net.PacketConn
	running bool
	wg      sync.WaitGroup
}

func NewSession(interfaceAddress string, port int) *Session {
	return &Session{interfaceAddress: interfaceAddress, port: port}
}

func (session *Session) Start() error {
	if session == nil {
		return errors.New("torrent session is nil")
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.running {
		return nil
	}

	address := net.JoinHostPort(session.interfaceAddress, strconv.Itoa(session.port))
	tcp, err := net.Listen("tcp4", address)
	if err != nil {
		return fmt.Errorf("listen for BitTorrent TCP on %s: %w", address, err)
	}
	port := tcp.Addr().(*net.TCPAddr).Port
	udpAddress := net.JoinHostPort(session.interfaceAddress, strconv.Itoa(port))
	udp, err := net.ListenPacket("udp4", udpAddress)
	if err != nil {
		_ = tcp.Close()
		return fmt.Errorf("listen for BitTorrent uTP on %s: %w", udpAddress, err)
	}

	session.tcp = tcp
	session.udp = udp
	session.running = true
	session.wg.Add(2)
	go session.acceptTCP(tcp)
	go session.receiveUDP(udp)
	return nil
}

func (session *Session) Stop() error {
	if session == nil {
		return nil
	}
	session.mu.Lock()
	if !session.running {
		session.mu.Unlock()
		return nil
	}
	tcp := session.tcp
	udp := session.udp
	session.tcp = nil
	session.udp = nil
	session.running = false
	session.mu.Unlock()

	tcpErr := tcp.Close()
	udpErr := udp.Close()
	session.wg.Wait()
	return errors.Join(tcpErr, udpErr)
}

func (session *Session) Status() map[string]any {
	if session == nil {
		return map[string]any{"running": false}
	}
	session.mu.RLock()
	running := session.running
	session.mu.RUnlock()
	return map[string]any{"running": running}
}

func (session *Session) Addresses() (net.Addr, net.Addr) {
	if session == nil {
		return nil, nil
	}
	session.mu.RLock()
	defer session.mu.RUnlock()
	if session.tcp == nil || session.udp == nil {
		return nil, nil
	}
	return session.tcp.Addr(), session.udp.LocalAddr()
}

func (session *Session) acceptTCP(listener net.Listener) {
	defer session.wg.Done()
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		_ = connection.Close()
	}
}

func (session *Session) receiveUDP(connection net.PacketConn) {
	defer session.wg.Done()
	buffer := make([]byte, 64*1024)
	for {
		if _, _, err := connection.ReadFrom(buffer); err != nil {
			return
		}
	}
}
