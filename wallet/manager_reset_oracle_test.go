package wallet

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

type managerResetOracleCase struct {
	Events [][]any        `json:"events"`
	Config map[string]any `json:"config"`
	Error  any            `json:"error"`
}

func TestWalletManagerResetMatchesPinnedPythonOracle(t *testing.T) {
	response := runManagerResetOracle(t)
	for _, name := range []string{"unset", "explicit", "stop_failure", "start_failure"} {
		var result managerResetOracleCase
		encoded, err := json.Marshal(response[name])
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(encoded, &result); err != nil {
			t.Fatal(err)
		}
		if result.Config["auto_connect"] != true || result.Config["default_servers"] == nil {
			t.Fatalf("Python %s reset config = %#v", name, result.Config)
		}
	}
	unset := response["unset"].(map[string]any)["config"].(map[string]any)
	explicit := response["explicit"].(map[string]any)["config"].(map[string]any)
	if !reflect.DeepEqual(unset["explicit_servers"], []any{}) ||
		!reflect.DeepEqual(explicit["explicit_servers"], []any{[]any{"explicit", float64(50002)}}) {
		t.Fatalf("Python explicit transition = unset %#v, explicit %#v",
			unset["explicit_servers"], explicit["explicit_servers"])
	}
	stopEvents := response["stop_failure"].(map[string]any)["events"].([]any)
	startEvents := response["start_failure"].(map[string]any)["events"].([]any)
	if len(stopEvents) != 1 || len(startEvents) != 2 {
		t.Fatalf("Python failure order = stop %d, start %d", len(stopEvents), len(startEvents))
	}
}

func runManagerResetOracle(t *testing.T) map[string]any {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate manager reset oracle")
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
	command := exec.Command(python, filepath.Join(daemonRoot, "compat", "manager_reset_oracle.py"), "--sdk-root", sdkRoot)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("Python manager reset oracle failed: %v\n%s", err, stderr.String())
	}
	var response map[string]any
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("decode manager reset oracle: %v\n%s", err, output)
	}
	return response
}
