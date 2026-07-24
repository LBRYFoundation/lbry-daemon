package config

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

const (
	cliOraclePinnedCommit  = "e7666f489418e96b6d2104974e93915b539235c5"
	cliOraclePinnedVersion = "0.113.0"
)

type cliOracleResult struct {
	Command        string         `json:"command"`
	Settings       map[string]any `json:"settings"`
	Help           bool           `json:"help"`
	Version        bool           `json:"version"`
	Quiet          bool           `json:"quiet"`
	NoLogging      bool           `json:"no_logging"`
	Verbose        []string       `json:"verbose"`
	VerboseSet     bool           `json:"verbose_set"`
	InitialHeaders string         `json:"initial_headers"`
	Unknown        []string       `json:"unknown"`
}

type cliOracleCase struct {
	Argv   []string         `json:"argv"`
	Result *cliOracleResult `json:"result"`
	Error  *string          `json:"error"`
}

type cliOracleOutput struct {
	Reference struct {
		Commit  string `json:"commit"`
		Version string `json:"version"`
	} `json:"reference"`
	Cases []cliOracleCase `json:"cases"`
}

func TestCommandLineMatchesPinnedPythonOracle(t *testing.T) {
	sdkRoot, script := cliOraclePaths(t)
	python := cliOraclePython(t, sdkRoot)
	cases := [][]string{
		{},
		{"--api", "root:5279", "--version", "--help"},
		{"--api", "root:5279", "--config", "root.yml", "--help", "--version", "start", "--tcp-port", "4445", "--version"},
		{"start", "--use-upnp", "--no-use-upnp", "--use-upnp", "--save-files"},
		{"start", "--components-to-skip", "wallet", "--components-to-skip=dht", "--known-dht-nodes=a.example:4444", "--known-dht-nodes", "bad"},
		{"start", "--max-key-fee", "-1", "usd"},
		{"start", "--max-key-fee=null", "--no-max-key-fee"},
		{"start", "--max-key-fee=2", "BTC"},
		{"start", "--max-key-fee=null", "junk"},
		{"--root-unknown", "start", "--start-unknown=value", "positional", "--tcp-p", "4"},
		{"start", "--verbose", "lbry", "aiohttp", "--initial-h", "headers.bin", "--quiet"},
		{"start", "--verbose=lbry", "aiohttp"},
		{"--", "--version"},
		{"--", "start", "--tcp-port", "4"},
		{"start", "--", "--tcp-port", "4"},
		{"-vv"},
		{"-vfoo"},
		{"-v=foo"},
		{"start", "--tcp-port"},
		{"start", "--max-key-fee", "--quiet"},
		{"start", "--stream"},
		{"start", "--use-upnp=false"},
	}

	oracle := runCLIOracle(t, python, script, sdkRoot, cases)
	if oracle.Reference.Commit != cliOraclePinnedCommit || oracle.Reference.Version != cliOraclePinnedVersion {
		t.Fatalf("oracle reference = %#v", oracle.Reference)
	}
	if len(oracle.Cases) != len(cases) {
		t.Fatalf("oracle cases = %d, want %d", len(oracle.Cases), len(cases))
	}

	for index, argv := range cases {
		want := oracle.Cases[index]
		if !reflect.DeepEqual(want.Argv, argv) {
			t.Fatalf("case %d oracle argv = %#v, want %#v", index, want.Argv, argv)
		}
		parsed, err := ParseCommandLine(argv)
		if want.Error != nil {
			var usage *UsageError
			if !errors.As(err, &usage) {
				t.Fatalf("case %d argv %#v: Go error = %T %v, Python usage error = %q", index, argv, err, err, *want.Error)
			}
			if err.Error() != *want.Error {
				t.Fatalf("case %d argv %#v error mismatch\nGo:     %q\nPython: %q", index, argv, err.Error(), *want.Error)
			}
			continue
		}
		if err != nil {
			t.Fatalf("case %d argv %#v: Go error = %v, Python succeeded", index, argv, err)
		}
		got := commandLineOracleResult(parsed)
		if !reflect.DeepEqual(normalizeCLIOracleValue(got), normalizeCLIOracleValue(want.Result)) {
			gotJSON, _ := json.MarshalIndent(got, "", "  ")
			wantJSON, _ := json.MarshalIndent(want.Result, "", "  ")
			t.Fatalf("case %d argv %#v mismatch\nGo:     %s\nPython: %s", index, argv, gotJSON, wantJSON)
		}
	}
}

func commandLineOracleResult(parsed CommandLine) *cliOracleResult {
	return &cliOracleResult{
		Command:        parsed.Command,
		Settings:       parsed.Settings,
		Help:           parsed.Help,
		Version:        parsed.Version,
		Quiet:          parsed.Quiet,
		NoLogging:      parsed.NoLogging,
		Verbose:        parsed.Verbose,
		VerboseSet:     parsed.VerboseSet,
		InitialHeaders: parsed.InitialHeaders,
		Unknown:        parsed.Unknown,
	}
}

func normalizeCLIOracleValue(value any) any {
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

func cliOraclePaths(t *testing.T) (sdkRoot, script string) {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate CLI oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot = os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	script = filepath.Join(daemonRoot, "compat", "cli_oracle.py")
	for _, required := range []string{
		filepath.Join(sdkRoot, "lbry", "__init__.py"),
		filepath.Join(sdkRoot, "lbry", "conf.py"),
		filepath.Join(sdkRoot, "lbry", "extras", "cli.py"),
		filepath.Join(sdkRoot, "lbry", "wallet", "coinselection.py"),
		script,
	} {
		if _, err := os.Stat(required); errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned local Python SDK CLI oracle source is unavailable: %s", required)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	return sdkRoot, script
}

func cliOraclePython(t *testing.T, sdkRoot string) string {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	check := exec.Command(python, "-c", "import yaml")
	check.Dir = sdkRoot
	if output, err := check.CombinedOutput(); err != nil {
		t.Skipf("PyYAML is unavailable to the Python CLI oracle: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	return python
}

func runCLIOracle(t *testing.T, python, script, sdkRoot string, cases [][]string) cliOracleOutput {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"cases": cases})
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(
		python,
		script,
		"--sdk-root", sdkRoot,
		"--work-dir", t.TempDir(),
	)
	command.Stdin = bytes.NewReader(payload)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("Python CLI oracle failed: %v\n%s", err, stderr.String())
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.UseNumber()
	var result cliOracleOutput
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("decode Python CLI oracle: %v\n%s", err, output)
	}
	return result
}
