package rpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	walletpkg "lbry/daemon/wallet"
)

const (
	resolveOracleCommit  = "e7666f489418e96b6d2104974e93915b539235c5"
	resolveOracleVersion = "0.113.0"
)

var resolveOracleSources = map[string]string{
	"lbry/__init__.py":             "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
	"lbry/error/__init__.py":       "4a279d245ffc8c9e3a966625f800340d7325183ba900b727d39d4074f196a795",
	"lbry/extras/daemon/daemon.py": "15f9b43c5fc0f21a6361320e82768ef2632746c3ad17c252eab9e8d5e0291be0",
	"lbry/schema/url.py":           "8792ddfef84331c8b2e56b441b738565b6018c085944efa6179dbb03df97f6cd",
	"lbry/wallet/ledger.py":        "5b5e3deacd5a87ec69a91b42c7aa6460605146724205ff0b387402dd193be2a5",
	"lbry/wallet/manager.py":       "042e3410753f5f903871d87e65b2b0cdbaf8d82cd66ae4e677e0bdd47109c7db",
	"lbry/wallet/network.py":       "cfe6661af4c2028a542582e2c7fffc8c97dce93ca0f619a752a4a5af389b3e6b",
	"lbry/wallet/wallet.py":        "45a26975bd59d11ccad332e16447807b6f67e1d35729d4c4a509b3ab7eaa9010",
}

var resolveOracleMethods = map[string]string{
	"Daemon.jsonrpc_resolve":              "92a5bdbe7b286bc70e70805fc2c58b7a792cba3754eb4300aa2d789554e63af1",
	"Daemon.ledger":                       "c0aad64201976cc6d3b4ae3fa49fe9434093c578706b84f45b8cc687c7276f46",
	"Daemon.resolve":                      "2db0511da5825f1f420c699b45555bde6b8fb968e4afa2f81350aa5f6c45853d",
	"Ledger.resolve":                      "bb0af2c7bbae73d82424c27bcfa9eab458eac18891267336d9e83ac3cd57ce23",
	"Network.resolve":                     "8aa40f10cb04442c7c87133bcaf4a401512d3fa8d9cc60af210dbaa7682a4abb",
	"URL.has_channel":                     "d97d6b07b1da5692603a02ca5d223eb6f79124012c6874fa4d4d643b21f398a1",
	"URL.has_stream":                      "cb59fec78ae4cc6a47636a81fd11c4e60a805e35f2eaf1d5472bd5196199f509",
	"URL.has_stream_in_channel":           "17b802114df2eae33dfaff2c608fcadd08e30a4fad9191ce2b2b3dbf9c433b55",
	"URL.parse":                           "9c7da8c30083337ecbfbb64fced493b41f3d2c7ef50355cbaed7100eb9c9c1b8",
	"Wallet.default_account":              "76e84d5c63726f3c268e161ee2ef54e0573ab02a4aab04d9b7c6dae0fc95961e",
	"WalletManager.default_account":       "6b5ae4ee1fd368d8b3bb05e3a8a3362a0f958f4e5385787958ff83fdb855e731",
	"WalletManager.default_wallet":        "b985d6bbf6126a982f1f0084fc6872592cff7717f50b59fbe3a745f498c8de48",
	"WalletManager.get_wallet_or_default": "a78f3e4003c8bc2c25c95681532cb166eb3685a611aecd6024893fa6c94e8537",
	"WalletManager.get_wallet_or_error":   "ac6310a5232801623f12f4be0909a0e64a595a94330465f3c825b9ac34c51eec",
}

type resolveOracleResponse struct {
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
	Cases []resolveOracleCase `json:"cases"`
}

type resolveOracleCase struct {
	Name   string              `json:"name"`
	Mode   string              `json:"mode"`
	Params map[string]any      `json:"params"`
	Result map[string]any      `json:"result"`
	Error  *resolveOracleError `json:"error"`
	Calls  struct {
		Events    []map[string]any       `json:"events"`
		Inflate   []resolveOracleInflate `json:"inflate"`
		Retriable []resolveOracleRetry   `json:"retriable"`
		RPC       []resolveOracleRPC     `json:"rpc"`
		Signature []resolveOracleSign    `json:"signature"`
		Storage   []resolveOracleStorage `json:"storage"`
	} `json:"calls"`
}

