package wallet

import (
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

	"lbry/daemon/wallet/keys"
)

const (
	hubOutputsFetchAdapterOracleCommit  = "e7666f489418e96b6d2104974e93915b539235c5"
	hubOutputsFetchAdapterOracleVersion = "0.113.0"
)

var hubOutputsFetchAdapterOracleSources = map[string]string{
	"lbry/__init__.py":           "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
	"lbry/schema/result.py":      "b5a506fedc9f40c5e9ea1b0691e1e36f9559acaabafe9e3599ed7db52031a4cf",
	"lbry/wallet/ledger.py":      "5b5e3deacd5a87ec69a91b42c7aa6460605146724205ff0b387402dd193be2a5",
	"lbry/wallet/transaction.py": "e73491aeb915fbce931acbb4d9631f3e05440a7d26c598db85e66e524a798d15",
}

var hubOutputsFetchAdapterOracleMethods = map[string]string{
	"Ledger._inflate_outputs":     "2eb53ed61cabd4456010c5c3c23ec848c5888ca749acb68ec864fc1e92be5cfe",
	"Ledger.request_transactions": "229439ed82b706d22676f59261b4b162450ac68e72ca34aee50fc119f990001b",
	"Output.update_annotations":   "93c3f5bdac129fa70c6e887c3648030396fdd638c06defead49de63599816eb6",
	"Outputs.inflate":             "61bfff753fc883560eb1982a08316fc2b0bd8e5aa7fe2ca143dd8a50b71d5870",
	"Outputs.inflate_blocked":     "e66de7986c315fd7982f58ba68ca5c110282a279ee18e75a6246d25ee8734343",
	"Outputs.message_to_txo":      "4369696def2c977a904df2db3d397219bf2b2e1a6e0c3550f3ad184b286d1ce5",
}

type hubOutputsFetchAdapterOracleResponse struct {
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
	Cases []map[string]any `json:"cases"`
}

func TestHubOutputsFetchAdapterPinnedOracle(t *testing.T) {
	oracle := runHubOutputsFetchAdapterOracle(t)
	if oracle.Reference.Commit != hubOutputsFetchAdapterOracleCommit ||
		oracle.Reference.Version != hubOutputsFetchAdapterOracleVersion ||
		!reflect.DeepEqual(oracle.Reference.SourceSHA256, hubOutputsFetchAdapterOracleSources) ||
		!reflect.DeepEqual(oracle.Reference.MethodSHA256, hubOutputsFetchAdapterOracleMethods) {
		t.Fatalf("hub outputs fetch adapter oracle reference = %+v", oracle.Reference)
	}
	if oracle.Metadata.PythonVersion == "" || !oracle.Metadata.ExtractedMethodsExecuted ||
		oracle.Metadata.ExternalNetworkUsed || oracle.Metadata.CaseCount != 4 ||
		len(oracle.Cases) != 4 {
		t.Fatalf("hub outputs fetch adapter metadata = %+v, cases %d", oracle.Metadata, len(oracle.Cases))
	}
	assertHubOutputsFetchAdapterOracleContract(t, oracle)
}

