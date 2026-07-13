package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	walletpkg "lbry/daemon/wallet"
	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/ledgerdb"
)

const (
	transactionListOraclePinnedCommit  = "e7666f489418e96b6d2104974e93915b539235c5"
	transactionListOraclePinnedVersion = "0.113.0"
)

var transactionListOraclePinnedSources = map[string]string{
	"lbry/__init__.py":             "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
	"lbry/error/__init__.py":       "4a279d245ffc8c9e3a966625f800340d7325183ba900b727d39d4074f196a795",
	"lbry/extras/daemon/daemon.py": "15f9b43c5fc0f21a6361320e82768ef2632746c3ad17c252eab9e8d5e0291be0",
	"lbry/wallet/account.py":       "ea2ca30bddf9c0145469e989d9855dbe7be5184943ae7b8ca690eda41eb7db50",
	"lbry/wallet/database.py":      "621ce600e8923f9802755cef73b98081af1deb078fc9324c765ee4d6b726ef5a",
	"lbry/wallet/manager.py":       "042e3410753f5f903871d87e65b2b0cdbaf8d82cd66ae4e677e0bdd47109c7db",
	"lbry/wallet/wallet.py":        "45a26975bd59d11ccad332e16447807b6f67e1d35729d4c4a509b3ab7eaa9010",
}