type resolveOracleError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type resolveOracleInflate struct {
	Ledger                 string   `json:"ledger"`
	Accounts               []string `json:"accounts"`
	EncodedURLs            []string `json:"encoded_urls"`
	IncludePurchaseReceipt any      `json:"include_purchase_receipt"`
	IncludeIsMyOutput      any      `json:"include_is_my_output"`
	IncludeSentSupports    any      `json:"include_sent_supports"`
	IncludeSentTips        any      `json:"include_sent_tips"`
	IncludeReceivedTips    any      `json:"include_received_tips"`
	State                  string   `json:"state"`
	Error                  string   `json:"error"`
	ResultCount            int      `json:"result_count"`
}

type resolveOracleRetry struct {
	Ledger   string         `json:"ledger"`
	Function string         `json:"function"`
	Args     [][]string     `json:"args"`
	Kwargs   map[string]any `json:"kwargs"`
}

type resolveOracleRPC struct {
	Ledger     string   `json:"ledger"`
	Method     string   `json:"method"`
	Params     []string `json:"params"`
	Restricted bool     `json:"restricted"`
	Session    any      `json:"session"`
}

type resolveOracleSign struct {
	Output  string `json:"output"`
	Channel string `json:"channel"`
	Ledger  string `json:"ledger"`
	Valid   *bool  `json:"valid"`
}

type resolveOracleStorage struct {
	Ledger  string   `json:"ledger"`
	Outputs []string `json:"outputs"`
}

func TestResolveURLParserMatchesPinnedCorpus(t *testing.T) {
	const claimID = "63f2da17b0d90042c559cc73b6b17f853945c43e"
	valid := []struct {
		url             string
		streamInChannel bool
	}{
		{"test", false},
		{"test*1", false},
		{"test$1", false},
		{"test#" + claimID, false},
		{"test:" + claimID, false},
		{"@test", false},
		{"@test$1", false},
		{"@test#" + claimID, false},
		{"@test:" + claimID, false},
		{"lbry://@test/stuff", true},
		{"lbry://@test$1/stuff", true},
		{"lbry://@test#" + claimID + "/stuff", true},
		{"lbry://@test:" + claimID + "/stuff", true},
		{"@test:1/stuff#2", true},
		{"\ud799", false},
		{"\ue000", false},
		{"\ufffd", false},
		// Python's regex `$` anchor ignores one final LF.
		{"test\n", false},
		{"lbry://test\n", false},
		{"lbry://@test/stuff\n", true},
	}
	for index, fixture := range valid {
		fixture := fixture
		t.Run(fmt.Sprintf("valid_%02d", index), func(t *testing.T) {
			parsed, err := walletpkg.ParseLBRYURL(fixture.url)
			if err != nil || parsed.HasStreamInChannel != fixture.streamInChannel {
				t.Fatalf("ParseLBRYURL(%q) = %+v, %v", fixture.url, parsed, err)
			}
			streamInChannel, ok := parseResolveURL(fixture.url)
			if !ok || streamInChannel != fixture.streamInChannel {
				t.Fatalf("parseResolveURL(%q) = %t, %t", fixture.url, streamInChannel, ok)
			}
		})
	}

	invalid := []string{
		"lbry://",
		"lbry://\x00",
		"lbry://\x08",
		"lbry://\x0b",
		"lbry://\x0c",
		"lbry://\x0e",
		"lbry://\x1f",
		"lbry://\xed\xa0\x80", // UTF-8 encoding of the invalid U+D800 surrogate.
		"lbry://\xed\xbf\xbf", // UTF-8 encoding of the invalid U+DFFF surrogate.
		"lbry://\xed\xbf\xbe", // UTF-8 encoding of the invalid U+DFFE surrogate.
		"lbry://\uffff",
		"lbry://;",
		"lbry://no\ttab",
		"lbry://no space",
		"lbry://no\rcr",
		"lbry://no\new\nline",
		"lbry://\"",
		"lbry://\\",
		"lbry:///",
		"lbry://<",
		"lbry://>",
		"lbry://{",
		"lbry://}",
		"lbry://[",
		"lbry://]",
		"lbry://%",
		"lbry://|",
		"lbry://^",
		"lbry://~",
		"lbry://`",
		"lbry://test:3$1",
		"lbry://test$1:1",
		"lbry://test#x",
		"lbry://test#x/page",
		"lbry://test$",
		"lbry://test#",
		"lbry://test:",
		"lbry://test$x",
		"lbry://test:x",
		"lbry://@test@",
		"lbry://@test:",
		"lbry://test@",
		"lbry://tes@t",
		"lbry://test:1#" + claimID,
		"lbry://test$0",
		"lbry://test/path",
		"lbry://test:1:1:1",
		"whatever/lbry://test",
		"lbry://lbry://test",
		"lbry://@/what",
		"lbry://abc:0x123",
		"lbry://abc:0x123/page",
		"lbry://@test1#ABCDEF/fakepath",
		"lbry://@test1$1/fakepath?arg1&arg2&arg3",
		"test\r\n",
		"test\n\n",
	}
	for index, url := range invalid {
		url := url
		t.Run(fmt.Sprintf("invalid_%02d", index), func(t *testing.T) {
			_, err := walletpkg.ParseLBRYURL(url)
			if err == nil || err.Error() != "Invalid LBRY URL" ||
				!errors.Is(err, walletpkg.ErrInvalidLBRYURL) || recoveredErrorName(err) != "ValueError" {
				t.Fatalf("ParseLBRYURL(%q) error = %T %v", url, err, err)
			}
			streamInChannel, ok := parseResolveURL(url)
			if ok || streamInChannel {
				t.Fatalf("parseResolveURL(%q) = %t, %t", url, streamInChannel, ok)
			}
		})
	}
}

