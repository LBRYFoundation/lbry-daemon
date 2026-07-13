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
	listWrapperOraclePinnedCommit  = "e7666f489418e96b6d2104974e93915b539235c5"
	listWrapperOraclePinnedVersion = "0.113.0"
)

var listWrapperOraclePinnedSources = map[string]string{
	"lbry/__init__.py":             "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
	"lbry/error/__init__.py":       "4a279d245ffc8c9e3a966625f800340d7325183ba900b727d39d4074f196a795",
	"lbry/extras/daemon/daemon.py": "15f9b43c5fc0f21a6361320e82768ef2632746c3ad17c252eab9e8d5e0291be0",
	"lbry/wallet/account.py":       "ea2ca30bddf9c0145469e989d9855dbe7be5184943ae7b8ca690eda41eb7db50",
	"lbry/wallet/constants.py":     "099e5b3a18a70439b9d7039717f0cb61c096c5936126fe6574a4ccda600a780f",
	"lbry/wallet/manager.py":       "042e3410753f5f903871d87e65b2b0cdbaf8d82cd66ae4e677e0bdd47109c7db",
	"lbry/wallet/wallet.py":        "45a26975bd59d11ccad332e16447807b6f67e1d35729d4c4a509b3ab7eaa9010",
}

var listWrapperOraclePinnedMethods = map[string]string{
	"Account.get_collection_count":          "c97e9844ce97192ac53fcd57225e49a13528eb2932842fcb0104728c2c86655b",
	"Account.get_collections":               "ea06f6578fb9292136c1b448c823430a8eb4a18a196faf3ad09c039760f74302",
	"Daemon.jsonrpc_channel_list":           "f3a69dfbbd0876d2350dbfe62d56139d0b8a57d65483c57c894ddbce3658f857",
	"Daemon.jsonrpc_claim_list":             "51c3ea59814eebd26eeccab67ac1fa9906260c593971de00247dbd8d231f074c",
	"Daemon.jsonrpc_collection_list":        "e8448728400a8f4410bdc203d793578fa7880f82b4917e0e8669a651f18bbaed",
	"Daemon.jsonrpc_purchase_list":          "3c1bebbee525dce586ce9461f089c4c02dcc44b9afc509dcada787d71726c670",
	"Daemon.jsonrpc_stream_list":            "88b0a70621d9c0697d9098d512e4ac90fa09e042c6481aa2a829811dfd3537a0",
	"Daemon.jsonrpc_support_list":           "2de0a07d0aa7fb099a58ecdfc6492a7e2b6edf47dd024253cbef00bfdb2563fb",
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
	"paginate_rows":                         "d5af80505ca81eafd134236e1e5fbc8242e9a0ea97d88716be2ec1e846f8becc",
}

type listWrapperOracleResponse struct {
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
		PublicListMethods        []string `json:"public_list_methods"`
	} `json:"metadata"`
	Cases []listWrapperOracleCase `json:"cases"`
}

type listWrapperOracleCase struct {
	Name         string                  `json:"name"`
	Method       string                  `json:"method"`
	ManagerState string                  `json:"manager_state"`
	LedgerState  string                  `json:"ledger_state"`
	OmitParams   bool                    `json:"omit_params"`
	Params       json.RawMessage         `json:"params"`
	Result       json.RawMessage         `json:"result"`
	Error        *listWrapperOracleError `json:"error"`
	Calls        []listWrapperOracleCall `json:"calls"`
}

