package rpc

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"testing"

	walletpkg "lbry/daemon/wallet"
	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/ledgerdb"
	spvpkg "lbry/daemon/wallet/spv"
)

const (
	transactionShowOraclePinnedCommit  = "e7666f489418e96b6d2104974e93915b539235c5"
	transactionShowOraclePinnedVersion = "0.113.0"
	transactionShowOracleParentHex     = "01000000010000000000000000000000000000000000000000000000000000000000000000" +
		"ffffffff020101ffffffff0115cd5b07000000001976a914444444444444444444444444444444444444444488ac00000000"
	transactionShowOracleTransactionHex = "0100000001af53c3ea5813b9277322f9790c1227926cde8f59b8a572f54f70df6afef33c1b" +
		"0000000000ffffffff0200e1f505000000001976a9145555555555555555555555555555555555" +
		"55555588ac0000000000000000046a02aabb00000000"
	transactionShowOracleTransactionID = "e56ce90f16f0eed5500f2d6013cfc5cda251a53f0ca41618bc8300e987e0325c"
)

var transactionShowOraclePinnedSources = map[string]string{
	"lbry/__init__.py":                            "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
	"lbry/extras/daemon/daemon.py":                "15f9b43c5fc0f21a6361320e82768ef2632746c3ad17c252eab9e8d5e0291be0",
	"lbry/extras/daemon/json_response_encoder.py": "047fd406c20236025414b8805669b1a830b0b412386c1613498aa1ebaa021732",
	"lbry/wallet/database.py":                     "621ce600e8923f9802755cef73b98081af1deb078fc9324c765ee4d6b726ef5a",
	"lbry/wallet/dewies.py":                       "67506d75a5f0ddb3f7c2ea832ba7b13fb49ae4193f060a1fdf541b5f50a3084a",
	"lbry/wallet/manager.py":                      "042e3410753f5f903871d87e65b2b0cdbaf8d82cd66ae4e677e0bdd47109c7db",
	"lbry/wallet/network.py":                      "cfe6661af4c2028a542582e2c7fffc8c97dce93ca0f619a752a4a5af389b3e6b",
	"lbry/wallet/rpc/jsonrpc.py":                  "6da90b83bdb2e192929abddbb8b33824eac7d24f7ab126c1942db5ed6b7c1269",
	"lbry/wallet/transaction.py":                  "e73491aeb915fbce931acbb4d9631f3e05440a7d26c598db85e66e524a798d15",
	"lbry/wallet/util.py":                         "08f697c88ec36d2bb417609194266f279eba2f69b1a62a10b1de69b9c1733d5a",
	"lbry/wallet/wallet.py":                       "45a26975bd59d11ccad332e16447807b6f67e1d35729d4c4a509b3ab7eaa9010",
}

