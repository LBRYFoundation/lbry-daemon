package rpc

import (
	"bufio"
	"encoding/json"
	"net"
	"testing"
	"time"
)

// fakeHub listens on a loopback port. A hub with a reply answers every request line with it; one without
// accepts the connection and never answers, like the dead hub address in the issue.
func fakeHub(t *testing.T, reply string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				if _, err := bufio.NewReader(conn).ReadString('\n'); err != nil || reply == "" {
					time.Sleep(time.Second)
					return
				}
				conn.Write([]byte(reply + "\r\n"))
			}()
		}
	}()
	return listener.Addr().String()
}

func shortHubTimeouts(t *testing.T) {
	dial, reply := HubDialTimeout, HubReplyTimeout
	HubDialTimeout, HubReplyTimeout = 200*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { HubDialTimeout, HubReplyTimeout = dial, reply })
}

func TestSendJSONFallsBackWhenTheFirstAddressIsSilent(t *testing.T) {
	shortHubTimeouts(t)
	silent := fakeHub(t, "")
	answering := fakeHub(t, `{"jsonrpc":"2.0","id":1,"result":"ok"}`)
	resp, err := SendJSONToAddresses([]string{silent, answering}, map[string]any{"method": "x"})
	if err != nil || resp["result"] != "ok" {
		t.Fatalf("expected the second address to answer, got %v %v", resp, err)
	}
}

func TestSendJSONFailsWhenNoAddressAnswers(t *testing.T) {
	shortHubTimeouts(t)
	silent := fakeHub(t, "")
	// Nothing listens on the second address.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := listener.Addr().String()
	listener.Close()
	if resp, err := SendJSONToAddresses([]string{silent, closed}, map[string]any{}); err == nil {
		t.Fatalf("expected an error, got %v", resp)
	}
	if _, err := SendJSONToAddresses(nil, map[string]any{}); err == nil {
		t.Fatal("expected an error for no addresses")
	}
}

func TestSendJSONRejectsAReplyThatIsNotAnObject(t *testing.T) {
	shortHubTimeouts(t)
	if resp, err := SendJSONToAddresses([]string{fakeHub(t, "not json")}, map[string]any{}); err == nil {
		t.Fatalf("expected an error, got %v", resp)
	}
}

func TestClaimSearchPaging(t *testing.T) {
	for name, tc := range map[string]struct {
		params         any
		page, pageSize int64
	}{
		"defaults":     {nil, 1, 10},
		"requested":    {map[string]any{"page": json.Number("3"), "page_size": json.Number("50")}, 3, 50},
		"invalid":      {map[string]any{"page": json.Number("0"), "page_size": json.Number("-1")}, 1, 10},
		"wrong type":   {map[string]any{"page": "2"}, 1, 10},
		"fraction":     {map[string]any{"page": json.Number("2.5")}, 1, 10},
		"array params": {[]any{}, 1, 10},
	} {
		if page, size := claimSearchPaging(tc.params); page != tc.page || size != tc.pageSize {
			t.Errorf("%s: got %v %v", name, page, size)
		}
	}
}