func TestResolveGoPreprocessingMatchesPinnedOracle(t *testing.T) {
	oracle := runResolveOracle(t)
	cases := make(map[string]resolveOracleCase, len(oracle.Cases))
	for _, fixture := range oracle.Cases {
		cases[fixture.Name] = fixture
	}

	scalar := cases["scalar string default wallet"]
	values, err := resolveIterableValues(scalar.Params["urls"])
	if err != nil || !reflect.DeepEqual(values, []any{"lbry://one"}) {
		t.Fatalf("scalar iterable = %#v, %v", values, err)
	}
	if got := resolveStringSlice(t, values); !reflect.DeepEqual(got, scalar.Calls.RPC[0].Params) {
		t.Fatalf("scalar preprocessing = %v, oracle %v", got, scalar.Calls.RPC[0].Params)
	}

	listed := cases["list deduplicates and filters invalid"]
	values, err = resolveIterableValues(listed.Params["urls"])
	if err != nil || !reflect.DeepEqual(values, listed.Params["urls"]) {
		t.Fatalf("list iterable = %#v, want %#v, error %v", values, listed.Params["urls"], err)
	}
	empty := cases["empty list"]
	values, err = resolveIterableValues(empty.Params["urls"])
	if err != nil || len(values) != 0 {
		t.Fatalf("empty iterable = %#v, %v", values, err)
	}

	mapping := cases["mapping iterates keys"]
	values, err = resolveIterableValues(mapping.Params["urls"])
	if err != nil || !sameStrings(resolveStringSlice(t, values), mapping.Calls.RPC[0].Params) {
		t.Fatalf("mapping iterable = %#v, oracle %v, error %v", values, mapping.Calls.RPC[0].Params, err)
	}

	for _, name := range []string{"null is not iterable", "number is not iterable"} {
		fixture := cases[name]
		value := fixture.Params["urls"]
		if name == "number is not iterable" {
			// The server's Decoder.UseNumber preserves Python's int/float distinction;
			// decoding the oracle envelope with json.Unmarshal does not.
			value = json.Number("7")
		}
		values, err = resolveIterableValues(value)
		if values != nil || err == nil || fixture.Error == nil ||
			recoveredErrorName(err) != fixture.Error.Type || err.Error() != fixture.Error.Message {
			t.Fatalf("%s iterable error = %#v, %T %v; oracle %+v",
				name, values, err, err, fixture.Error)
		}
	}

	nullEntry := cases["null list entry aborts validation"]
	values, err = resolveIterableValues(nullEntry.Params["urls"])
	if err != nil || len(values) != 2 {
		t.Fatalf("null-entry iterable = %#v, %v", values, err)
	}
	typeMessage := fmt.Sprintf(
		"expected string or bytes-like object, got '%s'", pythonTypeName(values[1]),
	)
	if nullEntry.Error == nil || nullEntry.Error.Type != "TypeError" ||
		typeMessage != nullEntry.Error.Message {
		t.Fatalf("null-entry type error = %q, oracle %+v", typeMessage, nullEntry.Error)
	}

	option := cases["documented server option fails before retry"]
	normalized := normalizedRPCParams{
		named: option.Params, orderedKwargs: []string{"urls", "new_sdk_server"},
	}
	unexpected := firstUnexpectedResolveParameter(normalized)
	wantSuffix := "unexpected keyword argument " + pythonRepr(unexpected)
	if unexpected != "new_sdk_server" || option.Error == nil ||
		!strings.HasSuffix(option.Error.Message, wantSuffix) {
		t.Fatalf("unexpected resolve parameter = %q, suffix %q, oracle %+v",
			unexpected, wantSuffix, option.Error)
	}

	includes := cases["all include flags bind at inflation"]
	if unexpected := firstUnexpectedResolveParameter(normalizedRPCParams{named: includes.Params}); unexpected != "" {
		t.Fatalf("include parameter unexpectedly rejected: %q", unexpected)
	}
	ordered := normalizedRPCParams{
		named:         map[string]any{"urls": "lbry://one", "alpha": 1, "zeta": 2},
		orderedKwargs: []string{"urls", "zeta", "alpha"},
	}
	if unexpected := firstUnexpectedResolveParameter(ordered); unexpected != "zeta" {
		t.Fatalf("ordered unexpected parameter = %q, want zeta", unexpected)
	}
	ordered.orderedKwargs = nil
	if unexpected := firstUnexpectedResolveParameter(ordered); unexpected != "alpha" {
		t.Fatalf("deterministic unexpected parameter = %q, want alpha", unexpected)
	}

	// Iterable wrapping and URL parsing both preserve the spelling sent to the Hub.
	finalLF := "lbry://@test/stuff\n"
	values, err = resolveIterableValues(finalLF)
	if err != nil || len(values) != 1 || values[0] != finalLF {
		t.Fatalf("final-LF iterable = %#v, %v", values, err)
	}
	streamInChannel, valid := parseResolveURL(values[0].(string))
	if !valid || !streamInChannel || values[0].(string) != finalLF {
		t.Fatalf("final-LF preprocessing = %#v, %t, %t", values, streamInChannel, valid)
	}
}