var transactionShowOraclePinnedMethods = map[string]string{
	"CodeMessageError.code":                  "60732917bbdfac4a69c89c9923c7184b4a21d4d861b04f9846aeb01bc88a760b",
	"CodeMessageError.message":               "be31c27412c9dcaef89ed8240b1936c8aaa771a29239e2828f2ae21ce002dc05",
	"Daemon.jsonrpc_transaction_show":        "10ec4201cf4cce44bf3442ff6654732bc2d99a1634e905e5d65cc1741d898ad6",
	"Daemon.ledger":                          "c0aad64201976cc6d3b4ae3fa49fe9434093c578706b84f45b8cc687c7276f46",
	"Database.get_transaction":               "170b2b7217a51a966b609a936ef8585cb31e1bc97463157c24278c46658a9107",
	"Input.amount":                           "ab8d93c4660ea7e42b32857b490008d04182917e50d8a83cef0fcb7f130f7e86",
	"JSONResponseEncoder.__init__":           "bf1a658c1eed62bbae283ebe132f8067f986e534771ebf3417685536472fdb1e",
	"JSONResponseEncoder.default":            "298986ed087ef927a948ecc2d8f55730ca2e57a9c6ec032255d30bc92448c4a8",
	"JSONResponseEncoder.encode_input":       "e5da304910667c242ed9ac6be076a3a6db087404f1769287d35e294b635ee5ce",
	"JSONResponseEncoder.encode_output":      "fc124a8362451a2449d83b06e252d9c3d85ec6b006b5f9d0dc5dfd60b5db92be",
	"JSONResponseEncoder.encode_transaction": "4ed03ea6d0a24d79213e63da6b78304a3b1a1c17b368cde3804d76d6efc78f7e",
	"JSONRPCError.__init__":                  "6694af6fe018ba7f86d734992597aaea26b18d70389263a7ff5fd2be1995144b",
	"JSONRPCError.create_command_exception":  "fc56255c3a3e15b5279f3d583d6ee67959109f5f0c4766c0d10928bf12cc659e",
	"JSONRPCError.filter_traceback":          "db8da5a9ff8f43e6ce64bdaad60f5e67cc3e071f1992b29d79fa8f2dafa97f86",
	"JSONRPCError.to_dict":                   "4a92e56be4937d195c7307f337b8fcac7a36b306d945b2dbe29108748882a347",
	"Network.get_transaction_and_merkle":     "cbc3a98dc4ea8ddd83218df36146f98ceb2ba6e127f57e5270a336d47745c0a8",
	"Output.get_address":                     "031f5c186213ba42ed354461e31d3d7075fda2cb285384485077f4e7adab1e8e",
	"Output.has_address":                     "7b1852917e901fef3875a1c7867bf943f09a4dcc9187feb8c5d87296f2adf2db",
	"Output.is_pubkey_hash":                  "9ee9f33bac7e1e6fbd748dd79737073f799a7e64e516b81866c363d538b1f4d9",
	"Output.is_script_hash":                  "bd52941646cbd63eb7eb2df43c0c9708938f26045b1293b9f3d262eb565d0773",
	"Output.pubkey_hash":                     "d693277604cac1e0861ab6ddf0655fac721f17c235f0aabcc9e9f6999df90099",
	"Output.script_hash":                     "54452f794077f1418dd41d56bc844bfaf44b3be0d422465d73b40ebdf0191a3f",
	"Transaction.fee":                        "6ac5b3a88a8bdf8a1d219c56ba0fee4e547c2d3ffa1c2cdff7ee35934e9eb608",
	"Transaction.id":                         "bac6bf453a7f3cdd446b2dffd60943aa02d8757a207ee78e30f284984dfea695",
	"Transaction.input_sum":                  "77c367ccf71154a325ebcef4f0731d04ddaccd41d3f5a0c90343fa6834d38295",
	"Transaction.output_sum":                 "2dc8ce7917177c03dc33b87124b9106edafaa2dd6619455c44d04c974d7cf0c8",
	"Transaction.raw":                        "109ab68599c6d3a509614d71062874b3d609ec890b440bb7db77acb5d86cc2eb",
	"Wallet.default_account":                 "76e84d5c63726f3c268e161ee2ef54e0573ab02a4aab04d9b7c6dae0fc95961e",
	"WalletManager.db":                       "591bfa029cbe61758280557e7e00e5f50f67e5cc6667226b54a83808749f1e93",
	"WalletManager.default_account":          "6b5ae4ee1fd368d8b3bb05e3a8a3362a0f958f4e5385787958ff83fdb855e731",
	"WalletManager.default_wallet":           "b985d6bbf6126a982f1f0084fc6872592cff7717f50b59fbe3a745f498c8de48",
	"WalletManager.get_transaction":          "b71d91ee306c7fe80dbab674633b55b7a07adf314b2d4943e5414ef3641ad2aa",
	"WalletManager.ledger":                   "20539d4b6adaf2dc3570a00a20a7d9e7bd8653edeb5ae4433603c253a9e0205a",
	"dewies_to_lbc":                          "e134ee4ea5e7d5000bb7f3a1d37dd40b6913724e142ba5c6b8e1f235c064fc5b",
	"jsonrpc_dumps_pretty":                   "96430605d1c0312de2f3d13690cb38568b4b8671d383af71833639b3590c5fef",
	"satoshis_to_coins":                      "ff81838bc9fc0d2583372395b8299c1cd6aca6ee95b5e4819b28e883b2e1ad50",
}

type transactionShowOracleResponse struct {
	Reference struct {
		Commit       string            `json:"commit"`
		Version      string            `json:"version"`
		SourceSHA256 map[string]string `json:"source_sha256"`
		MethodSHA256 map[string]string `json:"method_sha256"`
	} `json:"reference"`
	Metadata struct {
		PythonVersion            string          `json:"python_version"`
		ExtractedMethodsExecuted bool            `json:"extracted_methods_executed"`
		StdlibSQLiteUsed         bool            `json:"stdlib_sqlite_used"`
		ExternalNetworkUsed      bool            `json:"external_network_used"`
		FixtureTransactionID     string          `json:"fixture_transaction_id"`
		FixtureHasPaymentAndData bool            `json:"fixture_has_payment_and_data"`
		ClaimAndProtobufCovered  bool            `json:"claim_and_protobuf_covered"`
		CaseCount                int             `json:"case_count"`
		AdapterSelfChecks        map[string]bool `json:"adapter_self_checks"`
	} `json:"metadata"`
	Cases []transactionShowOracleCase `json:"cases"`
}

