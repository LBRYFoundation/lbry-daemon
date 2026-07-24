package spv

import (
	"bufio"
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	DefaultConnectTimeout = 6 * time.Second
	DefaultRequestTimeout = 30 * time.Second
	DefaultConcurrency    = 32
	// Python sets its newline framer to 4 GiB. Keep a practical safety bound,
	// but allow large address-history batches returned for established wallets.
	DefaultMaxFrameSize = 64 << 20
)

var (
	ErrConnection       = errors.New("SPV connection error")
	ErrClientClosed     = fmt.Errorf("%w: client is closed", ErrConnection)
	ErrRequestTimeout   = errors.New("SPV request timed out")
	ErrFrameTooLarge    = errors.New("SPV response frame exceeds the resource limit")
	ErrInvalidClient    = errors.New("invalid SPV client configuration")
	globalRequestNumber atomic.Int64
)

type Server struct {
	Host string
	Port int
}

func (server Server) Address() string {
	return net.JoinHostPort(server.Host, strconv.Itoa(server.Port))
}

func (server Server) String() string { return server.Address() }

type Dialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

type NotificationHandler func(context.Context, string, any)

type ClientConfig struct {
	ConnectTimeout      time.Duration
	RequestTimeout      time.Duration
	Concurrency         int
	MaxFrameSize        int
	Dialer              Dialer
	NotificationHandler NotificationHandler
}

func (config ClientConfig) normalized() (ClientConfig, error) {
	if config.ConnectTimeout == 0 {
		config.ConnectTimeout = DefaultConnectTimeout
	}
	if config.RequestTimeout == 0 {
		config.RequestTimeout = DefaultRequestTimeout
	}
	if config.Concurrency == 0 {
		config.Concurrency = DefaultConcurrency
	}
	if config.MaxFrameSize == 0 {
		config.MaxFrameSize = DefaultMaxFrameSize
	}
	if config.Dialer == nil {
		config.Dialer = &net.Dialer{}
	}
	if config.ConnectTimeout < 0 || config.RequestTimeout < 0 ||
		config.Concurrency < 1 || config.MaxFrameSize < 1 {
		return ClientConfig{}, ErrInvalidClient
	}
	return config, nil
}

type ConnectionError struct {
	Operation string
	Server    Server
	Err       error
}

func (err *ConnectionError) Error() string {
	if err == nil {
		return ErrConnection.Error()
	}
	if err.Server.Host == "" {
		return fmt.Sprintf("SPV %s: %v", err.Operation, err.Err)
	}
	return fmt.Sprintf("SPV %s %s: %v", err.Operation, err.Server, err.Err)
}

func (err *ConnectionError) Unwrap() []error {
	if err == nil || err.Err == nil {
		return []error{ErrConnection}
	}
	return []error{ErrConnection, err.Err}
}

type RPCError struct {
	Code    int64
	Message string
}

func (err *RPCError) Error() string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("SPV RPC error %d: %s", err.Code, err.Message)
}

func (err *RPCError) RPCCode() int64 {
	if err == nil {
		return 0
	}
	return err.Code
}

func (err *RPCError) RPCMessage() string {
	if err == nil {
		return ""
	}
	return err.Message
}

type ProtocolError struct {
	Message string
}

func (err *ProtocolError) Error() string {
	if err == nil {
		return ""
	}
	return err.Message
}

type callResult struct {
	value any
	err   error
}

type pendingCall struct {
	response      chan callResult
	orderedObject bool
}

type clientNotification struct {
	method string
	params any
}

type Client struct {
	server        Server
	conn          net.Conn
	timeout       time.Duration
	maxFrameSize  int
	handler       NotificationHandler
	slots         chan struct{}
	notifications chan clientNotification

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[int64]pendingCall
	lastErr error
	lastRx  time.Time
	lastTx  time.Time
	close   sync.Once
}

func Dial(ctx context.Context, server Server, config ClientConfig) (*Client, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: dial context is nil", ErrInvalidClient)
	}
	normalized, err := config.normalized()
	if err != nil {
		return nil, err
	}
	dialCtx, cancel := context.WithTimeout(ctx, normalized.ConnectTimeout)
	defer cancel()
	connection, err := normalized.Dialer.DialContext(dialCtx, "tcp", server.Address())
	if err != nil {
		return nil, &ConnectionError{Operation: "connect", Server: server, Err: err}
	}
	return newClient(server, connection, normalized), nil
}

