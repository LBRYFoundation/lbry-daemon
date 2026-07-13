package rpc

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	databasepkg "lbry/daemon/database"
)

func TestFileListFilteringMatchesPinnedPythonOracle(t *testing.T) {
	oracle := runFileListOracle(t)
	rows := []databasepkg.ManagedFileRow{
		{RowID: 2, StreamHash: "b", Status: "stopped", SuggestedFileName: "beta", AddedOn: 20,
			ClaimID: "claim-b", ClaimOutpoint: "tx-b:1"},
		{RowID: 1, StreamHash: "a", Status: "running", SuggestedFileName: "alpha", AddedOn: 10,
			ClaimID: "claim-a", ClaimOutpoint: "tx-a:0", ChannelClaimID: fileListOracleString("channel-a")},
	}
	cases := map[string]struct {
		sort, comparison string
		reverse          bool
		filters          map[string]any
	}{
		"default":            {sort: "rowid", comparison: "eq"},
		"reverse":            {sort: "rowid", comparison: "eq", reverse: true},
		"status":             {sort: "rowid", comparison: "eq", filters: map[string]any{"status": "stopped"}},
		"greater":            {sort: "rowid", comparison: "g", filters: map[string]any{"added_on": json.Number("10")}},
		"claim_set":          {sort: "rowid", comparison: "ne", filters: map[string]any{"claim_id": []any{"claim-a"}}},
		"full_status":        {sort: "rowid", comparison: "eq", filters: map[string]any{"full_status": true}},
		"invalid_sort":       {sort: "wrong", comparison: "eq"},
		"invalid_comparison": {sort: "rowid", comparison: "wat"},
		"invalid_search":     {sort: "rowid", comparison: "eq", filters: map[string]any{"wrong": json.Number("1")}},
	}
	oracleCases := oracle["cases"].(map[string]any)
	for name, fixture := range cases {
		normalized := normalizedRPCParams{named: fixture.filters}
		if normalized.named == nil {
			normalized.named = map[string]any{}
		}
		got, err := filterManagedFiles(rows, fixture.sort, fixture.comparison, normalized)
		if fixture.reverse {
			for left, right := 0, len(got)-1; left < right; left, right = left+1, right-1 {
				got[left], got[right] = got[right], got[left]
			}
		}
		want := oracleCases[name].(map[string]any)
		if message, failed := want["message"].(string); failed {
			if err == nil || err.Error() != message {
				t.Errorf("%s error = %v, want %q", name, err, message)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s error = %v", name, err)
			continue
		}
		ids := make([]any, len(got))
		for index := range got {
			ids[index] = got[index].StreamHash
		}
		if !reflect.DeepEqual(ids, want["items"]) {
			t.Errorf("%s items = %v, want %v", name, ids, want["items"])
		}
	}
}

func fileListOracleString(value string) *string { return &value }

func runFileListOracle(t *testing.T) map[string]any {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate file list oracle")
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
	command := exec.Command(python, filepath.Join(daemonRoot, "compat", "file_list_oracle.py"), "--sdk-root", sdkRoot)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("Python file list oracle failed: %v\n%s", err, stderr.String())
	}
	var response map[string]any
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("decode file list oracle: %v\n%s", err, output)
	}
	return response
}