type transactionShowOracleCase struct {
	Name        string                     `json:"name"`
	Scenario    string                     `json:"scenario"`
	OmitParams  bool                       `json:"omit_params"`
	Params      json.RawMessage            `json:"params"`
	Repetitions int                        `json:"repetitions"`
	Responses   []json.RawMessage          `json:"responses"`
	Calls       transactionShowOracleCalls `json:"calls"`
}

type transactionShowOracleCalls struct {
	Database []map[string]any               `json:"database"`
	Network  []transactionShowOracleRPCCall `json:"network"`
}

type transactionShowOracleRPCCall struct {
	Method     string `json:"method"`
	Params     []any  `json:"params"`
	Restricted bool   `json:"restricted"`
}

func TestTransactionShowMatchesPinnedPythonOracle(t *testing.T) {
	oracle := runTransactionShowOracle(t)
	if oracle.Reference.Commit != transactionShowOraclePinnedCommit ||
		oracle.Reference.Version != transactionShowOraclePinnedVersion ||
		!reflect.DeepEqual(oracle.Reference.SourceSHA256, transactionShowOraclePinnedSources) ||
		!reflect.DeepEqual(oracle.Reference.MethodSHA256, transactionShowOraclePinnedMethods) {
		t.Fatalf("transaction show oracle reference = %+v", oracle.Reference)
	}
	metadata := oracle.Metadata
	if !metadata.ExtractedMethodsExecuted || !metadata.StdlibSQLiteUsed ||
		metadata.ExternalNetworkUsed || metadata.FixtureTransactionID != transactionShowOracleTransactionID ||
		!metadata.FixtureHasPaymentAndData || metadata.ClaimAndProtobufCovered ||
		metadata.CaseCount != 12 || len(oracle.Cases) != 12 {
		t.Fatalf("transaction show oracle metadata = %+v, cases %d", metadata, len(oracle.Cases))
	}
	for name, passed := range metadata.AdapterSelfChecks {
		if !passed {
			t.Fatalf("transaction show adapter self-check %q failed", name)
		}
	}
	if want := os.Getenv("LBRY_ORACLE_PYTHON_VERSION"); want != "" && metadata.PythonVersion != want {
		t.Fatalf("transaction show Python version = %q, want %q", metadata.PythonVersion, want)
	}
	assertTransactionShowOracleContract(t, oracle.Cases)

	for _, fixture := range oracle.Cases {
		t.Run(fixture.Name, func(t *testing.T) {
			manager, network := transactionShowOracleManager(t, fixture.Scenario)
			providerCalls := 0
			server := CreateServer(WithWalletManagerProvider(func() *walletpkg.WalletManager {
				providerCalls++
				return manager
			}))
			body := `{"method":"transaction_show"}`
			if !fixture.OmitParams {
				body = fmt.Sprintf(`{"method":"transaction_show","params":%s}`, fixture.Params)
			}
			repetitions := fixture.Repetitions
			if repetitions == 0 {
				repetitions = 1
			}
			for index := 0; index < repetitions; index++ {
				response := performRequest(server, http.MethodPost, "/", body, nil)
				if response.Code != http.StatusOK {
					t.Fatalf("response %d HTTP status = %d, body %s", index, response.Code, response.Body.String())
				}
				got := decodeResponse(t, response)
				want := decodeTransactionShowOracleJSON(t, fixture.Responses[index]).(map[string]any)
				normalizeTransactionShowTraceback(t, got, want)
				if !reflect.DeepEqual(got, want) {
					gotJSON, _ := json.MarshalIndent(got, "", "  ")
					wantJSON, _ := json.MarshalIndent(want, "", "  ")
					t.Fatalf("transaction_show response =\n%s\nwant\n%s", gotJSON, wantJSON)
				}
			}
			if providerCalls != repetitions {
				t.Fatalf("provider calls = %d, want %d", providerCalls, repetitions)
			}
			if calls := network.snapshotCalls(); !reflect.DeepEqual(calls, fixture.Calls.Network) {
				t.Fatalf("transaction info calls = %+v, Python %+v", calls, fixture.Calls.Network)
			}
			if fixture.Scenario != "local" && !fixture.OmitParams {
				stored, err := manager.DefaultLedger().Database.GetTransaction(
					context.Background(), transactionShowOracleTransactionID,
				)
				if err != nil || stored != nil {
					t.Fatalf("remote lookup was cached: row=%+v err=%v", stored, err)
				}
			}
		})
	}
}

