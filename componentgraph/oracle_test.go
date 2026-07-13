package componentgraph

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

const (
	componentOraclePinnedCommit  = "e7666f489418e96b6d2104974e93915b539235c5"
	componentOraclePinnedVersion = "0.113.0"
)

type componentOracleDefinition struct {
	Class     string   `json:"class"`
	Name      string   `json:"name"`
	DependsOn []string `json:"depends_on"`
}

type componentOracleCase struct {
	Skipped    []string   `json:"skipped"`
	Start      [][]string `json:"start"`
	Stop       [][]string `json:"stop"`
	Unresolved []string   `json:"unresolved"`
}

type componentOracleOutput struct {
	Reference struct {
		Commit  string `json:"commit"`
		Version string `json:"version"`
	} `json:"reference"`
	Components []componentOracleDefinition `json:"components"`
	Cases      []componentOracleCase       `json:"cases"`
}

func TestLegacyGraphMatchesPinnedPythonASTOracle(t *testing.T) {
	sdkRoot, script := componentOraclePaths(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}

	allComponents := LegacyComponents()
	allSkipped := make([]string, len(allComponents))
	for index, component := range allComponents {
		allSkipped[index] = component.Name
	}
	skipCases := [][]string{
		{},
		{HashAnnouncer},
		{DHT},
		{DHT, HashAnnouncer},
		{Database},
		{"unknown-component"},
		allSkipped,
	}
	oracle := runComponentOracle(t, python, script, sdkRoot, skipCases)
	if oracle.Reference.Commit != componentOraclePinnedCommit || oracle.Reference.Version != componentOraclePinnedVersion {
		t.Fatalf("oracle reference = %#v", oracle.Reference)
	}

	wantComponents := make([]Component, len(oracle.Components))
	for index, component := range oracle.Components {
		wantComponents[index] = Component{Name: component.Name, DependsOn: component.DependsOn}
	}
	if got := LegacyComponents(); !reflect.DeepEqual(got, wantComponents) {
		gotJSON, _ := json.MarshalIndent(got, "", "  ")
		wantJSON, _ := json.MarshalIndent(wantComponents, "", "  ")
		t.Fatalf("legacy component definitions differ\nGo:     %s\nPython: %s", gotJSON, wantJSON)
	}
	if len(oracle.Cases) != len(skipCases) {
		t.Fatalf("oracle cases = %d, want %d", len(oracle.Cases), len(skipCases))
	}
	for index, oracleCase := range oracle.Cases {
		if !reflect.DeepEqual(oracleCase.Skipped, skipCases[index]) {
			t.Fatalf("case %d skipped = %#v, want %#v", index, oracleCase.Skipped, skipCases[index])
		}
		start, startErr := LegacyStartStages(skipCases[index])
		stop, stopErr := LegacyStopStages(skipCases[index])
		if oracleCase.Unresolved != nil {
			if startErr == nil || stopErr == nil {
				t.Fatalf("case %d Python unresolved=%v, Go start=%v err=%v stop=%v err=%v", index, oracleCase.Unresolved, start, startErr, stop, stopErr)
			}
			continue
		}
		if startErr != nil || stopErr != nil {
			t.Fatalf("case %d Go errors: start=%v stop=%v", index, startErr, stopErr)
		}
		if !reflect.DeepEqual(start, oracleCase.Start) || !reflect.DeepEqual(stop, oracleCase.Stop) {
			t.Fatalf(
				"case %d skipped=%v mismatch\nGo start:     %#v\nPython start: %#v\nGo stop:      %#v\nPython stop:  %#v",
				index, skipCases[index], start, oracleCase.Start, stop, oracleCase.Stop,
			)
		}
	}
}

func componentOraclePaths(t *testing.T) (sdkRoot, script string) {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate component graph oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot = os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	script = filepath.Join(daemonRoot, "compat", "component_graph_oracle.py")
	for _, required := range []string{
		filepath.Join(sdkRoot, "lbry", "__init__.py"),
		filepath.Join(sdkRoot, "lbry", "extras", "daemon", "component.py"),
		filepath.Join(sdkRoot, "lbry", "extras", "daemon", "componentmanager.py"),
		filepath.Join(sdkRoot, "lbry", "extras", "daemon", "components.py"),
		script,
	} {
		if _, err := os.Stat(required); errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned local Python SDK component source is unavailable: %s", required)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	return sdkRoot, script
}

func runComponentOracle(
	t *testing.T,
	python, script, sdkRoot string,
	skipCases [][]string,
) componentOracleOutput {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"skip_cases": skipCases})
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(python, script, "--sdk-root", sdkRoot)
	command.Stdin = bytes.NewReader(payload)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("Python component graph oracle failed: %v\n%s", err, stderr.String())
	}
	var result componentOracleOutput
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("decode Python component graph oracle: %v\n%s", err, output)
	}
	return result
}
