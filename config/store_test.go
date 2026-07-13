package config

import (
	"encoding/json"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"

	"go.yaml.in/yaml/v3"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	paths := Paths{
		Config:      filepath.Join(t.TempDir(), "settings.yml"),
		DataDir:     "/data",
		DownloadDir: "/downloads",
		WalletDir:   "/wallets",
	}
	store, err := New(Options{Paths: &paths, Environment: map[string]string{}, InMemory: true})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func pythonError(t *testing.T, err error, name, message string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error is nil, want %s", name)
	}
	python, ok := err.(*PythonError)
	if !ok {
		t.Fatalf("error type = %T, want *PythonError", err)
	}
	if python.Name != name || python.Message != message {
		t.Fatalf("error = %s: %q, want %s: %q", python.Name, python.Message, name, message)
	}
}

func TestRegistryAndDefaults(t *testing.T) {
	store := testStore(t)
	snapshot := store.Snapshot()
	if got, want := len(snapshot), 55; got != want {
		t.Fatalf("settings = %d, want %d", got, want)
	}
	if names := SortedNames(store); len(names) != 55 || names[0] != "allowed_origin" || names[54] != "wallets" {
		t.Fatalf("sorted names = %#v", names)
	}
	checks := map[string]any{
		"allowed_origin":         "",
		"download_timeout":       30.0,
		"jurisdiction":           nil,
		"share_usage_data":       false,
		"transaction_cache_size": 131072,
		"udp_port":               4444,
	}
	for name, want := range checks {
		if got := snapshot[name]; !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %#v, want %#v", name, got, want)
		}
	}
	servers := snapshot["lbryum_servers"].([]Server)
	if servers[0] != (Server{Host: "spv11.lbry.com", Port: 50001}) {
		t.Fatalf("first lbryum server = %#v", servers[0])
	}
	encoded, err := json.Marshal(servers[:1])
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(encoded), `[["spv11.lbry.com",50001]]`; got != want {
		t.Fatalf("server JSON = %s, want %s", got, want)
	}
}

func TestSettingConfiguredLayerIntrospection(t *testing.T) {
	store := testStore(t)
	if store.IsSet("lbryum_servers") || store.IsSetToDefault("lbryum_servers") {
		t.Fatal("default-only server setting reported as configured")
	}
	defaultServers, exists := store.Default("lbryum_servers")
	if !exists {
		t.Fatal("lbryum_servers default is missing")
	}
	servers := defaultServers.([]Server)
	configuredDefault := make([]any, len(servers))
	for index, server := range servers {
		configuredDefault[index] = server.Host + ":50001"
	}
	if _, err := store.Set("lbryum_servers", configuredDefault); err != nil {
		t.Fatal(err)
	}
	if !store.IsSet("lbryum_servers") || !store.IsSetToDefault("lbryum_servers") {
		t.Fatal("explicit default server setting was not detected")
	}
	if _, err := store.Set("lbryum_servers", []any{"custom:50001"}); err != nil {
		t.Fatal(err)
	}
	if !store.IsSet("lbryum_servers") || store.IsSetToDefault("lbryum_servers") {
		t.Fatal("custom server setting introspection is incorrect")
	}
}

func TestSettingCoercionByType(t *testing.T) {
	store := testStore(t)
	tests := []struct {
		name  string
		value any
		want  any
	}{
		{name: "udp_port", value: "123", want: 123},
		{name: "udp_port", value: json.Number("12.9"), want: 12},
		{name: "udp_port", value: true, want: 1},
		{name: "download_timeout", value: "1.25", want: 1.25},
		{name: "download_timeout", value: json.Number("12"), want: 12.0},
		{name: "share_usage_data", value: true, want: true},
		{name: "components_to_skip", value: []any{"wallet", "dht"}, want: []any{"wallet", "dht"}},
		{name: "coin_selection_strategy", value: "sqlite", want: "sqlite"},
		{name: "max_key_fee", value: map[string]any{"amount": "2.5", "currency": "LBC"}, want: map[string]any{"amount": 2.5, "currency": "LBC"}},
		{name: "max_key_fee", value: "1 btc", want: map[string]any{"amount": 1.0, "currency": "BTC"}},
		{name: "max_key_fee", value: "null", want: nil},
	}
	for _, test := range tests {
		t.Run(test.name+"/"+reflect.TypeOf(test.value).String(), func(t *testing.T) {
			got, err := store.Set(test.name, test.value)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("Set(%s) = %#v, want %#v", test.name, got, test.want)
			}
		})
	}
}