func TestHubOutputsFetchAdapterProductionMatchesPinnedCacheBatchOracle(t *testing.T) {
	const requestCount = 105
	transactions := make([]*Transaction, requestCount)
	rawByID := make(map[string]string, requestCount)
	ascending := make([]TransactionFetchRequest, requestCount)
	for index := range requestCount {
		rawHex := transactionFetchFixtureHex + fmt.Sprintf("%08x", index)
		raw, err := hex.DecodeString(rawHex)
		if err != nil {
			t.Fatal(err)
		}
		transaction, err := ParseTransaction(raw)
		if err != nil {
			t.Fatal(err)
		}
		transactions[index] = transaction
		rawByID[transaction.ID] = rawHex
		ascending[index] = TransactionFetchRequest{TxID: transaction.ID, Height: int64(index)}
	}
	requests := make([]TransactionFetchRequest, requestCount)
	for index := range ascending {
		requests[index] = ascending[len(ascending)-1-index]
	}

	ledger := &Ledger{Headers: &Headers{}}
	cache := ledger.ledgerTransactionCache()
	verified := transactions[10]
	verified.IsVerified = true
	if err := cache.insertPlaceholder(ascending[10].TxID); err != nil {
		t.Fatal(err)
	}
	if err := cache.setExisting(ascending[10].TxID, verified); err != nil {
		t.Fatal(err)
	}
	staleValue := *transactions[102]
	stale := &staleValue
	if err := cache.insertPlaceholder(ascending[102].TxID); err != nil {
		t.Fatal(err)
	}
	if err := cache.setExisting(ascending[102].TxID, stale); err != nil {
		t.Fatal(err)
	}

	misses := make([]TransactionFetchRequest, 0, requestCount-1)
	for index, request := range ascending {
		if index != 10 {
			misses = append(misses, request)
		}
	}
	responses := make([]any, 0, 2)
	for start := 0; start < len(misses); start += TransactionFetchBatchSize {
		end := min(start+TransactionFetchBatchSize, len(misses))
		response := make(map[string]any, end-start)
		for _, request := range misses[start:end] {
			response[request.TxID] = []any{rawByID[request.TxID], nil}
		}
		responses = append(responses, response)
	}
	rpc := &transactionFetchExecutionRPC{responses: responses}
	ledger.SPVNetwork = rpc
	fetched, err := ledger.fetchCachedHubTransactions(context.Background(), requests)
	if err != nil {
		t.Fatal(err)
	}
	if len(fetched) != requestCount || fetched[0] != verified ||
		fetched[1].ID != ascending[0].TxID || fetched[100].ID != ascending[100].TxID ||
		fetched[101].ID != ascending[101].TxID || fetched[104].ID != ascending[104].TxID {
		t.Fatalf("cached transaction order = %#v", fetched)
	}
	calls := rpc.snapshotCalls()
	if len(calls) != 2 || len(calls[0].params) != 100 || len(calls[1].params) != 4 ||
		calls[0].method != SPVTransactionBatchMethod ||
		calls[1].method != SPVTransactionBatchMethod ||
		!calls[0].restricted || !calls[1].restricted {
		t.Fatalf("cached transaction RPC calls = %#v", calls)
	}
	if calls[0].params[0] != ascending[0].TxID ||
		calls[0].params[99] != ascending[100].TxID ||
		calls[1].params[0] != ascending[101].TxID ||
		calls[1].params[3] != ascending[104].TxID {
		t.Fatalf("height-sorted batch parameters = %#v", calls)
	}
	stillVerified, exists := cache.get(ascending[10].TxID)
	if !exists || stillVerified != verified {
		t.Fatalf("verified cache identity = %p, %t, want %p", stillVerified, exists, verified)
	}
	replacedStale, exists := cache.get(ascending[102].TxID)
	if !exists || replacedStale == stale || replacedStale != fetched[102] {
		t.Fatalf("stale cache replacement = %p, %t; stale %p fetched %p", replacedStale, exists, stale, fetched[102])
	}
	if cache.length() != requestCount {
		t.Fatalf("cache length = %d, want %d", cache.length(), requestCount)
	}
}

