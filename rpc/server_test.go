package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func performRequest(server *RPCServer, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload map[string]any
	decoder := json.NewDecoder(response.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		t.Fatalf("decode response: %v\nbody: %s", err, response.Body.String())
	}
	return payload
}

func assertRPCError(t *testing.T, response *httptest.ResponseRecorder, code json.Number, message string) map[string]any {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("HTTP status = %d, want 200; body: %s", response.Code, response.Body.String())
	}
	payload := decodeResponse(t, response)
	if _, exists := payload["id"]; exists {
		t.Fatal("legacy response must not include request id")
	}
	errorObject, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("error object missing: %#v", payload)
	}
	if got := errorObject["code"]; got != code {
		t.Fatalf("error code = %#v, want %s", got, code)
	}
	if got := errorObject["message"]; got != message {
		t.Fatalf("error message = %#v, want %q", got, message)
	}
	if _, exists := errorObject["data"]; !exists {
		t.Fatal("legacy error must always include data")
	}
	return errorObject
}

func TestMethodContractMatchesHandlerRegistry(t *testing.T) {
	if got, want := len(methodSpecs), 93; got != want {
		t.Fatalf("active method specs = %d, want %d", got, want)
	}
	if got, want := len(handlers), 93; got != want {
		t.Fatalf("active handlers = %d, want %d", got, want)
	}
	for name := range methodSpecs {
		if _, exists := handlers[name]; !exists {
			t.Errorf("method %q has no handler", name)
		}
	}
	for name := range handlers {
		if _, exists := methodSpecs[name]; !exists {
			t.Errorf("handler %q has no Python signature", name)
		}
	}
	if got := deprecatedMethods["channel_new"]; got != "channel_create" {
		t.Fatalf("channel_new replacement = %q, want channel_create", got)
	}
}

func TestRPCTransportRoutes(t *testing.T) {
	server := CreateServer()
	validBody := `{"method":"version"}`
	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantBody   string
		wantJSON   bool
		wantNoBody bool
	}{
		{name: "post root", method: http.MethodPost, path: "/", wantStatus: 200, wantJSON: true},
		{name: "post legacy path", method: http.MethodPost, path: "/lbryapi", wantStatus: 200, wantJSON: true},
		{name: "get legacy path", method: http.MethodGet, path: "/lbryapi?ignored=true", wantStatus: 200, wantJSON: true},
		{name: "head legacy path", method: http.MethodHead, path: "/lbryapi", wantStatus: 200, wantNoBody: true},
		{name: "get root", method: http.MethodGet, path: "/", wantStatus: 405, wantBody: "405: Method Not Allowed"},
		{name: "options legacy path", method: http.MethodOptions, path: "/lbryapi", wantStatus: 405, wantBody: "405: Method Not Allowed"},
		{name: "trailing slash", method: http.MethodPost, path: "/lbryapi/", wantStatus: 404, wantBody: "404: Not Found"},
		{name: "unknown path", method: http.MethodPost, path: "/other", wantStatus: 404, wantBody: "404: Not Found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := performRequest(server, test.method, test.path, validBody, nil)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", response.Code, test.wantStatus, response.Body.String())
			}
			if test.wantBody != "" && response.Body.String() != test.wantBody {
				t.Fatalf("body = %q, want %q", response.Body.String(), test.wantBody)
			}
			if test.wantNoBody && response.Body.Len() != 0 {
				t.Fatalf("HEAD body = %q, want empty", response.Body.String())
			}
			if test.wantJSON {
				if got := response.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
					t.Fatalf("content type = %q", got)
				}
				decodeResponse(t, response)
			}
		})
	}

	assertDefaultOptionsAborts(t, server)
}

func assertDefaultOptionsAborts(t *testing.T, server *RPCServer) {
	t.Helper()
	request := httptest.NewRequest(http.MethodOptions, "/", nil)
	response := httptest.NewRecorder()
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		server.ServeHTTP(response, request)
	}()
	if recovered != http.ErrAbortHandler {
		t.Fatalf("default OPTIONS panic = %#v, want http.ErrAbortHandler", recovered)
	}
}