func TestResolvePinnedOracle(t *testing.T) {
	oracle := runResolveOracle(t)
	if oracle.Reference.Commit != resolveOracleCommit ||
		oracle.Reference.Version != resolveOracleVersion ||
		!reflect.DeepEqual(oracle.Reference.SourceSHA256, resolveOracleSources) ||
		!reflect.DeepEqual(oracle.Reference.MethodSHA256, resolveOracleMethods) {
		t.Fatalf("resolve oracle reference = %+v", oracle.Reference)
	}
	if oracle.Metadata.PythonVersion == "" || !oracle.Metadata.ExtractedMethodsExecuted ||
		oracle.Metadata.ExternalNetworkUsed || oracle.Metadata.CaseCount != 28 ||
		len(oracle.Cases) != 28 {
		t.Fatalf("resolve oracle metadata = %+v, cases %d", oracle.Metadata, len(oracle.Cases))
	}

	cases := make(map[string]resolveOracleCase, len(oracle.Cases))
	for _, fixture := range oracle.Cases {
		if _, duplicate := cases[fixture.Name]; duplicate {
			t.Fatalf("duplicate resolve oracle fixture %q", fixture.Name)
		}
		cases[fixture.Name] = fixture
	}
	assertResolvePreprocessing(t, cases)
	assertResolveWalletAndIncludes(t, cases)
	assertResolveBatching(t, cases)
	assertResolveMapping(t, cases)
	assertResolvePersistence(t, cases)
}