var transactionListOraclePinnedMethods = map[string]string{
	"Account.get_transaction_history":       "86ad050ddcb07b015ac40f76cdd321d5f148b6673cdc287b6bf5a14dab59222b",
	"Account.get_transaction_history_count": "f9b01e74174ca43a3286ad85406a4c371c7dc61b7805b7b99423fe40cdb3cb77",
	"Database.select_transactions":          "e90345f73d9b5cda3444c90c3c316b86fce4433fce86344be43c93f2edad224e",
	"Daemon.jsonrpc_transaction_list":       "b12a50a7275c160f383257aabcecefeb57822dd6641d56da143b256fccded213",
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

type transactionListOracleResponse struct {
	Reference struct {
		Commit       string            `json:"commit"`
		Version      string            `json:"version"`
		SourceSHA256 map[string]string `json:"source_sha256"`
		MethodSHA256 map[string]string `json:"method_sha256"`
	} `json:"reference"`
	Metadata struct {
		PythonVersion            string `json:"python_version"`
		ExtractedMethodsExecuted bool   `json:"extracted_methods_executed"`
		StdlibSQLiteUsed         bool   `json:"stdlib_sqlite_used"`
		ExternalNetworkUsed      bool   `json:"external_network_used"`
		FixtureRows              int    `json:"fixture_rows"`
		CaseCount                int    `json:"case_count"`
	} `json:"metadata"`
	Cases []transactionListOracleCase `json:"cases"`
}

type transactionListOracleCase struct {
	Name         string                      `json:"name"`
	ManagerState string                      `json:"manager_state"`
	OmitParams   bool                        `json:"omit_params"`
	Params       json.RawMessage             `json:"params"`
	Result       json.RawMessage             `json:"result"`
	Error        *transactionListOracleError `json:"error"`
	Calls        []transactionListOracleCall `json:"calls"`
}

type transactionListOracleError struct {
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

type transactionListOracleCall struct {
	Method   string      `json:"method"`
	Ledger   string      `json:"ledger"`
	Wallet   string      `json:"wallet"`
	Accounts []string    `json:"accounts"`
	ReadOnly bool        `json:"read_only"`
	Offset   json.Number `json:"offset"`
	Limit    json.Number `json:"limit"`
}

func TestTransactionListMatchesPinnedPythonOracle(t *testing.T) {
	oracle := runTransactionListOracle(t)
	if oracle.Reference.Commit != transactionListOraclePinnedCommit ||
		oracle.Reference.Version != transactionListOraclePinnedVersion ||
		!reflect.DeepEqual(oracle.Reference.SourceSHA256, transactionListOraclePinnedSources) ||
		!reflect.DeepEqual(oracle.Reference.MethodSHA256, transactionListOraclePinnedMethods) {
		t.Fatalf("transaction list oracle reference = %+v", oracle.Reference)
	}
	if !oracle.Metadata.ExtractedMethodsExecuted || !oracle.Metadata.StdlibSQLiteUsed ||
		oracle.Metadata.ExternalNetworkUsed || oracle.Metadata.FixtureRows != 5 ||
		oracle.Metadata.CaseCount != 47 || len(oracle.Cases) != 47 {
		t.Fatalf("transaction list oracle metadata = %+v, cases %d", oracle.Metadata, len(oracle.Cases))
	}
	if want := os.Getenv("LBRY_ORACLE_PYTHON_VERSION"); want != "" &&
		oracle.Metadata.PythonVersion != want {
		t.Fatalf("transaction list Python version = %q, want %q", oracle.Metadata.PythonVersion, want)
	}
	assertTransactionListOracleContract(t, oracle.Cases)

	manager, namesByTXID := transactionListOracleManager(t)
	managers := transactionListOracleManagerStates(manager)
	for _, fixture := range oracle.Cases {
		t.Run(fixture.Name, func(t *testing.T) {
			selectedManager, exists := managers[fixture.ManagerState]
			if !exists {
				t.Fatalf("unknown transaction-list manager state %q", fixture.ManagerState)
			}
			server := CreateServer(WithWalletManagerProvider(func() *walletpkg.WalletManager {
				return selectedManager
			}))
			body := `{"method":"transaction_list"}`
			if !fixture.OmitParams {
				body = fmt.Sprintf(`{"method":"transaction_list","params":%s}`, fixture.Params)
			}
			response := performRequest(server, http.MethodPost, "/", body, nil)
			if response.Code != http.StatusOK {
				t.Fatalf("HTTP status = %d, body %s", response.Code, response.Body.String())
			}
			payload := decodeResponse(t, response)
			if fixture.Error != nil {
				assertTransactionListOracleError(t, payload, fixture.Error)
				return
			}
			assertTransactionListOracleResult(t, payload, fixture.Result, namesByTXID)
		})
	}
}

func TestTransactionListProviderAvailabilityAndRequestTimeLookup(t *testing.T) {
	manager, namesByTXID := transactionListOracleManager(t)
	var current *walletpkg.WalletManager
	providerCalls := 0
	server := CreateServer(WithWalletManagerProvider(func() *walletpkg.WalletManager {
		providerCalls++
		return current
	}))
	if providerCalls != 0 {
		t.Fatalf("provider called %d times during server construction", providerCalls)
	}

	unavailable := performRequest(
		server, http.MethodPost, "/", `{"method":"transaction_list"}`, nil,
	)
	assertTransactionListProviderUnavailable(t, unavailable)
	if providerCalls != 1 {
		t.Fatalf("provider calls after unavailable request = %d, want 1", providerCalls)
	}

	current = manager
	available := performRequest(
		server, http.MethodPost, "/", `{"method":"transaction_list","params":{"page_size":1}}`, nil,
	)
	payload := decodeResponse(t, available)
	expected := json.RawMessage(`{"items":["r0"],"page":1,"page_size":1,"total_pages":5,"total_items":5}`)
	assertTransactionListOracleResult(t, payload, expected, namesByTXID)
	if providerCalls != 2 {
		t.Fatalf("provider calls after available request = %d, want 2", providerCalls)
	}

	current = nil
	secondUnavailable := performRequest(
		server, http.MethodPost, "/", `{"method":"transaction_list"}`, nil,
	)
	assertTransactionListProviderUnavailable(t, secondUnavailable)
	if providerCalls != 3 {
		t.Fatalf("provider calls after second unavailable request = %d, want 3", providerCalls)
	}
}

func TestTransactionListNonFinitePaginationCompatibility(t *testing.T) {
	manager, namesByTXID := transactionListOracleManager(t)
	server := CreateServer(WithWalletManagerProvider(func() *walletpkg.WalletManager {
		return manager
	}))

	for _, test := range []struct {
		name     string
		body     string
		expected json.RawMessage
	}{
		{
			name: "NaN clamps to one",
			body: `{"method":"transaction_list","params":{"account_id":"a2","page":NaN,"page_size":NaN}}`,
			expected: json.RawMessage(
				`{"items":["r0"],"page":1,"page_size":1,"total_pages":5,"total_items":5}`,
			),
		},
		{
			name: "negative infinity clamps to one",
			body: `{"method":"transaction_list","params":{"account_id":"a2","page":-Infinity,"page_size":-Infinity}}`,
			expected: json.RawMessage(
				`{"items":["r0"],"page":1,"page_size":1,"total_pages":5,"total_items":5}`,
			),
		},
		{
			name: "extreme underflow is falsy",
			body: `{"method":"transaction_list","params":{"account_id":1e-1000000000,"page":1e-1000000000,"page_size":2}}`,
			expected: json.RawMessage(
				`{"items":["r0","r1"],"page":1,"page_size":2,"total_pages":3,"total_items":5}`,
			),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := performRequest(server, http.MethodPost, "/", test.body, nil)
			payload := decodeResponse(t, response)
			assertTransactionListOracleResult(t, payload, test.expected, namesByTXID)
		})
	}

	for _, test := range []struct {
		name      string
		pageValue string
		pageSize  string
	}{
		{name: "positive infinity", pageValue: "Infinity", pageSize: "2"},
		{name: "overflowing exponent", pageValue: "1e309", pageSize: "2"},
		{name: "infinity precedes fractional size", pageValue: "Infinity", pageSize: "1.5"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := fmt.Sprintf(
				`{"method":"transaction_list","params":{"account_id":"a2","page":%s,"page_size":%s}}`,
				test.pageValue, test.pageSize,
			)
			response := performRequest(server, http.MethodPost, "/", body, nil)
			payload := decodeTransactionListLegacySpecialResponse(t, response)
			errorObject := payload["error"].(map[string]any)
			data := errorObject["data"].(map[string]any)
			if errorObject["code"] != json.Number("-32500") ||
				errorObject["message"] != "no such column: inf" ||
				data["name"] != "OperationalError" {
				t.Fatalf("nonfinite pagination error = %#v", errorObject)
			}
			kwargs := data["kwargs"].(map[string]any)
			if page, ok := kwargs["page"].(float64); !ok || !math.IsInf(page, 1) {
				t.Fatalf("nonfinite error kwargs = %#v", kwargs)
			}
		})
	}

	underflowError := performRequest(
		server, http.MethodPost, "/",
		`{"method":"transaction_list","params":{"wallet_id":"missing","page":1e-4000}}`, nil,
	)
	underflowPayload := decodeResponse(t, underflowError)
	underflowData := underflowPayload["error"].(map[string]any)["data"].(map[string]any)
	underflowKwargs := underflowData["kwargs"].(map[string]any)
	if underflowKwargs["page"] != json.Number("0.0") {
		t.Fatalf("underflow error kwargs = %#v, want canonical 0.0", underflowKwargs)
	}
	quotedInfinity := performRequest(
		server, http.MethodPost, "/",
		`{"method":"transaction_list","params":{"account_id":"a2","page":"Infinity"}}`, nil,
	)
	quotedError := decodeResponse(t, quotedInfinity)["error"].(map[string]any)
	quotedData := quotedError["data"].(map[string]any)
	if quotedError["message"] != "'>' not supported between instances of 'str' and 'int'" ||
		quotedData["name"] != "TypeError" {
		t.Fatalf("quoted special-looking page error = %#v", quotedError)
	}

	hugeWithInfinity := fmt.Sprintf(
		`{"method":"transaction_list","params":{"account_id":"a2","page":1%s,"page_size":Infinity}}`,
		strings.Repeat("0", 400),
	)
	hugeResponse := performRequest(server, http.MethodPost, "/", hugeWithInfinity, nil)
	hugePayload := decodeTransactionListLegacySpecialResponse(t, hugeResponse)
	hugeError := hugePayload["error"].(map[string]any)
	hugeData := hugeError["data"].(map[string]any)
	if hugeError["message"] != "int too large to convert to float" ||
		hugeData["name"] != "OverflowError" {
		t.Fatalf("mixed huge/infinity error = %#v", hugeError)
	}
}

