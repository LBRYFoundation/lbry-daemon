package main

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
	statusOraclePinnedCommit  = "e7666f489418e96b6d2104974e93915b539235c5"
	statusOraclePinnedVersion = "0.113.0"
)

type statusOracleCase struct {
	InstallationID    string                    `json:"installation_id"`
	SkippedComponents []string                  `json:"skipped_components"`
	StartupStatus     map[string]bool           `json:"startup_status"`
	FFmpegStatus      map[string]any            `json:"ffmpeg_status"`
	ComponentStatus   map[string]map[string]any `json:"component_status,omitempty"`
	ComponentFixtures map[string]map[string]any `json:"component_fixtures,omitempty"`
}

type statusOracleOutput struct {
	Reference struct {
		Commit  string `json:"commit"`
		Version string `json:"version"`
	} `json:"reference"`
	Responses []map[string]any `json:"responses"`
}

func TestComponentStatusMatchesPinnedPythonOracle(t *testing.T) {
	cases := []statusOracleCase{
		statusOracleFixture("before-start", nil, nil),
		statusOracleFixture("partially-running", []string{"wallet", "libtorrent_component"}, map[string]bool{
			"blob_manager": true, "dht": true, "peer_protocol_server": true,
		}),
		statusOracleFixture("fully-running", []string{"wallet"}, statusOracleAllRunning([]string{"wallet"})),
		statusOracleFixture("everything-skipped", append([]string(nil), legacyComponents...), map[string]bool{}),
	}
	oracle := runStatusOracle(t, cases)

	for index, fixture := range cases {
		status := newComponentStatus(fixture.InstallationID, fixture.SkippedComponents, fixture.FFmpegStatus)
		for name, running := range fixture.StartupStatus {
			status.setComponent(name, running)
		}
		got := normalizeStatusOracleValue(status.Status())
		want := normalizeStatusOracleValue(oracle.Responses[index])
		if !reflect.DeepEqual(got, want) {
			gotJSON, _ := json.MarshalIndent(got, "", "  ")
			wantJSON, _ := json.MarshalIndent(want, "", "  ")
			t.Fatalf("case %d status mismatch\nGo:     %s\nPython: %s", index, gotJSON, wantJSON)
		}
	}
}

func TestPinnedPythonStatusComponentDetails(t *testing.T) {
	ffmpeg := map[string]any{
		"analyze_audio_volume": true,
		"available":            false,
		"which":                nil,
	}
	componentNames := map[string]bool{
		"blob_manager":         false,
		"dht":                  false,
		"peer_protocol_server": false,
		"upnp":                 false,
	}
	emptyFixtures := map[string]map[string]any{
		"blob_manager":         {"started": false},
		"dht":                  {"node_id": nil},
		"peer_protocol_server": {},
		"upnp":                 {"redirects": map[string]any{}, "gateway": nil, "external_ip": nil},
	}

	nodeID := strings.Repeat("AB", 48)
	connections := map[string]any{
		"incoming_bps":       map[string]any{"192.0.2.10:4444": 125.5},
		"outgoing_bps":       map[string]any{"198.51.100.20:5567": 250.25},
		"total_incoming_mbs": 0.000125,
		"total_outgoing_mbs": 0.000250,
		"total_sent":         1000,
		"total_received":     500,
		"max_incoming_mbs":   0.5,
		"max_outgoing_mbs":   0.75,
	}
	runningComponents := map[string]bool{
		"blob_manager":         true,
		"dht":                  true,
		"peer_protocol_server": true,
		"upnp":                 true,
	}
	runningFixtures := map[string]map[string]any{
		"blob_manager": {
			"started": true, "finished_blobs": 3, "connections": connections,
		},
		"dht": {
			"node_id": nodeID, "peers_in_routing_table": 2,
		},
		"peer_protocol_server": {},
		"upnp": {
			"redirects": map[string]any{"UDP": 4444, "TCP": 5567},
			"gateway":   "Fixture Gateway", "external_ip": "203.0.113.8",
		},
	}

	skipped := []string{"blob_manager", "dht", "upnp"}
	cases := []statusOracleCase{
		{
			InstallationID: "details-before-start", SkippedComponents: []string{},
			StartupStatus: componentNames, FFmpegStatus: ffmpeg, ComponentFixtures: emptyFixtures,
		},
		{
			InstallationID: "details-running", SkippedComponents: []string{},
			StartupStatus: runningComponents, FFmpegStatus: ffmpeg, ComponentFixtures: runningFixtures,
		},
		{
			InstallationID: "details-skipped", SkippedComponents: skipped,
			StartupStatus: map[string]bool{"peer_protocol_server": true},
			FFmpegStatus:  ffmpeg, ComponentFixtures: runningFixtures,
		},
	}

	want := []map[string]any{
		{
			"installation_id": "details-before-start", "is_running": false,
			"skipped_components": []string{}, "startup_status": componentNames, "ffmpeg_status": ffmpeg,
			"blob_manager": map[string]any{"finished_blobs": 0, "connections": map[string]any{}},
			"dht":          map[string]any{"node_id": nil, "peers_in_routing_table": 0},
			"upnp": map[string]any{
				"aioupnp_version": "0.0.18", "redirects": map[string]any{},
				"gateway": "No gateway found", "dht_redirect_set": false,
				"peer_redirect_set": false, "external_ip": nil,
			},
		},
		{
			"installation_id": "details-running", "is_running": true,
			"skipped_components": []string{}, "startup_status": runningComponents, "ffmpeg_status": ffmpeg,
			"blob_manager": map[string]any{"finished_blobs": 3, "connections": connections},
			"dht":          map[string]any{"node_id": strings.ToLower(nodeID), "peers_in_routing_table": 2},
			"upnp": map[string]any{
				"aioupnp_version": "0.0.18", "redirects": map[string]any{"UDP": 4444, "TCP": 5567},
				"gateway": "Fixture Gateway", "dht_redirect_set": true,
				"peer_redirect_set": true, "external_ip": "203.0.113.8",
			},
		},
		{
			"installation_id": "details-skipped", "is_running": true,
			"skipped_components": skipped,
			"startup_status":     map[string]bool{"peer_protocol_server": true},
			"ffmpeg_status":      ffmpeg,
		},
	}

	oracle := runStatusOracle(t, cases)
	for index := range want {
		gotValue := normalizeStatusOracleValue(oracle.Responses[index])
		wantValue := normalizeStatusOracleValue(want[index])
		if !reflect.DeepEqual(gotValue, wantValue) {
			gotJSON, _ := json.MarshalIndent(gotValue, "", "  ")
			wantJSON, _ := json.MarshalIndent(wantValue, "", "  ")
			t.Fatalf("detail case %d mismatch\nGot:  %s\nWant: %s", index, gotJSON, wantJSON)
		}
		if _, exists := oracle.Responses[index]["peer_protocol_server"]; exists {
			t.Fatalf("detail case %d unexpectedly includes peer_protocol_server detail", index)
		}
	}
}

