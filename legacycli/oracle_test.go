package legacycli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

type clientCLIOracleResult struct {
	RequestedMethod string         `json:"requested_method"`
	Method          string         `json:"method"`
	Params          map[string]any `json:"params"`
	Notice          string         `json:"notice"`
}

type clientCLIOracleCase struct {
	Argv   []string               `json:"argv"`
	Result *clientCLIOracleResult `json:"result"`
	Error  *string                `json:"error"`
}

type clientCLIOracleOutput struct {
	Reference struct {
		Commit  string `json:"commit"`
		Version string `json:"version"`
	} `json:"reference"`
	Manifest commandManifest       `json:"manifest"`
	Cases    []clientCLIOracleCase `json:"cases"`
}

func TestParserMatchesPinnedPythonClientCLIOracle(t *testing.T) {
	sdkRoot, script := clientCLIOraclePaths(t)
	python := clientCLIOraclePython(t, sdkRoot)
	cases := [][]string{
		{"publish", "123", "--title=true", "--tags=1", "--tags=false"},
		{"account", "add", "123", "--seed=001", "--single_key"},
		{"resolve", "123", "true"},
		{"claim", "search", "--text=music", "--page=2", "--no_totals"},
		{"claim", "search", "--trending_global=3", "--trending_score=4"},
		{"wallet", "send", "1.0", "bAddress", "123", "--funding_account_ids=a", "--funding_account_ids=2"},
		{"channel", "new", "@name", "1"},
		{"get", "--", "-uri"},
		{"get", "uri", "--", "--help"},
		{"wallet", "list", "--page_s=2"},
		{"wallet", "list", "--page=\u0662", "--page_size=\uff11\uff10"},
		{"get", "\u0661\u0662\u0663"},
		{"account", "fund", "1"},
		{"get", "x", "z", "--file_name=y"},
	}
	oracle := runClientCLIOracle(t, python, script, sdkRoot, cases)
	if oracle.Reference.Commit != "e7666f489418e96b6d2104974e93915b539235c5" || oracle.Reference.Version != "0.113.0" {
		t.Fatalf("oracle reference = %#v", oracle.Reference)
	}
	var embedded commandManifest
	if err := json.Unmarshal(manifestJSON, &embedded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(oracle.Manifest, embedded) {
		t.Fatal("embedded CLI manifest differs from the pinned Python daemon source")
	}
	if len(oracle.Cases) != len(cases) {
		t.Fatalf("oracle cases = %d, want %d", len(oracle.Cases), len(cases))
	}
	for index, argv := range cases {
		want := oracle.Cases[index]
		result, err := Parse(argv)
		if want.Error != nil {
			var usage *UsageError
			if !errors.As(err, &usage) {
				t.Fatalf("case %d %#v Go error = %T %v, Python = %q", index, argv, err, err, *want.Error)
			}
			if usage.Message != *want.Error {
				t.Fatalf("case %d %#v error mismatch\nGo:     %q\nPython: %q", index, argv, usage.Message, *want.Error)
			}
			continue
		}
		if err != nil {
			t.Fatalf("case %d %#v Go error = %v, Python succeeded", index, argv, err)
		}
		got := &clientCLIOracleResult{
			RequestedMethod: result.Invocation.RequestedMethod,
			Method:          result.Invocation.Method,
			Params:          result.Invocation.Params,
			Notice:          result.Notice,
		}
		if !reflect.DeepEqual(normalizeClientCLIOracle(got), normalizeClientCLIOracle(want.Result)) {
			gotJSON, _ := json.MarshalIndent(got, "", "  ")
			wantJSON, _ := json.MarshalIndent(want.Result, "", "  ")
			t.Fatalf("case %d %#v mismatch\nGo:     %s\nPython: %s", index, argv, gotJSON, wantJSON)
		}
	}
}

func normalizeClientCLIOracle(value any) any {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		panic(err)
	}
	return normalized
}

func clientCLIOraclePaths(t *testing.T) (sdkRoot, script string) {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate client CLI oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot = os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	script = filepath.Join(daemonRoot, "compat", "client_cli_oracle.py")
	for _, required := range []string{
		filepath.Join(sdkRoot, "lbry", "__init__.py"),
		filepath.Join(sdkRoot, "lbry", "extras", "cli.py"),
		filepath.Join(sdkRoot, "lbry", "extras", "daemon", "daemon.py"),
		script,
	} {
		if _, err := os.Stat(required); errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned Python client CLI oracle source is unavailable: %s", required)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	return sdkRoot, script
}

func clientCLIOraclePython(t *testing.T, sdkRoot string) string {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	check := exec.Command(python, "-c", "import docopt; assert docopt.__version__ == '0.6.2'")
	check.Dir = sdkRoot
	if output, err := check.CombinedOutput(); err != nil {
		t.Skipf("Python docopt 0.6.2 is unavailable: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	return python
}

func runClientCLIOracle(
	t *testing.T, python, script, sdkRoot string, cases [][]string,
) clientCLIOracleOutput {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"cases": cases})
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(python, script, "--sdk-root", sdkRoot)
	command.Stdin = bytes.NewReader(payload)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("Python client CLI oracle failed: %v\n%s", err, stderr.String())
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.UseNumber()
	var result clientCLIOracleOutput
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("decode Python client CLI oracle: %v\n%s", err, output)
	}
	return result
}