func TestDefaultOptionsClosesConnectionWithoutResponse(t *testing.T) {
	server := httptest.NewServer(CreateServer())
	defer server.Close()
	request, err := http.NewRequest(http.MethodOptions, server.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(request)
	if response != nil {
		response.Body.Close()
		t.Fatalf("default OPTIONS unexpectedly returned status %d", response.StatusCode)
	}
	if err == nil {
		t.Fatal("default OPTIONS unexpectedly returned an HTTP response")
	}
}

func TestOriginPolicyAndLegacyCORSHeaders(t *testing.T) {
	body := `{"method":"version"}`
	defaultServer := CreateServer()
	if response := performRequest(defaultServer, http.MethodPost, "/", body, nil); response.Code != 200 {
		t.Fatalf("request without Origin = %d", response.Code)
	}
	rejected := performRequest(defaultServer, http.MethodPost, "/", body, map[string]string{"Origin": "null"})
	if rejected.Code != 403 || rejected.Body.String() != "403: Forbidden" {
		t.Fatalf("rejected origin = %d %q", rejected.Code, rejected.Body.String())
	}
	if got := rejected.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("rejected response CORS origin = %q, want empty", got)
	}

	server := CreateServer(WithAllowedOrigin("localhost"))
	allowed := performRequest(server, http.MethodPost, "/", body, map[string]string{"Origin": "localhost"})
	if allowed.Code != 200 {
		t.Fatalf("allowed origin status = %d", allowed.Code)
	}
	for _, header := range []string{
		"Access-Control-Allow-Origin",
		"Access-Control-Allow-Methods",
		"Access-Control-Allow-Headers",
	} {
		if got := allowed.Header().Get(header); got != "localhost" {
			t.Errorf("%s = %q, want localhost", header, got)
		}
	}
	mismatch := performRequest(server, http.MethodPost, "/", body, map[string]string{"Origin": "example.com"})
	if mismatch.Code != 403 || mismatch.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("mismatched origin response = %d, CORS %q", mismatch.Code, mismatch.Header().Get("Access-Control-Allow-Origin"))
	}
	options := performRequest(server, http.MethodOptions, "/", "", map[string]string{"Origin": "example.com"})
	if options.Code != 200 || options.Header().Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("configured OPTIONS = %d, type %q", options.Code, options.Header().Get("Content-Type"))
	}

	starServer := CreateServer(WithAllowedOrigin("*"))
	star := performRequest(starServer, http.MethodPost, "/", body, map[string]string{"Origin": "example.com"})
	if star.Code != 200 || star.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("wildcard origin response = %d, CORS %q", star.Code, star.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestMalformedRequestsMatchLegacyHTTPFailure(t *testing.T) {
	server := CreateServer(WithAllowedOrigin("localhost"))
	tests := []string{
		"",
		"   ",
		"{",
		"[]",
		"null",
		"true",
		`{"method":"version"} {}`,
		`{"method":["version"]}`,
		`{"method":{"name":"version"}}`,
		`{Infinity:1}`,
		`{"method":"version","params":{NaN : 1}}`,
		`{"method":"version","params":[Infinityx]}`,
	}
	for _, body := range tests {
		response := performRequest(server, http.MethodPost, "/", body, nil)
		if response.Code != 500 {
			t.Errorf("body %q: status = %d, want 500", body, response.Code)
			continue
		}
		if got, want := response.Body.String(), "500 Internal Server Error\n\nServer got itself in trouble"; got != want {
			t.Errorf("body %q: response body = %q, want %q", body, got, want)
		}
		if got := response.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
			t.Errorf("body %q: content type = %q", body, got)
		}
		if got := response.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("body %q: unexpected CORS header %q", body, got)
		}
	}
}

func TestRequestBodyLimit(t *testing.T) {
	server := CreateServer()
	base := `{"method":"version"}`
	belowLimit := base + strings.Repeat(" ", int(legacyRPCMaxRequestBodySize)-len(base)-1)
	accepted := performRequest(server, http.MethodPost, "/", belowLimit, nil)
	if accepted.Code != http.StatusOK {
		t.Fatalf("request below body limit = %d, body %s", accepted.Code, accepted.Body.String())
	}

	for _, size := range []int64{legacyRPCMaxRequestBodySize, legacyRPCMaxRequestBodySize + 1} {
		body := base + strings.Repeat(" ", int(size)-len(base))
		rejected := performRequest(server, http.MethodPost, "/", body, nil)
		wantBody := fmt.Sprintf(
			"Maximum request body size %d exceeded, actual body size %d",
			legacyRPCMaxRequestBodySize, size,
		)
		if rejected.Code != http.StatusRequestEntityTooLarge ||
			rejected.Header().Get("Content-Type") != "text/plain; charset=utf-8" ||
			rejected.Body.String() != wantBody {
			t.Fatalf("request size %d = %d, type %q, body %q", size,
				rejected.Code, rejected.Header().Get("Content-Type"), rejected.Body.String())
		}
	}
}