func TestTransactionShowProviderAvailabilityUsesRequestTimeLookup(t *testing.T) {
	manager, _ := transactionShowOracleManager(t, "not_found")
	var current *walletpkg.WalletManager
	providerCalls := 0
	server := CreateServer(WithWalletManagerProvider(func() *walletpkg.WalletManager {
		providerCalls++
		return current
	}))
	body := `{"method":"transaction_show","params":{"txid":"0000000000000000000000000000000000000000000000000000000000000000"}}`

	first := decodeResponse(t, performRequest(server, http.MethodPost, "/", body, nil))
	assertTransactionShowComponentUnavailable(t, first)
	current = manager
	second := decodeResponse(t, performRequest(server, http.MethodPost, "/", body, nil))
	want := decodeTransactionShowOracleJSON(t, json.RawMessage(
		`{"jsonrpc":"2.0","result":{"code":404,"message":"transaction not found","success":false}}`,
	))
	if !reflect.DeepEqual(second, want) {
		t.Fatalf("available transaction_show = %#v, want %#v", second, want)
	}
	current = nil
	third := decodeResponse(t, performRequest(server, http.MethodPost, "/", body, nil))
	assertTransactionShowComponentUnavailable(t, third)
	if providerCalls != 3 {
		t.Fatalf("request-time provider calls = %d, want 3", providerCalls)
	}
}

func TestTransactionShowMissingTXIDChecksWalletFirst(t *testing.T) {
	manager, _ := transactionShowOracleManager(t, "remote_mempool")
	var current *walletpkg.WalletManager
	server := CreateServer(WithWalletManagerProvider(func() *walletpkg.WalletManager {
		return current
	}))
	body := `{"method":"transaction_show"}`

	unavailable := decodeResponse(t, performRequest(server, http.MethodPost, "/", body, nil))
	errorObject := unavailable["error"].(map[string]any)
	data := errorObject["data"].(map[string]any)
	if data["name"] != "ComponentsNotStartedError" {
		t.Fatalf("unavailable missing-txid error = %#v", unavailable)
	}

	current = manager
	available := decodeResponse(t, performRequest(server, http.MethodPost, "/", body, nil))
	errorObject = available["error"].(map[string]any)
	data = errorObject["data"].(map[string]any)
	if errorObject["message"] != "Daemon.jsonrpc_transaction_show() missing 1 required positional argument: 'txid'" ||
		data["name"] != "TypeError" {
		t.Fatalf("available missing-txid error = %#v", available)
	}
}

func TestTransactionShowPrimitiveAndInvalidTXIDArguments(t *testing.T) {
	manager, network := transactionShowOracleManager(t, "remote_mempool")
	server := CreateServer(WithWalletManagerProvider(func() *walletpkg.WalletManager { return manager }))

	primitiveBodies := []string{
		`{"method":"transaction_show","params":{"txid":null}}`,
		`{"method":"transaction_show","params":{"txid":true}}`,
		`{"method":"transaction_show","params":{"txid":7}}`,
		`{"method":"transaction_show","params":{"txid":1.25}}`,
	}
	primitiveValues := []any{nil, true, json.Number("7"), json.Number("1.25")}
	for index, body := range primitiveBodies {
		response := decodeResponse(t, performRequest(server, http.MethodPost, "/", body, nil))
		if _, ok := response["result"].(map[string]any); !ok {
			t.Fatalf("primitive TXID %d response = %#v", index, response)
		}
	}
	calls := network.snapshotCalls()
	if len(calls) != len(primitiveValues) {
		t.Fatalf("primitive transaction_show calls = %#v", calls)
	}
	for index, value := range primitiveValues {
		if !reflect.DeepEqual(calls[index].Params, []any{value}) {
			t.Fatalf("primitive transaction_show call %d = %#v, want %#v", index, calls[index].Params, []any{value})
		}
	}
	for _, body := range []string{
		`{"method":"transaction_show","params":{"txid":NaN}}`,
		`{"method":"transaction_show","params":{"txid":Infinity}}`,
		`{"method":"transaction_show","params":{"txid":-Infinity}}`,
	} {
		response := decodeResponse(t, performRequest(server, http.MethodPost, "/", body, nil))
		if _, ok := response["result"].(map[string]any); !ok {
			t.Fatalf("nonfinite TXID response = %#v", response)
		}
	}
	calls = network.snapshotCalls()
	if len(calls) != len(primitiveValues)+3 ||
		!math.IsNaN(calls[len(primitiveValues)].Params[0].(float64)) ||
		!math.IsInf(calls[len(primitiveValues)+1].Params[0].(float64), 1) ||
		!math.IsInf(calls[len(primitiveValues)+2].Params[0].(float64), -1) {
		t.Fatalf("nonfinite transaction_show calls = %#v", calls)
	}

	invalid := []struct {
		body    string
		name    string
		message string
	}{
		{body: `{"method":"transaction_show","params":{"txid":[]}}`, name: "ProgrammingError", message: "Error binding parameter 1: type 'list' is not supported"},
		{body: `{"method":"transaction_show","params":{"txid":{}}}`, name: "ProgrammingError", message: "Error binding parameter 1: type 'dict' is not supported"},
		{body: `{"method":"transaction_show","params":{"txid":9223372036854775808}}`, name: "OverflowError", message: "Python int too large to convert to SQLite INTEGER"},
	}
	for _, test := range invalid {
		response := decodeResponse(t, performRequest(server, http.MethodPost, "/", test.body, nil))
		errorObject := response["error"].(map[string]any)
		data := errorObject["data"].(map[string]any)
		if errorObject["message"] != test.message || data["name"] != test.name {
			t.Fatalf("invalid TXID response = %#v", response)
		}
	}
}