type listWrapperOracleError struct {
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

type listWrapperOracleCall struct {
	Method      string         `json:"method"`
	Ledger      string         `json:"ledger"`
	Params      map[string]any `json:"params"`
	Constraints map[string]any `json:"constraints"`
}

func TestListWrappersMatchPinnedPythonOracle(t *testing.T) {
	oracle := runListWrapperOracle(t)
	if oracle.Reference.Commit != listWrapperOraclePinnedCommit ||
		oracle.Reference.Version != listWrapperOraclePinnedVersion ||
		!reflect.DeepEqual(oracle.Reference.SourceSHA256, listWrapperOraclePinnedSources) ||
		!reflect.DeepEqual(oracle.Reference.MethodSHA256, listWrapperOraclePinnedMethods) {
		t.Fatalf("list wrapper oracle reference = %+v", oracle.Reference)
	}
	if !oracle.Metadata.ExtractedMethodsExecuted || oracle.Metadata.ExternalNetworkUsed ||
		oracle.Metadata.CaseCount != 53 || len(oracle.Cases) != 53 {
		t.Fatalf("list wrapper oracle metadata = %+v, cases %d", oracle.Metadata, len(oracle.Cases))
	}
	if want := os.Getenv("LBRY_ORACLE_PYTHON_VERSION"); want != "" &&
		oracle.Metadata.PythonVersion != want {
		t.Fatalf("list wrapper Python version = %q, want %q", oracle.Metadata.PythonVersion, want)
	}
	if !reflect.DeepEqual(oracle.Metadata.PublicListMethods, []string{
		"channel_list", "claim_list", "collection_list", "purchase_list",
		"stream_list", "support_list",
	}) {
		t.Fatalf("pinned public list methods = %v", oracle.Metadata.PublicListMethods)
	}
	assertListWrapperOracleContract(t, oracle.Cases)
}

func assertListWrapperOracleContract(t *testing.T, cases []listWrapperOracleCase) {
	t.Helper()
	wantNames := []string{
		"claim defaults", "claim scalar type", "claim type list", "claim empty type defaults",
		"claim spent false", "claim spent true", "claim overwrites unspent",
		"claim forwards txo controls", "channel defaults", "channel positional account",
		"channel spent false", "channel spent true", "channel retains explicit unspent",
		"channel overwrites unspent", "stream defaults", "stream positional account",
		"stream spent false", "stream spent true", "stream overwrites unspent without spent key",
		"support defaults", "support received", "support sent", "support sent removes spent filters",
		"support staked", "support flag precedence", "support false flags",
		"support truthy numeric received", "support positional account", "purchase defaults",
		"purchase claim resolve page", "purchase selected wallet", "purchase selected account",
		"purchase falsey ids", "purchase negative pagination", "purchase missing wallet",
		"purchase missing account", "purchase foreign account", "purchase no wallets",
		"purchase empty default", "purchase records error", "purchase count error",
		"collection defaults", "collection resolve page", "collection selected account",
		"collection falsey account", "collection negative pagination", "collection missing wallet",
		"collection missing account", "collection foreign account", "collection no wallets",
		"collection empty selected", "collection records error", "collection count error",
	}
	if len(cases) != len(wantNames) {
		t.Fatalf("list wrapper case count = %d, want %d", len(cases), len(wantNames))
	}
	byName := make(map[string]listWrapperOracleCase, len(cases))
	for index, fixture := range cases {
		if fixture.Name != wantNames[index] {
			t.Fatalf("list wrapper case %d = %q, want %q", index, fixture.Name, wantNames[index])
		}
		byName[fixture.Name] = fixture
	}
	assertClaimListDelegation(t, byName)
	assertTypedListDelegation(t, byName)
	assertSupportListDelegation(t, byName)
	assertDirectListCalls(t, byName)
	assertDirectListErrors(t, byName)
}

func assertClaimListDelegation(t *testing.T, cases map[string]listWrapperOracleCase) {
	t.Helper()
	defaults := listWrapperDelegatedParams(t, cases["claim defaults"])
	claimTypes := []any{"stream", "channel", "collection", "repost"}
	if !reflect.DeepEqual(defaults["type"], claimTypes) || defaults["is_not_spent"] != true {
		t.Fatalf("claim defaults = %#v", defaults)
	}
	if got := listWrapperDelegatedParams(t, cases["claim scalar type"])["type"]; got != "stream" {
		t.Fatalf("claim scalar type = %#v", got)
	}
	if got := listWrapperDelegatedParams(t, cases["claim type list"])["type"]; !reflect.DeepEqual(got, []any{"channel", "repost"}) {
		t.Fatalf("claim type list = %#v", got)
	}
	if got := listWrapperDelegatedParams(t, cases["claim empty type defaults"])["type"]; !reflect.DeepEqual(got, claimTypes) {
		t.Fatalf("claim empty type = %#v", got)
	}
	spentFalse := listWrapperDelegatedParams(t, cases["claim spent false"])
	if spentFalse["is_spent"] != false || spentFalse["is_not_spent"] != true {
		t.Fatalf("claim spent false = %#v", spentFalse)
	}
	spentTrue := listWrapperDelegatedParams(t, cases["claim spent true"])
	if spentTrue["is_spent"] != true || listWrapperHas(spentTrue, "is_not_spent") {
		t.Fatalf("claim spent true = %#v", spentTrue)
	}
	if listWrapperDelegatedParams(t, cases["claim overwrites unspent"])["is_not_spent"] != true {
		t.Fatalf("claim did not overwrite explicit unspent=false")
	}
	controls := listWrapperDelegatedParams(t, cases["claim forwards txo controls"])
	for key, want := range map[string]any{
		"account_id": "a2", "wallet_id": "wallet-a", "page": float64(3),
		"page_size": float64(4), "resolve": true, "order_by": "height",
		"no_totals": true, "include_received_tips": true,
	} {
		if controls[key] != want {
			t.Fatalf("claim control %s = %#v, want %#v", key, controls[key], want)
		}
	}
}

func assertTypedListDelegation(t *testing.T, cases map[string]listWrapperOracleCase) {
	t.Helper()
	for _, name := range []string{"channel defaults", "channel positional account"} {
		params := listWrapperDelegatedParams(t, cases[name])
		if params["type"] != "channel" || params["is_not_spent"] != true {
			t.Fatalf("%q params = %#v", name, params)
		}
	}
	if listWrapperDelegatedParams(t, cases["channel positional account"])["account_id"] != "a2" {
		t.Fatalf("channel positional account was not forwarded")
	}
	channelFalse := listWrapperDelegatedParams(t, cases["channel spent false"])
	if channelFalse["is_spent"] != false || channelFalse["is_not_spent"] != true {
		t.Fatalf("channel spent false = %#v", channelFalse)
	}
	channelTrue := listWrapperDelegatedParams(t, cases["channel spent true"])
	if channelTrue["is_spent"] != true || listWrapperHas(channelTrue, "is_not_spent") {
		t.Fatalf("channel spent true = %#v", channelTrue)
	}
	if listWrapperDelegatedParams(t, cases["channel retains explicit unspent"])["is_not_spent"] != true ||
		listWrapperDelegatedParams(t, cases["channel overwrites unspent"])["is_not_spent"] != true {
		t.Fatalf("channel explicit unspent handling changed")
	}

	for _, name := range []string{"stream defaults", "stream positional account"} {
		params := listWrapperDelegatedParams(t, cases[name])
		if params["type"] != "stream" || params["is_not_spent"] != true {
			t.Fatalf("%q params = %#v", name, params)
		}
	}
	streamFalse := listWrapperDelegatedParams(t, cases["stream spent false"])
	if streamFalse["is_spent"] != false || listWrapperHas(streamFalse, "is_not_spent") {
		t.Fatalf("stream spent false = %#v", streamFalse)
	}
	streamTrue := listWrapperDelegatedParams(t, cases["stream spent true"])
	if streamTrue["is_spent"] != true || listWrapperHas(streamTrue, "is_not_spent") {
		t.Fatalf("stream spent true = %#v", streamTrue)
	}
	if listWrapperDelegatedParams(t, cases["stream overwrites unspent without spent key"])["is_not_spent"] != true {
		t.Fatalf("stream missing-spent default did not overwrite explicit unspent=false")
	}
}

func assertSupportListDelegation(t *testing.T, cases map[string]listWrapperOracleCase) {
	t.Helper()
	defaults := listWrapperDelegatedParams(t, cases["support defaults"])
	if defaults["type"] != "support" || defaults["is_not_spent"] != true {
		t.Fatalf("support defaults = %#v", defaults)
	}
	received := listWrapperDelegatedParams(t, cases["support received"])
	if received["is_not_my_input"] != true || received["is_my_output"] != true ||
		received["is_not_spent"] != true {
		t.Fatalf("support received = %#v", received)
	}
	for _, name := range []string{"support sent", "support sent removes spent filters"} {
		params := listWrapperDelegatedParams(t, cases[name])
		if params["is_my_input"] != true || params["is_not_my_output"] != true ||
			listWrapperHas(params, "is_spent") || listWrapperHas(params, "is_not_spent") {
			t.Fatalf("%q params = %#v", name, params)
		}
	}
	staked := listWrapperDelegatedParams(t, cases["support staked"])
	if staked["is_my_input"] != true || staked["is_my_output"] != true ||
		staked["is_not_spent"] != true {
		t.Fatalf("support staked = %#v", staked)
	}
	precedence := listWrapperDelegatedParams(t, cases["support flag precedence"])
	if precedence["is_not_my_input"] != true || precedence["is_my_output"] != true ||
		listWrapperHas(precedence, "is_my_input") || listWrapperHas(precedence, "is_not_my_output") {
		t.Fatalf("support flag precedence = %#v", precedence)
	}
	if listWrapperDelegatedParams(t, cases["support false flags"])["is_not_spent"] != true ||
		listWrapperDelegatedParams(t, cases["support truthy numeric received"])["is_not_my_input"] != true ||
		listWrapperDelegatedParams(t, cases["support positional account"])["account_id"] != "a2" {
		t.Fatalf("support truthiness or positional forwarding changed")
	}
}

func assertDirectListCalls(t *testing.T, cases map[string]listWrapperOracleCase) {
	t.Helper()
	errorCases := map[string]int{
		"purchase missing wallet": 0, "purchase missing account": 0,
		"purchase foreign account": 0, "purchase no wallets": 0,
		"purchase empty default": 0, "purchase records error": 1,
		"purchase count error": 2, "collection missing wallet": 0,
		"collection missing account": 0, "collection foreign account": 0,
		"collection no wallets": 0, "collection records error": 1,
		"collection count error": 2,
	}
	for name, fixture := range cases {
		if fixture.Method != "purchase_list" && fixture.Method != "collection_list" {
			continue
		}
		if wantCalls, isError := errorCases[name]; isError {
			if len(fixture.Calls) != wantCalls {
				t.Fatalf("%q calls = %+v, want %d", name, fixture.Calls, wantCalls)
			}
			continue
		}
		if fixture.Error != nil || len(fixture.Calls) != 2 {
			t.Fatalf("%q direct result/error/calls = %s / %+v / %+v", name, fixture.Result, fixture.Error, fixture.Calls)
		}
		wantRecords, wantCount := "get_purchases", "get_purchase_count"
		if fixture.Method == "collection_list" {
			wantRecords, wantCount = "get_collections", "get_collection_count"
		}
		if fixture.Calls[0].Method != wantRecords || fixture.Calls[1].Method != wantCount ||
			fixture.Calls[0].Ledger != fixture.Calls[1].Ledger ||
			!reflect.DeepEqual(fixture.Calls[0].Constraints, fixture.Calls[1].Constraints) {
			t.Fatalf("%q call sequence = %+v", name, fixture.Calls)
		}
	}

	purchaseWallet := cases["purchase selected wallet"].Calls[0]
	if purchaseWallet.Ledger != "ledger-a" || purchaseWallet.Constraints["wallet"] != "wallet-b" ||
		!reflect.DeepEqual(purchaseWallet.Constraints["accounts"], []any{"b1", "b2"}) {
		t.Fatalf("purchase selected wallet = %+v", purchaseWallet)
	}
	purchaseAccount := cases["purchase selected account"].Calls[0]
	if purchaseAccount.Ledger != "ledger-a" ||
		!reflect.DeepEqual(purchaseAccount.Constraints["accounts"], []any{"b2"}) {
		t.Fatalf("purchase selected account = %+v", purchaseAccount)
	}
	collectionWallet := cases["collection resolve page"].Calls[0]
	if collectionWallet.Ledger != "ledger-a" || collectionWallet.Constraints["wallet"] != "wallet-b" ||
		!reflect.DeepEqual(collectionWallet.Constraints["accounts"], []any{"b1", "b2"}) {
		t.Fatalf("collection selected wallet = %+v", collectionWallet)
	}
	collectionAccount := cases["collection selected account"].Calls[0]
	if collectionAccount.Ledger != "ledger-b" ||
		!reflect.DeepEqual(collectionAccount.Constraints["accounts"], []any{"b2"}) {
		t.Fatalf("collection selected account = %+v", collectionAccount)
	}

	resolvedPurchase := cases["purchase claim resolve page"].Calls[0].Constraints
	if resolvedPurchase["resolve"] != true || resolvedPurchase["purchased_claim_id"] != "claim" ||
		resolvedPurchase["offset"] != float64(2) || resolvedPurchase["limit"] != float64(2) {
		t.Fatalf("resolved purchase constraints = %#v", resolvedPurchase)
	}
	if _, exists := cases["purchase falsey ids"].Calls[0].Constraints["purchased_claim_id"]; exists {
		t.Fatalf("falsey purchase claim id became a constraint")
	}
	resolvedCollection := cases["collection resolve page"].Calls[0].Constraints
	if resolvedCollection["resolve"] != true || resolvedCollection["resolve_claims"] != float64(7) ||
		resolvedCollection["offset"] != float64(2) || resolvedCollection["limit"] != float64(2) {
		t.Fatalf("resolved collection constraints = %#v", resolvedCollection)
	}
	defaults := cases["collection defaults"].Calls[0].Constraints
	if defaults["resolve"] != false || defaults["resolve_claims"] != float64(0) {
		t.Fatalf("collection default resolve constraints = %#v", defaults)
	}

	for _, name := range []string{"purchase defaults", "collection defaults"} {
		result := listWrapperDecodeObject(t, cases[name].Result)
		if result["page"] != float64(1) || result["page_size"] != float64(20) ||
			result["total_items"] != float64(5) || result["total_pages"] != float64(1) {
			t.Fatalf("%q pagination = %#v", name, result)
		}
	}
	for _, name := range []string{"purchase negative pagination", "collection negative pagination"} {
		result := listWrapperDecodeObject(t, cases[name].Result)
		if result["page"] != float64(1) || result["page_size"] != float64(1) ||
			result["total_pages"] != float64(5) {
			t.Fatalf("%q negative pagination = %#v", name, result)
		}
	}
	empty := listWrapperDecodeObject(t, cases["collection empty selected"].Result)
	if !reflect.DeepEqual(empty, map[string]any{
		"items": []any{}, "page": float64(7), "page_size": float64(3),
		"total_items": float64(0), "total_pages": float64(0),
	}) {
		t.Fatalf("empty selected collection = %#v", empty)
	}
}

func assertDirectListErrors(t *testing.T, cases map[string]listWrapperOracleCase) {
	t.Helper()
	want := map[string][2]string{
		"purchase missing wallet":    {"WalletNotLoadedError", "Wallet missing is not loaded."},
		"purchase missing account":   {"ValueError", "Couldn't find account: missing."},
		"purchase foreign account":   {"ValueError", "Couldn't find account: a2."},
		"purchase records error":     {"RuntimeError", "fixture records failure"},
		"purchase count error":       {"RuntimeError", "fixture count failure"},
		"collection missing wallet":  {"WalletNotLoadedError", "Wallet missing is not loaded."},
		"collection missing account": {"ValueError", "Couldn't find account: missing."},
		"collection foreign account": {"ValueError", "Couldn't find account: a2."},
		"collection records error":   {"RuntimeError", "fixture records failure"},
		"collection count error":     {"RuntimeError", "fixture count failure"},
	}
	for name, expected := range want {
		actual := cases[name].Error
		if actual == nil || actual.Code != -32500 || actual.Data.Command != cases[name].Method ||
			actual.Data.Name != expected[0] || actual.Message != expected[1] ||
			len(actual.Data.Traceback) != 1 {
			t.Fatalf("%q error = %+v, want %q %q", name, actual, expected[0], expected[1])
		}
	}
	for _, name := range []string{"purchase no wallets", "purchase empty default", "collection no wallets"} {
		if actual := cases[name].Error; actual == nil || actual.Data.Name != "AttributeError" {
			t.Fatalf("%q error = %+v", name, actual)
		}
	}
}

func listWrapperDelegatedParams(t *testing.T, fixture listWrapperOracleCase) map[string]any {
	t.Helper()
	if fixture.Error != nil || len(fixture.Calls) != 1 || fixture.Calls[0].Method != "txo_list" {
		t.Fatalf("%q delegation = error %+v, calls %+v", fixture.Name, fixture.Error, fixture.Calls)
	}
	return fixture.Calls[0].Params
}

func listWrapperHas(values map[string]any, key string) bool {
	_, exists := values[key]
	return exists
}

func listWrapperDecode(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode list wrapper oracle JSON %s: %v", raw, err)
	}
	return value
}

func listWrapperDecodeObject(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	value, ok := listWrapperDecode(t, raw).(map[string]any)
	if !ok {
		t.Fatalf("list wrapper result %s is not an object", raw)
	}
	return value
}

func runListWrapperOracle(t *testing.T) listWrapperOracleResponse {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate list wrapper oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot := os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	for relative := range listWrapperOraclePinnedSources {
		path := filepath.Join(sdkRoot, filepath.FromSlash(relative))
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned local list wrapper source is unavailable: %s", path)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(daemonRoot, "compat", "list_wrapper_oracle.py")
	if _, err := os.Stat(script); errors.Is(err, os.ErrNotExist) {
		t.Skipf("list wrapper oracle script is unavailable: %s", script)
	} else if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("python3", script, "--sdk-root", sdkRoot)
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1", "TZ=UTC")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("list wrapper oracle failed: %v\n%s", err, output)
	}
	var oracle listWrapperOracleResponse
	if err := json.Unmarshal(output, &oracle); err != nil {
		t.Fatalf("decode list wrapper oracle: %v\n%s", err, output)
	}
	return oracle
}
