package database

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
	databaseOraclePinnedCommit  = "e7666f489418e96b6d2104974e93915b539235c5"
	databaseOraclePinnedVersion = "0.113.0"
)

type databaseOracleCase struct {
	Name      string `json:"name"`
	Exists    *bool  `json:"exists,omitempty"`
	Contents  string `json:"contents,omitempty"`
	Migration string `json:"migration,omitempty"`
}

type databaseOracleOutcome struct {
	Name          string  `json:"name"`
	Calls         [][]int `json:"calls"`
	ErrorType     *string `json:"error_type"`
	Error         *string `json:"error"`
	FinalExists   bool    `json:"final_exists"`
	FinalContents *string `json:"final_contents"`
}

type databaseOracleOutput struct {
	Reference struct {
		Commit  string `json:"commit"`
		Version string `json:"version"`
	} `json:"reference"`
	Metadata struct {
		CurrentRevision  int    `json:"current_revision"`
		RevisionFilename string `json:"revision_filename"`
		SQLiteFilename   string `json:"sqlite_filename"`
	} `json:"metadata"`
	Cases []databaseOracleOutcome `json:"cases"`
}

func TestRevisionLifecycleMatchesPinnedPythonOracle(t *testing.T) {
	sdkRoot, script := databaseOraclePaths(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	missing := false
	cases := []databaseOracleCase{
		{Name: "missing", Exists: &missing},
		{Name: "current", Contents: "15"},
		{Name: "python integer forms", Contents: "\x1c +0_0_1_5\x1f"},
		{Name: "unicode digits", Contents: "\u0661\uff15"},
		{Name: "older", Contents: "14"},
		{Name: "migration error", Contents: "14", Migration: "error"},
		{Name: "newer", Contents: "  +0016\n"},
		{Name: "empty", Contents: ""},
		{Name: "invalid", Contents: "15.0"},
	}
	oracle := runDatabaseOracle(t, python, script, sdkRoot, cases)
	if oracle.Reference.Commit != databaseOraclePinnedCommit || oracle.Reference.Version != databaseOraclePinnedVersion {
		t.Fatalf("oracle reference = %#v", oracle.Reference)
	}
	if oracle.Metadata.CurrentRevision != CurrentRevision ||
		oracle.Metadata.RevisionFilename != RevisionFilename ||
		oracle.Metadata.SQLiteFilename != SQLiteFilename {
		t.Fatalf("oracle metadata = %#v", oracle.Metadata)
	}

	got := make([]databaseOracleOutcome, len(cases))
	for index, fixture := range cases {
		got[index] = executeGoRevisionCase(t, fixture)
	}
	if !reflect.DeepEqual(got, oracle.Cases) {
		gotJSON, _ := json.MarshalIndent(got, "", "  ")
		wantJSON, _ := json.MarshalIndent(oracle.Cases, "", "  ")
		t.Fatalf("database revision outcomes differ\nGo:     %s\nPython: %s", gotJSON, wantJSON)
	}
}

func executeGoRevisionCase(t *testing.T, fixture databaseOracleCase) databaseOracleOutcome {
	t.Helper()
	directory := t.TempDir()
	path := RevisionPath(directory)
	if fixture.Exists == nil || *fixture.Exists {
		if err := os.WriteFile(path, []byte(fixture.Contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	fixtureMigrationError := errors.New("fixture migration failed")
	outcome := databaseOracleOutcome{Name: fixture.Name, Calls: make([][]int, 0)}
	_, err := EnsureRevision(directory, func(fromRevision, toRevision int) error {
		outcome.Calls = append(outcome.Calls, []int{fromRevision, toRevision})
		if fixture.Migration == "error" {
			return fixtureMigrationError
		}
		return nil
	})
	if err != nil {
		message := err.Error()
		outcome.Error = &message
		errorType := "error"
		var invalid *InvalidRevisionError
		var incompatible *IncompatibleRevisionError
		switch {
		case errors.As(err, &invalid):
			errorType = "ValueError"
		case errors.As(err, &incompatible):
			errorType = "Exception"
		case errors.Is(err, fixtureMigrationError):
			errorType = "RuntimeError"
		}
		outcome.ErrorType = &errorType
	}

	contents, readErr := os.ReadFile(path)
	switch {
	case readErr == nil:
		outcome.FinalExists = true
		text := string(contents)
		outcome.FinalContents = &text
	case errors.Is(readErr, os.ErrNotExist):
	case readErr != nil:
		t.Fatal(readErr)
	}
	return outcome
}

func databaseOraclePaths(t *testing.T) (sdkRoot, script string) {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate database oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot = os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	script = filepath.Join(daemonRoot, "compat", "database_revision_oracle.py")
	for _, required := range []string{
		filepath.Join(sdkRoot, "lbry", "__init__.py"),
		filepath.Join(sdkRoot, "lbry", "extras", "daemon", "components.py"),
		script,
	} {
		if _, err := os.Stat(required); errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned local Python SDK database source is unavailable: %s", required)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	return sdkRoot, script
}

func runDatabaseOracle(
	t *testing.T,
	python, script, sdkRoot string,
	cases []databaseOracleCase,
) databaseOracleOutput {
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
		t.Fatalf("Python database revision oracle failed: %v\n%s", err, stderr.String())
	}
	var result databaseOracleOutput
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("decode Python database revision oracle: %v\n%s", err, output)
	}
	return result
}