func TestHubOutputsFetchAdapterProductionMatchesPinnedInflationOracle(t *testing.T) {
	channelHash := hubOutputsFetchAdapterHash(0x20)
	claimHash := hubOutputsFetchAdapterHash(0x60)
	channelTransaction := &Transaction{
		Hash: channelHash, IsVerified: true,
		Outputs: []TransactionOutput{{
			TransactionHash: channelHash,
			Script: TransactionOutputScript{
				Template: TransactionScriptClaimPubKeyHash,
				Claim: append(
					[]byte{0x00, 0x12, 0x23, 0x0a, 0x21}, make([]byte, 33)...,
				),
			},
		}},
	}
	claimTransaction := &Transaction{
		Hash: claimHash, IsVerified: true,
		Outputs: []TransactionOutput{{
			TransactionHash:    claimHash,
			IsInternalTransfer: hubOutputsFetchAdapterBool(true),
			IsSpent:            hubOutputsFetchAdapterBool(true),
			IsMyOutput:         hubOutputsFetchAdapterBool(true),
			IsMyInput:          hubOutputsFetchAdapterBool(true),
			SentSupports:       hubOutputsFetchAdapterInt64(101),
			SentTips:           hubOutputsFetchAdapterInt64(102),
			ReceivedTips:       hubOutputsFetchAdapterInt64(103),
			PrivateKey:         new(keys.PrivateKey),
			PurchaseReceipt:    &TransactionOutput{Position: 91},
		}},
	}
	channelReference := &HubOutput{TransactionHash: channelHash[:]}
	outputs := &HubOutputs{
		TXOs: []*HubOutput{{
			TransactionHash: claimHash[:], Height: 10,
			Claim: &HubClaimMeta{ShortURL: "stream", Channel: channelReference},
		}},
		ExtraTXOs: []*HubOutput{{
			TransactionHash: channelHash[:], Height: 20,
			Claim: &HubClaimMeta{ShortURL: "@channel", ClaimsInChannel: 77},
		}},
		Offset: 12, Total: 34, BlockedTotal: 5,
		Blocked: []*HubBlocked{{Count: 4, Channel: channelReference}},
	}

	ledger := &Ledger{}
	cache := ledger.ledgerTransactionCache()
	transactionsByHash := map[[32]byte]*Transaction{
		claimHash: claimTransaction, channelHash: channelTransaction,
	}
	for _, request := range outputs.TransactionRequests() {
		displayHash, err := hex.DecodeString(request.TxID)
		if err != nil {
			t.Fatal(err)
		}
		var hash [32]byte
		copy(hash[:], reverseTransactionBytes(displayHash))
		if err := cache.insertPlaceholder(request.TxID); err != nil {
			t.Fatal(err)
		}
		if err := cache.setExisting(request.TxID, transactionsByHash[hash]); err != nil {
			t.Fatal(err)
		}
	}

	page, err := ledger.InflateHubOutputs(context.Background(), outputs)
	if err != nil {
		t.Fatal(err)
	}
	if page.Offset != 12 || page.Total != 34 || len(page.Items) != 1 ||
		page.Items[0].Output == nil {
		t.Fatalf("inflated page = %#v", page)
	}
	result := page.Items[0].Output
	claimSource := &claimTransaction.Outputs[0]
	channelSource := &channelTransaction.Outputs[0]
	if result == claimSource || result.Channel != channelSource ||
		result.Meta["short_url"] != "lbry://stream" ||
		channelSource.Meta["short_url"] != "lbry://@channel" ||
		channelSource.Meta["claims_in_channel"] != uint32(77) {
		t.Fatalf("extras-first copied result = %#v; channel %#v", result, channelSource)
	}
	result.Meta["shallow_copy_probe"] = true
	if claimSource.Meta["shallow_copy_probe"] != true {
		t.Fatalf("primary meta map was deep-copied: result %#v source %#v", result.Meta, claimSource.Meta)
	}
	if result.IsInternalTransfer != nil || result.IsSpent != nil ||
		result.IsMyOutput != nil || result.IsMyInput != nil ||
		result.SentSupports != nil || result.SentTips != nil || result.ReceivedTips != nil ||
		result.PrivateKey != nil || result.PurchaseReceipt != nil {
		t.Fatalf("throwaway annotations were not reset: %#v", result)
	}
	if claimSource.IsSpent == nil || !*claimSource.IsSpent || claimSource.SentTips == nil ||
		*claimSource.SentTips != 102 || claimSource.PrivateKey == nil ||
		claimSource.PurchaseReceipt == nil {
		t.Fatalf("cached source annotations were mutated: %#v", claimSource)
	}
	if page.Blocked.Total != 5 || len(page.Blocked.Channels) != 1 ||
		page.Blocked.Channels[0].Blocked != 4 ||
		page.Blocked.Channels[0].Channel.Output != channelSource {
		t.Fatalf("blocked summary = %#v", page.Blocked)
	}
}

func hubOutputsFetchAdapterHash(first byte) [32]byte {
	var hash [32]byte
	for index := range hash {
		hash[index] = first + byte(index)
	}
	return hash
}

func hubOutputsFetchAdapterBool(value bool) *bool { return &value }

func hubOutputsFetchAdapterInt64(value int64) *int64 { return &value }

