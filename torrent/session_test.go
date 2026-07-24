package torrent

import (
	"net"
	"testing"
	"time"
)

func TestSessionOwnsTCPAndUDPListenEndpoints(t *testing.T) {
	session := NewSession("127.0.0.1", 0)
	if session.Status()["running"] != false {
		t.Fatal("new torrent session is running")
	}
	if err := session.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Stop() })
	if err := session.Start(); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if session.Status()["running"] != true {
		t.Fatal("started torrent session is not running")
	}

	tcpAddress, udpAddress := session.Addresses()
	if tcpAddress == nil || udpAddress == nil {
		t.Fatalf("listen addresses = %v, %v", tcpAddress, udpAddress)
	}
	if tcpAddress.(*net.TCPAddr).Port != udpAddress.(*net.UDPAddr).Port {
		t.Fatalf("TCP and UDP ports differ: %v, %v", tcpAddress, udpAddress)
	}
	tcp, err := net.DialTimeout("tcp4", tcpAddress.String(), time.Second)
	if err != nil {
		t.Fatalf("dial TCP endpoint: %v", err)
	}
	_ = tcp.Close()
	udp, err := net.DialTimeout("udp4", udpAddress.String(), time.Second)
	if err != nil {
		t.Fatalf("dial UDP endpoint: %v", err)
	}
	if _, err := udp.Write([]byte("probe")); err != nil {
		t.Fatalf("write UDP endpoint: %v", err)
	}
	_ = udp.Close()

	if err := session.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := session.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	if session.Status()["running"] != false {
		t.Fatal("stopped torrent session is running")
	}
	if tcpAddress, udpAddress := session.Addresses(); tcpAddress != nil || udpAddress != nil {
		t.Fatalf("stopped listen addresses = %v, %v", tcpAddress, udpAddress)
	}
}

func TestSessionReportsTCPBindFailureWithoutRunning(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	session := NewSession("127.0.0.1", port)
	if err := session.Start(); err == nil {
		_ = session.Stop()
		t.Fatal("Start succeeded on an occupied TCP port")
	}
	if session.Status()["running"] != false {
		t.Fatal("failed torrent session is running")
	}
}
