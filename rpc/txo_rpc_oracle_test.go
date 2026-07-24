package rpc

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
	txoRPCOraclePinnedCommit  = "e7666f489418e96b6d2104974e93915b539235c5"
	txoRPCOraclePinnedVersion = "0.113.0"
)

var txoRPCOraclePinnedSources = map[string]string{
	"lbry/__init__.py":             "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
	"lbry/error/__init__.py":       "4a279d245ffc8c9e3a966625f800340d7325183ba900b727d39d4074f196a795",
	"lbry/extras/daemon/daemon.py": "15f9b43c5fc0f21a6361320e82768ef2632746c3ad17c252eab9e8d5e0291be0",
	"lbry/wallet/account.py":       "ea2ca30bddf9c0145469e989d9855dbe7be5184943ae7b8ca690eda41eb7db50",
	"lbry/wallet/constants.py":     "099e5b3a18a70439b9d7039717f0cb61c096c5936126fe6574a4ccda600a780f",
	"lbry/wallet/database.py":      "621ce600e8923f9802755cef73b98081af1deb078fc9324c765ee4d6b726ef5a",
	"lbry/wallet/manager.py":       "042e3410753f5f903871d87e65b2b0cdbaf8d82cd66ae4e677e0bdd47109c7db",
	"lbry/wallet/wallet.py":        "45a26975bd59d11ccad332e16447807b6f67e1d35729d4c4a509b3ab7eaa9010",
}

var txoRPCOraclePinnedMethods = map[string]string{
	"Account.get_txo_count":                 "b8c777541f32104a03c4238b31872f9ff9649d7118b8dea8a074d8c6c59b75a6",
	"Account.get_txos":                      "4a5e44989f39294eaadc4429093d71033ee6315fb7cea86d0cc386978090901f",
	"Daemon._constrain_txo_from_kwargs":     "06967894afd91192eec4a0c8853457894fb3d2e6f1de4a15ffce131e281d9f3b",
	"Daemon.jsonrpc_txo_list":               "907389b3558247e0ea65cdf75d56ef4bc3c13261a154f79b5dfbfaab348bc0b5",
	"Daemon.jsonrpc_txo_sum":                "d9e8f81e9814fe6d51155d04f9ff06b3cf50772910e97424e75d4463e712da5b",
	"Daemon.ledger":                         "c0aad64201976cc6d3b4ae3fa49fe9434093c578706b84f45b8cc687c7276f46",
	"JSONRPCError.__init__":                 "6694af6fe018ba7f86d734992597aaea26b18d70389263a7ff5fd2be1995144b",
	"JSONRPCError.create_command_exception": "fc56255c3a3e15b5279f3d583d6ee67959109f5f0c4766c0d10928bf12cc659e",
	"JSONRPCError.filter_traceback":         "db8da5a9ff8f43e6ce64bdaad60f5e67cc3e071f1992b29d79fa8f2dafa97f86",
	"JSONRPCError.to_dict":                  "4a92e56be4937d195c7307f337b8fcac7a36b306d945b2dbe29108748882a347",
	"Wallet.default_account":                "76e84d5c63726f3c268e161ee2ef54e0573ab02a4aab04d9b7c6dae0fc95961e",
	"Wallet.get_account_or_error":           "e5296b46722e7337b8332c93047cf8c7aef042a35dd762c777d2b0150541305c",
	"WalletManager.default_account":         "6b5ae4ee1fd368d8b3bb05e3a8a3362a0f958f4e5385787958ff83fdb855e731",
	"WalletManager.default_wallet":          "b985d6bbf6126a982f1f0084fc6872592cff7717f50b59fbe3a745f498c8de48",
	"WalletManager.get_wallet_or_default":   "a78f3e4003c8bc2c25c95681532cb166eb3685a611aecd6024893fa6c94e8537",
	"WalletManager.get_wallet_or_error":     "ac6310a5232801623f12f4be0909a0e64a595a94330465f3c825b9ac34c51eec",
	"WalletNotLoadedError.__init__":         "be7802498b2b6c25bef47a189cafe172d0e5b702989cd318957686f1f0ea2d29",
	"constrain_single_or_list":              "42bbf049d51cf87b94a4b95367f8fb2415e9b97422e9886ea03e3079de97abba",
	"paginate_rows":                         "d5af80505ca81eafd134236e1e5fbc8242e9a0ea97d88716be2ec1e846f8becc",
}