func TestRequestCharsetMatchesAiohttpTextDecoding(t *testing.T) {
	server := CreateServer()
	latin1 := []byte(`{"method":"settings_set","params":{"key":"jurisdiction","value":"caf`)
	latin1 = append(latin1, 0xe9)
	latin1 = append(latin1, []byte(`"}}`)...)
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(latin1))
	request.Header.Set("Content-Type", "text/plain; charset=iso-8859-1")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	payload := decodeResponse(t, response)
	result, _ := payload["result"].(map[string]any)
	if response.Code != http.StatusOK || result["jurisdiction"] != "café" {
		t.Fatalf("Latin-1 request = %d %#v", response.Code, payload)
	}

	for _, contentType := range []string{
		"application/json; charset=does-not-exist",
		"application/json; charset=us-ascii",
	} {
		request = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(latin1))
		request.Header.Set("Content-Type", contentType)
		response = httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusInternalServerError ||
			response.Body.String() != "500 Internal Server Error\n\nServer got itself in trouble" {
			t.Fatalf("invalid charset %q = %d %q", contentType, response.Code, response.Body.String())
		}
	}
}

func TestDispatcherCompatibilityErrors(t *testing.T) {
	server := CreateServer()
	tests := []struct {
		name    string
		body    string
		code    json.Number
		message string
	}{
		{name: "missing method", body: `{}`, code: "-32601", message: "Missing 'method' value in request."},
		{name: "unknown method", body: `{"method":"wat"}`, code: "-32601", message: "Command 'wat' does not exist."},
		{name: "null method", body: `{"method":null}`, code: "-32601", message: "Command 'None' does not exist."},
		{name: "boolean method", body: `{"method":true}`, code: "-32601", message: "Command 'True' does not exist."},
		{name: "null params", body: `{"method":"version","params":null}`, code: "-32602", message: "Invalid parameters format: None"},
		{name: "boolean params", body: `{"method":"version","params":true}`, code: "-32602", message: "Invalid parameters format: True"},
		{name: "string params", body: `{"method":"version","params":"abc"}`, code: "-32602", message: "Invalid parameters format: abc"},
		{name: "standard positional params rejected", body: `{"method":"version","params":["a"]}`, code: "-32602", message: "Invalid parameters format: ['a']"},
		{name: "extraneous order", body: `{"method":"version","params":{"z":1,"a":2}}`, code: "-32602", message: "Extraneous parameters for version command: z, a"},
		{name: "duplicate JSON key keeps first position", body: `{"method":"version","params":{"z":1,"z":2}}`, code: "-32602", message: "Extraneous parameters for version command: z"},
		{name: "wrapped include protobuf remains", body: `{"method":"version","params":[{"include_protobuf":true}]}`, code: "-32602", message: "Extraneous parameters for version command: include_protobuf"},
		{name: "missing get uri", body: `{"method":"get","params":{}}`, code: "-32602", message: "Missing required parameters for get command: uri"},
		{name: "duplicate legacy argument", body: `{"method":"settings_set","params":[["theme","dark"],{"key":"other"}]}`, code: "-32602", message: "Duplicate parameters for settings_set command: key"},
		{name: "alias retains requested name", body: `{"method":"channel_new","params":{"extra":1}}`, code: "-32602", message: "Missing required parameters for channel_new command: name, bid"},
		{name: "missing required bug becomes application error", body: `{"method":"settings_set","params":{}}`, code: "-32500", message: "Daemon.jsonrpc_settings_set() missing 2 required positional arguments: 'key' and 'value'"},
		{name: "too many arguments become application error", body: `{"method":"version","params":[[1],{}]}`, code: "-32500", message: "Daemon.jsonrpc_version() takes 1 positional argument but 2 were given"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := performRequest(server, http.MethodPost, "/", test.body, nil)
			assertRPCError(t, response, test.code, test.message)
		})
	}
}

func TestAcceptedEmptyParameterFormsAndIgnoredEnvelopeFields(t *testing.T) {
	server := CreateServer()
	bodies := []string{
		`{"method":"version"}`,
		`{"method":"version","params":{}}`,
		`{"method":"version","params":[]}`,
		`{"method":"version","params":[{}]}`,
		`{"jsonrpc":"1.0","id":42,"extra":"ignored","method":"version","params":{"include_protobuf":true}}`,
	}
	for _, body := range bodies {
		response := performRequest(server, http.MethodPost, "/", body, nil)
		if response.Code != 200 {
			t.Fatalf("body %s: status = %d", body, response.Code)
		}
		payload := decodeResponse(t, response)
		if _, exists := payload["result"]; !exists {
			t.Fatalf("body %s: result missing: %#v", body, payload)
		}
		if _, exists := payload["id"]; exists {
			t.Fatalf("body %s: response unexpectedly echoed id", body)
		}
		if got := payload["jsonrpc"]; got != "2.0" {
			t.Fatalf("body %s: jsonrpc = %#v", body, got)
		}
	}
}

