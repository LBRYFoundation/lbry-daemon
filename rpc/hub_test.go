package rpc

import (
	"bufio"
	"encoding/json"
	"fmt"
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

// batchHub answers every request line with the reply the function makes for it, last request first, so the
// replies are out of order. A nil function never answers.
func batchHub(t *testing.T, reply func(request map[string]any) string) string {
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
				if reply == nil {
					time.Sleep(time.Second)
					return
				}
				conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond)) // the batch arrives together
				var replies []string
				reader := bufio.NewReader(conn)
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						break
					}
					var request map[string]any
					json.Unmarshal([]byte(line), &request)
					replies = append(replies, reply(request))
				}
				for i := len(replies) - 1; i >= 0; i-- {
					conn.Write([]byte(replies[i] + "\n"))
				}
			}()
		}
	}()
	return listener.Addr().String()
}

func echoReply(request map[string]any) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%v,"result":%q}`, request["id"], request["method"])
}

func TestSendJSONBatchMatchesRepliesToRequests(t *testing.T) {
	shortHubTimeouts(t)
	address := batchHub(t, echoReply)
	requests := []map[string]any{{"method": "a"}, {"method": "b"}, {"method": "c"}}
	replies, err := SendJSONBatchToAddresses([]string{address}, requests)
	if err != nil || len(replies) != 3 {
		t.Fatalf("%v %v", replies, err)
	}
	for i, want := range []string{"a", "b", "c"} {
		if replies[i]["result"] != want {
			t.Errorf("reply %d = %v, want %s", i, replies[i]["result"], want)
		}
	}
	if replies, err := SendJSONBatchToAddresses([]string{address}, nil); err != nil || replies != nil {
		t.Errorf("no requests: %v %v", replies, err)
	}
}

func TestSendJSONBatchFailsOverAndRejectsBadReplies(t *testing.T) {
	shortHubTimeouts(t)
	requests := []map[string]any{{"method": "a"}, {"method": "b"}}

	silent := batchHub(t, nil)
	answering := batchHub(t, echoReply)
	if replies, err := SendJSONBatchToAddresses([]string{silent, answering}, requests); err != nil || len(replies) != 2 {
		t.Errorf("fail over: %v %v", replies, err)
	}

	// Answers only the first request: the second is missing, which is a failure, not a partial result.
	partial := batchHub(t, func(request map[string]any) string {
		if request["id"] == 2.0 {
			return `{"jsonrpc":"2.0","id":1,"result":"duplicate"}`
		}
		return echoReply(request)
	})
	if replies, err := SendJSONBatchToAddresses([]string{partial}, requests); err == nil {
		t.Errorf("a duplicate reply must fail, got %v", replies)
	}
	unknown := batchHub(t, func(request map[string]any) string { return `{"jsonrpc":"2.0","id":99,"result":1}` })
	if replies, err := SendJSONBatchToAddresses([]string{unknown}, requests); err == nil {
		t.Errorf("an unknown id must fail, got %v", replies)
	}
	notJSON := batchHub(t, func(request map[string]any) string { return "garbage" })
	if replies, err := SendJSONBatchToAddresses([]string{notJSON}, requests); err == nil {
		t.Errorf("a reply that is not JSON must fail, got %v", replies)
	}
}

// A dead address is tried last once it has failed, so it costs one timeout, not one per call.
func TestHubAddressesThatFailedAreTriedLast(t *testing.T) {
	shortHubTimeouts(t)
	memory := HubFailureMemory
	HubFailureMemory = time.Hour
	t.Cleanup(func() { HubFailureMemory = memory })

	dead := batchHub(t, nil) // accepts and never answers
	good := batchHub(t, echoReply)
	requests := []map[string]any{{"method": "a"}}

	first := time.Now()
	if _, err := SendJSONBatchToAddresses([]string{dead, good}, requests); err != nil {
		t.Fatal(err)
	}
	if time.Since(first) < HubReplyTimeout {
		t.Fatalf("the first call should have waited for the silent address, took %v", time.Since(first))
	}
	second := time.Now()
	if _, err := SendJSONBatchToAddresses([]string{dead, good}, requests); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(second); took >= HubReplyTimeout {
		t.Errorf("the second call still waited for the dead address: %v", took)
	}
	if order := hubAddressHealth.order([]string{dead, good}); order[0] != good {
		t.Errorf("order = %v, want the good address first", order)
	}

	// Once the memory has run out the address is tried again, in its place.
	HubFailureMemory = time.Nanosecond
	time.Sleep(time.Millisecond)
	if order := hubAddressHealth.order([]string{dead, good}); order[0] != dead {
		t.Errorf("after the memory ran out, order = %v", order)
	}
}