type txoRPCOracleResponse struct {
	Reference struct {
		Commit       string            `json:"commit"`
		Version      string            `json:"version"`
		SourceSHA256 map[string]string `json:"source_sha256"`
		MethodSHA256 map[string]string `json:"method_sha256"`
	} `json:"reference"`
	Metadata struct {
		PythonVersion            string   `json:"python_version"`
		ExtractedMethodsExecuted bool     `json:"extracted_methods_executed"`
		ExternalNetworkUsed      bool     `json:"external_network_used"`
		CaseCount                int      `json:"case_count"`
		PublicTXOMethods         []string `json:"public_txo_methods"`
		HasPublicTXOCount        bool     `json:"has_public_txo_count"`
	} `json:"metadata"`
	Cases []txoRPCOracleCase `json:"cases"`
}

type txoRPCOracleCase struct {
	Name         string             `json:"name"`
	Method       string             `json:"method"`
	ManagerState string             `json:"manager_state"`
	LedgerState  string             `json:"ledger_state"`
	OmitParams   bool               `json:"omit_params"`
	Params       json.RawMessage    `json:"params"`
	Result       json.RawMessage    `json:"result"`
	Error        *txoRPCOracleError `json:"error"`
	Calls        []txoRPCOracleCall `json:"calls"`
}

type txoRPCOracleError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Name      string          `json:"name"`
		Command   string          `json:"command"`
		Args      json.RawMessage `json:"args"`
		Kwargs    json.RawMessage `json:"kwargs"`
		Traceback []string        `json:"traceback"`
	} `json:"data"`
}

type txoRPCOracleCall struct {
	Method      string         `json:"method"`
	Ledger      string         `json:"ledger"`
	Constraints map[string]any `json:"constraints"`
}

func TestTXORPCMatchesPinnedPythonOracle(t *testing.T) {
	oracle := runTXORPCOracle(t)
	if oracle.Reference.Commit != txoRPCOraclePinnedCommit ||
		oracle.Reference.Version != txoRPCOraclePinnedVersion ||
		!reflect.DeepEqual(oracle.Reference.SourceSHA256, txoRPCOraclePinnedSources) ||
		!reflect.DeepEqual(oracle.Reference.MethodSHA256, txoRPCOraclePinnedMethods) {
		t.Fatalf("TXO RPC oracle reference = %+v", oracle.Reference)
	}
	if !oracle.Metadata.ExtractedMethodsExecuted || oracle.Metadata.ExternalNetworkUsed ||
		oracle.Metadata.CaseCount != 50 || len(oracle.Cases) != 50 {
		t.Fatalf("TXO RPC oracle metadata = %+v, cases %d", oracle.Metadata, len(oracle.Cases))
	}
	if want := os.Getenv("LBRY_ORACLE_PYTHON_VERSION"); want != "" &&
		oracle.Metadata.PythonVersion != want {
		t.Fatalf("TXO RPC Python version = %q, want %q", oracle.Metadata.PythonVersion, want)
	}
	if oracle.Metadata.HasPublicTXOCount || !reflect.DeepEqual(
		oracle.Metadata.PublicTXOMethods,
		[]string{"txo_list", "txo_plot", "txo_spend", "txo_sum"},
	) {
		t.Fatalf("pinned public TXO methods = %v, has count %t",
			oracle.Metadata.PublicTXOMethods, oracle.Metadata.HasPublicTXOCount)
	}
	assertTXORPCOracleContract(t, oracle.Cases)
}