func decodeTransactionListLegacySpecialResponse(
	t *testing.T, response *httptest.ResponseRecorder,
) map[string]any {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("HTTP status = %d, body %s", response.Code, response.Body.String())
	}
	converted, tokens, err := quoteLegacySpecialJSONFloats(response.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(converted))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		t.Fatalf("decode legacy-special response: %v\n%s", err, response.Body.String())
	}
	return restoreLegacySpecialJSONFloats(payload, tokens).(map[string]any)
}

func TestTransactionListPaginationNumericContract(t *testing.T) {
	pagination, err := transactionListPaginationParameters(map[string]any{
		"page": json.Number("9007199254740993"), "page_size": json.Number("2.0"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if pagination.offset != 18014398509481984 ||
		pagination.wirePage != json.Number("9007199254740993") ||
		pagination.wirePageSize != json.Number("2.0") {
		t.Fatalf("mixed pagination = %+v", pagination)
	}

	maxSize, err := transactionListNormalizedPageNumber(
		map[string]any{"page_size": json.Number("9223372036854775807")},
		"page_size", walletpkg.TransactionHistoryDefaultPageSize,
	)
	if err != nil {
		t.Fatal(err)
	}
	oneSize, err := transactionListNormalizedPageNumber(
		map[string]any{"page_size": json.Number("1")},
		"page_size", walletpkg.TransactionHistoryDefaultPageSize,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		totalItems int64
		pageSize   transactionListPageNumber
		want       json.Number
	}{
		{name: "max over max rounds to two", totalItems: math.MaxInt64, pageSize: maxSize, want: "2"},
		{name: "max over one rounds above int64", totalItems: math.MaxInt64, pageSize: oneSize, want: "9223372036854775808"},
		{name: "empty max page rounds to one", totalItems: 0, pageSize: maxSize, want: "1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := transactionListPythonTotalPages(test.totalItems, test.pageSize); got != test.want {
				t.Fatalf("total pages = %s, want %s", got, test.want)
			}
		})
	}
}

func assertTransactionListOracleContract(t *testing.T, cases []transactionListOracleCase) {
	t.Helper()
	wantNames := []string{
		"defaults", "explicit nulls", "middle page", "tail page", "beyond page",
		"zero defaults", "negative clamps", "boolean one", "empty strings default",
		"empty containers default", "second default-size page", "falsy account selects wallet",
		"selected wallet uses default ledger", "empty totals retain page",
		"selected account uses account ledger", "empty account selects wallet", "legacy positional",
		"missing wallet", "empty wallet", "missing account", "foreign account", "string page error",
		"string page size error", "list page error", "legacy positional page error",
		"integral float pagination", "fractional page integral offset",
		"no wallets wallet wide", "no wallets account", "empty default precedes pagination",
		"empty default account error", "empty selected assertion",
		"empty selected pagination error", "object wallet id", "object account id",
		"empty object account selects wallet", "missing wallet precedes invalid page",
		"missing account precedes invalid page",
		"fractional offset sqlite error", "fractional size sqlite error",
		"largest sqlite offset", "oversized sqlite offset", "largest sqlite page size",
		"oversized sqlite page size", "mixed huge float overflow",
		"mixed fractional overflow precedence", "mixed subtraction before float conversion",
	}
	for index, fixture := range cases {
		if fixture.Name != wantNames[index] {
			t.Fatalf("oracle case %d = %q, want %q", index, fixture.Name, wantNames[index])
		}
		if fixture.Error != nil {
			wantCalls := 0
			if fixture.Name == "empty selected assertion" ||
				fixture.Name == "fractional offset sqlite error" ||
				fixture.Name == "fractional size sqlite error" ||
				fixture.Name == "oversized sqlite offset" ||
				fixture.Name == "oversized sqlite page size" {
				wantCalls = 1
			}
			if fixture.Error.Code != -32500 || fixture.Error.Data.Command != "transaction_list" ||
				len(fixture.Error.Data.Traceback) == 0 || len(fixture.Calls) != wantCalls {
				t.Fatalf("oracle error case %q = %+v, calls %+v", fixture.Name, fixture.Error, fixture.Calls)
			}
			if wantCalls == 1 && fixture.Calls[0].Method != "get_transaction_history" {
				t.Fatalf("oracle error call for %q = %+v", fixture.Name, fixture.Calls)
			}
			continue
		}
		if len(fixture.Result) == 0 || len(fixture.Calls) != 2 {
			t.Fatalf("oracle success case %q result/calls = %s / %+v", fixture.Name, fixture.Result, fixture.Calls)
		}
		first, second := fixture.Calls[0], fixture.Calls[1]
		if first.Method != "get_transaction_history" || second.Method != "get_transaction_history_count" ||
			!first.ReadOnly || !second.ReadOnly || first.Ledger != second.Ledger ||
			first.Wallet != second.Wallet || !reflect.DeepEqual(first.Accounts, second.Accounts) ||
			first.Offset != second.Offset || first.Limit != second.Limit {
			t.Fatalf("oracle call sequence for %q = %+v", fixture.Name, fixture.Calls)
		}
	}

	byName := make(map[string]transactionListOracleCase, len(cases))
	for _, fixture := range cases {
		byName[fixture.Name] = fixture
	}
	walletWide := byName["selected wallet uses default ledger"].Calls[0]
	if walletWide.Ledger != "ledger-a" || walletWide.Wallet != "wallet-b" ||
		!reflect.DeepEqual(walletWide.Accounts, []string{"b1", "b2"}) {
		t.Fatalf("wallet-wide selected-ledger contract = %+v", walletWide)
	}
	account := byName["selected account uses account ledger"].Calls[0]
	if account.Ledger != "ledger-b" || account.Wallet != "wallet-b" ||
		!reflect.DeepEqual(account.Accounts, []string{"b2"}) {
		t.Fatalf("account selected-ledger contract = %+v", account)
	}
}

func assertTransactionListOracleResult(
	t *testing.T, payload map[string]any, expectedRaw json.RawMessage, namesByTXID map[string]string,
) {
	t.Helper()
	if payload["jsonrpc"] != "2.0" {
		t.Fatalf("JSON-RPC version = %#v", payload["jsonrpc"])
	}
	if _, exists := payload["id"]; exists {
		t.Fatal("legacy response unexpectedly includes id")
	}
	if _, exists := payload["error"]; exists {
		t.Fatalf("unexpected transaction_list error = %#v", payload["error"])
	}
	result, ok := payload["result"].(map[string]any)
	if !ok {
		t.Fatalf("transaction_list result = %#v", payload["result"])
	}
	items, ok := result["items"].([]any)
	if !ok {
		t.Fatalf("transaction_list items = %#v, want list", result["items"])
	}
	names := make([]any, len(items))
	for index, itemValue := range items {
		item, ok := itemValue.(map[string]any)
		if !ok {
			t.Fatalf("transaction_list item %d = %#v", index, itemValue)
		}
		assertTransactionListHistoryShape(t, item)
		txid, ok := item["txid"].(string)
		if !ok {
			t.Fatalf("transaction_list item %d txid = %#v", index, item["txid"])
		}
		name, exists := namesByTXID[txid]
		if !exists {
			t.Fatalf("transaction_list item %d has unknown txid %q", index, txid)
		}
		names[index] = name
	}
	result["items"] = names
	expected := decodeTransactionListOracleJSON(t, expectedRaw)
	if !reflect.DeepEqual(result, expected) {
		t.Fatalf("transaction_list result = %#v, want %#v", result, expected)
	}
}

func assertTransactionListHistoryShape(t *testing.T, item map[string]any) {
	t.Helper()
	wantKeys := []string{
		"abandon_info", "claim_info", "confirmations", "date", "fee", "purchase_info",
		"support_info", "timestamp", "txid", "update_info", "value",
	}
	gotKeys := make([]string, 0, len(item))
	for key := range item {
		gotKeys = append(gotKeys, key)
	}
	slices.Sort(gotKeys)
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("transaction history keys = %v, want %v", gotKeys, wantKeys)
	}
	for _, name := range []string{"abandon_info", "claim_info", "purchase_info", "support_info", "update_info"} {
		if _, ok := item[name].([]any); !ok {
			t.Fatalf("transaction history %s = %#v, want list", name, item[name])
		}
	}
}