func TestTopLevelIncludeProtobufIsRetainedOutsideCommandKwargs(t *testing.T) {
	server := CreateServer()
	received := make(chan normalizedRPCParams, 1)
	server.handlers["version"] = func(w http.ResponseWriter, params any) {
		normalized := params.(normalizedRPCParams)
		received <- normalized
		sendResultResponse(w, true)
	}
	response := performRequest(
		server, http.MethodPost, "/",
		`{"method":"version","params":{"include_protobuf":[1]}}`, nil,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("include_protobuf status = %d", response.Code)
	}
	normalized := <-received
	if !reflect.DeepEqual(normalized.includeProtobuf, []any{json.Number("1")}) ||
		len(normalized.kwargs) != 0 || len(normalized.named) != 0 {
		t.Fatalf("normalized include_protobuf = %#v", normalized)
	}
}

func TestLegacyPositionalParametersPreserveLargeIntegers(t *testing.T) {
	server := CreateServer()
	received := make(chan normalizedRPCParams, 1)
	server.handlers["settings_set"] = func(w http.ResponseWriter, params any) {
		normalized := params.(normalizedRPCParams)
		received <- normalized
		sendResultResponse(w, normalized.named)
	}
	response := performRequest(
		server,
		http.MethodPost,
		"/",
		`{"method":"settings_set","params":[["limit",900719925474099312345],{}]}`,
		nil,
	)
	if response.Code != 200 {
		t.Fatalf("status = %d", response.Code)
	}
	normalized := <-received
	value, ok := normalized.named["value"].(json.Number)
	if !ok || value.String() != "900719925474099312345" {
		t.Fatalf("normalized value = %#v", normalized.named["value"])
	}
	if !strings.Contains(response.Body.String(), "900719925474099312345") {
		t.Fatalf("response lost integer precision: %s", response.Body.String())
	}
}

func TestHandlerPanicBecomesApplicationError(t *testing.T) {
	server := CreateServer()
	server.handlers["version"] = func(w http.ResponseWriter, _ any) {
		sendResultResponse(w, "partial response must be discarded")
		panic("boom")
	}
	response := performRequest(server, http.MethodPost, "/", `{"method":"version"}`, nil)
	errorObject := assertRPCError(t, response, "-32500", "boom")
	data, ok := errorObject["data"].(map[string]any)
	if !ok {
		t.Fatalf("application error data = %#v", errorObject["data"])
	}
	if data["command"] != "version" {
		t.Fatalf("application error command = %#v", data["command"])
	}
	if _, exists := data["traceback"]; !exists {
		t.Fatal("application error traceback missing")
	}
	if strings.Contains(response.Body.String(), "partial response") {
		t.Fatalf("panic leaked partial response: %s", response.Body.String())
	}

	server.handlers["wallet_unlock"] = func(http.ResponseWriter, any) {
		panic("unlock failed")
	}
	passwordResponse := performRequest(
		server,
		http.MethodPost,
		"/",
		`{"method":"wallet_unlock","params":{"password":"secret"}}`,
		nil,
	)
	passwordError := assertRPCError(t, passwordResponse, "-32500", "unlock failed")
	passwordData := passwordError["data"].(map[string]any)
	passwordKwargs := passwordData["kwargs"].(map[string]any)
	if got := passwordKwargs["password"]; got != "******" {
		t.Fatalf("redacted password = %#v, want six asterisks", got)
	}
}