func assertTXORPCOracleContract(t *testing.T, cases []txoRPCOracleCase) {
	t.Helper()
	wantNames := []string{
		"list defaults", "list explicit nulls", "list middle page",
		"list zero pagination defaults", "list negative pagination clamps",
		"list string page error", "list no totals", "list truthy no totals",
		"list resolve and received tips", "list selected account", "list selected wallet",
		"list selected wallet account", "list selected empty wallet",
		"list falsy account is wallet wide", "list legacy positional", "list order name",
		"list order height", "list order amount", "list order none", "list invalid order",
		"list scalar filters and precedence", "list negative filters",
		"list ownership union precedence", "list ownership booleans require true singleton",
		"list one element filters collapse", "list multiple filters use in",
		"list empty filters ignored", "list invalid type", "list unknown filter",
		"list missing wallet", "list missing account", "list foreign account",
		"list wallet error precedes order", "list account error precedes type",
		"list no wallets", "list empty default wallet", "list records error stops count",
		"list count error follows records", "sum defaults", "sum selected account",
		"sum selected wallet", "sum selected wallet account", "sum legacy positional",
		"sum filters", "sum invalid type", "sum unknown filter", "sum missing wallet",
		"sum missing account", "sum foreign account", "sum no wallets",
	}
	if len(cases) != len(wantNames) {
		t.Fatalf("TXO RPC case count = %d, want %d", len(cases), len(wantNames))
	}
	for index, fixture := range cases {
		if fixture.Name != wantNames[index] {
			t.Fatalf("TXO RPC case %d = %q, want %q", index, fixture.Name, wantNames[index])
		}
		for _, call := range fixture.Calls {
			if _, exists := call.Constraints["no_tx"]; exists {
				t.Fatalf("%q unexpectedly injects no_tx: %+v", fixture.Name, call)
			}
			if _, exists := call.Constraints["no_channel_info"]; exists {
				t.Fatalf("%q unexpectedly injects no_channel_info: %+v", fixture.Name, call)
			}
		}
		assertTXORPCCallSequence(t, fixture)
	}

	byName := make(map[string]txoRPCOracleCase, len(cases))
	for _, fixture := range cases {
		byName[fixture.Name] = fixture
	}
	assertTXORPCPagination(t, byName)
	assertTXORPCSelection(t, byName)
	assertTXORPCConstraints(t, byName)
	assertTXORPCErrors(t, byName)
}

func assertTXORPCCallSequence(t *testing.T, fixture txoRPCOracleCase) {
	t.Helper()
	if fixture.Error != nil {
		if fixture.Error.Code != -32500 || fixture.Error.Data.Command != fixture.Method ||
			fixture.Error.Data.Name == "" || len(fixture.Error.Data.Traceback) == 0 {
			t.Fatalf("%q error envelope = %+v", fixture.Name, fixture.Error)
		}
		wantCalls := 0
		switch fixture.Name {
		case "list records error stops count":
			wantCalls = 1
		case "list count error follows records":
			wantCalls = 2
		}
		if len(fixture.Calls) != wantCalls {
			t.Fatalf("%q calls = %+v, want %d", fixture.Name, fixture.Calls, wantCalls)
		}
		return
	}
	if len(fixture.Result) == 0 {
		t.Fatalf("%q has neither result nor error", fixture.Name)
	}
	if fixture.Method == "txo_sum" {
		if len(fixture.Calls) != 1 || fixture.Calls[0].Method != "get_txo_sum" {
			t.Fatalf("%q sum calls = %+v", fixture.Name, fixture.Calls)
		}
		return
	}
	wantCalls := 2
	if fixture.Name == "list no totals" || fixture.Name == "list truthy no totals" ||
		fixture.Name == "list legacy positional" {
		wantCalls = 1
	}
	if len(fixture.Calls) != wantCalls || fixture.Calls[0].Method != "get_txos" {
		t.Fatalf("%q list calls = %+v, want %d", fixture.Name, fixture.Calls, wantCalls)
	}
	if wantCalls == 2 {
		if fixture.Calls[1].Method != "get_txo_count" ||
			!reflect.DeepEqual(fixture.Calls[0].Constraints, fixture.Calls[1].Constraints) ||
			fixture.Calls[0].Ledger != fixture.Calls[1].Ledger {
			t.Fatalf("%q records/count call mismatch = %+v", fixture.Name, fixture.Calls)
		}
	}
}

func assertTXORPCPagination(t *testing.T, cases map[string]txoRPCOracleCase) {
	t.Helper()
	want := map[string]map[string]any{
		"list defaults": {
			"items": []any{"row-0", "row-1", "row-2", "row-3", "row-4"},
			"page":  float64(1), "page_size": float64(20),
			"total_pages": float64(1), "total_items": float64(5),
		},
		"list middle page": {
			"items": []any{"row-2", "row-3"}, "page": float64(2), "page_size": float64(2),
			"total_pages": float64(3), "total_items": float64(5),
		},
		"list negative pagination clamps": {
			"items": []any{"row-0"}, "page": float64(1), "page_size": float64(1),
			"total_pages": float64(5), "total_items": float64(5),
		},
		"list no totals": {
			"items": []any{"row-2", "row-3"}, "page": float64(2), "page_size": float64(2),
		},
		"list selected empty wallet": {
			"items": []any{}, "page": float64(7), "page_size": float64(3),
			"total_pages": float64(0), "total_items": float64(0),
		},
	}
	for name, expected := range want {
		if got := txoRPCDecodeObject(t, cases[name].Result); !reflect.DeepEqual(got, expected) {
			t.Fatalf("%q result = %#v, want %#v", name, got, expected)
		}
	}
}