func assertTransactionListOracleError(
	t *testing.T, payload map[string]any, expected *transactionListOracleError,
) {
	t.Helper()
	if payload["jsonrpc"] != "2.0" {
		t.Fatalf("JSON-RPC version = %#v", payload["jsonrpc"])
	}
	if _, exists := payload["id"]; exists {
		t.Fatal("legacy error unexpectedly includes id")
	}
	if _, exists := payload["result"]; exists {
		t.Fatalf("error response includes result %#v", payload["result"])
	}
	errorObject, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("transaction_list error = %#v", payload["error"])
	}
	if fmt.Sprint(errorObject["code"]) != fmt.Sprint(expected.Code) ||
		errorObject["message"] != expected.Message {
		t.Fatalf("transaction_list error = %#v, want code %d message %q",
			errorObject, expected.Code, expected.Message)
	}
	data, ok := errorObject["data"].(map[string]any)
	if !ok {
		t.Fatalf("transaction_list error data = %#v", errorObject["data"])
	}
	if len(data) != 5 || data["name"] != expected.Data.Name ||
		data["command"] != expected.Data.Command {
		t.Fatalf("transaction_list error data = %#v, want name %q command %q",
			data, expected.Data.Name, expected.Data.Command)
	}
	traceback, ok := data["traceback"].([]any)
	if !ok || len(traceback) == 0 {
		t.Fatalf("transaction_list traceback = %#v", data["traceback"])
	}
	if !reflect.DeepEqual(data["args"], decodeTransactionListOracleJSON(t, expected.Data.Args)) ||
		!reflect.DeepEqual(data["kwargs"], decodeTransactionListOracleJSON(t, expected.Data.Kwargs)) {
		t.Fatalf("transaction_list error args/kwargs = %#v / %#v, want %s / %s",
			data["args"], data["kwargs"], expected.Data.Args, expected.Data.Kwargs)
	}
}

