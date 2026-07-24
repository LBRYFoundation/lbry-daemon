package wallet

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
	lifecycleOraclePinnedCommit  = "e7666f489418e96b6d2104974e93915b539235c5"
	lifecycleOraclePinnedVersion = "0.113.0"
)

type lifecycleOracleResponse struct {
	Reference struct {
		Commit  string `json:"commit"`
		Version string `json:"version"`
	} `json:"reference"`
	Metadata struct {
		PythonAssertions bool   `json:"python_assertions"`
		PythonVersion    string `json:"python_version"`
	} `json:"metadata"`
	WalletManager struct {
		Empty struct {
			AfterStart bool `json:"after_start"`
			AfterStop  bool `json:"after_stop"`
		} `json:"empty"`
		StartSuccess lifecycleManagerProbe `json:"start_success"`
		StartFailure lifecycleManagerProbe `json:"start_failure"`
		StopSuccess  lifecycleManagerProbe `json:"stop_success"`
		StopFailure  lifecycleManagerProbe `json:"stop_failure"`
	} `json:"wallet_manager"`
	LedgerStart []struct {
		ChildFailures  []string `json:"child_failures"`
		ChildAttempts  []string `json:"child_attempts"`
		ErrorType      string   `json:"error_type"`
		ReachedNetwork bool     `json:"reached_network"`
	} `json:"ledger_start"`
	ComponentManager struct {
		StartChildFailure struct {
			ErrorType           *string `json:"error_type"`
			StartedSet          bool    `json:"started_set"`
			LaterStageAttempted bool    `json:"later_stage_attempted"`
			FailedRunning       bool    `json:"failed_running"`
			PeerRunning         bool    `json:"peer_running"`
			DependentRunning    bool    `json:"dependent_running"`
		} `json:"start_child_failure"`
		StopChildFailure struct {
			ErrorType           *string `json:"error_type"`
			LaterStageAttempted bool    `json:"later_stage_attempted"`
			FailedRunning       bool    `json:"failed_running"`
			BaseRunning         bool    `json:"base_running"`
		} `json:"stop_child_failure"`
	} `json:"component_manager"`
	WalletComponent struct {
		StartFailure lifecycleWalletComponentProbe `json:"start_failure"`
		StopFailure  lifecycleWalletComponentProbe `json:"stop_failure"`
		StopSuccess  struct {
			ManagerCleared bool `json:"manager_cleared"`
		} `json:"stop_success"`
	} `json:"wallet_component"`
	UnhandledChildErrors []string `json:"unhandled_child_errors"`
}

type lifecycleManagerProbe struct {
	ErrorType   string `json:"error_type"`
	Running     bool   `json:"running"`
	AllEntered  bool   `json:"all_entered"`
	RunningSeen []bool `json:"running_seen"`
}

type lifecycleWalletComponentProbe struct {
	ErrorType       string `json:"error_type"`
	ManagerRetained bool   `json:"manager_retained"`
}