func assertTXORPCSelection(t *testing.T, cases map[string]txoRPCOracleCase) {
	t.Helper()
	assertCall := func(name, method, ledger, wallet string, accounts []any, readOnly *bool) {
		t.Helper()
		call := cases[name].Calls[0]
		if call.Method != method || call.Ledger != ledger || call.Constraints["wallet"] != wallet ||
			!reflect.DeepEqual(call.Constraints["accounts"], accounts) {
			t.Fatalf("%q selection = %+v", name, call)
		}
		value, exists := call.Constraints["read_only"]
		if readOnly == nil {
			if exists {
				t.Fatalf("%q unexpectedly supplies read_only: %+v", name, call)
			}
		} else if !exists || value != *readOnly {
			t.Fatalf("%q read_only = %#v, exists %t", name, value, exists)
		}
	}
	yes := true
	assertCall("list defaults", "get_txos", "ledger-a", "wallet-a", []any{"a1", "a2"}, &yes)
	assertCall("list selected account", "get_txos", "ledger-a", "wallet-a", []any{"a2"}, nil)
	assertCall("list selected wallet", "get_txos", "ledger-a", "wallet-b", []any{"b1", "b2"}, &yes)
	assertCall("list selected wallet account", "get_txos", "ledger-b", "wallet-b", []any{"b2"}, nil)
	assertCall("sum selected wallet account", "get_txo_sum", "ledger-a", "wallet-b", []any{"b2"}, &yes)

	for name, amount := range map[string]float64{
		"sum defaults": 30, "sum selected account": 20, "sum selected wallet": 70,
		"sum selected wallet account": 40, "sum legacy positional": 40,
	} {
		if got := txoRPCDecode(t, cases[name].Result); got != amount {
			t.Fatalf("%q result = %#v, want %v", name, got, amount)
		}
	}
}

func assertTXORPCConstraints(t *testing.T, cases map[string]txoRPCOracleCase) {
	t.Helper()
	defaults := cases["list defaults"].Calls[0].Constraints
	for key, value := range map[string]any{
		"resolve": false, "include_is_spent": true, "include_is_my_input": true,
		"include_is_my_output": true, "include_received_tips": false,
		"exclude_internal_transfers": false, "offset": float64(0), "limit": float64(20),
	} {
		if defaults[key] != value {
			t.Fatalf("default list constraint %s = %#v, want %#v", key, defaults[key], value)
		}
	}
	tips := cases["list resolve and received tips"].Calls[0].Constraints
	if tips["resolve"] != true || tips["include_received_tips"] != true {
		t.Fatalf("resolve/tips constraints = %#v", tips)
	}
	for name, order := range map[string]string{
		"list order name": "txo.claim_name", "list order height": "height",
		"list order amount": "amount", "list order none": "none",
	} {
		if got := cases[name].Calls[0].Constraints["order_by"]; got != order {
			t.Fatalf("%q order = %#v, want %q", name, got, order)
		}
	}

	scalar := cases["list scalar filters and precedence"].Calls[0].Constraints
	for key, value := range map[string]any{
		"txo_type": float64(1), "txid": "tx", "claim_id": "claim", "claim_name": "name",
		"channel_id": "channel", "reposted_claim_id": "repost", "is_spent": true,
		"has_source": true, "exclude_internal_transfers": true,
	} {
		if scalar[key] != value {
			t.Fatalf("scalar constraint %s = %#v, want %#v", key, scalar[key], value)
		}
	}
	negative := cases["list negative filters"].Calls[0].Constraints
	for _, key := range []string{"is_spent", "has_source", "is_my_input", "is_my_output"} {
		if value, exists := negative[key]; !exists || value != false {
			t.Fatalf("negative constraint %s = %#v, exists %t", key, value, exists)
		}
	}
	union := cases["list ownership union precedence"].Calls[0].Constraints
	if union["is_my_input_or_output"] != true {
		t.Fatalf("ownership union = %#v", union)
	}
	for _, key := range []string{"is_my_input", "is_my_output"} {
		if _, exists := union[key]; exists {
			t.Fatalf("ownership union retained %s: %#v", key, union)
		}
	}
	exact := cases["list ownership booleans require true singleton"].Calls[0].Constraints
	for _, key := range []string{"is_my_input_or_output", "is_my_input", "is_my_output"} {
		if _, exists := exact[key]; exists {
			t.Fatalf("numeric ownership flag retained %s: %#v", key, exact)
		}
	}

	one := cases["list one element filters collapse"].Calls[0].Constraints
	if one["txo_type"] != float64(2) || one["txid"] != "tx" || one["channel_id"] != "channel" {
		t.Fatalf("one-element constraints = %#v", one)
	}
	multiple := cases["list multiple filters use in"].Calls[0].Constraints
	if !reflect.DeepEqual(multiple["txo_type__in"], []any{float64(1), float64(3)}) ||
		!reflect.DeepEqual(multiple["txid__in"], []any{"tx1", "tx2"}) ||
		!reflect.DeepEqual(multiple["channel_id__in"], []any{"channel1", "channel2"}) {
		t.Fatalf("multiple constraints = %#v", multiple)
	}
	empty := cases["list empty filters ignored"].Calls[0].Constraints
	for _, key := range []string{"txo_type", "txo_type__in", "txid", "claim_id", "channel_id"} {
		if _, exists := empty[key]; exists {
			t.Fatalf("empty filter retained %s: %#v", key, empty)
		}
	}

	sum := cases["sum defaults"].Calls[0].Constraints
	if len(sum) != 4 || sum["exclude_internal_transfers"] != false || sum["read_only"] != true {
		t.Fatalf("default sum constraints = %#v", sum)
	}
	for _, key := range []string{
		"resolve", "include_is_spent", "include_is_my_input", "include_is_my_output",
		"include_received_tips", "offset", "limit", "order_by",
	} {
		if _, exists := sum[key]; exists {
			t.Fatalf("default sum retained list-only constraint %s: %#v", key, sum)
		}
	}
}