func assertResolvePreprocessing(t *testing.T, cases map[string]resolveOracleCase) {
	t.Helper()
	scalar := cases["scalar string default wallet"]
	assertResolveRPC(t, scalar, []string{"lbry://one"})
	assertResolveOutput(t, scalar, "lbry://one", "one", nil)

	deduplicated := cases["list deduplicates and filters invalid"]
	if deduplicated.Error != nil || len(deduplicated.Calls.RPC) != 1 ||
		!sameStrings(deduplicated.Calls.RPC[0].Params, []string{"lbry://one", "lbry://two"}) {
		t.Fatalf("deduplicated resolve = %+v", deduplicated)
	}
	invalid := resolveResultMap(t, deduplicated, "not a url")
	if invalid["error"] != "not a url is not a valid url" {
		t.Fatalf("invalid URL result = %#v", invalid)
	}

	empty := cases["empty list"]
	if empty.Error != nil || len(empty.Result) != 0 || len(empty.Calls.RPC) != 0 ||
		len(empty.Calls.Inflate) != 0 || len(empty.Calls.Retriable) != 0 {
		t.Fatalf("empty resolve = %+v", empty)
	}
	mapping := cases["mapping iterates keys"]
	if mapping.Error != nil || len(mapping.Calls.RPC) != 1 ||
		!sameStrings(mapping.Calls.RPC[0].Params, []string{"lbry://one", "lbry://two"}) {
		t.Fatalf("mapping URL form = %+v", mapping)
	}

	assertResolveError(t, cases["null is not iterable"], "TypeError", "'NoneType' object is not iterable", 0)
	assertResolveError(t, cases["number is not iterable"], "TypeError", "'int' object is not iterable", 0)
	assertResolveError(t, cases["null list entry aborts validation"], "TypeError",
		"expected string or bytes-like object, got 'NoneType'", 0)
	assertResolveError(t, cases["missing wallet precedes urls"], "WalletNotLoadedError",
		"Wallet missing is not loaded.", 0)
	if got := resolveEventNames(cases["null list entry aborts validation"]); !reflect.DeepEqual(got,
		[]string{"url_parse", "url_parse"}) {
		t.Fatalf("null entry failure order = %v", got)
	}
	if got := resolveEventNames(cases["missing wallet precedes urls"]); len(got) != 0 {
		t.Fatalf("missing wallet failure order = %v", got)
	}
}

func assertResolveWalletAndIncludes(t *testing.T, cases map[string]resolveOracleCase) {
	t.Helper()
	selected := onlyResolveInflate(t, cases["selected wallet uses default ledger"])
	if selected.Ledger != "default-ledger" || !reflect.DeepEqual(selected.Accounts,
		[]string{"other-account-0", "other-account-1"}) {
		t.Fatalf("selected wallet inflation = %+v", selected)
	}
	if call := cases["selected wallet uses default ledger"].Calls.RPC[0]; call.Ledger != "default-ledger" {
		t.Fatalf("selected wallet RPC ledger = %+v", call)
	}
	empty := onlyResolveInflate(t, cases["empty selected wallet"])
	if empty.Ledger != "default-ledger" || len(empty.Accounts) != 0 {
		t.Fatalf("empty selected wallet inflation = %+v", empty)
	}
	missingLedger := cases["selected wallet cannot replace missing default ledger"]
	assertResolveError(t, missingLedger, "AttributeError", "'NoneType' object has no attribute 'resolve'", 0)
	if got := resolveEventNames(missingLedger); !reflect.DeepEqual(got, []string{"url_parse"}) {
		t.Fatalf("missing default ledger failure order = %v", got)
	}

	includesCase := cases["all include flags bind at inflation"]
	includes := onlyResolveInflate(t, includesCase)
	if includes.IncludePurchaseReceipt != true || includes.IncludeIsMyOutput != float64(1) ||
		includes.IncludeSentSupports != "enabled" || includes.IncludeSentTips != nil ||
		!reflect.DeepEqual(includes.IncludeReceivedTips, []any{}) {
		t.Fatalf("resolve include bindings = %+v", includes)
	}
	assertResolveRPC(t, includesCase, []string{"lbry://includes"})

	option := cases["documented server option fails before retry"]
	if option.Error == nil || option.Error.Type != "TypeError" ||
		!strings.Contains(option.Error.Message, "unexpected keyword argument 'new_sdk_server'") ||
		len(option.Calls.Inflate) != 0 || len(option.Calls.Retriable) != 0 || len(option.Calls.RPC) != 0 ||
		!reflect.DeepEqual(resolveEventNames(option), []string{"url_parse"}) {
		t.Fatalf("new_sdk_server failure = %+v", option)
	}
}

