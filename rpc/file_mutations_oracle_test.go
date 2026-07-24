package rpc

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFileMutationMethodsPinnedPythonOracle(t *testing.T) {
	oracle := runFileMutationsOracle(t)
	cases := oracle["cases"].(map[string]any)
	results := map[string]any{
		"resume": "Resumed download", "stop": "Stopped download",
		"already_stopped": "File was already stopped", "delete_guard": false,
		"delete_all": true, "save_missing": false, "save_multiple": false,
		"save_one": "one",
	}
	for name, want := range results {
		got := cases[name].(map[string]any)["result"]
		if got != want {
			t.Errorf("Python %s result = %#v, want %#v", name, got, want)
		}
	}
	invalid := cases["invalid_status"].(map[string]any)["error"].(map[string]any)
	missing := cases["missing_status"].(map[string]any)["error"].(map[string]any)
	if invalid["message"] != `Status must be "start" or "stop".` ||
		missing["message"] != "Unable to find a file for {'sd_hash': 'x'}" {
		t.Fatalf("Python mutation errors = invalid %#v, missing %#v", invalid, missing)
	}
	saveCalls := cases["save_one"].(map[string]any)["stream_calls"].(map[string]any)["one"].([]any)
	if len(saveCalls) != 1 {
		t.Fatalf("Python save calls = %#v", saveCalls)
	}
}

func runFileMutationsOracle(t *testing.T) map[string]any {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate file mutations oracle")
	}
	daemonRoot := filepath.Dir(filepath.Dir(source))
	sdkRoot := os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	command := exec.Command(python,
		filepath.Join(daemonRoot, "compat", "file_mutations_oracle.py"), "--sdk-root", sdkRoot,
	)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("Python file mutation oracle failed: %v\n%s", err, stderr.String())
	}
	var response map[string]any
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("decode file mutation oracle: %v\n%s", err, output)
	}
	return response
}