func runStatusOracle(t *testing.T, cases []statusOracleCase) statusOracleOutput {
	t.Helper()
	sdkRoot, script := statusOraclePaths(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
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
		t.Fatalf("Python status oracle failed: %v\n%s", err, stderr.String())
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.UseNumber()
	var oracle statusOracleOutput
	if err := decoder.Decode(&oracle); err != nil {
		t.Fatalf("decode Python status oracle: %v\n%s", err, output)
	}
	if oracle.Reference.Commit != statusOraclePinnedCommit || oracle.Reference.Version != statusOraclePinnedVersion {
		t.Fatalf("oracle reference = %#v", oracle.Reference)
	}
	if len(oracle.Responses) != len(cases) {
		t.Fatalf("oracle responses = %d, want %d", len(oracle.Responses), len(cases))
	}
	return oracle
}

func statusOracleFixture(
	installationID string,
	skipped []string,
	overrides map[string]bool,
) statusOracleCase {
	skippedSet := make(map[string]struct{}, len(skipped))
	for _, name := range skipped {
		skippedSet[name] = struct{}{}
	}
	startup := make(map[string]bool)
	for _, name := range legacyComponents {
		if _, isSkipped := skippedSet[name]; isSkipped {
			continue
		}
		startup[name] = overrides[name]
	}
	return statusOracleCase{
		InstallationID:    installationID,
		SkippedComponents: append([]string(nil), skipped...),
		StartupStatus:     startup,
		FFmpegStatus: map[string]any{
			"analyze_audio_volume": true,
			"available":            false,
			"which":                nil,
		},
	}
}

func statusOracleAllRunning(skipped []string) map[string]bool {
	skippedSet := make(map[string]struct{}, len(skipped))
	for _, name := range skipped {
		skippedSet[name] = struct{}{}
	}
	running := make(map[string]bool)
	for _, name := range legacyComponents {
		if _, isSkipped := skippedSet[name]; !isSkipped {
			running[name] = true
		}
	}
	return running
}

func statusOraclePaths(t *testing.T) (sdkRoot, script string) {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate status oracle test source")
	}
	daemonRoot := filepath.Dir(sourceFile)
	sdkRoot = os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	script = filepath.Join(daemonRoot, "compat", "status_oracle.py")
	for _, required := range []string{
		filepath.Join(sdkRoot, "setup.py"),
		filepath.Join(sdkRoot, "lbry", "__init__.py"),
		filepath.Join(sdkRoot, "lbry", "extras", "daemon", "component.py"),
		filepath.Join(sdkRoot, "lbry", "extras", "daemon", "components.py"),
		filepath.Join(sdkRoot, "lbry", "extras", "daemon", "daemon.py"),
		filepath.Join(sdkRoot, "lbry", "extras", "daemon", "json_response_encoder.py"),
		script,
	} {
		if _, err := os.Stat(required); errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned local Python SDK status source is unavailable: %s", required)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	return sdkRoot, script
}

func normalizeStatusOracleValue(value any) any {
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
