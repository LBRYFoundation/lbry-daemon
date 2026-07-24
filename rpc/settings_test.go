package rpc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	daemonconfig "lbry/daemon/config"
)

func persistentSettingsStore(t *testing.T) (*daemonconfig.Store, string) {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, "daemon_settings.yml")
	paths := daemonconfig.Paths{
		Config:      path,
		DataDir:     directory,
		DownloadDir: filepath.Join(directory, "Downloads"),
		WalletDir:   filepath.Join(directory, "wallets"),
	}
	store, err := daemonconfig.New(daemonconfig.Options{
		Paths:       &paths,
		Environment: map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, path
}

func rpcResult(t *testing.T, responseBody map[string]any) map[string]any {
	t.Helper()
	result, ok := responseBody["result"].(map[string]any)
	if !ok {
		t.Fatalf("result = %#v, want object", responseBody["result"])
	}
	return result
}

func TestSettingsGetExposesCompleteContract(t *testing.T) {
	server := CreateServer()
	response := performRequest(server, "POST", "/", `{"method":"settings_get"}`, nil)
	result := rpcResult(t, decodeResponse(t, response))
	if got, want := len(result), 55; got != want {
		t.Fatalf("settings count = %d, want %d", got, want)
	}
	if result["udp_port"] != json.Number("4444") || result["download_timeout"] != json.Number("30") {
		t.Fatalf("representative numeric settings = udp %#v, timeout %#v", result["udp_port"], result["download_timeout"])
	}
	servers, ok := result["lbryum_servers"].([]any)
	if !ok || len(servers) == 0 || !reflect.DeepEqual(servers[0], []any{"spv11.lbry.com", json.Number("50001")}) {
		t.Fatalf("lbryum_servers = %#v", result["lbryum_servers"])
	}
	if result["jurisdiction"] != nil || result["share_usage_data"] != false {
		t.Fatalf("nullable/toggle defaults = %#v / %#v", result["jurisdiction"], result["share_usage_data"])
	}
}

func TestSettingsSetClearAndPersistence(t *testing.T) {
	store, path := persistentSettingsStore(t)
	server := CreateServer(WithSettingsStore(store))

	setResponse := performRequest(
		server,
		"POST",
		"/",
		`{"method":"settings_set","params":{"key":"udp_port","value":"5000"}}`,
		nil,
	)
	setResult := rpcResult(t, decodeResponse(t, setResponse))
	if len(setResult) != 1 || setResult["udp_port"] != json.Number("5000") {
		t.Fatalf("settings_set result = %#v", setResult)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "udp_port: 5000\n"; got != want {
		t.Fatalf("persisted settings = %q, want %q", got, want)
	}

	clearResponse := performRequest(
		server,
		"POST",
		"/",
		`{"method":"settings_clear","params":{"key":"udp_port"}}`,
		nil,
	)
	clearResult := rpcResult(t, decodeResponse(t, clearResponse))
	if len(clearResult) != 1 || clearResult["udp_port"] != json.Number("4444") {
		t.Fatalf("settings_clear result = %#v", clearResult)
	}
	data, err = os.ReadFile(path)
	if err != nil || string(data) != "{}\n" {
		t.Fatalf("cleared settings file = %q, %v", data, err)
	}
}

func TestSettingsSetPreservesArbitraryPrecisionIntegers(t *testing.T) {
	store, path := persistentSettingsStore(t)
	server := CreateServer(WithSettingsStore(store))
	const huge = "999999999999999999999999999999"
	response := performRequest(
		server,
		"POST",
		"/",
		`{"method":"settings_set","params":{"key":"udp_port","value":`+huge+`}}`,
		nil,
	)
	if response.Code != 200 {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"udp_port": `+huge) {
		t.Fatalf("response lost integer precision: %s", response.Body.String())
	}
	result := rpcResult(t, decodeResponse(t, response))
	if result["udp_port"] != json.Number(huge) {
		t.Fatalf("decoded integer = %#v", result["udp_port"])
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "udp_port: "+huge+"\n" {
		t.Fatalf("persisted integer = %q, %v", data, err)
	}
}

func TestSettingsSetParsesLegacyJSONStringValues(t *testing.T) {
	server := CreateServer()

	componentsResponse := performRequest(
		server,
		"POST",
		"/",
		`{"method":"settings_set","params":{"key":"components_to_skip","value":"[\"wallet\",\"dht\"]"}}`,
		nil,
	)
	components := rpcResult(t, decodeResponse(t, componentsResponse))["components_to_skip"]
	if !reflect.DeepEqual(components, []any{"wallet", "dht"}) {
		t.Fatalf("parsed components = %#v", components)
	}

	feeResponse := performRequest(
		server,
		"POST",
		"/",
		`{"method":"settings_set","params":{"key":"max_key_fee","value":"{\"currency\":\"LBC\",\"amount\":\"2.5\"}"}}`,
		nil,
	)
	fee := rpcResult(t, decodeResponse(t, feeResponse))["max_key_fee"]
	if !reflect.DeepEqual(fee, map[string]any{"amount": json.Number("2.5"), "currency": "LBC"}) {
		t.Fatalf("parsed max_key_fee = %#v", fee)
	}

	invalid := performRequest(
		server,
		"POST",
		"/",
		`{"method":"settings_set","params":{"key":"components_to_skip","value":"["}}`,
		nil,
	)
	errorObject := assertRPCError(t, invalid, "-32500", "Expecting value: line 1 column 2 (char 1)")
	data := errorObject["data"].(map[string]any)
	if data["name"] != "JSONDecodeError" {
		t.Fatalf("JSON error name = %#v", data["name"])
	}

	for value, message := range map[string]string{
		"{":             "Expecting property name enclosed in double quotes: line 1 column 2 (char 1)",
		"[] []":         "Extra data: line 1 column 4 (char 3)",
		"[1,]":          "Illegal trailing comma before end of array: line 1 column 3 (char 2)",
		`{"a":1,}`:      "Illegal trailing comma before end of object: line 1 column 7 (char 6)",
		`{"a" 1}`:       "Expecting ':' delimiter: line 1 column 6 (char 5)",
		`{"a":1 "b":2}`: "Expecting ',' delimiter: line 1 column 8 (char 7)",
		`["\x"]`:        `Invalid \escape: line 1 column 3 (char 2)`,
		`{"a"`:          "Expecting ':' delimiter: line 1 column 5 (char 4)",
		`{"a":1`:        "Expecting ',' delimiter: line 1 column 7 (char 6)",
	} {
		body, err := json.Marshal(map[string]any{
			"method": "settings_set",
			"params": map[string]any{"key": "components_to_skip", "value": value},
		})
		if err != nil {
			t.Fatal(err)
		}
		response := performRequest(server, "POST", "/", string(body), nil)
		errorObject := assertRPCError(t, response, "-32500", message)
		if got := errorObject["data"].(map[string]any)["name"]; got != "JSONDecodeError" {
			t.Fatalf("JSON error name for %q = %#v", value, got)
		}
	}
}

func TestSettingsErrorsPreservePythonShape(t *testing.T) {
	server := CreateServer()
	tests := []struct {
		name      string
		body      string
		message   string
		exception string
		command   string
		wantKey   any
		wantValue any
	}{
		{
			name:      "toggle assertion",
			body:      `{"method":"settings_set","params":{"key":"share_usage_data","value":"true"}}`,
			message:   "Setting 'share_usage_data' must be a true/false value.",
			exception: "AssertionError",
			command:   "settings_set",
			wantKey:   "share_usage_data",
			wantValue: "true",
		},
		{
			name:      "integer conversion",
			body:      `{"method":"settings_set","params":{"key":"udp_port","value":"x"}}`,
			message:   "invalid literal for int() with base 10: 'x'",
			exception: "ValueError",
			command:   "settings_set",
			wantKey:   "udp_port",
			wantValue: "x",
		},
		{
			name:      "unknown set",
			body:      `{"method":"settings_set","params":{"key":"wat","value":true}}`,
			message:   "type object 'Config' has no attribute 'wat'",
			exception: "AttributeError",
			command:   "settings_set",
			wantKey:   "wat",
			wantValue: true,
		},
		{
			name:      "unknown clear",
			body:      `{"method":"settings_clear","params":{"key":"wat"}}`,
			message:   "'wat'",
			exception: "KeyError",
			command:   "settings_clear",
			wantKey:   "wat",
		},
		{
			name:      "non string key",
			body:      `{"method":"settings_clear","params":{"key":1}}`,
			message:   "attribute name must be string, not 'int'",
			exception: "TypeError",
			command:   "settings_clear",
			wantKey:   json.Number("1"),
		},
		{
			name:      "float key",
			body:      `{"method":"settings_clear","params":{"key":1.50}}`,
			message:   "attribute name must be string, not 'float'",
			exception: "TypeError",
			command:   "settings_clear",
			wantKey:   json.Number("1.5"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := performRequest(server, "POST", "/", test.body, nil)
			errorObject := assertRPCError(t, response, "-32500", test.message)
			data := errorObject["data"].(map[string]any)
			if data["name"] != test.exception || data["command"] != test.command {
				t.Fatalf("error metadata = %#v", data)
			}
			kwargs := data["kwargs"].(map[string]any)
			if kwargs["key"] != test.wantKey {
				t.Fatalf("error kwargs key = %#v, want %#v", kwargs["key"], test.wantKey)
			}
			if test.command == "settings_set" && kwargs["value"] != test.wantValue {
				t.Fatalf("error kwargs value = %#v, want %#v", kwargs["value"], test.wantValue)
			}
			if traceback, ok := data["traceback"].([]any); !ok || len(traceback) == 0 {
				t.Fatalf("traceback = %#v", data["traceback"])
			}
		})
	}
}

func TestAllowedOriginMutationUsesPostCommandValue(t *testing.T) {
	store := daemonconfig.NewMemory()
	server := CreateServer(WithSettingsStore(store))

	set := performRequest(
		server,
		"POST",
		"/",
		`{"method":"settings_set","params":{"key":"allowed_origin","value":"https://example.test"}}`,
		nil,
	)
	if set.Code != 200 {
		t.Fatalf("set status = %d: %s", set.Code, set.Body.String())
	}
	for _, header := range []string{
		"Access-Control-Allow-Origin",
		"Access-Control-Allow-Methods",
		"Access-Control-Allow-Headers",
	} {
		if got := set.Header().Get(header); got != "https://example.test" {
			t.Errorf("set %s = %q", header, got)
		}
	}

	clear := performRequest(
		server,
		"POST",
		"/",
		`{"method":"settings_clear","params":{"key":"allowed_origin"}}`,
		map[string]string{"Origin": "https://example.test"},
	)
	if clear.Code != 200 {
		t.Fatalf("clear status = %d: %s", clear.Code, clear.Body.String())
	}
	for name, values := range clear.Header() {
		if strings.HasPrefix(name, "Access-Control-") && len(values) > 0 {
			t.Errorf("clear response retained %s: %q", name, values)
		}
	}

	rejected := performRequest(
		server,
		"POST",
		"/",
		`{"method":"settings_get"}`,
		map[string]string{"Origin": "https://example.test"},
	)
	if rejected.Code != 403 {
		t.Fatalf("cleared origin request status = %d, want 403", rejected.Code)
	}
}