func TestArbitraryPrecisionIntegerCompatibility(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "settings.yml")
	paths := Paths{Config: path}
	store, err := New(Options{Paths: &paths, Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	const huge = "999999999999999999999999999999"
	value, err := store.Set("udp_port", huge)
	if err != nil {
		t.Fatal(err)
	}
	if value != BigInteger(huge) {
		t.Fatalf("huge integer = %#v", value)
	}
	encoded, err := json.Marshal(value)
	if err != nil || string(encoded) != huge {
		t.Fatalf("huge integer JSON = %q, %v", encoded, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "udp_port: "+huge+"\n" {
		t.Fatalf("huge integer YAML = %q, %v", data, err)
	}
	reloaded, err := New(Options{Paths: &paths, Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := reloaded.Get("udp_port"); got != BigInteger(huge) {
		t.Fatalf("reloaded huge integer = %#v", got)
	}

	value, err = store.Set("udp_port", json.Number("1e20"))
	if err != nil || value != BigInteger("100000000000000000000") {
		t.Fatalf("large float integer = %#v, %v", value, err)
	}
	_, err = store.Set("udp_port", math.NaN())
	pythonError(t, err, "ValueError", "cannot convert float NaN to integer")
	_, err = store.Set("udp_port", math.Inf(1))
	pythonError(t, err, "OverflowError", "cannot convert float infinity to integer")
	_, err = store.Set("udp_port", json.Number("1e309"))
	pythonError(t, err, "OverflowError", "cannot convert float infinity to integer")
	value, err = store.Set("download_timeout", "1e309")
	if err != nil || !math.IsInf(value.(float64), 1) {
		t.Fatalf("float overflow = %#v, %v", value, err)
	}
}

func TestInvalidSettingValues(t *testing.T) {
	store := testStore(t)
	_, err := store.Set("share_usage_data", "true")
	pythonError(t, err, "AssertionError", "Setting 'share_usage_data' must be a true/false value.")
	_, err = store.Set("allowed_origin", json.Number("1"))
	pythonError(t, err, "AssertionError", "Setting 'allowed_origin' must be a string.")
	_, err = store.Set("components_to_skip", []any{"wallet", json.Number("1")})
	pythonError(t, err, "AssertionError", "Value of '1' at index 1 in setting 'components_to_skip' must be a string.")
	_, err = store.Set("components_to_skip", []any{"wallet", true})
	pythonError(t, err, "AssertionError", "Value of 'True' at index 1 in setting 'components_to_skip' must be a string.")
	_, err = store.Set("udp_port", "x")
	pythonError(t, err, "ValueError", "invalid literal for int() with base 10: 'x'")
	_, err = store.Set("coin_selection_strategy", "wat")
	pythonError(t, err, "ValueError", "Setting 'coin_selection_strategy' value must be one of: sqlite, prefer_confirmed, only_confirmed, standard, branch_and_bound, closest_match, random_draw")
	_, err = store.Set("max_key_fee", "1 BCH")
	pythonError(t, err, "InvalidCurrencyError", "Invalid currency: BCH is not a supported currency.")
	_, err = store.Set("max_key_fee", []any{nil, "USD"})
	pythonError(t, err, "TypeError", "float() argument must be a string or a real number, not 'NoneType'")
	_, err = store.Set("wat", true)
	pythonError(t, err, "AttributeError", "type object 'Config' has no attribute 'wat'")
	_, err = store.Clear("wat")
	pythonError(t, err, "KeyError", "'wat'")
}

func TestServerSettingCompatibility(t *testing.T) {
	store := testStore(t)
	value, err := store.Set("lbryum_servers", []any{
		"host:50001", "bad", "many:colons:1", "port:no", ":80", "neg:-1", "host:50001",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []Server{
		{Host: "host", Port: 50001},
		{Host: "", Port: 80},
		{Host: "neg", Port: -1},
		{Host: "host", Port: 50001},
	}
	if !reflect.DeepEqual(value, want) {
		t.Fatalf("servers = %#v, want %#v", value, want)
	}
	value, err = store.Set("lbryum_servers", []any{[]any{"host", json.Number("50001")}})
	if err != nil || !reflect.DeepEqual(value, []Server{}) {
		t.Fatalf("nested server input = %#v, %v", value, err)
	}
	for _, input := range []any{map[string]any{"host": 1}, "host:1", nil} {
		value, err = store.Set("lbryum_servers", input)
		if err != nil || !reflect.DeepEqual(value, []Server{}) {
			t.Fatalf("server input %#v = %#v, %v", input, value, err)
		}
	}
}

func TestPersistenceRoundTripAndClear(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "settings.yml")
	paths := Paths{Config: path, DataDir: directory, DownloadDir: "/downloads", WalletDir: "/wallets"}
	store, err := New(Options{Paths: &paths, Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	sets := []struct {
		name  string
		value any
	}{
		{name: "components_to_skip", value: []any{"wallet", "dht"}},
		{name: "lbryum_servers", value: []any{"server:50001"}},
		{name: "share_usage_data", value: true},
		{name: "udp_port", value: json.Number("5000")},
	}
	for _, set := range sets {
		if _, err := store.Set(set.name, set.value); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wantYAML := "components_to_skip:\n- wallet\n- dht\nlbryum_servers:\n- server:50001\nshare_usage_data: true\nudp_port: 5000\n"
	if string(data) != wantYAML {
		t.Fatalf("YAML mismatch\ngot:\n%s\nwant:\n%s", data, wantYAML)
	}
	reloaded, err := New(Options{Paths: &paths, Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := reloaded.Get("udp_port"); got != 5000 {
		t.Fatalf("reloaded udp_port = %#v", got)
	}
	if got, _ := reloaded.Get("lbryum_servers"); !reflect.DeepEqual(got, []Server{{Host: "server", Port: 50001}}) {
		t.Fatalf("reloaded servers = %#v", got)
	}

	pathOnly := filepath.Join(directory, "clear.yml")
	clearPaths := paths
	clearPaths.Config = pathOnly
	clearStore, err := New(Options{Paths: &clearPaths, Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clearStore.Set("share_usage_data", true); err != nil {
		t.Fatal(err)
	}
	value, err := clearStore.Clear("share_usage_data")
	if err != nil || value != false {
		t.Fatalf("clear result = %#v, %v", value, err)
	}
	if data, err := os.ReadFile(pathOnly); err != nil || string(data) != "{}\n" {
		t.Fatalf("cleared YAML = %q, %v", data, err)
	}
}

func TestPythonFloatYAMLFormatting(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "settings.yml")
	paths := Paths{Config: path}
	store, err := New(Options{Paths: &paths, Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set("download_timeout", 1); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "download_timeout: 1.0\n" {
		t.Fatalf("integral float YAML = %q, %v", data, err)
	}
	if _, err := store.Set("download_timeout", 1e20); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "download_timeout: 1.0e+20\n" {
		t.Fatalf("exponent float YAML = %q, %v", data, err)
	}
	if _, err := store.Set("download_timeout", "nan"); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "download_timeout: .nan\n" {
		t.Fatalf("NaN float YAML = %q, %v", data, err)
	}
	if value, _ := store.Get("download_timeout"); !math.IsNaN(value.(float64)) {
		t.Fatalf("NaN setting = %#v", value)
	}
	if _, err := store.Set("download_timeout", 1e15); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "download_timeout: 1000000000000000.0\n" {
		t.Fatalf("fixed float YAML = %q, %v", data, err)
	}
}

func TestPythonStringYAMLFormatting(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "settings.yml")
	paths := Paths{Config: path}
	tests := map[string]string{
		"yes":        "allowed_origin: 'yes'\n",
		"1e3":        "allowed_origin: 1e3\n",
		"0o777":      "allowed_origin: 0o777\n",
		"1:20":       "allowed_origin: '1:20'\n",
		"2020-01-01": "allowed_origin: '2020-01-01'\n",
	}
	for value, want := range tests {
		t.Run(value, func(t *testing.T) {
			store, err := New(Options{Paths: &paths, Environment: map[string]string{}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Set("allowed_origin", value); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != want {
				t.Fatalf("string YAML = %q, want %q (%v)", data, want, err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConfigurationPrecedence(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "settings.yml")
	if err := os.WriteFile(path, []byte("udp_port: 1111\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	paths := Paths{Config: path, DataDir: directory, DownloadDir: "/downloads", WalletDir: "/wallets"}
	store, err := New(Options{
		Paths:       &paths,
		Runtime:     map[string]any{"udp_port": 4444},
		Arguments:   map[string]any{"udp_port": "3333"},
		Environment: map[string]string{"LBRY_UDP_PORT": "2222"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Get("udp_port"); got != 4444 {
		t.Fatalf("runtime precedence = %#v", got)
	}
	if _, err := store.Set("udp_port", "5555"); err != nil {
		t.Fatal(err)
	}
	value, err := store.Clear("udp_port")
	if err != nil || value != 3333 {
		t.Fatalf("clear fallback = %#v, %v", value, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "udp_port") {
		t.Fatalf("clear retained persisted value: %s", data)
	}
}

func TestNilArgumentListSettingsAreUnset(t *testing.T) {
	paths := Paths{Config: filepath.Join(t.TempDir(), "settings.yml")}
	store, err := New(Options{
		Paths:       &paths,
		Arguments:   map[string]any{"components_to_skip": nil, "lbryum_servers": nil},
		Environment: map[string]string{},
		InMemory:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Get("components_to_skip"); !reflect.DeepEqual(got, []string{}) {
		t.Fatalf("nil argument replaced components default: %#v", got)
	}
	if got, _ := store.Get("lbryum_servers"); len(got.([]Server)) == 0 {
		t.Fatalf("nil argument replaced server defaults: %#v", got)
	}
}

func TestLegacyYAMLUpgrade(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "settings.yml")
	data := "udp_port: 1111\ndht_node_port: 2222\npeer_port: 3333\ndownload_directory: ~/legacy\nunknown: ignored\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	paths := Paths{Config: path, DataDir: directory, DownloadDir: "/downloads", WalletDir: "/wallets"}
	store, err := New(Options{Paths: &paths, Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Get("udp_port"); got != 2222 {
		t.Fatalf("legacy alias did not override canonical: %#v", got)
	}
	if got, _ := store.Get("tcp_port"); got != 3333 {
		t.Fatalf("peer_port upgrade = %#v", got)
	}
	upgraded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, old := range []string{"dht_node_port", "peer_port", "download_directory", "unknown"} {
		if strings.Contains(string(upgraded), old+":") {
			t.Errorf("upgraded YAML retained %s: %s", old, upgraded)
		}
	}
}

func TestLegacyYAML11AndMergeCompatibility(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "settings.yml")
	data := "defaults: &defaults\n  udp_port: 1:20\n  share_usage_data: yes\n<<: *defaults\nallowed_origin: 0o777\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	paths := Paths{Config: path}
	store, err := New(Options{Paths: &paths, Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Get("udp_port"); got != 80 {
		t.Fatalf("merged udp_port = %#v", got)
	}
	if got, _ := store.Get("share_usage_data"); got != true {
		t.Fatalf("YAML 1.1 boolean = %#v", got)
	}
	if got, _ := store.Get("allowed_origin"); got != "0o777" {
		t.Fatalf("YAML 1.2-only octal was resolved: %#v", got)
	}

	for _, falsey := range []string{"false\n", "0\n", "[]\n"} {
		if err := os.WriteFile(path, []byte(falsey), 0o644); err != nil {
			t.Fatal(err)
		}
		store, err := New(Options{Paths: &paths, Environment: map[string]string{}})
		if err != nil {
			t.Fatalf("falsey YAML %q: %v", falsey, err)
		}
		if got, _ := store.Get("udp_port"); got != 4444 {
			t.Fatalf("falsey YAML %q changed defaults: %#v", falsey, got)
		}
	}
	if err := os.WriteFile(path, []byte("true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = New(Options{Paths: &paths, Environment: map[string]string{}})
	pythonError(t, err, "AttributeError", "'bool' object has no attribute 'items'")
}

func TestPyYAMLNumericResolutionCompatibility(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "settings.yml")
	paths := Paths{Config: path}

	if err := os.WriteFile(path, []byte("udp_port: 1e3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := New(Options{Paths: &paths, Environment: map[string]string{}})
	pythonError(t, err, "ValueError", "invalid literal for int() with base 10: '1e3'")

	const hexadecimal = "0xffffffffffffffffffffffffffffffff"
	if err := os.WriteFile(path, []byte("udp_port: "+hexadecimal+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := New(Options{Paths: &paths, Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	want := BigInteger("340282366920938463463374607431768211455")
	if got, _ := store.Get("udp_port"); got != want {
		t.Fatalf("large hexadecimal integer = %#v, want %#v", got, want)
	}

	data := "fee: &fee\n  amount: 2.5\n  currency: USD\nmax_key_fee:\n  <<: *fee\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err = New(Options{Paths: &paths, Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Get("max_key_fee"); !reflect.DeepEqual(got, map[string]any{"amount": 2.5, "currency": "USD"}) {
		t.Fatalf("nested YAML merge = %#v", got)
	}
}

func TestDuplicateYAMLKeyUsesLastValue(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "settings.yml")
	if err := os.WriteFile(path, []byte("udp_port: 1111\nudp_port: 2222\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	paths := Paths{Config: path}
	store, err := New(Options{Paths: &paths, Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Get("udp_port"); got != 2222 {
		t.Fatalf("duplicate YAML key = %#v, want last value", got)
	}
}

func TestPathExpansionAndRawSetResult(t *testing.T) {
	store := testStore(t)
	t.Setenv("LBRY_TEST_PATH", "expanded")
	raw := "~/$LBRY_TEST_PATH"
	result, err := store.Set("download_dir", raw)
	if err != nil || result != raw {
		t.Fatalf("raw path set result = %#v, %v", result, err)
	}
	effective, _ := store.Get("download_dir")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, "expanded")
	if effective != want {
		t.Fatalf("expanded path = %#v, want %q", effective, want)
	}
	if got := ExpandPath("${LBRY_MISSING_PATH}/$LBRY_MISSING_PATH/x"); got != "${LBRY_MISSING_PATH}/$LBRY_MISSING_PATH/x" {
		t.Fatalf("unknown variables changed: %q", got)
	}
	t.Setenv("LBRY_BRACED_PATH", "braced")
	if got := ExpandPath("${LBRY_BRACED_PATH}/x"); got != "braced/x" {
		t.Fatalf("braced variable = %q", got)
	}
	if got := ExpandPath("~/a/../x"); got != home+"/a/../x" {
		t.Fatalf("home expansion cleaned path: %q", got)
	}
	if runtime.GOOS != "windows" {
		if got := ExpandPath(`~\foo`); got != `~\foo` {
			t.Fatalf("POSIX backslash path expanded: %q", got)
		}
	}
}

func TestPersistenceFailureRollsBackMemory(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "settings.yml")
	paths := Paths{Config: path}
	store, err := New(Options{Paths: &paths, Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	store.persistPath = directory

	if _, err := store.Set("udp_port", 5000); err == nil {
		t.Fatal("Set succeeded with a directory as persistence target")
	}
	if got, _ := store.Get("udp_port"); got != 4444 {
		t.Fatalf("failed Set retained runtime value %#v", got)
	}

	store.persistPath = path
	if _, err := store.Set("udp_port", 5000); err != nil {
		t.Fatal(err)
	}
	store.persistPath = directory
	if _, err := store.Clear("udp_port"); err == nil {
		t.Fatal("Clear succeeded with a directory as persistence target")
	}
	if got, _ := store.Get("udp_port"); got != 5000 {
		t.Fatalf("failed Clear removed runtime value %#v", got)
	}
}

func TestPersistencePreservesSymlinkAndPermissions(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.yml")
	path := filepath.Join(directory, "settings.yml")
	if err := os.WriteFile(target, []byte("udp_port: 4000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	paths := Paths{Config: path}
	store, err := New(Options{Paths: &paths, Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set("udp_port", 5000); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("config symlink replaced: %v, %v", info, err)
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("target permissions = %v, %v", info, err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "udp_port: 5000\n" {
		t.Fatalf("symlink target = %q, %v", data, err)
	}
}

func TestDirectRuntimeIntegerAcceptsBoolean(t *testing.T) {
	store, err := New(Options{
		Runtime:     map[string]any{"udp_port": true},
		Environment: map[string]string{},
		InMemory:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Get("udp_port"); got != true {
		t.Fatalf("runtime bool integer = %#v", got)
	}
}

func TestDirectRuntimeValuesAreValidatedAndCloned(t *testing.T) {
	integer := new(big.Int)
	integer.SetString("999999999999999999999999999999", 10)
	store, err := New(Options{
		Runtime:     map[string]any{"udp_port": integer},
		Environment: map[string]string{},
		InMemory:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	integer.SetInt64(1)
	if got, _ := store.Get("udp_port"); got.(*big.Int).String() != "999999999999999999999999999999" {
		t.Fatalf("runtime big integer aliased caller memory: %v", got)
	}

	_, err = New(Options{
		Runtime: map[string]any{
			"lbryum_servers": []Server{{Host: "host", Port: "50001"}},
		},
		Environment: map[string]string{},
		InMemory:    true,
	})
	pythonError(t, err, "AssertionError", "Server defined '('host', '50001')' at index 0 in setting 'lbryum_servers' must be have port as int in second position.")
}

func TestInvalidExtension(t *testing.T) {
	paths := Paths{Config: filepath.Join(t.TempDir(), "settings.json")}
	_, err := New(Options{Paths: &paths, Environment: map[string]string{}})
	pythonError(t, err, "AssertionError", "File extension '.json' is not supported, configuration file must be in YAML (.yaml).")
}

func TestConcurrentAccessAndDeepCopies(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "settings.yml")
	paths := Paths{Config: path, DataDir: directory}
	store, err := New(Options{Paths: &paths, Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for worker := range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for iteration := range 20 {
				_, _ = store.Set("udp_port", worker*100+iteration)
				_ = store.Snapshot()
				_, _ = store.Get("lbryum_servers")
			}
		}()
	}
	workers.Wait()

	snapshot := store.Snapshot()
	servers := snapshot["lbryum_servers"].([]Server)
	servers[0].Host = "mutated"
	again, _ := store.Get("lbryum_servers")
	if again.([]Server)[0].Host == "mutated" {
		t.Fatal("snapshot mutated store state")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("persisted YAML is invalid: %v\n%s", err, data)
	}
}