func newClient(server Server, connection net.Conn, config ClientConfig) *Client {
	ctx, cancel := context.WithCancel(context.Background())
	client := &Client{
		server:        server,
		conn:          connection,
		timeout:       config.RequestTimeout,
		maxFrameSize:  config.MaxFrameSize,
		handler:       config.NotificationHandler,
		slots:         make(chan struct{}, config.Concurrency),
		notifications: make(chan clientNotification, config.Concurrency),
		ctx:           ctx,
		cancel:        cancel,
		done:          make(chan struct{}),
		pending:       make(map[int64]pendingCall),
		lastRx:        time.Now(),
		lastTx:        time.Now(),
	}
	go client.notificationLoop()
	go client.readLoop()
	return client
}

func (client *Client) Server() Server {
	if client == nil {
		return Server{}
	}
	return client.server
}

func (client *Client) Done() <-chan struct{} {
	if client == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return client.done
}

func (client *Client) Err() error {
	if client == nil {
		return ErrClientClosed
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.lastErr
}

func (client *Client) IsConnected() bool {
	if client == nil {
		return false
	}
	select {
	case <-client.done:
		return false
	default:
		return true
	}
}

func (client *Client) Close() error {
	if client == nil {
		return nil
	}
	client.shutdown(ErrClientClosed)
	return nil
}

func (client *Client) SetNotificationHandler(handler NotificationHandler) {
	if client == nil {
		return
	}
	client.mu.Lock()
	client.handler = handler
	client.mu.Unlock()
}

func (client *Client) Call(ctx context.Context, method string, params []any) (any, error) {
	return client.call(ctx, method, false, func(requestID int64) ([]byte, error) {
		return encodeRequest(method, params, requestID)
	})
}

// CallOrderedObject is Call with insertion-order preservation enabled when
// the top-level result is a JSON object. Non-object results retain the normal
// decoding contract.
func (client *Client) CallOrderedObject(ctx context.Context, method string, params []any) (any, error) {
	return client.call(ctx, method, true, func(requestID int64) ([]byte, error) {
		return encodeRequest(method, params, requestID)
	})
}

// CallNamed sends params as a JSON object, including an empty object when
// params is nil or empty.
func (client *Client) CallNamed(ctx context.Context, method string, params map[string]any) (any, error) {
	return client.call(ctx, method, false, func(requestID int64) ([]byte, error) {
		return encodeNamedRequest(method, params, requestID)
	})
}

func (client *Client) call(
	ctx context.Context, method string, orderedObject bool,
	encode func(int64) ([]byte, error),
) (any, error) {
	if client == nil {
		return nil, ErrClientClosed
	}
	if ctx == nil {
		return nil, errors.New("SPV request context is nil")
	}
	select {
	case client.slots <- struct{}{}:
		defer func() { <-client.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-client.done:
		return nil, client.terminalError()
	}

	requestID := globalRequestNumber.Add(1) - 1
	response := make(chan callResult, 1)
	client.mu.Lock()
	select {
	case <-client.done:
		client.mu.Unlock()
		return nil, client.terminalError()
	default:
		client.pending[requestID] = pendingCall{
			response: response, orderedObject: orderedObject,
		}
	}
	client.mu.Unlock()
	defer client.removePending(requestID, response)

	message, err := encode(requestID)
	if err != nil {
		return nil, err
	}
	if err := client.write(ctx, message); err != nil {
		connectionErr := &ConnectionError{Operation: "write", Server: client.server, Err: err}
		client.shutdown(connectionErr)
		return nil, connectionErr
	}

	timer := time.NewTimer(client.timeout)
	defer timer.Stop()
	for {
		select {
		case result := <-response:
			return result.value, result.err
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-client.done:
			select {
			case result := <-response:
				return result.value, result.err
			default:
				return nil, client.terminalError()
			}
		case <-timer.C:
			if client.packetReceivedWithin(client.timeout) {
				timer.Reset(client.timeout)
				continue
			}
			return nil, fmt.Errorf("%w: %s", ErrRequestTimeout, method)
		}
	}
}

func encodeRequest(method string, params []any, requestID int64) ([]byte, error) {
	return encodeRequestParams(method, params, requestID, len(params) > 0)
}

func encodeNamedRequest(method string, params map[string]any, requestID int64) ([]byte, error) {
	if params == nil {
		params = map[string]any{}
	}
	return encodeRequestParams(method, params, requestID, true)
}

func encodeRequestParams(method string, params any, requestID int64, includeParams bool) ([]byte, error) {
	tokens, err := newSPVSpecialFloatTokens()
	if err != nil {
		return nil, &ProtocolError{Message: fmt.Sprintf("JSON payload encoding error: %v", err)}
	}
	if includeParams {
		params = allowSPVSpecialJSONFloats(params, tokens)
	} else {
		params = nil
	}
	payload := struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		ID      int64  `json:"id"`
		Params  any    `json:"params,omitempty"`
	}{
		JSONRPC: "2.0",
		Method:  method,
		ID:      requestID,
		Params:  params,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, &ProtocolError{Message: fmt.Sprintf("JSON payload encoding error: %v", err)}
	}
	for placeholder, literal := range map[string]string{
		tokens.nan:         "NaN",
		tokens.infinity:    "Infinity",
		tokens.negInfinity: "-Infinity",
	} {
		quoted, _ := json.Marshal(placeholder)
		encoded = bytes.ReplaceAll(encoded, quoted, []byte(literal))
	}
	return append(encoded, '\n'), nil
}

type spvSpecialFloatTokens struct {
	nan         string
	infinity    string
	negInfinity string
}

func newSPVSpecialFloatTokens() (spvSpecialFloatTokens, error) {
	var nonce [16]byte
	if _, err := cryptorand.Read(nonce[:]); err != nil {
		return spvSpecialFloatTokens{}, err
	}
	prefix := "\x00LBRY_SPV_JSON_" + hex.EncodeToString(nonce[:]) + "_"
	return spvSpecialFloatTokens{
		nan:         prefix + "NAN\x00",
		infinity:    prefix + "INFINITY\x00",
		negInfinity: prefix + "NEG_INFINITY\x00",
	}, nil
}

func allowSPVSpecialJSONFloats(value any, tokens spvSpecialFloatTokens) any {
	switch typed := value.(type) {
	case float64:
		switch {
		case math.IsNaN(typed):
			return tokens.nan
		case math.IsInf(typed, 1):
			return tokens.infinity
		case math.IsInf(typed, -1):
			return tokens.negInfinity
		default:
			return typed
		}
	case []any:
		converted := make([]any, len(typed))
		for index, item := range typed {
			converted[index] = allowSPVSpecialJSONFloats(item, tokens)
		}
		return converted
	case map[string]any:
		converted := make(map[string]any, len(typed))
		for name, item := range typed {
			converted[name] = allowSPVSpecialJSONFloats(item, tokens)
		}
		return converted
	default:
		return value
	}
}

func (client *Client) write(ctx context.Context, message []byte) error {
	client.writeMu.Lock()
	defer client.writeMu.Unlock()
	deadline := time.Now().Add(client.timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := client.conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	defer client.conn.SetWriteDeadline(time.Time{})
	for len(message) > 0 {
		written, err := client.conn.Write(message)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrUnexpectedEOF
		}
		message = message[written:]
	}
	client.mu.Lock()
	client.lastTx = time.Now()
	client.mu.Unlock()
	return nil
}

func (client *Client) readLoop() {
	reader := bufio.NewReaderSize(client.conn, 64*1024)
	for {
		message, err := client.readFrame(reader)
		if err != nil {
			if errors.Is(err, ErrFrameTooLarge) {
				client.shutdown(err)
			} else {
				client.shutdown(&ConnectionError{Operation: "read", Server: client.server, Err: err})
			}
			return
		}
		if err := client.handleMessage(message); err != nil {
			var protocolErr *ProtocolError
			if errors.As(err, &protocolErr) && protocolErr.Message == "invalid JSON" {
				client.shutdown(err)
				return
			}
		}
	}
}

func (client *Client) readFrame(reader *bufio.Reader) ([]byte, error) {
	message := make([]byte, 0, min(client.maxFrameSize, 64*1024))
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(fragment) > 0 {
			client.markPacketReceived()
		}
		if len(message) > client.maxFrameSize-len(fragment) {
			return nil, fmt.Errorf("%w: limit %d bytes", ErrFrameTooLarge, client.maxFrameSize)
		}
		message = append(message, fragment...)
		switch {
		case err == nil:
			return bytes.TrimSuffix(message, []byte{'\n'}), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		default:
			return nil, err
		}
	}
}