func assertHubOutputsFetchAdapterOracleContract(
	t *testing.T, oracle hubOutputsFetchAdapterOracleResponse,
) {
	t.Helper()
	wantContract := map[string]string{
		"method": "(*Ledger).InflateHubOutputs(context.Context, *HubOutputs) (HubOutputsPage, error)",
		"result": "HubOutputsPage{Items, Blocked, Offset, Total}",
		"cache":  "per-ledger verified-transaction cache keyed by requested txid",
		"note":   "Names are advisory; fixture semantics are normative.",
	}
	if !reflect.DeepEqual(oracle.Metadata.ProposedGoContract, wantContract) {
		t.Fatalf("proposed Go contract = %#v", oracle.Metadata.ProposedGoContract)
	}
	if len(oracle.Metadata.ContractNotes) != 5 {
		t.Fatalf("contract notes = %#v", oracle.Metadata.ContractNotes)
	}
	wantNames := []string{
		"verified hits precede height-sorted miss batches",
		"later fetched transaction wins duplicate hash map",
		"extras inflate before copied primary and page metadata survives",
		"empty page skips transaction generator",
	}
	for index, name := range wantNames {
		if got := hubOutputsFetchString(t, oracle.Cases[index], "name"); got != name {
			t.Fatalf("case %d name = %q, want %q", index, got, name)
		}
	}

	cache := oracle.Cases[0]
	assertHubOutputsFetchNumbers(t, cache, "yield_sizes", []float64{1, 100, 4})
	assertHubOutputsFetchStrings(t, cache, "cache_hit_keys", []string{"tx010"})
	assertHubOutputsFetchNumbers(t, cache, "batch_sizes", []float64{100, 4})
	assertHubOutputsFetchStrings(t, cache, "first_batch_first_ids", []string{
		"tx000", "tx001", "tx002", "tx003",
	})
	assertHubOutputsFetchStrings(t, cache, "first_batch_last_ids", []string{
		"tx097", "tx098", "tx099", "tx100",
	})
	assertHubOutputsFetchStrings(t, cache, "second_batch_ids", []string{
		"tx101", "tx102", "tx103", "tx104",
	})
	assertHubOutputsFetchNumbers(t, cache, "remote_height_map_is_global", []float64{104, 104})
	for _, field := range []string{
		"verified_identity_preserved", "unverified_replaced", "placeholder_filled", "absent_inserted",
	} {
		if !hubOutputsFetchBool(t, cache, field) {
			t.Fatalf("cache case %s = false", field)
		}
	}

	duplicate := oracle.Cases[1]
	assertHubOutputsFetchStrings(t, duplicate, "supplied_transaction_order", []string{
		"cached-transaction", "fetched-transaction",
	})
	if got := hubOutputsFetchString(t, duplicate, "selected_output"); got != "fetched-choice" {
		t.Fatalf("duplicate hash selected output = %q", got)
	}
	for _, field := range []string{
		"selected_is_throwaway_copy", "cached_candidate_unchanged", "fetched_source_unchanged",
	} {
		if !hubOutputsFetchBool(t, duplicate, field) {
			t.Fatalf("duplicate hash case %s = false", field)
		}
	}

	copyCase := oracle.Cases[2]
	if !hubOutputsFetchBool(t, copyCase, "throwaway_copy_created") ||
		!hubOutputsFetchBool(t, copyCase, "meta_is_shallow_shared") ||
		!hubOutputsFetchBool(t, copyCase, "channel_identity_preserved") {
		t.Fatalf("copy identity contract = %#v", copyCase)
	}
	annotations := hubOutputsFetchMap(t, copyCase, "result_annotations")
	for field, value := range annotations {
		if value != nil {
			t.Fatalf("reset annotation %s = %#v, want null", field, value)
		}
	}
	channelMeta := hubOutputsFetchMap(t, copyCase, "channel_extra_meta_visible")
	if hubOutputsFetchString(t, channelMeta, "short_url") != "lbry://@channel" ||
		hubOutputsFetchNumber(t, channelMeta, "claims_in_channel") != 77 {
		t.Fatalf("extra channel metadata = %#v", channelMeta)
	}
	blocked := hubOutputsFetchMap(t, copyCase, "blocked")
	if hubOutputsFetchNumber(t, blocked, "total") != 5 {
		t.Fatalf("blocked summary = %#v", blocked)
	}
	channels := hubOutputsFetchList(t, blocked, "channels")
	if len(channels) != 1 {
		t.Fatalf("blocked channels = %#v", channels)
	}
	blockedChannel, ok := channels[0].(map[string]any)
	if !ok || hubOutputsFetchString(t, blockedChannel, "label") != "channel-source" ||
		!hubOutputsFetchBool(t, blockedChannel, "same_channel_identity") ||
		hubOutputsFetchNumber(t, blockedChannel, "blocked") != 4 {
		t.Fatalf("blocked channel = %#v", channels[0])
	}
	if hubOutputsFetchNumber(t, copyCase, "offset") != 12 ||
		hubOutputsFetchNumber(t, copyCase, "total") != 34 {
		t.Fatalf("page metadata = %#v", copyCase)
	}

	empty := oracle.Cases[3]
	if hubOutputsFetchNumber(t, empty, "single_batch_calls") != 0 ||
		hubOutputsFetchString(t, empty, "encoded_input") != "" ||
		len(hubOutputsFetchList(t, empty, "txos")) != 0 {
		t.Fatalf("empty page = %#v", empty)
	}
}