func assertResolveBatching(t *testing.T, cases map[string]resolveOracleCase) {
	t.Helper()
	batched := cases["ledger batches 205 urls"]
	if batched.Error != nil || len(batched.Calls.RPC) != 3 || len(batched.Calls.Retriable) != 3 ||
		len(batched.Calls.Inflate) != 3 {
		t.Fatalf("205 URL batch calls = %+v", batched.Calls)
	}
	wantSizes := []int{100, 100, 5}
	var flattened []string
	for index, rpcCall := range batched.Calls.RPC {
		if len(rpcCall.Params) != wantSizes[index] || rpcCall.Method != "blockchain.claimtrie.resolve" ||
			rpcCall.Restricted || rpcCall.Session != nil || rpcCall.Ledger != "default-ledger" {
			t.Fatalf("batch %d RPC = %+v", index, rpcCall)
		}
		retry := batched.Calls.Retriable[index]
		if retry.Function != "resolve" || retry.Ledger != "default-ledger" || len(retry.Args) != 1 ||
			len(retry.Kwargs) != 0 || !reflect.DeepEqual(retry.Args[0], rpcCall.Params) {
			t.Fatalf("batch %d retriable call = %+v, RPC %+v", index, retry, rpcCall)
		}
		if inflate := batched.Calls.Inflate[index]; inflate.State != "complete" ||
			inflate.ResultCount != wantSizes[index] || !reflect.DeepEqual(inflate.EncodedURLs, rpcCall.Params) {
			t.Fatalf("batch %d inflation = %+v", index, inflate)
		}
		flattened = append(flattened, rpcCall.Params...)
	}
	wantURLs := resolveStringSlice(t, batched.Params["urls"])
	if !reflect.DeepEqual(flattened, wantURLs) {
		t.Fatal("205 URL batching changed URL order")
	}

	failed := cases["second batch retry failure stops"]
	assertResolveError(t, failed, "HubFailure", "offline hub failure on call 2", 1)
	if len(failed.Calls.Retriable) != 2 || len(failed.Calls.Inflate) != 2 ||
		failed.Calls.Inflate[0].State != "complete" || failed.Calls.Inflate[1].State != "error" ||
		failed.Calls.Inflate[1].Error != "HubFailure" {
		t.Fatalf("retry failure calls = %+v", failed.Calls)
	}

	mismatch := cases["length mismatch precedes mapping"]
	assertResolveError(t, mismatch, "AssertionError",
		"Mismatch between urls requested for resolve and responses received.", 1)
	if strings.Contains(strings.Join(resolveEventNames(mismatch), ","), "url_parse") {
		t.Fatalf("length mismatch reached result mapping: %v", resolveEventNames(mismatch))
	}
}

