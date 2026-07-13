package legacycli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func jsonResponse(payload string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(payload)),
	}
}

func TestClientUsesLegacyGETJSONTransport(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Path != "/lbryapi" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content type = %q", request.Header.Get("Content-Type"))
		}
		decoder := json.NewDecoder(request.Body)
		decoder.UseNumber()
		var payload map[string]any
		if err := decoder.Decode(&payload); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{
			"method": "settings_set",
			"params": map[string]any{"key": "udp_port", "value": json.Number("5000")},
		}
		if !reflect.DeepEqual(payload, want) {
			t.Errorf("payload = %#v, want %#v", payload, want)
		}
		return jsonResponse(`{"jsonrpc":"2.0","result":{"number":3,"value":"caf\u00e9"}}`), nil
	})}

	client := Client{Endpoint: "http://daemon.test/lbryapi", HTTPClient: httpClient}
	payload, err := client.Call(context.Background(), Invocation{
		Method: "settings_set",
		Params: map[string]any{"key": "udp_port", "value": 5000},
	})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := WriteDisplay(&output, payload); err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"number\": 3,\n  \"value\": \"caf\\u00e9\"\n}\n"
	if output.String() != want {
		t.Fatalf("display = %q, want %q", output.String(), want)
	}
}

func TestClientDisplaysRPCErrorPayload(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(`{"jsonrpc":"2.0","error":{"code":-32601,"message":"missing"}}`), nil
	})}
	payload, err := (Client{Endpoint: "http://daemon.test/lbryapi", HTTPClient: httpClient}).Call(
		context.Background(), Invocation{Method: "wat", Params: map[string]any{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != `{"code":-32601,"message":"missing"}` {
		t.Fatalf("error payload = %s", payload)
	}
}

func TestClientConnectionErrorMessage(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})}
	_, err := (Client{Endpoint: "http://daemon.test/lbryapi", HTTPClient: httpClient}).Call(
		context.Background(), Invocation{Method: "status", Params: map[string]any{}},
	)
	var connection *ConnectionError
	if !errors.As(err, &connection) || err.Error() != ConnectionErrorMessage {
		t.Fatalf("connection error = %T %v", err, err)
	}
}

func TestClientRejectsInvalidEnvelopeAndDisplay(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(`{"jsonrpc":"2.0"}`), nil
	})}
	_, err := (Client{Endpoint: "http://daemon.test/lbryapi", HTTPClient: httpClient}).Call(
		context.Background(), Invocation{Method: "status", Params: map[string]any{}},
	)
	if err == nil || !strings.Contains(err.Error(), "neither result nor error") {
		t.Fatalf("envelope error = %v", err)
	}
	if err := WriteDisplay(&bytes.Buffer{}, json.RawMessage(`NaN`)); err == nil {
		t.Fatal("expected nonstandard JSON display error")
	}
}