func hubOutputsFetchMap(t *testing.T, value map[string]any, key string) map[string]any {
	t.Helper()
	result, ok := value[key].(map[string]any)
	if !ok {
		t.Fatalf("field %q = %#v, want object", key, value[key])
	}
	return result
}

func hubOutputsFetchList(t *testing.T, value map[string]any, key string) []any {
	t.Helper()
	result, ok := value[key].([]any)
	if !ok {
		t.Fatalf("field %q = %#v, want list", key, value[key])
	}
	return result
}

func hubOutputsFetchString(t *testing.T, value map[string]any, key string) string {
	t.Helper()
	result, ok := value[key].(string)
	if !ok {
		t.Fatalf("field %q = %#v, want string", key, value[key])
	}
	return result
}

func hubOutputsFetchNumber(t *testing.T, value map[string]any, key string) float64 {
	t.Helper()
	result, ok := value[key].(float64)
	if !ok {
		t.Fatalf("field %q = %#v, want number", key, value[key])
	}
	return result
}

func hubOutputsFetchBool(t *testing.T, value map[string]any, key string) bool {
	t.Helper()
	result, ok := value[key].(bool)
	if !ok {
		t.Fatalf("field %q = %#v, want bool", key, value[key])
	}
	return result
}

func assertHubOutputsFetchStrings(t *testing.T, value map[string]any, key string, want []string) {
	t.Helper()
	items := hubOutputsFetchList(t, value, key)
	got := make([]string, len(items))
	for index, item := range items {
		var ok bool
		got[index], ok = item.(string)
		if !ok {
			t.Fatalf("field %q item %d = %#v, want string", key, index, item)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("field %q = %#v, want %#v", key, got, want)
	}
}

func assertHubOutputsFetchNumbers(t *testing.T, value map[string]any, key string, want []float64) {
	t.Helper()
	items := hubOutputsFetchList(t, value, key)
	got := make([]float64, len(items))
	for index, item := range items {
		var ok bool
		got[index], ok = item.(float64)
		if !ok {
			t.Fatalf("field %q item %d = %#v, want number", key, index, item)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("field %q = %#v, want %#v", key, got, want)
	}
}

func runHubOutputsFetchAdapterOracle(t *testing.T) hubOutputsFetchAdapterOracleResponse {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate hub outputs fetch adapter oracle test")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot := os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	script := filepath.Join(daemonRoot, "compat", "hub_outputs_fetch_adapter_oracle.py")
	for _, path := range []string{sdkRoot, script} {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			t.Skipf("hub outputs fetch adapter oracle dependency is unavailable: %s", path)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command(python, script, "--sdk-root", sdkRoot)
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1", "PYTHONHASHSEED=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Python hub outputs fetch adapter oracle failed: %v\n%s", err, output)
	}
	var oracle hubOutputsFetchAdapterOracleResponse
	if err := json.Unmarshal(output, &oracle); err != nil {
		t.Fatalf("decode hub outputs fetch adapter oracle: %v\n%s", err, output)
	}
	return oracle
}