func (client *Client) handleMessage(message []byte) error {
	normalized, tokens, err := normalizeSPVJSON(message)
	if err != nil {
		return &ProtocolError{Message: "invalid JSON"}
	}
	trimmed := bytes.TrimSpace(normalized)
	if len(trimmed) == 0 {
		return &ProtocolError{Message: "invalid JSON"}
	}
	if trimmed[0] == '[' {
		var payloads []json.RawMessage
		decoder := json.NewDecoder(bytes.NewReader(trimmed))
		decoder.UseNumber()
		if err := decoder.Decode(&payloads); err != nil {
			return &ProtocolError{Message: "invalid JSON"}
		}
		if err := requireJSONEnd(decoder); err != nil {
			return &ProtocolError{Message: "invalid JSON"}
		}
		if len(payloads) == 0 {
			return &ProtocolError{Message: "batch is empty"}
		}
		for _, payload := range payloads {
			if err := client.handlePayload(payload, tokens); err != nil {
				return err
			}
		}
		return nil
	}
	if trimmed[0] != '{' {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(trimmed))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return &ProtocolError{Message: "invalid JSON"}
		}
		if err := requireJSONEnd(decoder); err != nil {
			return &ProtocolError{Message: "invalid JSON"}
		}
		return &ProtocolError{Message: "request object must be a dictionary"}
	}
	return client.handlePayload(trimmed, tokens)
}

