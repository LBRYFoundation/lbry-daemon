package spv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestClientJSONRPCWireAndResultContract(t *testing.T) {
	client, server := newPipeClient(t, ClientConfig{})
	result := make(chan callResult, 1)
	go func() {
		value, err := client.Call(context.Background(), "blockchain.block.headers", []any{9000, 1000, 0, true})
		result <- callResult{value: value, err: err}
	}()

	request := readPipeRequest(t, server)
	if request.JSONRPC != "2.0" || request.Method != "blockchain.block.headers" || request.ID == nil ||
		!reflect.DeepEqual(request.Params, []any{json.Number("9000"), json.Number("1000"), json.Number("0"), true}) {
		t.Fatalf("wire request = %#v", request)
	}
	writePipeResponse(t, server, *request.ID, `{"base64":"chunk"}`)
	response := <-result
	if response.err != nil || !reflect.DeepEqual(response.value, map[string]any{"base64": "chunk"}) {
		t.Fatalf("call response = %#v, %v", response.value, response.err)
	}

	second := make(chan error, 1)
	go func() {
		_, err := client.Call(context.Background(), "server.features", []any{})
		second <- err
	}()
	empty := readPipeRequest(t, server)
	if empty.ParamsPresent {
		t.Fatalf("empty list params were emitted: %#v", empty.Params)
	}
	writePipeResponse(t, server, *empty.ID, `{}`)
	if err := <-second; err != nil {
		t.Fatal(err)
	}
}

func TestClientOrderedObjectResultPreservesWireOrderAndDuplicatePosition(t *testing.T) {
	client, server := newPipeClient(t, ClientConfig{})
	result := make(chan callResult, 1)
	go func() {
		value, err := client.CallOrderedObject(
			context.Background(), "blockchain.transaction.get_batch", []any{"first", "second"},
		)
		result <- callResult{value: value, err: err}
	}()
	request := readPipeRequest(t, server)
	writePipeResponse(t, server, *request.ID, `{"second":1,"first":2,"second":3}`)
	response := <-result
	ordered, ok := response.value.(*OrderedObject)
	if response.err != nil || !ok {
		t.Fatalf("ordered response = %T %#v, %v", response.value, response.value, response.err)
	}
	if keys := ordered.Keys(); !reflect.DeepEqual(keys, []string{"second", "first"}) {
		t.Fatalf("ordered keys = %#v", keys)
	}
	if second, exists := ordered.Get("second"); !exists || second != json.Number("3") {
		t.Fatalf("duplicate replacement = %#v, %t", second, exists)
	}

	go func() {
		value, err := client.Call(context.Background(), "ordinary.object", nil)
		result <- callResult{value: value, err: err}
	}()
	request = readPipeRequest(t, server)
	writePipeResponse(t, server, *request.ID, `{"second":1,"first":2}`)
	response = <-result
	if response.err != nil || !reflect.DeepEqual(response.value, map[string]any{
		"second": json.Number("1"), "first": json.Number("2"),
	}) {
		t.Fatalf("ordinary response = %T %#v, %v", response.value, response.value, response.err)
	}
}

func TestClientRoutesConcurrentResponsesByID(t *testing.T) {
	client, server := newPipeClient(t, ClientConfig{Concurrency: 2})
	responses := make(chan string, 2)
	for _, method := range []string{"first", "second"} {
		method := method
		go func() {
			value, err := client.Call(context.Background(), method, []any{method})
			if err != nil {
				responses <- "error:" + err.Error()
				return
			}
			responses <- fmt.Sprintf("%s:%s", method, value)
		}()
	}
	first := readPipeRequest(t, server)
	second := readPipeRequest(t, server)
	writePipeResponse(t, server, *second.ID, fmt.Sprintf("%q", second.Method))
	writePipeResponse(t, server, *first.ID, fmt.Sprintf("%q", first.Method))
	got := map[string]bool{<-responses: true, <-responses: true}
	if !got["first:first"] || !got["second:second"] {
		t.Fatalf("routed responses = %v", got)
	}
}

