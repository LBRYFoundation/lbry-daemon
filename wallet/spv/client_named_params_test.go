package spv

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestNamedClientPipeWireAndResult(t *testing.T) {
	client, server := newPipeClient(t, ClientConfig{})
	result := make(chan callResult, 1)
	go func() {
		value, err := client.CallNamed(context.Background(), "blockchain.claimtrie.search", map[string]any{
			"claim_type": []any{"stream", "channel"},
			"page":       2,
		})
		result <- callResult{value: value, err: err}
	}()

	request := readPipeRequest(t, server)
	wantParams := map[string]any{
		"claim_type": []any{"stream", "channel"},
		"page":       json.Number("2"),
	}
	if request.Method != "blockchain.claimtrie.search" || !request.ParamsPresent ||
		!reflect.DeepEqual(request.Params, wantParams) {
		t.Fatalf("named wire request = %#v", request)
	}
	writePipeResponse(t, server, *request.ID, `{"total":3}`)
	response := <-result
	if response.err != nil || !reflect.DeepEqual(response.value, map[string]any{"total": json.Number("3")}) {
		t.Fatalf("named response = %#v, %v", response.value, response.err)
	}
}

func TestNamedClientAlwaysEmitsEmptyObject(t *testing.T) {
	for _, test := range []struct {
		name   string
		params map[string]any
	}{
		{name: "nil", params: nil},
		{name: "empty", params: map[string]any{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, server := newPipeClient(t, ClientConfig{})
			done := make(chan error, 1)
			go func() {
				_, err := client.CallNamed(context.Background(), "named.empty", test.params)
				done <- err
			}()

			request := readPipeRequest(t, server)
			if !request.ParamsPresent || !reflect.DeepEqual(request.Params, map[string]any{}) {
				t.Fatalf("empty named params = %#v, present = %t", request.Params, request.ParamsPresent)
			}
			writePipeResponse(t, server, *request.ID, `{}`)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNamedClientEncodingAllowsLegacySpecialFloats(t *testing.T) {
	encoded, err := encodeNamedRequest("named.special", map[string]any{
		"nan": math.NaN(),
		"nested": map[string]any{
			"negative": math.Inf(-1),
			"positive": math.Inf(1),
			"text":     "Infinity",
		},
	}, 17)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"jsonrpc\":\"2.0\",\"method\":\"named.special\",\"id\":17," +
		"\"params\":{\"nan\":NaN,\"nested\":{\"negative\":-Infinity," +
		"\"positive\":Infinity,\"text\":\"Infinity\"}}}\n"
	if string(encoded) != want {
		t.Fatalf("special-float named request = %s, want %s", encoded, want)
	}
}

func TestNamedClientReturnsRPCAndEncodingErrors(t *testing.T) {
	client, server := newPipeClient(t, ClientConfig{})
	rpcResult := make(chan error, 1)
	go func() {
		_, err := client.CallNamed(context.Background(), "named.rpc_error", map[string]any{"page": 1})
		rpcResult <- err
	}()
	request := readPipeRequest(t, server)
	writePipeRaw(t, server, `{"jsonrpc":"2.0","error":{"code":-32602,"message":"bad named arguments"},"id":`+
		strconv.FormatInt(*request.ID, 10)+"}\n")
	var rpcErr *RPCError
	if err := <-rpcResult; !errors.As(err, &rpcErr) || rpcErr.Code != -32602 || rpcErr.Message != "bad named arguments" {
		t.Fatalf("named RPC error = %#v, %v", rpcErr, err)
	}

	_, err := client.CallNamed(context.Background(), "named.encode_error", map[string]any{
		"unsupported": make(chan int),
	})
	var protocolErr *ProtocolError
	if !errors.As(err, &protocolErr) || !strings.Contains(protocolErr.Message, "unsupported type: chan int") {
		t.Fatalf("named encoding error = %#v, %v", protocolErr, err)
	}
}