func assertTransactionListProviderUnavailable(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("provider-unavailable HTTP status = %d, body %s", response.Code, response.Body.String())
	}
	payload := decodeResponse(t, response)
	want := decodeTransactionListOracleJSON(t, json.RawMessage(`{
		"jsonrpc":"2.0",
		"error":{
			"code":-32500,
			"data":{
				"args":[],
				"command":"transaction_list",
				"kwargs":{},
				"name":"ComponentsNotStartedError",
				"traceback":["<runtime>"]
			},
			"message":"the following required components have not yet started: [\"wallet\"]"
		}
	}`))
	data := payload["error"].(map[string]any)["data"].(map[string]any)
	traceback, ok := data["traceback"].([]any)
	if !ok || len(traceback) == 0 {
		t.Fatalf("provider-unavailable traceback = %#v", data["traceback"])
	}
	data["traceback"] = []any{"<runtime>"}
	if !reflect.DeepEqual(payload, want) {
		t.Fatalf("provider-unavailable payload = %#v, want %#v", payload, want)
	}
}

func decodeTransactionListOracleJSON(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode transaction list oracle JSON %s: %v", raw, err)
	}
	return value
}

func transactionListOracleManager(t *testing.T) (*walletpkg.WalletManager, map[string]string) {
	t.Helper()
	ctx := context.Background()
	manager := walletpkg.NewWalletManager()
	root := t.TempDir()
	ledgerA := transactionListOracleLedger(t, manager, keys.MainNet, root)
	ledgerB := transactionListOracleLedger(t, manager, keys.TestNet, root)

	a1 := transactionListOracleAccount(t, keys.MainNet, 1, "a1")
	a2 := transactionListOracleAccount(t, keys.MainNet, 2, "a2")
	b1 := transactionListOracleAccount(t, keys.TestNet, 1, "b1")
	b2 := transactionListOracleAccount(t, keys.TestNet, 2, "b2")
	walletA := walletpkg.NewWallet(
		walletpkg.WithWalletName("wallet-a"), walletpkg.WithWalletAccounts([]*walletpkg.Account{a1, a2}),
	)
	walletB := walletpkg.NewWallet(
		walletpkg.WithWalletName("wallet-b"), walletpkg.WithWalletAccounts([]*walletpkg.Account{b1, b2}),
	)
	manager.Wallets = []*walletpkg.Wallet{walletA, walletB}
	for _, account := range []*walletpkg.Account{a1, a2} {
		if err := manager.RegisterAccount(keys.MainNet.ID(), account); err != nil {
			t.Fatal(err)
		}
	}
	for _, account := range []*walletpkg.Account{b1, b2} {
		if err := manager.RegisterAccount(keys.TestNet.ID(), account); err != nil {
			t.Fatal(err)
		}
	}

	names := transactionListOracleSeed(t, ctx, ledgerA, a2, "owned-a2", 1)
	for txid, name := range transactionListOracleSeed(t, ctx, ledgerB, b2, "owned-b2", 2) {
		names[txid] = name
	}
	return manager, names
}

