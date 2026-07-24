package rpc

import (
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
	claimSearchOracleCommit  = "e7666f489418e96b6d2104974e93915b539235c5"
	claimSearchOracleVersion = "0.113.0"
)

var claimSearchOracleSources = map[string]string{
	"lbry/__init__.py":             "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
	"lbry/error/__init__.py":       "4a279d245ffc8c9e3a966625f800340d7325183ba900b727d39d4074f196a795",
	"lbry/extras/daemon/daemon.py": "15f9b43c5fc0f21a6361320e82768ef2632746c3ad17c252eab9e8d5e0291be0",
	"lbry/wallet/ledger.py":        "5b5e3deacd5a87ec69a91b42c7aa6460605146724205ff0b387402dd193be2a5",
	"lbry/wallet/manager.py":       "042e3410753f5f903871d87e65b2b0cdbaf8d82cd66ae4e677e0bdd47109c7db",
	"lbry/wallet/network.py":       "cfe6661af4c2028a542582e2c7fffc8c97dce93ca0f619a752a4a5af389b3e6b",
	"lbry/wallet/wallet.py":        "45a26975bd59d11ccad332e16447807b6f67e1d35729d4c4a509b3ab7eaa9010",
}

var claimSearchOracleMethods = map[string]string{
	"Daemon.jsonrpc_claim_search":         "c0fe99e03604d69c828d5ff7dbe700da6926a5cf25fac78e0f2107481923ca1a",
	"Daemon.ledger":                       "c0aad64201976cc6d3b4ae3fa49fe9434093c578706b84f45b8cc687c7276f46",
	"Ledger.claim_search":                 "1f40805539780f244d81b2c436794dcbcf27dad1e80c3417b4e70d7f8cf227a8",
	"Network.claim_search":                "8ffee30536ef5354fc5c6028eb06b70512ce0f2ed09bf9567edb8689a4957968",
	"Wallet.default_account":              "76e84d5c63726f3c268e161ee2ef54e0573ab02a4aab04d9b7c6dae0fc95961e",
	"WalletManager.default_account":       "6b5ae4ee1fd368d8b3bb05e3a8a3362a0f958f4e5385787958ff83fdb855e731",
	"WalletManager.default_wallet":        "b985d6bbf6126a982f1f0084fc6872592cff7717f50b59fbe3a745f498c8de48",
	"WalletManager.get_wallet_or_default": "a78f3e4003c8bc2c25c95681532cb166eb3685a611aecd6024893fa6c94e8537",
	"WalletManager.get_wallet_or_error":   "ac6310a5232801623f12f4be0909a0e64a595a94330465f3c825b9ac34c51eec",
}

type claimSearchOracleResponse struct {
	Reference struct {
		Commit       string            `json:"commit"`
		Version      string            `json:"version"`
		SourceSHA256 map[string]string `json:"source_sha256"`
		MethodSHA256 map[string]string `json:"method_sha256"`
	} `json:"reference"`
	Metadata struct {
		PythonVersion            string            `json:"python_version"`
		ExtractedMethodsExecuted bool              `json:"extracted_methods_executed"`
		ExternalNetworkUsed      bool              `json:"external_network_used"`
		CaseCount                int               `json:"case_count"`
		ProposedGoContract       map[string]string `json:"proposed_go_contract"`
		ContractNotes            map[string]string `json:"contract_notes"`
	} `json:"metadata"`
	Cases []claimSearchOracleCase `json:"cases"`
}

type claimSearchOracleCase struct {
	Name   string                  `json:"name"`
	Params map[string]any          `json:"params"`
	Result map[string]any          `json:"result"`
	Error  *claimSearchOracleError `json:"error"`
	Calls  struct {
		RPC     []claimSearchOracleRPC     `json:"rpc"`
		Inflate []claimSearchOracleInflate `json:"inflate"`
	} `json:"calls"`
}

type claimSearchOracleError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type claimSearchOracleRPC struct {
	Ledger     string         `json:"ledger"`
	Method     string         `json:"method"`
	Params     map[string]any `json:"params"`
	Restricted bool           `json:"restricted"`
	Session    any            `json:"session"`
}

type claimSearchOracleInflate struct {
	Ledger                 string         `json:"ledger"`
	Accounts               []string       `json:"accounts"`
	Encoded                string         `json:"encoded"`
	IncludePurchaseReceipt any            `json:"include_purchase_receipt"`
	IncludeIsMyOutput      any            `json:"include_is_my_output"`
	ExtraOptions           map[string]any `json:"extra_options"`
}