func TestClientDispatchesBatchedNotificationsWithoutDisconnect(t *testing.T) {
	received := make(chan string, 2)
	client, server := newPipeClient(t, ClientConfig{
		NotificationHandler: func(_ context.Context, method string, _ any) {
			received <- method
		},
	})
	writePipeRaw(t, server, `[`+
		`{"jsonrpc":"2.0","method":"first","params":["address-one"]},`+
		`{"jsonrpc":"2.0","method":"second","params":["address-two"]}`+
		`]`+"\n")
	for _, want := range []string{"first", "second"} {
		select {
		case got := <-received:
			if got != want {
				t.Fatalf("batched notification method = %q, want %q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("batched notification was not dispatched")
		}
	}
	if !client.IsConnected() {
		t.Fatalf("batched notifications closed client: %v", client.Err())
	}
}

func TestClientAcceptsPythonSpecialFloatResponse(t *testing.T) {
	client, server := newPipeClient(t, ClientConfig{})
	result := make(chan callResult, 1)
	go func() {
		value, err := client.Call(context.Background(), "legacy.special", nil)
		result <- callResult{value: value, err: err}
	}()
	request := readPipeRequest(t, server)
	writePipeRaw(t, server, fmt.Sprintf(
		`{"jsonrpc":"2.0","result":{"nan":NaN,"positive":Infinity,`+
			`"negative":-Infinity,"literal":"NaN"},"id":%d}`+"\n",
		*request.ID,
	))
	response := <-result
	values, ok := response.value.(map[string]any)
	if response.err != nil || !ok {
		t.Fatalf("special-float response = %T %#v, %v", response.value, response.value, response.err)
	}
	if nan, ok := values["nan"].(float64); !ok || !math.IsNaN(nan) {
		t.Fatalf("NaN response = %T %#v", values["nan"], values["nan"])
	}
	if positive, ok := values["positive"].(float64); !ok || !math.IsInf(positive, 1) {
		t.Fatalf("Infinity response = %T %#v", values["positive"], values["positive"])
	}
	if negative, ok := values["negative"].(float64); !ok || !math.IsInf(negative, -1) {
		t.Fatalf("negative Infinity response = %T %#v", values["negative"], values["negative"])
	}
	if values["literal"] != "NaN" {
		t.Fatalf("literal response = %#v", values["literal"])
	}
	if !client.IsConnected() {
		t.Fatalf("special-float response closed client: %v", client.Err())
	}
}

func TestClientReturnsTypedRPCAndProtocolErrors(t *testing.T) {
	client, server := newPipeClient(t, ClientConfig{})
	errorsByCall := make(chan error, 2)
	go func() {
		_, err := client.Call(context.Background(), "rpc-error", nil)
		errorsByCall <- err
	}()
	request := readPipeRequest(t, server)
	writePipeRaw(t, server, fmt.Sprintf(
		`{"jsonrpc":"2.0","error":{"code":-32602,"message":"bad arguments"},"id":%d}`+"\n",
		*request.ID,
	))
	var rpcErr *RPCError
	if err := <-errorsByCall; !errors.As(err, &rpcErr) || rpcErr.Code != -32602 || rpcErr.Message != "bad arguments" {
		t.Fatalf("RPC error = %#v, %v", rpcErr, err)
	}

	go func() {
		_, err := client.Call(context.Background(), "protocol-error", nil)
		errorsByCall <- err
	}()
	request = readPipeRequest(t, server)
	writePipeRaw(t, server, fmt.Sprintf(
		`{"jsonrpc":"2.0","result":{},"error":null,"id":%d}`+"\n",
		*request.ID,
	))
	var protocolErr *ProtocolError
	if err := <-errorsByCall; !errors.As(err, &protocolErr) || protocolErr.Message != `response contains both "result" and "error"` {
		t.Fatalf("protocol error = %#v, %v", protocolErr, err)
	}
}

func TestClientRoutesInvalidJSONRPCVersionToPendingCall(t *testing.T) {
	client, server := newPipeClient(t, ClientConfig{RequestTimeout: time.Second})
	result := make(chan error, 1)
	go func() {
		_, err := client.Call(context.Background(), "wrong-version", nil)
		result <- err
	}()
	request := readPipeRequest(t, server)
	writePipeRaw(t, server, fmt.Sprintf(
		`{"jsonrpc":"1.0","result":{},"id":%d}`+"\n", *request.ID,
	))
	select {
	case err := <-result:
		var protocolErr *ProtocolError
		if !errors.As(err, &protocolErr) || protocolErr.Message != `"jsonrpc" is not "2.0"` {
			t.Fatalf("wrong-version response error = %#v, %v", protocolErr, err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("wrong-version response aged into a request timeout")
	}
}

func TestClientAcceptsPythonNumericResponseIDEquivalence(t *testing.T) {
	for _, test := range []struct {
		name     string
		prepare  func()
		formatID func(int64) string
	}{
		{name: "integral float", formatID: func(id int64) string { return fmt.Sprintf("%d.0", id) }},
		{name: "boolean", prepare: func() { globalRequestNumber.Store(1) }, formatID: func(int64) string { return "true" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.prepare != nil {
				test.prepare()
			}
			client, server := newPipeClient(t, ClientConfig{})
			result := make(chan error, 1)
			go func() {
				_, err := client.Call(context.Background(), "numeric-id", nil)
				result <- err
			}()
			request := readPipeRequest(t, server)
			writePipeRaw(t, server, fmt.Sprintf(
				`{"jsonrpc":"2.0","result":{},"id":%s}`+"\n", test.formatID(*request.ID),
			))
			if err := <-result; err != nil {
				t.Fatalf("numeric-equivalent ID failed: %v", err)
			}
		})
	}
}

func TestClientPacketActivityExtendsRequestTimeout(t *testing.T) {
	notifications := make(chan any, 1)
	client, server := newPipeClient(t, ClientConfig{
		RequestTimeout: 100 * time.Millisecond,
		NotificationHandler: func(_ context.Context, method string, params any) {
			notifications <- []any{method, params}
		},
	})
	result := make(chan error, 1)
	go func() {
		_, err := client.Call(context.Background(), "slow", nil)
		result <- err
	}()
	request := readPipeRequest(t, server)
	time.Sleep(60 * time.Millisecond)
	writePipeRaw(t, server, `{"jsonrpc":"2.0","method":"blockchain.headers.subscribe","params":[{"height":42}]}`+"\n")
	select {
	case notification := <-notifications:
		want := []any{"blockchain.headers.subscribe", []any{map[string]any{"height": json.Number("42")}}}
		if !reflect.DeepEqual(notification, want) {
			t.Fatalf("notification = %#v, want %#v", notification, want)
		}
	case <-time.After(time.Second):
		t.Fatal("notification was not dispatched")
	}
	time.Sleep(60 * time.Millisecond)
	writePipeResponse(t, server, *request.ID, `{}`)
	if err := <-result; err != nil {
		t.Fatalf("packet activity did not extend timeout: %v", err)
	}
}

func TestClientConcurrencyGateAndContextCancellation(t *testing.T) {
	client, server := newPipeClient(t, ClientConfig{Concurrency: 1})
	firstDone := make(chan error, 1)
	go func() {
		_, err := client.Call(context.Background(), "first", nil)
		firstDone <- err
	}()
	first := readPipeRequest(t, server)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	secondDone := make(chan error, 1)
	go func() {
		_, err := client.Call(ctx, "second", nil)
		secondDone <- err
	}()
	if err := server.SetReadDeadline(time.Now().Add(30 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 1)
	if _, err := server.Read(buffer); err == nil {
		t.Fatal("second request bypassed the concurrency gate")
	}
	if err := server.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("gated request error = %v", err)
	}
	writePipeResponse(t, server, *first.ID, `{}`)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
}

func TestClientConnectionLossFailsPendingCall(t *testing.T) {
	client, server := newPipeClient(t, ClientConfig{})
	result := make(chan error, 1)
	go func() {
		_, err := client.Call(context.Background(), "pending", nil)
		result <- err
	}()
	_ = readPipeRequest(t, server)
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrConnection) {
			t.Fatalf("connection-loss error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pending call was not released")
	}
}

func TestEncodeRequestAllowsLegacySpecialFloats(t *testing.T) {
	encoded, err := encodeRequest("special.values", []any{
		math.NaN(), math.Inf(1), math.Inf(-1),
		map[string]any{"nested": []any{math.Inf(1), "Infinity"}},
	}, 7)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"jsonrpc\":\"2.0\",\"method\":\"special.values\",\"id\":7," +
		"\"params\":[NaN,Infinity,-Infinity,{\"nested\":[Infinity,\"Infinity\"]}]}\n"
	if string(encoded) != want {
		t.Fatalf("special-float request = %s, want %s", encoded, want)
	}
}

func TestClientDispatchesNotificationsInWireOrder(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	received := make(chan string, 2)
	client, server := newPipeClient(t, ClientConfig{
		NotificationHandler: func(_ context.Context, method string, _ any) {
			received <- method
			if method == "first" {
				close(firstStarted)
				<-releaseFirst
			}
		},
	})
	_ = client
	writePipeRaw(t, server, `{"jsonrpc":"2.0","method":"first","params":[]}`+"\n")
	writePipeRaw(t, server, `{"jsonrpc":"2.0","method":"second","params":[]}`+"\n")
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first notification did not start")
	}
	if method := <-received; method != "first" {
		t.Fatalf("first dispatched method = %q", method)
	}
	select {
	case method := <-received:
		t.Fatalf("second notification overtook blocked first: %q", method)
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseFirst)
	select {
	case method := <-received:
		if method != "second" {
			t.Fatalf("second dispatched method = %q", method)
		}
	case <-time.After(time.Second):
		t.Fatal("second notification was not dispatched")
	}
}

func TestClientRejectsOversizedFrame(t *testing.T) {
	client, server := newPipeClient(t, ClientConfig{MaxFrameSize: 64})
	writeDone := make(chan error, 1)
	go func() {
		_, err := server.Write([]byte(strings.Repeat("x", 65) + "\n"))
		writeDone <- err
	}()
	select {
	case <-client.Done():
		if !errors.Is(client.Err(), ErrFrameTooLarge) {
			t.Fatalf("oversized-frame error = %v", client.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("oversized frame did not close client")
	}
	<-writeDone
}

type pipeRequest struct {
	JSONRPC       string
	Method        string
	ID            *int64
	Params        any
	ParamsPresent bool
}

func newPipeClient(t *testing.T, config ClientConfig) (*Client, net.Conn) {
	t.Helper()
	normalized, err := config.normalized()
	if err != nil {
		t.Fatal(err)
	}
	clientConnection, serverConnection := net.Pipe()
	client := newClient(Server{Host: "pipe", Port: 1}, clientConnection, normalized)
	t.Cleanup(func() {
		_ = client.Close()
		_ = serverConnection.Close()
	})
	return client, serverConnection
}

func readPipeRequest(t *testing.T, connection net.Conn) pipeRequest {
	t.Helper()
	if err := connection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var line strings.Builder
	var next [1]byte
	for {
		if _, err := connection.Read(next[:]); err != nil {
			t.Fatal(err)
		}
		line.WriteByte(next[0])
		if next[0] == '\n' {
			break
		}
	}
	encoded := line.String()
	if !strings.HasSuffix(encoded, "\n") {
		t.Fatalf("request is not newline framed: %q", encoded)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(encoded), &raw); err != nil {
		t.Fatal(err)
	}
	var request pipeRequest
	if err := json.Unmarshal(raw["jsonrpc"], &request.JSONRPC); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw["method"], &request.Method); err != nil {
		t.Fatal(err)
	}
	var id json.Number
	if err := json.Unmarshal(raw["id"], &id); err != nil {
		t.Fatal(err)
	}
	parsedID, err := id.Int64()
	if err != nil {
		t.Fatal(err)
	}
	request.ID = &parsedID
	if params, exists := raw["params"]; exists {
		request.ParamsPresent = true
		request.Params, err = decodeJSONValue(params)
		if err != nil {
			t.Fatal(err)
		}
	}
	return request
}

func writePipeResponse(t *testing.T, connection net.Conn, id int64, result string) {
	t.Helper()
	writePipeRaw(t, connection, fmt.Sprintf(
		`{"jsonrpc":"2.0","result":%s,"id":%d}`+"\n", result, id,
	))
}

func writePipeRaw(t *testing.T, connection net.Conn, message string) {
	t.Helper()
	if err := connection.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write([]byte(message)); err != nil {
		t.Fatal(err)
	}
}
