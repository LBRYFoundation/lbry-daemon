package wallet

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

const (
	spvTipOraclePinnedCommit  = "e7666f489418e96b6d2104974e93915b539235c5"
	spvTipOraclePinnedVersion = "0.113.0"
)

var spvTipOraclePinnedSources = map[string]string{
	"lbry/__init__.py":        "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
	"lbry/wallet/database.py": "621ce600e8923f9802755cef73b98081af1deb078fc9324c765ee4d6b726ef5a",
	"lbry/wallet/header.py":   "139376a70a383bb8b265b377b50abc959e370f7d7678614c938ab3ac65824a54",
	"lbry/wallet/ledger.py":   "5b5e3deacd5a87ec69a91b42c7aa6460605146724205ff0b387402dd193be2a5",
	"lbry/wallet/network.py":  "cfe6661af4c2028a542582e2c7fffc8c97dce93ca0f619a752a4a5af389b3e6b",
	"lbry/wallet/stream.py":   "969e237aedd3003d7d4f9d580bea108ae525f9f6deba126c04803f9173d2f461",
	"tests/integration/blockchain/test_blockchain_reorganization.py": "aa321de0af611cc7217dc1908baa28e2b049842bce13d7393c6a501429bef1cb",
	"tests/unit/wallet/test_ledger.py":                               "045a14bc252c0b9b6759d7444e582c5e17c6009689f5ee1fd05e74739711ab88",
}

type spvTipOracleResponse struct {
	Reference struct {
		Commit       string            `json:"commit"`
		Version      string            `json:"version"`
		SourceSHA256 map[string]string `json:"source_sha256"`
	} `json:"reference"`
	Metadata struct {
		PythonVersion          string `json:"python_version"`
		RPCMethod              string `json:"rpc_method"`
		BatchSize              int    `json:"batch_size"`
		RestrictionDistance    int    `json:"restriction_distance"`
		MaxRewind              int    `json:"max_rewind"`
		NotificationMethod     string `json:"notification_method"`
		NotificationSerialized bool   `json:"notification_serialized"`
		DatabaseRewindIsNoop   bool   `json:"database_rewind_is_noop"`
	} `json:"metadata"`
	Cases []spvTipOracleCase `json:"cases"`
}

type spvTipOracleCase struct {
	Name         string                `json:"name"`
	Requests     []spvTipOracleRequest `json:"requests"`
	Connects     []spvTipOracleConnect `json:"connects"`
	Events       [][]int               `json:"events"`
	Rewinds      []int                 `json:"rewinds"`
	CacheClears  int                   `json:"cache_clears"`
	FinalLength  int                   `json:"final_length"`
	ErrorType    *string               `json:"error_type"`
	ErrorMessage *string               `json:"error_message"`
}

type spvTipOracleRequest struct {
	Height int `json:"height"`
	Count  int `json:"count"`
}

type spvTipOracleConnect struct {
	Start int    `json:"start"`
	Hex   string `json:"hex"`
}

type spvTipProbeStore struct {
	length   int
	results  []int
	connects []spvTipOracleConnect
}

func (store *spvTipProbeStore) Len() int    { return store.length }
func (store *spvTipProbeStore) Height() int { return store.length - 1 }

func (store *spvTipProbeStore) ConnectContext(_ context.Context, start int, raw []byte) (int, error) {
	store.connects = append(store.connects, spvTipOracleConnect{Start: start, Hex: hex.EncodeToString(raw)})
	if len(store.results) == 0 {
		return 0, errors.New("unexpected header connect")
	}
	result := store.results[0]
	store.results = store.results[1:]
	if result > 0 {
		store.length = max(store.length, start+result)
	}
	return result, nil
}

type spvTipProbeNetwork struct {
	remoteHeight int
	responses    map[int][]map[string]any
	requests     []spvTipOracleRequest
}

func (*spvTipProbeNetwork) Start(context.Context) error { return nil }
func (*spvTipProbeNetwork) Stop(context.Context) error  { return nil }
func (network *spvTipProbeNetwork) RemoteHeight() int   { return network.remoteHeight }

func (network *spvTipProbeNetwork) RetriableCall(
	_ context.Context, method string, params []any, _ bool,
) (map[string]any, error) {
	if method != SPVHeaderRPCMethod || len(params) != 4 || params[2] != 0 || params[3] != false {
		return nil, fmt.Errorf("unexpected live-header call %s %#v", method, params)
	}
	height, heightOK := params[0].(int)
	count, countOK := params[1].(int)
	if !heightOK || !countOK {
		return nil, fmt.Errorf("unexpected live-header parameter types %#v", params)
	}
	network.requests = append(network.requests, spvTipOracleRequest{Height: height, Count: count})
	responses := network.responses[height]
	if len(responses) == 0 {
		return nil, fmt.Errorf("missing live-header response at %d", height)
	}
	network.responses[height] = responses[1:]
	return responses[0], nil
}

type spvTipGoFixture struct {
	length    int
	results   []int
	responses map[int][]map[string]any
	update    *SPVLiveHeaderUpdate
}