func (client *Client) handlePayload(message []byte, tokens spvSpecialFloatTokens) error {
	var payload map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(message))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return &ProtocolError{Message: "invalid JSON"}
	}
	if err := requireJSONEnd(decoder); err != nil {
		return &ProtocolError{Message: "invalid JSON"}
	}
	if payload == nil {
		return &ProtocolError{Message: "request object must be a dictionary"}
	}
	var version string
	if err := json.Unmarshal(payload["jsonrpc"], &version); err != nil || version != "2.0" {
		return client.routeProtocolError(payload, `"jsonrpc" is not "2.0"`)
	}
	if rawMethod, exists := payload["method"]; exists {
		var method string
		if err := json.Unmarshal(rawMethod, &method); err != nil {
			return &ProtocolError{Message: "method must be a string"}
		}
		params := any([]any{})
		if rawParams, exists := payload["params"]; exists {
			decoded, err := decodeJSONValueWithSpecialFloats(rawParams, tokens)
			if err != nil {
				return &ProtocolError{Message: fmt.Sprintf("invalid request arguments: %v", err)}
			}
			switch decoded.(type) {
			case []any, map[string]any:
			default:
				return &ProtocolError{Message: fmt.Sprintf("invalid request arguments: %v", decoded)}
			}
			params = decoded
		}
		select {
		case client.notifications <- clientNotification{method: method, params: params}:
		case <-client.ctx.Done():
		}
		return nil
	}

	rawID, exists := payload["id"]
	if !exists {
		return &ProtocolError{Message: `request has no "id"`}
	}
	requestID, err := decodeRequestID(rawID)
	if err != nil {
		return err
	}
	result, resultExists := payload["result"]
	rawError, errorExists := payload["error"]
	if resultExists && errorExists {
		client.deliver(requestID, callResult{err: &ProtocolError{Message: `response contains both "result" and "error"`}})
		return nil
	}
	if !resultExists && !errorExists {
		client.deliver(requestID, callResult{err: &ProtocolError{Message: `response contains neither "result" nor "error"`}})
		return nil
	}
	if errorExists {
		rpcErr, err := decodeRPCError(rawError)
		if err != nil {
			client.deliver(requestID, callResult{err: err})
		} else {
			client.deliver(requestID, callResult{err: rpcErr})
		}
		return nil
	}
	var decoded any
	if client.pendingCallPreservesObjectOrder(requestID) {
		decoded, err = decodeJSONValueWithOrderedObjectAndSpecialFloats(result, tokens)
	} else {
		decoded, err = decodeJSONValueWithSpecialFloats(result, tokens)
	}
	if err != nil {
		client.deliver(requestID, callResult{err: &ProtocolError{Message: err.Error()}})
		return nil
	}
	client.deliver(requestID, callResult{value: decoded})
	return nil
}

