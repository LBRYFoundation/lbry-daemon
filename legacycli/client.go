package legacycli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	DefaultEndpoint        = "http://localhost:5279/lbryapi"
	ConnectionErrorMessage = "Could not connect to daemon. Are you sure it's running?"
)

// Client executes the GET-with-JSON-body protocol used by the Python CLI.
type Client struct {
	Endpoint   string
	HTTPClient *http.Client
}

// ConnectionError is returned when the HTTP client cannot reach the daemon.
type ConnectionError struct {
	Cause error
}

func (err *ConnectionError) Error() string {
	return ConnectionErrorMessage
}

func (err *ConnectionError) Unwrap() error {
	return err.Cause
}

// Call returns the result or error member of the JSON-RPC response exactly as
// the legacy CLI selected it for display.
func (client Client) Call(ctx context.Context, invocation Invocation) (json.RawMessage, error) {
	endpoint := client.Endpoint
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	payload, err := json.Marshal(map[string]any{
		"method": invocation.Method,
		"params": invocation.Params,
	})
	if err != nil {
		return nil, fmt.Errorf("encode RPC request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create RPC request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	httpClient := client.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, &ConnectionError{Cause: err}
	}
	defer response.Body.Close()
	var envelope map[string]json.RawMessage
	decoder := json.NewDecoder(response.Body)
	if err := decoder.Decode(&envelope); err != nil {
		return nil, fmt.Errorf("decode daemon response: %w", err)
	}
	if result, exists := envelope["result"]; exists {
		return append(json.RawMessage(nil), result...), nil
	}
	if rpcError, exists := envelope["error"]; exists {
		return append(json.RawMessage(nil), rpcError...), nil
	}
	return nil, fmt.Errorf("daemon response contains neither result nor error")
}

// WriteDisplay renders a selected response value like json.dumps(value,
// indent=2), including the trailing newline printed by the Python CLI.
func WriteDisplay(writer io.Writer, payload json.RawMessage) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("decode display value: %w", err)
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("encode display value: %w", err)
	}
	_, err := writer.Write(escapeNonASCII(encoded.Bytes()))
	return err
}

func escapeNonASCII(data []byte) []byte {
	var escaped strings.Builder
	for len(data) > 0 {
		character, size := utf8.DecodeRune(data)
		data = data[size:]
		if character <= 0x7f {
			escaped.WriteRune(character)
			continue
		}
		if character <= 0xffff {
			_, _ = fmt.Fprintf(&escaped, "\\u%04x", character)
			continue
		}
		high, low := utf16.EncodeRune(character)
		_, _ = fmt.Fprintf(&escaped, "\\u%04x\\u%04x", high, low)
	}
	return []byte(escaped.String())
}