func TestRawErrorEncodingMatchesPythonFormatting(t *testing.T) {
	server := CreateServer()
	response := performRequest(server, http.MethodPost, "/", `{"method":"wat"}`, nil)
	want := "{\n" +
		"  \"error\": {\n" +
		"    \"code\": -32601,\n" +
		"    \"data\": {},\n" +
		"    \"message\": \"Command 'wat' does not exist.\"\n" +
		"  },\n" +
		"  \"jsonrpc\": \"2.0\"\n" +
		"}\n"
	if got := response.Body.String(); got != want {
		t.Fatalf("raw response mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestJSONEncodingMatchesPythonASCIIAndHTMLEscaping(t *testing.T) {
	server := CreateServer()
	server.handlers["version"] = func(w http.ResponseWriter, _ any) {
		sendResultResponse(w, "<caf\u00e9> \U0001F600")
	}
	response := performRequest(server, http.MethodPost, "/", `{"method":"version"}`, nil)
	if got, want := response.Body.String(), "<caf\\u00e9> \\ud83d\\ude00"; !strings.Contains(got, want) {
		t.Fatalf("encoded response %q does not contain %q", got, want)
	}
}

func TestJSONEncodingAllowsPythonSpecialFloats(t *testing.T) {
	server := CreateServer()
	server.handlers["version"] = func(w http.ResponseWriter, _ any) {
		sendResultResponse(w, []any{math.NaN(), math.Inf(1), math.Inf(-1)})
	}
	response := performRequest(server, http.MethodPost, "/", `{"method":"version"}`, nil)
	if got := response.Body.String(); !strings.Contains(got, "NaN") ||
		!strings.Contains(got, "Infinity") || !strings.Contains(got, "-Infinity") {
		t.Fatalf("special float response = %s", got)
	}
	server.handlers["version"] = func(w http.ResponseWriter, _ any) {
		sendResultResponse(w, "\x00LBRY_JSON_NAN\x00")
	}
	stringResponse := performRequest(server, http.MethodPost, "/", `{"method":"version"}`, nil)
	if result := decodeResponse(t, stringResponse)["result"]; result != "\x00LBRY_JSON_NAN\x00" {
		t.Fatalf("reserved-looking string changed: %#v", result)
	}
}

func TestResultEncodingFailureMatchesApplicationError(t *testing.T) {
	server := CreateServer()
	server.handlers["version"] = func(w http.ResponseWriter, _ any) {
		sendResultResponse(w, func() {})
	}
	response := performRequest(server, http.MethodPost, "/", `{"method":"version"}`, nil)
	errorObject := assertRPCError(
		t,
		response,
		"-32500",
		"After successfully executing the command, failed to encode result for JSON RPC response.",
	)
	data := errorObject["data"].(map[string]any)
	if _, ok := data["traceback"].(string); !ok {
		t.Fatalf("encoding traceback = %#v, want string", data["traceback"])
	}
}

func TestVersionWalletFallbackAndStop(t *testing.T) {
	var stopCalls atomic.Int32
	stopped := make(chan struct{})
	t.Setenv("XDG_CURRENT_DESKTOP", "SnapshotDesktop")
	server := CreateServer(WithShutdown(func() {
		if stopCalls.Add(1) == 1 {
			close(stopped)
		}
	}))
	t.Setenv("XDG_CURRENT_DESKTOP", "ChangedAfterStartup")

	versionResponse := performRequest(server, http.MethodPost, "/", `{"method":"version"}`, nil)
	versionPayload := decodeResponse(t, versionResponse)
	version := versionPayload["result"].(map[string]any)
	for _, key := range []string{
		"processor", "python_version", "platform", "os_release", "os_system",
		"lbrynet_version", "version", "build",
	} {
		if _, ok := version[key].(string); !ok {
			t.Errorf("version[%q] = %#v, want string", key, version[key])
		}
	}
	if version["version"] != "0.113.0" || version["lbrynet_version"] != "0.113.0" || version["build"] != "dev" {
		t.Fatalf("stable version fields = %#v", version)
	}
	if runtime.GOOS == "linux" && version["os_system"] == "Linux" {
		if version["desktop"] != "SnapshotDesktop" {
			t.Fatalf("desktop snapshot = %#v", version["desktop"])
		}
		if _, ok := version["distro"].(map[string]any); !ok {
			t.Fatalf("Linux distro info = %#v", version["distro"])
		}
	}

	walletResponse := performRequest(server, http.MethodPost, "/", `{"method":"wallet_status"}`, nil)
	walletPayload := decodeResponse(t, walletResponse)
	wallet := walletPayload["result"].(map[string]any)
	if len(wallet) != 3 || wallet["is_encrypted"] != nil || wallet["is_locked"] != nil || wallet["is_syncing"] != nil {
		t.Fatalf("wallet fallback = %#v", wallet)
	}

	for range 2 {
		stopResponse := performRequest(server, http.MethodPost, "/", `{"method":"stop"}`, nil)
		stopPayload := decodeResponse(t, stopResponse)
		if stopPayload["result"] != "Shutting down" {
			t.Fatalf("stop result = %#v", stopPayload["result"])
		}
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("shutdown callback was not called")
	}
	if got := stopCalls.Load(); got != 1 {
		t.Fatalf("shutdown callback calls = %d, want 1", got)
	}
}