// normalizeSPVJSON retains CPython json.loads compatibility for the legacy
// NaN and Infinity literals before handing the payload to encoding/json.
func normalizeSPVJSON(encoded []byte) ([]byte, spvSpecialFloatTokens, error) {
	tokens, err := newSPVSpecialFloatTokens()
	if err != nil {
		return nil, spvSpecialFloatTokens{}, err
	}
	var normalized bytes.Buffer
	inString, escaped := false, false
	for index := 0; index < len(encoded); {
		value := encoded[index]
		if inString {
			normalized.WriteByte(value)
			index++
			switch {
			case escaped:
				escaped = false
			case value == '\\':
				escaped = true
			case value == '"':
				inString = false
			}
			continue
		}
		if value == '"' {
			inString = true
			normalized.WriteByte(value)
			index++
			continue
		}
		var token string
		switch {
		case bytes.HasPrefix(encoded[index:], []byte("-Infinity")):
			token = tokens.negInfinity
			index += len("-Infinity")
		case bytes.HasPrefix(encoded[index:], []byte("Infinity")):
			token = tokens.infinity
			index += len("Infinity")
		case bytes.HasPrefix(encoded[index:], []byte("NaN")):
			token = tokens.nan
			index += len("NaN")
		default:
			normalized.WriteByte(value)
			index++
			continue
		}
		quoted, err := json.Marshal(token)
		if err != nil {
			return nil, spvSpecialFloatTokens{}, err
		}
		normalized.Write(quoted)
	}
	return normalized.Bytes(), tokens, nil
}

func (client *Client) notificationLoop() {
	for {
		select {
		case notification := <-client.notifications:
			client.mu.Lock()
			handler := client.handler
			client.mu.Unlock()
			if handler != nil {
				handler(client.ctx, notification.method, notification.params)
			}
		case <-client.ctx.Done():
			return
		}
	}
}

func (client *Client) routeProtocolError(payload map[string]json.RawMessage, message string) error {
	protocolErr := &ProtocolError{Message: message}
	if _, isRequest := payload["method"]; isRequest {
		return protocolErr
	}
	rawID, exists := payload["id"]
	if !exists {
		return protocolErr
	}
	requestID, err := decodeRequestID(rawID)
	if err != nil {
		return protocolErr
	}
	client.deliver(requestID, callResult{err: protocolErr})
	return nil
}

func decodeRequestID(raw json.RawMessage) (int64, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return 0, &ProtocolError{Message: fmt.Sprintf(`invalid "id": %s`, raw)}
	}
	switch typed := value.(type) {
	case json.Number:
		if requestID, err := typed.Int64(); err == nil {
			return requestID, nil
		}
		floating, err := typed.Float64()
		if err == nil && math.Trunc(floating) == floating &&
			floating >= math.MinInt64 && floating <= math.MaxInt64 {
			return int64(floating), nil
		}
	case bool:
		// CPython bool is a numbers.Number, and True/False compare equal to
		// integer request IDs 1/0 in JSONRPCConnection's pending map.
		if typed {
			return 1, nil
		}
		return 0, nil
	}
	return 0, &ProtocolError{Message: fmt.Sprintf(`invalid "id": %s`, raw)}
}

