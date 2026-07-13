package rpc

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGetWrapperPinnedPythonOracle(t *testing.T) {
	oracle := runGetOracle(t)
	cases := oracle["cases"].(map[string]any)
	if cases["success"].(map[string]any)["result"] != "stream" {
		t.Fatalf("Python get success = %#v", cases["success"])
	}
	missing := cases["missing_directory"].(map[string]any)
	missingResult := missing["result"].(map[string]any)
	if !strings.Contains(missingResult["error"].(string), "specified download directory") ||
		len(missing["download_calls"].([]any)) != 0 {
		t.Fatalf("Python missing-directory get = %#v", missing)
	}
	noneResult := cases["none"].(map[string]any)["result"].(map[string]any)
	failureResult := cases["failure"].(map[string]any)["result"].(map[string]any)
	if noneResult["error"] != "lbry://name" || failureResult["error"] != "download failed" {
		t.Fatalf("Python get errors = none %#v, failure %#v", noneResult, failureResult)
	}
}

func runGetOracle(t *testing.T) map[string]any {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate get oracle")
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
	command := exec.Command(python, filepath.Join(daemonRoot, "compat", "get_oracle.py"), "--sdk-root", sdkRoot)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("Python get oracle failed: %v\n%s", err, stderr.String())
	}
	var response map[string]any
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("decode get oracle: %v\n%s", err, output)
	}
	return response
}