func TestLifecycleMatchesPinnedPythonOracle(t *testing.T) {
	oracle := runLifecycleOracle(t)
	if oracle.Reference.Commit != lifecycleOraclePinnedCommit ||
		oracle.Reference.Version != lifecycleOraclePinnedVersion {
		t.Fatalf("oracle reference = %+v", oracle.Reference)
	}
	if !oracle.Metadata.PythonAssertions {
		t.Fatal("wallet lifecycle oracle ran with Python assertions disabled")
	}
	if want := os.Getenv("LBRY_ORACLE_PYTHON_VERSION"); want != "" &&
		oracle.Metadata.PythonVersion != want {
		t.Fatalf("oracle Python version = %q, want %q", oracle.Metadata.PythonVersion, want)
	}

	if !oracle.WalletManager.Empty.AfterStart || oracle.WalletManager.Empty.AfterStop {
		t.Fatalf("empty manager lifecycle = %+v", oracle.WalletManager.Empty)
	}
	assertLifecycleManagerProbe(t, "successful start", oracle.WalletManager.StartSuccess,
		"", true)
	assertLifecycleManagerProbe(t, "failed start", oracle.WalletManager.StartFailure,
		"StartFailure", true)
	assertLifecycleManagerProbe(t, "successful stop", oracle.WalletManager.StopSuccess,
		"", false)
	assertLifecycleManagerProbe(t, "failed stop", oracle.WalletManager.StopFailure,
		"StopFailure", true)

	wantLedgerFailures := [][]string{
		{"DatabaseOpenFailure"},
		{"HeadersOpenFailure"},
		{"DatabaseOpenFailure", "HeadersOpenFailure"},
	}
	if len(oracle.LedgerStart) != len(wantLedgerFailures) {
		t.Fatalf("ledger probes = %d, want %d", len(oracle.LedgerStart), len(wantLedgerFailures))
	}
	for index, probe := range oracle.LedgerStart {
		if !reflect.DeepEqual(probe.ChildFailures, wantLedgerFailures[index]) ||
			!reflect.DeepEqual(probe.ChildAttempts, []string{"database", "headers"}) ||
			probe.ErrorType != "ConnectedGateFailure" || !probe.ReachedNetwork {
			t.Fatalf("ledger probe %d = %+v", index, probe)
		}
	}

	start := oracle.ComponentManager.StartChildFailure
	if start.ErrorType != nil || !start.StartedSet || !start.LaterStageAttempted ||
		start.FailedRunning || !start.PeerRunning || !start.DependentRunning {
		t.Fatalf("component manager failed-start behavior = %+v", start)
	}
	stop := oracle.ComponentManager.StopChildFailure
	if stop.ErrorType != nil || !stop.LaterStageAttempted || !stop.FailedRunning || stop.BaseRunning {
		t.Fatalf("component manager failed-stop behavior = %+v", stop)
	}

	if start := oracle.WalletComponent.StartFailure; start.ErrorType != "StartFailure" ||
		!start.ManagerRetained {
		t.Fatalf("wallet component failed start = %+v", start)
	}
	if stop := oracle.WalletComponent.StopFailure; stop.ErrorType != "StopFailure" ||
		!stop.ManagerRetained {
		t.Fatalf("wallet component failed stop = %+v", stop)
	}
	if !oracle.WalletComponent.StopSuccess.ManagerCleared {
		t.Fatal("wallet component did not clear manager after successful stop")
	}

	wantUnhandled := []string{
		"ComponentSetupFailure", "ComponentStopFailure",
		"DatabaseOpenFailure", "DatabaseOpenFailure",
		"HeadersOpenFailure", "HeadersOpenFailure",
	}
	if !reflect.DeepEqual(oracle.UnhandledChildErrors, wantUnhandled) {
		t.Fatalf("unhandled child errors = %v, want %v", oracle.UnhandledChildErrors, wantUnhandled)
	}
}

func assertLifecycleManagerProbe(
	t *testing.T, name string, probe lifecycleManagerProbe, errorType string, running bool,
) {
	t.Helper()
	if probe.ErrorType != errorType || probe.Running != running || !probe.AllEntered ||
		!reflect.DeepEqual(probe.RunningSeen, []bool{true, true}) {
		t.Fatalf("%s = %+v", name, probe)
	}
}

func runLifecycleOracle(t *testing.T) lifecycleOracleResponse {
	t.Helper()
	sdkRoot, script := lifecycleOraclePaths(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	command := exec.Command(python, script, "--sdk-root", sdkRoot)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("Python wallet lifecycle oracle failed: %v\n%s", err, stderr.String())
	}
	var oracle lifecycleOracleResponse
	if err := json.Unmarshal(output, &oracle); err != nil {
		t.Fatalf("decode Python wallet lifecycle oracle: %v\n%s", err, output)
	}
	return oracle
}

func lifecycleOraclePaths(t *testing.T) (sdkRoot, script string) {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate wallet lifecycle oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot = os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	script = filepath.Join(daemonRoot, "compat", "wallet_lifecycle_oracle.py")
	for _, required := range []string{
		filepath.Join(sdkRoot, "lbry", "wallet", "manager.py"),
		filepath.Join(sdkRoot, "lbry", "wallet", "ledger.py"),
		filepath.Join(sdkRoot, "lbry", "extras", "daemon", "componentmanager.py"),
		filepath.Join(sdkRoot, "lbry", "extras", "daemon", "components.py"),
		script,
	} {
		if _, err := os.Stat(required); errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned local Python SDK lifecycle source is unavailable: %s", required)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	return sdkRoot, script
}