func TestTransactionShowDisconnectedHubMatchesPythonConnectionError(t *testing.T) {
	manager, _ := transactionShowOracleManager(t, "disconnected")
	server := CreateServer(WithWalletManagerProvider(func() *walletpkg.WalletManager { return manager }))
	response := decodeResponse(t, performRequest(
		server, http.MethodPost, "/",
		`{"method":"transaction_show","params":{"txid":null}}`, nil,
	))
	errorObject := response["error"].(map[string]any)
	data := errorObject["data"].(map[string]any)
	if errorObject["code"] != json.Number("-32500") || data["name"] != "ConnectionError" ||
		errorObject["message"] != "Attempting to send rpc request when connection is not available." {
		t.Fatalf("disconnected transaction_show = %#v", response)
	}
}

func TestTransactionShowMalformedHubErrorsMatchPython(t *testing.T) {
	tests := []struct {
		name    string
		value   any
		errName string
		message string
	}{
		{name: "null", value: nil, errName: "TypeError", message: "cannot unpack non-iterable NoneType object"},
		{name: "short", value: []any{}, errName: "ValueError", message: "not enough values to unpack (expected 2, got 0)"},
		{name: "long", value: []any{"raw", map[string]any{}, true}, errName: "ValueError", message: "too many values to unpack (expected 2)"},
		{name: "merkle wins", value: []any{true, nil}, errName: "AttributeError", message: "'NoneType' object has no attribute 'get'"},
		{name: "raw bool", value: []any{true, map[string]any{}}, errName: "TypeError", message: "argument should be bytes, buffer or ASCII string, not 'bool'"},
		{name: "odd hex", value: []any{"0", map[string]any{}}, errName: "Error", message: "Odd-length string"},
		{name: "short raw", value: []any{"00", map[string]any{}}, errName: "error", message: "unpack requires a buffer of 4 bytes"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, network := transactionShowOracleManager(t, "malformed")
			network.hasResponse = true
			network.response = test.value
			server := CreateServer(WithWalletManagerProvider(
				func() *walletpkg.WalletManager { return manager },
			))
			response := decodeResponse(t, performRequest(
				server, http.MethodPost, "/",
				`{"method":"transaction_show","params":{"txid":"missing"}}`, nil,
			))
			errorObject := response["error"].(map[string]any)
			data := errorObject["data"].(map[string]any)
			if errorObject["message"] != test.message || data["name"] != test.errName {
				t.Fatalf("malformed hub response = %#v", response)
			}
		})
	}
}