func TestSPVTipSyncMatchesPinnedPythonOracle(t *testing.T) {
	oracle := runSPVTipOracle(t)
	if oracle.Reference.Commit != spvTipOraclePinnedCommit || oracle.Reference.Version != spvTipOraclePinnedVersion ||
		!reflect.DeepEqual(oracle.Reference.SourceSHA256, spvTipOraclePinnedSources) {
		t.Fatalf("SPV tip oracle reference = %+v", oracle.Reference)
	}
	metadata := oracle.Metadata
	if metadata.RPCMethod != SPVHeaderRPCMethod || metadata.BatchSize != SPVLiveHeaderBatchSize ||
		metadata.RestrictionDistance != SPVHeaderRPCRestrictionDistance ||
		metadata.MaxRewind != SPVLiveHeaderMaxRewind ||
		metadata.NotificationMethod != "blockchain.headers.subscribe" ||
		!metadata.NotificationSerialized || !metadata.DatabaseRewindIsNoop {
		t.Fatalf("SPV tip oracle metadata = %+v", metadata)
	}
	if want := os.Getenv("LBRY_ORACLE_PYTHON_VERSION"); want != "" && metadata.PythonVersion != want {
		t.Fatalf("SPV tip oracle Python version = %q, want %q", metadata.PythonVersion, want)
	}

	for _, expected := range oracle.Cases {
		t.Run(expected.Name, func(t *testing.T) {
			fixture := spvTipFixture(expected.Name)
			store := &spvTipProbeStore{
				length: fixture.length, results: append([]int(nil), fixture.results...),
				connects: make([]spvTipOracleConnect, 0),
			}
			network := &spvTipProbeNetwork{
				remoteHeight: 1_000, responses: fixture.responses,
				requests: make([]spvTipOracleRequest, 0),
			}
			events := make([][]int, 0)
			rewinds := make([]int, 0)
			cacheClears := 0
			err := updateSPVLiveHeaders(
				context.Background(), store, network, "lbc_mainnet", fixture.update,
				spvLiveHeaderHooks{
					onAdded: func(height, change int) { events = append(events, []int{height, change}) },
					onRewind: func(_ context.Context, height int) error {
						rewinds = append(rewinds, height)
						return nil
					},
					onRejected: func() { cacheClears++ },
				},
			)
			if !reflect.DeepEqual(network.requests, expected.Requests) ||
				!reflect.DeepEqual(store.connects, expected.Connects) ||
				!reflect.DeepEqual(events, expected.Events) ||
				!reflect.DeepEqual(rewinds, expected.Rewinds) || cacheClears != expected.CacheClears ||
				store.length != expected.FinalLength {
				t.Fatalf(
					"Go tip case differs\nrequests %#v\nconnects %#v\nevents %#v\nrewinds %#v\nclears %d length %d\nPython %+v",
					network.requests, store.connects, events, rewinds, cacheClears, store.length, expected,
				)
			}
			if expected.ErrorMessage == nil {
				if err != nil {
					t.Fatalf("Go tip error = %v", err)
				}
			} else if err == nil || err.Error() != *expected.ErrorMessage {
				t.Fatalf("Go tip error = %v, Python %q", err, *expected.ErrorMessage)
			}
		})
	}
}

func spvTipFixture(name string) spvTipGoFixture {
	switch name {
	case "initial catch-up":
		return spvTipGoFixture{
			length: 2, results: []int{2},
			responses: map[int][]map[string]any{2: {{"hex": "aabb"}}, 4: {{"hex": ""}}},
		}
	case "direct subscription":
		return spvTipGoFixture{length: 4, results: []int{1}, update: &SPVLiveHeaderUpdate{Height: 4, Hex: "cc"}}
	case "future subscription gap":
		return spvTipGoFixture{
			length: 4, results: []int{2}, update: &SPVLiveHeaderUpdate{Height: 6, Hex: "ignored"},
			responses: map[int][]map[string]any{4: {{"hex": "ddee"}}, 6: {{"hex": ""}}},
		}
	case "reorganization":
		return spvTipGoFixture{
			length: 5, results: []int{0, 2}, update: &SPVLiveHeaderUpdate{Height: 5, Hex: "33"},
			responses: map[int][]map[string]any{4: {{"hex": "1122"}}, 6: {{"hex": ""}}},
		}
	case "genesis rewind failure":
		return spvTipGoFixture{length: 0, results: []int{0}, update: &SPVLiveHeaderUpdate{Height: 0, Hex: "44"}}
	case "negative connect failure":
		return spvTipGoFixture{length: 1, results: []int{-1}, update: &SPVLiveHeaderUpdate{Height: 1, Hex: "55"}}
	default:
		panic("unknown SPV tip oracle case " + name)
	}
}

func runSPVTipOracle(t *testing.T) spvTipOracleResponse {
	t.Helper()
	sdkRoot, script := spvTipOraclePaths(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	command := exec.Command(python, script, "--sdk-root", sdkRoot)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Python SPV tip oracle failed: %v\n%s", err, output)
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.UseNumber()
	var oracle spvTipOracleResponse
	if err := decoder.Decode(&oracle); err != nil {
		t.Fatalf("decode SPV tip oracle: %v\n%s", err, output)
	}
	return oracle
}

func spvTipOraclePaths(t *testing.T) (sdkRoot, script string) {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate SPV tip oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot = os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	script = filepath.Join(daemonRoot, "compat", "spv_tip_oracle.py")
	for relative := range spvTipOraclePinnedSources {
		path := filepath.Join(sdkRoot, filepath.FromSlash(relative))
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned local SPV tip source is unavailable: %s", path)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(script); errors.Is(err, os.ErrNotExist) {
		t.Skipf("SPV tip oracle script is unavailable: %s", script)
	} else if err != nil {
		t.Fatal(err)
	}
	return sdkRoot, script
}