func transactionListOracleManagerStates(
	manager *walletpkg.WalletManager,
) map[string]*walletpkg.WalletManager {
	return map[string]*walletpkg.WalletManager{
		"": manager,
		"no_wallets": {
			Ledgers: manager.Ledgers,
		},
		"empty_default": {
			Wallets: []*walletpkg.Wallet{
				walletpkg.NewWallet(walletpkg.WithWalletName("wallet-a")),
				manager.Wallets[1],
			},
			Ledgers: manager.Ledgers,
		},
		"empty_selected": {
			Wallets: []*walletpkg.Wallet{
				manager.Wallets[0],
				walletpkg.NewWallet(walletpkg.WithWalletName("wallet-b")),
			},
			Ledgers: manager.Ledgers,
		},
	}
}

func transactionListOracleLedger(
	t *testing.T, manager *walletpkg.WalletManager, network keys.Network, root string,
) *walletpkg.Ledger {
	t.Helper()
	ledger, err := manager.GetOrCreateLedger(network.ID(), walletpkg.LedgerConfig{"data_path": root})
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
			t.Errorf("close %s transaction-list database: %v", network.ID(), err)
		}
	})
	return ledger
}

func transactionListOracleAccount(
	t *testing.T, network keys.Network, seedByte byte, accountID string,
) *walletpkg.Account {
	t.Helper()
	privateKey, err := keys.PrivateKeyFromSeed(network, bytes.Repeat([]byte{seedByte}, 32))
	if err != nil {
		t.Fatal(err)
	}
	account, err := walletpkg.NewAccount(network, walletpkg.NewObject(
		walletpkg.Member{Key: "name", Value: accountID},
		walletpkg.Member{Key: "public_key", Value: privateKey.PublicKey().ExtendedKeyString()},
		walletpkg.Member{Key: "address_generator", Value: walletpkg.NewObject(
			walletpkg.Member{Key: "name", Value: walletpkg.SingleAddressGenerator},
		)},
		walletpkg.Member{Key: "modified_on", Value: 1},
	))
	if err != nil {
		t.Fatal(err)
	}
	account.ID = accountID
	return account
}