func assertTransactionShowOracleContract(t *testing.T, cases []transactionShowOracleCase) {
	t.Helper()
	wantNames := []string{
		"local named", "local wrapped named", "local legacy positional",
		"local include protobuf stripped", "remote mempool", "remote zero height",
		"remote raw id wins", "remote repeat is not cached", "remote not found",
		"remote not found substring", "remote other coded error", "missing txid",
	}
	for index, fixture := range cases {
		if fixture.Name != wantNames[index] {
			t.Fatalf("transaction show oracle case %d = %q, want %q", index, fixture.Name, wantNames[index])
		}
		repetitions := fixture.Repetitions
		if repetitions == 0 {
			repetitions = 1
		}
		if len(fixture.Responses) != repetitions {
			t.Fatalf("case %q responses = %d, want %d", fixture.Name, len(fixture.Responses), repetitions)
		}
		if fixture.Name == "missing txid" {
			if len(fixture.Calls.Database) != 0 || len(fixture.Calls.Network) != 0 {
				t.Fatalf("missing txid calls = %+v", fixture.Calls)
			}
			continue
		}
		if len(fixture.Calls.Database) != repetitions {
			t.Fatalf("case %q database calls = %+v", fixture.Name, fixture.Calls.Database)
		}
		wantNetworkCalls := repetitions
		if fixture.Scenario == "local" {
			wantNetworkCalls = 0
		}
		if len(fixture.Calls.Network) != wantNetworkCalls {
			t.Fatalf("case %q network calls = %+v", fixture.Name, fixture.Calls.Network)
		}
		for _, call := range fixture.Calls.Network {
			if call.Method != walletpkg.SPVTransactionInfoMethod || !call.Restricted || len(call.Params) != 1 {
				t.Fatalf("case %q network call = %+v", fixture.Name, call)
			}
		}
	}

	local := decodeTransactionShowOracleJSON(t, cases[0].Responses[0]).(map[string]any)["result"].(map[string]any)
	localInputs := local["inputs"].([]any)
	localOutputs := local["outputs"].([]any)
	if len(localInputs) != 1 || len(localInputs[0].(map[string]any)) != 8 ||
		len(localOutputs) != 2 || localOutputs[0].(map[string]any)["type"] != "payment" ||
		localOutputs[1].(map[string]any)["type"] != "data" ||
		localOutputs[1].(map[string]any)["is_my_output"] != false ||
		local["total_input"] != "1.23456789" || local["total_fee"] != "0.23456789" {
		t.Fatalf("local transaction encoding contract = %#v", local)
	}
	remote := decodeTransactionShowOracleJSON(t, cases[4].Responses[0]).(map[string]any)["result"].(map[string]any)
	if remote["height"] != json.Number("-1") || remote["total_input"] != "0.0" ||
		remote["total_fee"] != "-1.0" || len(remote["inputs"].([]any)[0].(map[string]any)) != 2 {
		t.Fatalf("remote transaction encoding contract = %#v", remote)
	}
	mismatch := decodeTransactionShowOracleJSON(t, cases[6].Responses[0]).(map[string]any)["result"].(map[string]any)
	if mismatch["txid"] != transactionShowOracleTransactionID {
		t.Fatalf("remote raw transaction ID = %#v", mismatch["txid"])
	}
	for _, index := range []int{8, 9, 10} {
		response := decodeTransactionShowOracleJSON(t, cases[index].Responses[0]).(map[string]any)
		if _, exists := response["error"]; exists {
			t.Fatalf("coded hub failure %q became JSON-RPC error: %#v", cases[index].Name, response)
		}
	}
}

func normalizeTransactionShowTraceback(t *testing.T, got, want map[string]any) {
	t.Helper()
	for _, payload := range []map[string]any{got, want} {
		errorObject, exists := payload["error"].(map[string]any)
		if !exists {
			continue
		}
		data, ok := errorObject["data"].(map[string]any)
		if !ok {
			t.Fatalf("transaction_show error data = %#v", errorObject["data"])
		}
		traceback, ok := data["traceback"].([]any)
		if !ok || len(traceback) == 0 {
			t.Fatalf("transaction_show traceback = %#v", data["traceback"])
		}
		data["traceback"] = []any{"<runtime>"}
	}
}

func assertTransactionShowComponentUnavailable(t *testing.T, payload map[string]any) {
	t.Helper()
	want := decodeTransactionShowOracleJSON(t, json.RawMessage(`{
		"jsonrpc":"2.0",
		"error":{
			"code":-32500,
			"data":{
				"args":[],
				"command":"transaction_show",
				"kwargs":{"txid":"0000000000000000000000000000000000000000000000000000000000000000"},
				"name":"ComponentsNotStartedError",
				"traceback":["<runtime>"]
			},
			"message":"the following required components have not yet started: [\"wallet\"]"
		}
	}`)).(map[string]any)
	normalizeTransactionShowTraceback(t, payload, want)
	if !reflect.DeepEqual(payload, want) {
		t.Fatalf("transaction_show unavailable = %#v, want %#v", payload, want)
	}
}