func assertResolveMapping(t *testing.T, cases map[string]resolveOracleCase) {
	t.Helper()
	entries := cases["missing and hub error entries"]
	assertResolveNestedError(t, entries, "lbry://missing", "NOT_FOUND",
		"lbry://missing did not resolve to a claim")
	assertResolveNestedError(t, entries, "lbry://blocked", "BLOCKED", "blocked by fixture")

	absent := cases["channel absent replaces output"]
	assertResolveNestedError(t, absent, "lbry://@chan/absent", "INVALID",
		"lbry://@chan/absent has invalid channel signature")
	if len(absent.Calls.Signature) != 0 {
		t.Fatalf("absent channel performed signature check: %+v", absent.Calls.Signature)
	}
	bad := cases["bad channel signature replaces output"]
	assertResolveNestedError(t, bad, "lbry://@chan/bad", "INVALID",
		"lbry://@chan/bad has invalid channel signature")
	badSignature := onlyResolveSignature(t, bad)
	if badSignature.Valid == nil || *badSignature.Valid || badSignature.Channel != "chan" {
		t.Fatalf("bad signature call = %+v", badSignature)
	}
	good := cases["valid channel signature preserves output"]
	assertResolveOutput(t, good, "lbry://@chan/good", "good", "chan")
	goodSignature := onlyResolveSignature(t, good)
	if goodSignature.Valid == nil || !*goodSignature.Valid {
		t.Fatalf("good signature call = %+v", goodSignature)
	}
	plain := cases["stream outside channel skips signature"]
	assertResolveOutput(t, plain, "lbry://plain", "plain", "ignored")
	if len(plain.Calls.Signature) != 0 {
		t.Fatalf("plain stream performed signature check: %+v", plain.Calls.Signature)
	}

	duplicate := cases["direct duplicate last result wins"]
	assertResolveOutput(t, duplicate, "lbry://duplicate", "second", nil)
	assertResolveRPC(t, duplicate, []string{"lbry://duplicate", "lbry://duplicate"})
	malformed := cases["direct malformed url fails after rpc"]
	assertResolveError(t, malformed, "ValueError", "Invalid LBRY URL", 1)
	wantOrder := []string{"inflate_start", "retriable", "rpc", "inflate_complete", "url_parse"}
	if got := resolveEventNames(malformed); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("malformed direct resolve order = %v, want %v", got, wantOrder)
	}
}

func assertResolvePersistence(t *testing.T, cases map[string]resolveOracleCase) {
	t.Helper()
	saved := cases["storage saves outputs only"]
	storage := onlyResolveStorage(t, saved)
	if storage.Ledger != "default-ledger" || !reflect.DeepEqual(storage.Outputs, []string{"saved"}) {
		t.Fatalf("saved resolve outputs = %+v", storage)
	}
	assertResolveNestedError(t, saved, "lbry://missing", "NOT_FOUND",
		"lbry://missing did not resolve to a claim")

	decode := cases["storage decode error is suppressed"]
	if decode.Error != nil || len(decode.Calls.Storage) != 1 {
		t.Fatalf("suppressed DecodeError = %+v", decode)
	}
	storageFailure := cases["storage failure follows resolution"]
	assertResolveError(t, storageFailure, "StorageFailure", "offline storage failure", 1)
	if got := resolveEventNames(storageFailure); len(got) == 0 || got[len(got)-1] != "storage" {
		t.Fatalf("storage failure order = %v", got)
	}
	allInvalid := cases["all invalid skips storage"]
	if allInvalid.Error != nil || len(allInvalid.Calls.Storage) != 0 || len(allInvalid.Calls.RPC) != 0 ||
		resolveResultMap(t, allInvalid, "not a url")["error"] != "not a url is not a valid url" {
		t.Fatalf("all-invalid resolve = %+v", allInvalid)
	}

	signatureFailure := cases["signature failure follows resolution"]
	assertResolveError(t, signatureFailure, "SignatureFailure", "signature check failed for failure", 1)
	if len(signatureFailure.Calls.Storage) != 0 {
		t.Fatalf("signature failure reached storage: %+v", signatureFailure.Calls.Storage)
	}
	if got := resolveEventNames(signatureFailure); len(got) == 0 || got[len(got)-1] != "signature" {
		t.Fatalf("signature failure order = %v", got)
	}
}

func assertResolveRPC(t *testing.T, fixture resolveOracleCase, params []string) {
	t.Helper()
	if fixture.Error != nil || len(fixture.Calls.RPC) != 1 {
		t.Fatalf("%s RPC calls = %+v, error %+v", fixture.Name, fixture.Calls.RPC, fixture.Error)
	}
	call := fixture.Calls.RPC[0]
	if call.Method != "blockchain.claimtrie.resolve" || call.Restricted || call.Session != nil ||
		call.Ledger != "default-ledger" || !reflect.DeepEqual(call.Params, params) {
		t.Fatalf("%s RPC = %+v, want params %v", fixture.Name, call, params)
	}
}