func transactionListOracleSeed(
	t *testing.T, ctx context.Context, ledger *walletpkg.Ledger, account *walletpkg.Account,
	address string, tag byte,
) map[string]string {
	t.Helper()
	accountKey := account.PublicKey.Address()
	if err := ledger.Database.AddKeys(ctx, accountKey, []ledgerdb.AddressKey{{
		Address: address, PublicKey: []byte{tag}, ChainCode: []byte{tag + 1},
	}}); err != nil {
		t.Fatal(err)
	}
	rows := make([]ledgerdb.TransactionIORow, 5)
	names := make(map[string]string, len(rows))
	for index := range rows {
		nonce := uint32(tag)*1_000 + uint32(index)
		transaction := walletpkg.NewTransaction()
		transaction.LockTime = nonce
		transaction.AddInputs([]walletpkg.TransactionInput{{
			PreviousIndex: math.MaxUint32, Sequence: math.MaxUint32,
			Coinbase: []byte{tag, byte(index)},
		}})
		transaction.AddOutputs([]walletpkg.TransactionOutput{
			walletpkg.NewPayPubKeyHashOutput(uint64(index+1), bytes.Repeat([]byte{tag + byte(index)}, 20)),
		})
		if err := transaction.RebuildDerived(); err != nil {
			t.Fatal(err)
		}
		output := &transaction.Outputs[0]
		rows[index] = ledgerdb.TransactionIORow{
			Transaction: ledgerdb.TransactionRow{
				TXID: transaction.ID, Raw: append([]byte(nil), transaction.Raw...),
				Height: 0, Position: int64(index),
			},
			Outputs: []ledgerdb.TransactionOutputRow{{
				TXOID: output.ID(), Address: &address, Position: 0, Amount: int64(output.Amount),
				Script:  append([]byte(nil), output.Script.Source...),
				TXOType: walletpkg.TransactionOutputTypeOther,
			}},
		}
		names[transaction.ID] = fmt.Sprintf("r%d", len(rows)-1-index)
	}
	if err := ledger.Database.SaveTransactionIOBatch(ctx, rows, address, ""); err != nil {
		t.Fatal(err)
	}
	return names
}

func runTransactionListOracle(t *testing.T) transactionListOracleResponse {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate transaction list oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot := os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	for relative := range transactionListOraclePinnedSources {
		path := filepath.Join(sdkRoot, filepath.FromSlash(relative))
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned local transaction list source is unavailable: %s", path)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(daemonRoot, "compat", "transaction_list_oracle.py")
	if _, err := os.Stat(script); errors.Is(err, os.ErrNotExist) {
		t.Skipf("transaction list oracle script is unavailable: %s", script)
	} else if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("python3", script, "--sdk-root", sdkRoot)
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1", "TZ=UTC")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("transaction list oracle failed: %v\n%s", err, output)
	}
	var oracle transactionListOracleResponse
	if err := json.Unmarshal(output, &oracle); err != nil {
		t.Fatalf("decode transaction list oracle: %v\n%s", err, output)
	}
	return oracle
}