func decodeRPCError(raw json.RawMessage) (*RPCError, error) {
	var payload struct {
		Code    json.Number `json:"code"`
		Message any         `json:"message"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, &ProtocolError{Message: fmt.Sprintf("ill-formed response error object: %s", raw)}
	}
	code, err := payload.Code.Int64()
	message, ok := payload.Message.(string)
	if err != nil || !ok {
		return nil, &ProtocolError{Message: fmt.Sprintf("ill-formed response error object: %s", raw)}
	}
	return &RPCError{Code: code, Message: message}, nil
}

func decodeJSONValue(raw json.RawMessage) (any, error) {
	return decodeJSONValueWithSpecialFloats(raw, spvSpecialFloatTokens{})
}

func decodeJSONValueWithSpecialFloats(
	raw json.RawMessage, tokens spvSpecialFloatTokens,
) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := requireJSONEnd(decoder); err != nil {
		return nil, err
	}
	return restoreSPVSpecialJSONFloats(value, tokens), nil
}

func decodeJSONValueWithOrderedObject(raw json.RawMessage) (any, error) {
	return decodeJSONValueWithOrderedObjectAndSpecialFloats(raw, spvSpecialFloatTokens{})
}

func decodeJSONValueWithOrderedObjectAndSpecialFloats(
	raw json.RawMessage, tokens spvSpecialFloatTokens,
) (any, error) {
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || trimmed[0] != '{' {
		return decodeJSONValueWithSpecialFloats(raw, tokens)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	opening, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if opening != json.Delim('{') {
		return nil, errors.New("JSON value is not an object")
	}
	object := newOrderedObject()
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("JSON object key is not a string")
		}
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		object.set(key, restoreSPVSpecialJSONFloats(value, tokens))
	}
	closing, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if closing != json.Delim('}') {
		return nil, errors.New("JSON object is not terminated")
	}
	if err := requireJSONEnd(decoder); err != nil {
		return nil, err
	}
	return object, nil
}

func restoreSPVSpecialJSONFloats(value any, tokens spvSpecialFloatTokens) any {
	switch typed := value.(type) {
	case string:
		switch typed {
		case tokens.nan:
			if tokens.nan != "" {
				return math.NaN()
			}
		case tokens.infinity:
			if tokens.infinity != "" {
				return math.Inf(1)
			}
		case tokens.negInfinity:
			if tokens.negInfinity != "" {
				return math.Inf(-1)
			}
		}
		return typed
	case []any:
		for index, item := range typed {
			typed[index] = restoreSPVSpecialJSONFloats(item, tokens)
		}
		return typed
	case map[string]any:
		for key, item := range typed {
			typed[key] = restoreSPVSpecialJSONFloats(item, tokens)
		}
		return typed
	default:
		return value
	}
}

func requireJSONEnd(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func (client *Client) deliver(requestID int64, result callResult) {
	client.mu.Lock()
	pending := client.pending[requestID]
	client.mu.Unlock()
	if pending.response == nil {
		return
	}
	select {
	case pending.response <- result:
	default:
	}
}

func (client *Client) pendingCallPreservesObjectOrder(requestID int64) bool {
	client.mu.Lock()
	pending := client.pending[requestID]
	client.mu.Unlock()
	return pending.orderedObject
}

func (client *Client) removePending(requestID int64, response chan callResult) {
	client.mu.Lock()
	if client.pending[requestID].response == response {
		delete(client.pending, requestID)
	}
	client.mu.Unlock()
}

func (client *Client) shutdown(err error) {
	client.close.Do(func() {
		if err == nil {
			err = ErrClientClosed
		}
		client.cancel()
		_ = client.conn.Close()
		client.mu.Lock()
		client.lastErr = err
		pending := client.pending
		client.pending = make(map[int64]pendingCall)
		close(client.done)
		client.mu.Unlock()
		for _, pending := range pending {
			select {
			case pending.response <- callResult{err: err}:
			default:
			}
		}
	})
}

func (client *Client) terminalError() error {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.lastErr == nil {
		return ErrClientClosed
	}
	return client.lastErr
}

func (client *Client) markPacketReceived() {
	client.mu.Lock()
	client.lastRx = time.Now()
	client.mu.Unlock()
}

func (client *Client) packetReceivedWithin(window time.Duration) bool {
	client.mu.Lock()
	lastPacket := client.lastRx
	client.mu.Unlock()
	return time.Since(lastPacket) < window
}

func (client *Client) activity() (lastSend, lastReceive time.Time) {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.lastTx, client.lastRx
}