func assertResolveError(t *testing.T, fixture resolveOracleCase, name, message string, rpcCalls int) {
	t.Helper()
	if fixture.Error == nil || fixture.Error.Type != name || fixture.Error.Message != message ||
		fixture.Result != nil || len(fixture.Calls.RPC) != rpcCalls {
		t.Fatalf("%s error = %+v, result %#v, calls %+v", fixture.Name, fixture.Error,
			fixture.Result, fixture.Calls)
	}
}

func assertResolveOutput(t *testing.T, fixture resolveOracleCase, url, label string, channel any) {
	t.Helper()
	result := resolveResultMap(t, fixture, url)
	want := map[string]any{"kind": "output", "label": label, "channel": channel}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("%s result[%s] = %#v, want %#v", fixture.Name, url, result, want)
	}
}

func assertResolveNestedError(t *testing.T, fixture resolveOracleCase, url, name, text string) {
	t.Helper()
	result := resolveResultMap(t, fixture, url)
	want := map[string]any{"error": map[string]any{"name": name, "text": text}}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("%s result[%s] = %#v, want %#v", fixture.Name, url, result, want)
	}
}

func resolveResultMap(t *testing.T, fixture resolveOracleCase, url string) map[string]any {
	t.Helper()
	if fixture.Error != nil {
		t.Fatalf("%s unexpected error: %+v", fixture.Name, fixture.Error)
	}
	value, exists := fixture.Result[url]
	if !exists {
		t.Fatalf("%s has no result for %s: %#v", fixture.Name, url, fixture.Result)
	}
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s result[%s] = %T %#v", fixture.Name, url, value, value)
	}
	return result
}

func onlyResolveInflate(t *testing.T, fixture resolveOracleCase) resolveOracleInflate {
	t.Helper()
	if fixture.Error != nil || len(fixture.Calls.Inflate) != 1 {
		t.Fatalf("%s inflation = %+v, error %+v", fixture.Name, fixture.Calls.Inflate, fixture.Error)
	}
	return fixture.Calls.Inflate[0]
}

func onlyResolveSignature(t *testing.T, fixture resolveOracleCase) resolveOracleSign {
	t.Helper()
	if fixture.Error != nil || len(fixture.Calls.Signature) != 1 {
		t.Fatalf("%s signatures = %+v, error %+v", fixture.Name, fixture.Calls.Signature, fixture.Error)
	}
	return fixture.Calls.Signature[0]
}

func onlyResolveStorage(t *testing.T, fixture resolveOracleCase) resolveOracleStorage {
	t.Helper()
	if fixture.Error != nil || len(fixture.Calls.Storage) != 1 {
		t.Fatalf("%s storage = %+v, error %+v", fixture.Name, fixture.Calls.Storage, fixture.Error)
	}
	return fixture.Calls.Storage[0]
}

func resolveEventNames(fixture resolveOracleCase) []string {
	names := make([]string, 0, len(fixture.Calls.Events))
	for _, event := range fixture.Calls.Events {
		if name, ok := event["event"].(string); ok {
			names = append(names, name)
		}
	}
	return names
}

func sameStrings(got, want []string) bool {
	got = append([]string(nil), got...)
	want = append([]string(nil), want...)
	sort.Strings(got)
	sort.Strings(want)
	return reflect.DeepEqual(got, want)
}

func resolveStringSlice(t *testing.T, value any) []string {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("value = %T, want []any", value)
	}
	result := make([]string, len(items))
	for index, item := range items {
		text, ok := item.(string)
		if !ok {
			t.Fatalf("value[%d] = %T, want string", index, item)
		}
		result[index] = text
	}
	return result
}

func runResolveOracle(t *testing.T) resolveOracleResponse {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate resolve oracle test")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot := os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	script := filepath.Join(daemonRoot, "compat", "resolve_oracle.py")
	for _, path := range []string{sdkRoot, script} {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			t.Skipf("resolve oracle dependency unavailable: %s", path)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command(python, script, "--sdk-root", sdkRoot)
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1", "PYTHONHASHSEED=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Python resolve oracle failed: %v\n%s", err, output)
	}
	var oracle resolveOracleResponse
	if err := json.Unmarshal(output, &oracle); err != nil {
		t.Fatalf("decode resolve oracle: %v\n%s", err, output)
	}
	return oracle
}