func decodeTransactionShowOracleJSON(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode transaction show oracle JSON %s: %v", raw, err)
	}
	return value
}

type transactionShowOracleNetwork struct {
	mu          sync.Mutex
	scenario    string
	calls       []transactionShowOracleRPCCall
	response    any
	hasResponse bool
}

func (network *transactionShowOracleNetwork) RetriableValue(
	_ context.Context, method string, params []any, restricted bool,
) (any, error) {
	network.mu.Lock()
	defer network.mu.Unlock()
	network.calls = append(network.calls, transactionShowOracleRPCCall{
		Method: method, Params: append([]any(nil), params...), Restricted: restricted,
	})
	if network.hasResponse {
		return network.response, nil
	}
	switch network.scenario {
	case "disconnected":
		return nil, spvpkg.ErrNetworkStopped
	case "not_found":
		return nil, transactionShowOracleRPCError{-5, "No such mempool or blockchain transaction."}
	case "not_found_substring":
		return nil, transactionShowOracleRPCError{9, "prefix No such mempool or blockchain transaction. suffix"}
	case "coded_error":
		return nil, transactionShowOracleRPCError{73, "hub rejected request"}
	case "local":
		return nil, errors.New("unexpected transaction info call for local hit")
	}
	height := int64(-1)
	if network.scenario == "remote_zero" {
		height = 0
	}
	return []any{transactionShowOracleTransactionHex, map[string]any{"block_height": height}}, nil
}

func (network *transactionShowOracleNetwork) OneShotValue(
	ctx context.Context, method string, params []any, restricted bool,
) (any, error) {
	return network.RetriableValue(ctx, method, params, restricted)
}

func (network *transactionShowOracleNetwork) snapshotCalls() []transactionShowOracleRPCCall {
	network.mu.Lock()
	defer network.mu.Unlock()
	result := make([]transactionShowOracleRPCCall, len(network.calls))
	copy(result, network.calls)
	return result
}

func (*transactionShowOracleNetwork) RetriableCall(
	context.Context, string, []any, bool,
) (map[string]any, error) {
	return nil, errors.New("unexpected header RPC")
}
func (*transactionShowOracleNetwork) Start(context.Context) error { return nil }
func (*transactionShowOracleNetwork) Stop(context.Context) error  { return nil }
func (*transactionShowOracleNetwork) RemoteHeight() int           { return 0 }
func (*transactionShowOracleNetwork) IsConnected() bool           { return true }
func (*transactionShowOracleNetwork) SetHeaderNotificationHandler(func(context.Context, any)) {
}
func (*transactionShowOracleNetwork) SetAddressNotificationHandler(func(context.Context, any)) {
}
func (*transactionShowOracleNetwork) SetConnectedHandler(func(context.Context)) {}
func (*transactionShowOracleNetwork) SubscribeAddresses(context.Context, []string) ([]any, error) {
	return nil, errors.New("unexpected address subscription")
}

type transactionShowOracleRPCError struct {
	code    int64
	message string
}

func (rpcError transactionShowOracleRPCError) Error() string      { return rpcError.message }
func (rpcError transactionShowOracleRPCError) RPCCode() int64     { return rpcError.code }
func (rpcError transactionShowOracleRPCError) RPCMessage() string { return rpcError.message }

func transactionShowOracleManager(
	t *testing.T, scenario string,
) (*walletpkg.WalletManager, *transactionShowOracleNetwork) {
	t.Helper()
	manager := walletpkg.NewWalletManager()
	root := t.TempDir()
	ledger, err := manager.GetOrCreateLedger(keys.MainNet.ID(), walletpkg.LedgerConfig{"data_path": root})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(ledger.Database.Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Database.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := ledger.Database.Close(context.Background()); err != nil {
			t.Errorf("close transaction-show database: %v", err)
		}
	})
	account := transactionShowOracleAccount(t)
	wallet := walletpkg.NewWallet(
		walletpkg.WithWalletName("transaction-show"),
		walletpkg.WithWalletAccounts([]*walletpkg.Account{account}),
	)
	manager.Wallets = []*walletpkg.Wallet{wallet}
	if err := manager.RegisterAccount(keys.MainNet.ID(), account); err != nil {
		t.Fatal(err)
	}
	network := &transactionShowOracleNetwork{scenario: scenario}
	ledger.SPVNetwork = network
	if scenario == "local" {
		transactionShowOracleSeedLocal(t, ledger)
	}
	return manager, network
}