func assertTXORPCErrors(t *testing.T, cases map[string]txoRPCOracleCase) {
	t.Helper()
	want := map[string][2]string{
		"list invalid order":   {"ValueError", "'txid' is not a valid --order_by value."},
		"list invalid type":    {"KeyError", "'video'"},
		"list missing wallet":  {"WalletNotLoadedError", "Wallet missing is not loaded."},
		"list missing account": {"ValueError", "Couldn't find account: missing."},
		"list foreign account": {"ValueError", "Couldn't find account: a2."},
		"sum invalid type":     {"KeyError", "'video'"},
		"sum missing wallet":   {"WalletNotLoadedError", "Wallet missing is not loaded."},
		"sum missing account":  {"ValueError", "Couldn't find account: missing."},
	}
	for name, expected := range want {
		actual := cases[name].Error
		if actual == nil || actual.Data.Name != expected[0] || actual.Message != expected[1] {
			t.Fatalf("%q error = %+v, want %q %q", name, actual, expected[0], expected[1])
		}
	}
	if cases["list wallet error precedes order"].Error.Data.Name != "WalletNotLoadedError" ||
		cases["list account error precedes type"].Error.Data.Name != "ValueError" {
		t.Fatalf("selection error precedence = %+v / %+v",
			cases["list wallet error precedes order"].Error,
			cases["list account error precedes type"].Error)
	}
}

func txoRPCDecode(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode TXO RPC oracle JSON %s: %v", raw, err)
	}
	return value
}

func txoRPCDecodeObject(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	value, ok := txoRPCDecode(t, raw).(map[string]any)
	if !ok {
		t.Fatalf("TXO RPC result %s is not an object", raw)
	}
	return value
}

func runTXORPCOracle(t *testing.T) txoRPCOracleResponse {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate TXO RPC oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot := os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	for relative := range txoRPCOraclePinnedSources {
		path := filepath.Join(sdkRoot, filepath.FromSlash(relative))
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned local TXO RPC source is unavailable: %s", path)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(daemonRoot, "compat", "txo_rpc_oracle.py")
	if _, err := os.Stat(script); errors.Is(err, os.ErrNotExist) {
		t.Skipf("TXO RPC oracle script is unavailable: %s", script)
	} else if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("python3", script, "--sdk-root", sdkRoot)
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1", "TZ=UTC")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("TXO RPC oracle failed: %v\n%s", err, output)
	}
	var oracle txoRPCOracleResponse
	if err := json.Unmarshal(output, &oracle); err != nil {
		t.Fatalf("decode TXO RPC oracle: %v\n%s", err, output)
	}
	return oracle
}