func TestClaimSearchPinnedOracle(t *testing.T) {
	oracle := runClaimSearchOracle(t)
	if oracle.Reference.Commit != claimSearchOracleCommit ||
		oracle.Reference.Version != claimSearchOracleVersion ||
		!reflect.DeepEqual(oracle.Reference.SourceSHA256, claimSearchOracleSources) ||
		!reflect.DeepEqual(oracle.Reference.MethodSHA256, claimSearchOracleMethods) {
		t.Fatalf("claim search oracle reference = %+v", oracle.Reference)
	}
	if oracle.Metadata.PythonVersion == "" || !oracle.Metadata.ExtractedMethodsExecuted ||
		oracle.Metadata.ExternalNetworkUsed || oracle.Metadata.CaseCount != 25 ||
		len(oracle.Cases) != 25 {
		t.Fatalf("claim search oracle metadata = %+v, cases %d", oracle.Metadata, len(oracle.Cases))
	}
	assertClaimSearchOracle(t, oracle.Cases)
}

func TestClaimSearchNormalizerMatchesPinnedOracle(t *testing.T) {
	oracle := runClaimSearchOracle(t)
	for _, fixture := range oracle.Cases {
		fixture := fixture
		t.Run(fixture.Name, func(t *testing.T) {
			input := claimSearchOracleCanonical(t, fixture.Params).(map[string]any)
			request, err := normalizeClaimSearchParams(normalizedRPCParams{named: input})
			if fixture.Error != nil && (fixture.Error.Type == "ConflictingInputValueError" ||
				fixture.Name == "bad page precedes missing wallet") {
				if err == nil || err.Error() != fixture.Error.Message ||
					recoveredErrorName(err) != fixture.Error.Type {
					t.Fatalf("normalizer error = %T %v, want %s: %s",
						err, err, fixture.Error.Type, fixture.Error.Message)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(fixture.Calls.RPC) > 0 {
				got := claimSearchOracleCanonical(t, request.HubParams)
				want := claimSearchOracleCanonical(t, fixture.Calls.RPC[0].Params)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("hub params = %#v, want %#v", got, want)
				}
			}
			if len(fixture.Calls.Inflate) > 0 {
				inflate := fixture.Calls.Inflate[0]
				if request.IncludePurchaseReceipt != transactionListTruthy(inflate.IncludePurchaseReceipt) ||
					request.IncludeIsMyOutput != transactionListTruthy(inflate.IncludeIsMyOutput) {
					t.Fatalf("include flags = %t, %t; want %#v, %#v",
						request.IncludePurchaseReceipt, request.IncludeIsMyOutput,
						inflate.IncludePurchaseReceipt, inflate.IncludeIsMyOutput)
				}
			}
			if fixture.Result != nil {
				gotPage := claimSearchOracleCanonical(t, request.Page.wireValue())
				gotPageSize := claimSearchOracleCanonical(t, request.PageSize.wireValue())
				if !reflect.DeepEqual(gotPage, fixture.Result["page"]) ||
					!reflect.DeepEqual(gotPageSize, fixture.Result["page_size"]) {
					t.Fatalf("pagination = %#v/%#v, want %#v/%#v",
						gotPage, gotPageSize, fixture.Result["page"], fixture.Result["page_size"])
				}
			}
		})
	}
}

func TestClaimSearchOrderDedupUsesRecursivePythonEquality(t *testing.T) {
	request, err := normalizeClaimSearchParams(normalizedRPCParams{named: map[string]any{
		"order_by": []any{
			map[string]any{"nested": json.Number("1")},
			map[string]any{"nested": true},
			[]any{json.Number("0")},
			[]any{false},
			json.Number("1e0"),
			true,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := []any{
		map[string]any{"nested": json.Number("1")},
		[]any{json.Number("0")},
		json.Number("1e0"),
	}
	if got := request.HubParams["order_by"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("recursive order dedup = %#v, want %#v", got, want)
	}
}

func claimSearchOracleCanonical(t *testing.T, value any) any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var canonical any
	if err := json.Unmarshal(encoded, &canonical); err != nil {
		t.Fatal(err)
	}
	return canonical
}

func assertClaimSearchOracle(t *testing.T, cases []claimSearchOracleCase) {
	t.Helper()
	byName := make(map[string]claimSearchOracleCase, len(cases))
	for _, fixture := range cases {
		byName[fixture.Name] = fixture
	}
	defaults := byName["defaults"]
	assertClaimSearchRPC(t, defaults, map[string]any{"limit": float64(20), "offset": float64(0)})
	assertClaimSearchPage(t, defaults, 1, 20, true, 3, 41)

	for _, name := range []string{
		"falsey claim ids null", "falsey claim ids empty list", "falsey claim ids false",
	} {
		if _, exists := onlyClaimSearchRPC(t, byName[name]).Params["claim_ids"]; exists {
			t.Fatalf("%s forwarded falsey claim_ids", name)
		}
	}
	conflict := byName["claim id conflict precedes pagination and wallet"]
	assertClaimSearchError(t, conflict, "ConflictingInputValueError", "Only 'claim_id' or 'claim_ids' is allowed, not both.", 0)

	if onlyClaimSearchRPC(t, byName["valid signature"]).Params["signature_valid"] != float64(1) ||
		onlyClaimSearchRPC(t, byName["invalid signature"]).Params["signature_valid"] != float64(0) ||
		onlyClaimSearchRPC(t, byName["invalid signature wins"]).Params["signature_valid"] != float64(0) {
		t.Fatal("signature migration mismatch")
	}
	if _, exists := onlyClaimSearchRPC(t, byName["false signature flags removed"]).Params["signature_valid"]; exists {
		t.Fatal("false signature flags produced signature_valid")
	}
	for name, want := range map[string]bool{
		"has no source false overwrites": true,
		"has no source true":             false,
		"has no source null":             true,
	} {
		if onlyClaimSearchRPC(t, byName[name]).Params["has_source"] != want {
			t.Fatalf("%s has_source mismatch", name)
		}
	}
	assertClaimSearchList(t, onlyClaimSearchRPC(t, byName["order scalar"]).Params["order_by"], []any{"height"})
	assertClaimSearchList(t, onlyClaimSearchRPC(t, byName["order migration and dedupe"]).Params["order_by"], []any{
		"trending_score", "height", "^amount",
	})
	assertClaimSearchList(t, onlyClaimSearchRPC(t, byName["order null"]).Params["order_by"], []any{nil})

	selected := byName["negative page cap selected wallet and includes"]
	rpcCall := onlyClaimSearchRPC(t, selected)
	wantSelectedParams := map[string]any{
		"account_id": "hub-only", "claim_id": "c", "has_source": true,
		"limit": float64(50), "no_totals": true, "offset": float64(100),
		"order_by": []any{"trending_score", "height"}, "signature_valid": float64(0),
	}
	if !reflect.DeepEqual(rpcCall.Params, wantSelectedParams) || rpcCall.Ledger != "default-ledger" {
		t.Fatalf("selected wallet hub call = %+v", rpcCall)
	}
	inflate := onlyClaimSearchInflate(t, selected)
	if !reflect.DeepEqual(inflate.Accounts, []string{"other-account-0", "other-account-1"}) ||
		inflate.Ledger != "default-ledger" || inflate.IncludePurchaseReceipt != true ||
		inflate.IncludeIsMyOutput != float64(1) {
		t.Fatalf("selected wallet inflation = %+v", inflate)
	}
	assertClaimSearchPage(t, selected, 3, 50, false, 0, 0)

	zero := byName["zero pagination no totals"]
	assertClaimSearchRPC(t, zero, map[string]any{"limit": float64(0), "no_totals": true, "offset": float64(0)})
	assertClaimSearchPage(t, zero, 0, 0, false, 0, 0)
	assertClaimSearchError(t, byName["zero page size totals fails after request"], "ZeroDivisionError", "division by zero", 1)
	boolean := byName["boolean pagination"]
	assertClaimSearchRPC(t, boolean, map[string]any{"limit": float64(1), "offset": float64(-1)})
	assertClaimSearchPage(t, boolean, 0, 1, true, 41, 41)
	fractional := byName["fractional pagination"]
	assertClaimSearchRPC(t, fractional, map[string]any{"limit": 6.5, "offset": 9.75})
	assertClaimSearchPage(t, fractional, 2.5, 6.5, true, 7, 41)
	assertClaimSearchError(t, byName["bad page precedes missing wallet"], "TypeError", "bad operand type for abs(): 'str'", 0)
	assertClaimSearchError(t, byName["missing wallet after preprocessing"], "WalletNotLoadedError", "Wallet missing is not loaded.", 0)

	empty := onlyClaimSearchInflate(t, byName["empty selected wallet"])
	if len(empty.Accounts) != 0 || empty.Ledger != "default-ledger" {
		t.Fatalf("empty selected wallet inflation = %+v", empty)
	}
	falseIncludes := byName["false include flags consumed"]
	if _, exists := onlyClaimSearchRPC(t, falseIncludes).Params["include_purchase_receipt"]; exists {
		t.Fatal("include_purchase_receipt reached hub")
	}
	if got := onlyClaimSearchInflate(t, falseIncludes); got.IncludePurchaseReceipt != false || got.IncludeIsMyOutput != nil {
		t.Fatalf("false include bindings = %+v", got)
	}
	session := onlyClaimSearchRPC(t, byName["session override consumed"])
	if session.Session != "alternate-session" {
		t.Fatalf("session override = %#v", session.Session)
	}
	if _, exists := session.Params["session_override"]; exists {
		t.Fatal("session_override reached hub params")
	}
}

func onlyClaimSearchRPC(t *testing.T, fixture claimSearchOracleCase) claimSearchOracleRPC {
	t.Helper()
	if len(fixture.Calls.RPC) != 1 {
		t.Fatalf("%s RPC calls = %+v", fixture.Name, fixture.Calls.RPC)
	}
	return fixture.Calls.RPC[0]
}

func onlyClaimSearchInflate(t *testing.T, fixture claimSearchOracleCase) claimSearchOracleInflate {
	t.Helper()
	if len(fixture.Calls.Inflate) != 1 {
		t.Fatalf("%s inflate calls = %+v", fixture.Name, fixture.Calls.Inflate)
	}
	return fixture.Calls.Inflate[0]
}

func assertClaimSearchRPC(t *testing.T, fixture claimSearchOracleCase, params map[string]any) {
	t.Helper()
	call := onlyClaimSearchRPC(t, fixture)
	if call.Method != "blockchain.claimtrie.search" || call.Restricted || call.Session != nil ||
		call.Ledger != "default-ledger" || !reflect.DeepEqual(call.Params, params) {
		t.Fatalf("%s RPC call = %+v, want params %#v", fixture.Name, call, params)
	}
}

func assertClaimSearchPage(t *testing.T, fixture claimSearchOracleCase, page, pageSize float64, totals bool, totalPages, totalItems float64) {
	t.Helper()
	if fixture.Error != nil || fixture.Result["page"] != page || fixture.Result["page_size"] != pageSize {
		t.Fatalf("%s page = %#v, error %+v", fixture.Name, fixture.Result, fixture.Error)
	}
	gotPages, pagesExist := fixture.Result["total_pages"]
	gotItems, itemsExist := fixture.Result["total_items"]
	if pagesExist != totals || itemsExist != totals ||
		totals && (gotPages != totalPages || gotItems != totalItems) {
		t.Fatalf("%s totals = %#v", fixture.Name, fixture.Result)
	}
}

func assertClaimSearchError(t *testing.T, fixture claimSearchOracleCase, name, message string, rpcCalls int) {
	t.Helper()
	if fixture.Error == nil || fixture.Error.Type != name || fixture.Error.Message != message ||
		len(fixture.Calls.RPC) != rpcCalls || fixture.Result != nil {
		t.Fatalf("%s error = %+v, calls %+v", fixture.Name, fixture.Error, fixture.Calls)
	}
}

func assertClaimSearchList(t *testing.T, got any, want []any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("list = %#v, want %#v", got, want)
	}
}

func runClaimSearchOracle(t *testing.T) claimSearchOracleResponse {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate claim search oracle test")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot := os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	script := filepath.Join(daemonRoot, "compat", "claim_search_oracle.py")
	for _, path := range []string{sdkRoot, script} {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			t.Skipf("claim search oracle dependency unavailable: %s", path)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command(python, script, "--sdk-root", sdkRoot)
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Python claim search oracle failed: %v\n%s", err, output)
	}
	var oracle claimSearchOracleResponse
	if err := json.Unmarshal(output, &oracle); err != nil {
		t.Fatalf("decode claim search oracle: %v\n%s", err, output)
	}
	return oracle
}