func transactionShowOracleAccount(t *testing.T) *walletpkg.Account {
	t.Helper()
	privateKey, err := keys.PrivateKeyFromSeed(keys.MainNet, bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	account, err := walletpkg.NewAccount(keys.MainNet, walletpkg.NewObject(
		walletpkg.Member{Key: "name", Value: "transaction-show"},
		walletpkg.Member{Key: "public_key", Value: privateKey.PublicKey().ExtendedKeyString()},
		walletpkg.Member{Key: "address_generator", Value: walletpkg.NewObject(
			walletpkg.Member{Key: "name", Value: walletpkg.SingleAddressGenerator},
		)},
		walletpkg.Member{Key: "modified_on", Value: 1},
	))
	if err != nil {
		t.Fatal(err)
	}
	account.ID = "transaction-show"
	return account
}

func transactionShowOracleSeedLocal(t *testing.T, ledger *walletpkg.Ledger) {
	t.Helper()
	parentRaw, err := hex.DecodeString(transactionShowOracleParentHex)
	if err != nil {
		t.Fatal(err)
	}
	transactionRaw, err := hex.DecodeString(transactionShowOracleTransactionHex)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := walletpkg.ParseTransaction(parentRaw)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := walletpkg.ParseTransaction(transactionRaw)
	if err != nil {
		t.Fatal(err)
	}
	parent.Height, parent.Position, parent.IsVerified = 3, 0, true
	transaction.Height, transaction.Position, transaction.IsVerified = 7, 1, true
	if transaction.ID != transactionShowOracleTransactionID {
		t.Fatalf("transaction fixture ID = %s, want %s", transaction.ID, transactionShowOracleTransactionID)
	}
	parentAddress, err := parent.Outputs[0].Address(ledger.Network)
	if err != nil {
		t.Fatal(err)
	}
	transactionAddress, err := transaction.Outputs[0].Address(ledger.Network)
	if err != nil {
		t.Fatal(err)
	}
	rows := []ledgerdb.TransactionIORow{
		{
			Transaction: ledgerdb.TransactionRow{
				TXID: parent.ID, Raw: parent.Raw, Height: parent.Height,
				Position: parent.Position, IsVerified: parent.IsVerified,
			},
			Outputs: []ledgerdb.TransactionOutputRow{{
				TXOID: parent.Outputs[0].ID(), Address: &parentAddress, Position: 0,
				Amount: int64(parent.Outputs[0].Amount), Script: parent.Outputs[0].Script.Source,
				TXOType: walletpkg.TransactionOutputTypeOther,
			}},
		},
		{
			Transaction: ledgerdb.TransactionRow{
				TXID: transaction.ID, Raw: transaction.Raw, Height: transaction.Height,
				Position: transaction.Position, IsVerified: transaction.IsVerified,
			},
			// OP_RETURN is intentionally absent: Python marks non-persisted raw
			// outputs as is_my_output=false during local hydration.
			Outputs: []ledgerdb.TransactionOutputRow{{
				TXOID: transaction.Outputs[0].ID(), Address: &transactionAddress, Position: 0,
				Amount: int64(transaction.Outputs[0].Amount), Script: transaction.Outputs[0].Script.Source,
				TXOType: walletpkg.TransactionOutputTypeOther,
			}},
		},
	}
	if err := ledger.Database.SaveTransactionIOBatch(context.Background(), rows, transactionAddress, ""); err != nil {
		t.Fatal(err)
	}
}

func runTransactionShowOracle(t *testing.T) transactionShowOracleResponse {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate transaction show oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot := os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	for relative := range transactionShowOraclePinnedSources {
		path := filepath.Join(sdkRoot, filepath.FromSlash(relative))
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned local transaction show source is unavailable: %s", path)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(daemonRoot, "compat", "transaction_show_oracle.py")
	if _, err := os.Stat(script); errors.Is(err, os.ErrNotExist) {
		t.Skipf("transaction show oracle script is unavailable: %s", script)
	} else if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("python3", script, "--sdk-root", sdkRoot)
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1", "TZ=UTC")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("transaction show oracle failed: %v\n%s", err, output)
	}
	var oracle transactionShowOracleResponse
	if err := json.Unmarshal(output, &oracle); err != nil {
		t.Fatalf("decode transaction show oracle: %v\n%s", err, output)
	}
	return oracle
}

var _ walletpkg.LedgerSPVNetwork = (*transactionShowOracleNetwork)(nil)
