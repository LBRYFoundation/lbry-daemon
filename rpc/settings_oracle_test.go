package rpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
	daemonconfig "lbry/daemon/config"
)

const (
	pinnedSDKCommit  = "e7666f489418e96b6d2104974e93915b539235c5"
	pinnedSDKVersion = "0.113.0"
)

type settingsOracleOperation struct {
	Method string         `json:"method"`
	Params map[string]any `json:"params"`
}

type settingsOracleStep struct {
	Method   string         `json:"method"`
	Response map[string]any `json:"response"`
	YAML     any            `json:"yaml"`
}

type settingsOracleOutput struct {
	Reference struct {
		Commit  string `json:"commit"`
		Version string `json:"version"`
	} `json:"reference"`
	Steps []settingsOracleStep `json:"steps"`
}

type settingsOracleNumber string

func TestSettingsMatchPinnedPythonOracle(t *testing.T) {
	sdkRoot, oracleScript := settingsOraclePaths(t)
	python := settingsOraclePython(t, sdkRoot)
	workRoot := t.TempDir()
	pythonRoot := filepath.Join(workRoot, "python")
	goRoot := filepath.Join(workRoot, "go")

	operations := []settingsOracleOperation{
		{Method: "settings_get", Params: map[string]any{}},
		{Method: "settings_set", Params: map[string]any{"key": "udp_port", "value": "5000"}},
		{Method: "settings_set", Params: map[string]any{
			"key": "udp_port", "value": "900719925474099312345",
		}},
		{Method: "settings_set", Params: map[string]any{
			"key":   "lbryum_servers",
			"value": []any{"oracle.example:50001", "bad", "host:not-a-port", "ipv6::50001"},
		}},
		{Method: "settings_set", Params: map[string]any{
			"key": "components_to_skip", "value": `["wallet","dht"]`,
		}},
		{Method: "settings_set", Params: map[string]any{
			"key": "max_key_fee", "value": `{"currency":"LBC","amount":"2.5"}`,
		}},
		{Method: "settings_set", Params: map[string]any{
			"key": "max_key_fee", "value": `{"currency":"DOGE","amount":"1"}`,
		}},
		{Method: "settings_set", Params: map[string]any{
			"key": "components_to_skip", "value": "[",
		}},
		{Method: "settings_set", Params: map[string]any{
			"key": "allowed_origin", "value": "https://oracle.example",
		}},
		{Method: "settings_set", Params: map[string]any{
			"key": "coin_selection_strategy", "value": "not-a-strategy",
		}},
		{Method: "settings_set", Params: map[string]any{
			"key": "share_usage_data", "value": "true",
		}},
		{Method: "settings_set", Params: map[string]any{"key": "wat", "value": true}},
		{Method: "settings_clear", Params: map[string]any{"key": "wat"}},
		{Method: "settings_clear", Params: map[string]any{"key": "udp_port"}},
		{Method: "settings_clear", Params: map[string]any{"key": "allowed_origin"}},
		{Method: "settings_get", Params: map[string]any{}},
	}

	pythonInitialYAML := settingsOracleInitialYAML(pythonRoot)
	oracle := runSettingsOracle(t, python, oracleScript, sdkRoot, pythonRoot, operations, pythonInitialYAML)
	if oracle.Reference.Commit != pinnedSDKCommit || oracle.Reference.Version != pinnedSDKVersion {
		t.Fatalf("oracle reference = %#v", oracle.Reference)
	}

	if err := os.MkdirAll(goRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	goConfigPath := filepath.Join(goRoot, "daemon_settings.yml")
	if err := os.WriteFile(goConfigPath, []byte(settingsOracleInitialYAML(goRoot)), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := daemonconfig.Paths{
		Config:      goConfigPath,
		DataDir:     filepath.Join(goRoot, "data"),
		DownloadDir: filepath.Join(goRoot, "downloads"),
		WalletDir:   filepath.Join(goRoot, "wallet"),
	}
	store, err := daemonconfig.New(daemonconfig.Options{
		Paths: &paths,
		Runtime: map[string]any{
			"config":       paths.Config,
			"data_dir":     paths.DataDir,
			"download_dir": paths.DownloadDir,
			"wallet_dir":   paths.WalletDir,
		},
		Environment: map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := CreateServer(WithSettingsStore(store))

	if len(oracle.Steps) != len(operations) {
		t.Fatalf("oracle steps = %d, want %d", len(oracle.Steps), len(operations))
	}
	for index, operation := range operations {
		requestBody, err := json.Marshal(operation)
		if err != nil {
			t.Fatal(err)
		}
		response := performRequest(server, http.MethodPost, "/", string(requestBody), nil)
		if response.Code != http.StatusOK {
			t.Fatalf("step %d %s status = %d: %s", index, operation.Method, response.Code, response.Body.String())
		}
		goResponse := decodeResponse(t, response)
		goYAML := readSettingsOracleYAML(t, goConfigPath)

		wantResponse := normalizeSettingsOracleValue(oracle.Steps[index].Response, pythonRoot)
		gotResponse := normalizeSettingsOracleValue(goResponse, goRoot)
		if !reflect.DeepEqual(gotResponse, wantResponse) {
			t.Fatalf(
				"step %d %s response mismatch\nGo:     %s\nPython: %s",
				index, operation.Method, prettyOracleJSON(gotResponse), prettyOracleJSON(wantResponse),
			)
		}

		wantYAML := normalizeSettingsOracleValue(oracle.Steps[index].YAML, pythonRoot)
		gotYAML := normalizeSettingsOracleValue(goYAML, goRoot)
		if !reflect.DeepEqual(gotYAML, wantYAML) {
			t.Fatalf(
				"step %d %s YAML mismatch\nGo:     %s\nPython: %s",
				index, operation.Method, prettyOracleJSON(gotYAML), prettyOracleJSON(wantYAML),
			)
		}
	}
}

func settingsOraclePaths(t *testing.T) (sdkRoot, script string) {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot = os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	script = filepath.Join(daemonRoot, "compat", "settings_oracle.py")
	for _, required := range []string{
		filepath.Join(sdkRoot, "lbry", "conf.py"),
		filepath.Join(sdkRoot, "lbry", "extras", "daemon", "daemon.py"),
		script,
	} {
		if _, err := os.Stat(required); errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned local Python SDK oracle source is unavailable: %s", required)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	return sdkRoot, script
}

func settingsOraclePython(t *testing.T, sdkRoot string) string {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	check := exec.Command(python, "-c", "import yaml")
	check.Dir = sdkRoot
	if output, err := check.CombinedOutput(); err != nil {
		t.Skipf("PyYAML is unavailable to the Python oracle: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	return python
}

func runSettingsOracle(
	t *testing.T,
	python, script, sdkRoot, workRoot string,
	operations []settingsOracleOperation,
	initialYAML string,
) settingsOracleOutput {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"initial_yaml": initialYAML,
		"operations":   operations,
	})
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(
		python,
		script,
		"--sdk-root", sdkRoot,
		"--work-dir", workRoot,
	)
	command.Stdin = bytes.NewReader(payload)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("Python settings oracle failed: %v\n%s", err, stderr.String())
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.UseNumber()
	var result settingsOracleOutput
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("decode Python settings oracle: %v\n%s", err, output)
	}
	return result
}

func settingsOracleInitialYAML(root string) string {
	return fmt.Sprintf(
		"dht_node_port: 4555\ndownload_directory: %q\nlbryum_servers:\n- oracle.example:50001\nignored_setting: true\nupload_log: true\n",
		filepath.ToSlash(filepath.Join(root, "legacy-download")),
	)
}

func readSettingsOracleYAML(t *testing.T, path string) any {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("decode Go settings YAML: %v\n%s", err, data)
	}
	if len(document.Content) == 0 {
		return nil
	}
	return settingsOracleYAMLNode(t, document.Content[0])
}

func settingsOracleYAMLNode(t *testing.T, node *yaml.Node) any {
	t.Helper()
	if node.Kind == yaml.AliasNode && node.Alias != nil {
		return settingsOracleYAMLNode(t, node.Alias)
	}
	switch node.Kind {
	case yaml.MappingNode:
		value := make(map[string]any, len(node.Content)/2)
		for index := 0; index+1 < len(node.Content); index += 2 {
			value[node.Content[index].Value] = settingsOracleYAMLNode(t, node.Content[index+1])
		}
		return value
	case yaml.SequenceNode:
		value := make([]any, len(node.Content))
		for index, child := range node.Content {
			value[index] = settingsOracleYAMLNode(t, child)
		}
		return value
	case yaml.ScalarNode:
		switch node.Tag {
		case "!!null":
			return nil
		case "!!bool":
			value, err := strconv.ParseBool(strings.ToLower(node.Value))
			if err != nil {
				t.Fatalf("decode YAML boolean %q: %v", node.Value, err)
			}
			return value
		case "!!int", "!!float":
			return json.Number(strings.ReplaceAll(node.Value, "_", ""))
		default:
			return node.Value
		}
	default:
		t.Fatalf("unsupported YAML node kind %d", node.Kind)
		return nil
	}
}

func normalizeSettingsOracleValue(value any, root string) any {
	switch typed := value.(type) {
	case map[string]any:
		normalized := make(map[string]any, len(typed))
		for name, child := range typed {
			if name == "traceback" {
				continue
			}
			normalized[name] = normalizeSettingsOracleValue(child, root)
		}
		return normalized
	case []any:
		normalized := make([]any, len(typed))
		for index, child := range typed {
			normalized[index] = normalizeSettingsOracleValue(child, root)
		}
		return normalized
	case string:
		return strings.ReplaceAll(filepath.ToSlash(typed), filepath.ToSlash(root), "<ORACLE_ROOT>")
	case json.Number:
		if number, ok := new(big.Rat).SetString(string(typed)); ok {
			return settingsOracleNumber(number.RatString())
		}
		return settingsOracleNumber(typed)
	default:
		return value
	}
}

func prettyOracleJSON(value any) string {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprintf("%#v", value)
	}
	return string(encoded)
}
