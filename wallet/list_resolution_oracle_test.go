package wallet

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"lbry/daemon/wallet/keys"
)

const (
	listResolutionOracleCommit  = "e7666f489418e96b6d2104974e93915b539235c5"
	listResolutionOracleVersion = "0.113.0"
)

var listResolutionOracleSources = map[string]string{
	"lbry/__init__.py":           "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
	"lbry/wallet/ledger.py":      "5b5e3deacd5a87ec69a91b42c7aa6460605146724205ff0b387402dd193be2a5",
	"lbry/wallet/transaction.py": "e73491aeb915fbce931acbb4d9631f3e05440a7d26c598db85e66e524a798d15",
}

var listResolutionOracleMethods = map[string]string{
	"Ledger.get_txos":                           "4dcd701a9e0cc5142af7dc96b0fdd45b21410df1c1f9ce7d90240b02888f8c01",
	"Ledger.get_purchases":                      "a10b63da0d141f7f094eb0d85f8734f4743dcbb76b5fecb5928d692cb6fe2bbb",
	"Ledger._resolve_for_local_results":         "ae8da93c15547112bf07a1208ac2c7065af2fc9f35f1afb0e7d39fbdd5a9a111",
	"Ledger._resolve_for_local_claim_results":   "2c28cec3e2220cf767bf510ae1ea835eb32c6039c51e4103932ff0c2d08525de",
	"Ledger._resolve_for_local_support_results": "49e3b2a9faf0109aa8b2f1cb016cf2e671d1a6b39f6399f2972264f00f80367b",
	"Ledger.resolve_collection":                 "9692e89042901f82f8a5f5cb06b300bf7ced49f5aadd4718da7ad2cc3c6c7ef3",
	"Ledger.get_collections":                    "7c7c3bf096b10bf0f03489ad21cba120276a9cafb9df06b57d82e35c8efe9260",
	"Output.update_annotations":                 "93c3f5bdac129fa70c6e887c3648030396fdd638c06defead49de63599816eb6",
}

type listResolutionOracleCallSet struct {
	Events []string `json:"events"`
	DB     []struct {
		Method      string         `json:"method"`
		Constraints map[string]any `json:"constraints"`
	} `json:"db"`
	Resolve []struct {
		Accounts []string `json:"accounts"`
		URLs     []string `json:"urls"`
	} `json:"resolve"`
	ClaimSearch []struct {
		Accounts       []string `json:"accounts"`
		ClaimIDs       []string `json:"claim_ids"`
		ConstraintKeys []string `json:"constraint_keys"`
	} `json:"claim_search"`
}

type listResolutionOracleError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type listResolutionOracleOutput struct {
	Label              string         `json:"label"`
	IsInternalTransfer *bool          `json:"is_internal_transfer"`
	IsSpent            *bool          `json:"is_spent"`
	IsMyOutput         *bool          `json:"is_my_output"`
	IsMyInput          *bool          `json:"is_my_input"`
	SentSupports       *int64         `json:"sent_supports"`
	SentTips           *int64         `json:"sent_tips"`
	ReceivedTips       *int64         `json:"received_tips"`
	Channel            *string        `json:"channel"`
	PrivateKey         *string        `json:"private_key"`
	PurchasedClaim     *string        `json:"purchased_claim"`
	PurchaseReceipt    *string        `json:"purchase_receipt"`
	RepostedClaim      *string        `json:"reposted_claim"`
	Claims             []*string      `json:"claims"`
	Meta               map[string]any `json:"meta"`
}

type listResolutionOracleResponse struct {
	Reference struct {
		Commit       string            `json:"commit"`
		Version      string            `json:"version"`
		SourceSHA256 map[string]string `json:"source_sha256"`
		MethodSHA256 map[string]string `json:"method_sha256"`
	} `json:"reference"`
	Metadata struct {
		PythonVersion            string `json:"python_version"`
		ExtractedMethodsExecuted bool   `json:"extracted_methods_executed"`
		ExternalNetworkUsed      bool   `json:"external_network_used"`
		ProbeCount               int    `json:"probe_count"`
	} `json:"metadata"`
	ClaimResults struct {
		Success struct {
			Calls              listResolutionOracleCallSet  `json:"calls"`
			ResultLabels       []string                     `json:"result_labels"`
			ResultIsRemote     bool                         `json:"result_is_remote"`
			ErrorResultIsLocal bool                         `json:"error_result_is_local"`
			PlainResultIsLocal bool                         `json:"plain_result_is_local"`
			Result             []listResolutionOracleOutput `json:"result"`
			LocalAfter         listResolutionOracleOutput   `json:"local_after"`
		} `json:"success"`
		Failure struct {
			Calls listResolutionOracleCallSet `json:"calls"`
			Error listResolutionOracleError   `json:"error"`
		} `json:"failure"`
	} `json:"claim_results"`
	SupportResults struct {
		Success struct {
			Calls               listResolutionOracleCallSet `json:"calls"`
			ResultLabels        []string                    `json:"result_labels"`
			Channels            []*string                   `json:"channels"`
			IdentitiesPreserved []bool                      `json:"identities_preserved"`
		} `json:"success"`
		Failure struct {
			Calls listResolutionOracleCallSet `json:"calls"`
			Error listResolutionOracleError   `json:"error"`
		} `json:"failure"`
	} `json:"support_results"`
	Purchases struct {
		Success struct {
			Calls           listResolutionOracleCallSet `json:"calls"`
			ResultLabels    []string                    `json:"result_labels"`
			PurchasedClaims []*string                   `json:"purchased_claims"`
		} `json:"success"`
		Failure struct {
			Calls           listResolutionOracleCallSet `json:"calls"`
			ResultLabels    []string                    `json:"result_labels"`
			PurchasedClaims []*string                   `json:"purchased_claims"`
			Logs            []string                    `json:"logs"`
		} `json:"failure"`
	} `json:"purchases"`
	Collections struct {
		List struct {
			Calls          listResolutionOracleCallSet  `json:"calls"`
			ResultLabels   []string                     `json:"result_labels"`
			Claims         [][]*string                  `json:"claims"`
			ResultIsRemote []bool                       `json:"result_is_remote"`
			Annotations    []listResolutionOracleOutput `json:"annotations"`
			Logs           []string                     `json:"logs"`
		} `json:"list"`
		Slice struct {
			Calls  listResolutionOracleCallSet `json:"calls"`
			Claims []*string                   `json:"claims"`
		} `json:"slice"`
	} `json:"collections"`
}

func TestListResolutionPinnedPythonOracle(t *testing.T) {
	oracle := runListResolutionOracle(t)
	if oracle.Reference.Commit != listResolutionOracleCommit ||
		oracle.Reference.Version != listResolutionOracleVersion ||
		!reflect.DeepEqual(oracle.Reference.SourceSHA256, listResolutionOracleSources) ||
		!reflect.DeepEqual(oracle.Reference.MethodSHA256, listResolutionOracleMethods) {
		t.Fatalf("list resolution oracle reference = %+v", oracle.Reference)
	}
	if oracle.Metadata.PythonVersion == "" || !oracle.Metadata.ExtractedMethodsExecuted ||
		oracle.Metadata.ExternalNetworkUsed || oracle.Metadata.ProbeCount != 8 {
		t.Fatalf("list resolution oracle metadata = %+v", oracle.Metadata)
	}
	if want := os.Getenv("LBRY_ORACLE_PYTHON_VERSION"); want != "" &&
		oracle.Metadata.PythonVersion != want {
		t.Fatalf("list resolution oracle Python version = %q, want %q",
			oracle.Metadata.PythonVersion, want)
	}

	assertListResolutionClaimOracle(t, oracle)
	assertListResolutionSupportOracle(t, oracle)
	assertListResolutionPurchaseOracle(t, oracle)
	assertListResolutionCollectionOracle(t, oracle)
	assertGoListResolutionHelpers(t, oracle)
}

func assertListResolutionClaimOracle(t *testing.T, oracle listResolutionOracleResponse) {
	t.Helper()
	success := oracle.ClaimResults.Success
	if !reflect.DeepEqual(success.Calls.Events, []string{"db:get_txos", "resolve"}) ||
		len(success.Calls.DB) != 1 || success.Calls.DB[0].Method != "get_txos" ||
		success.Calls.DB[0].Constraints["marker"] != "kept" ||
		len(success.Calls.Resolve) != 1 ||
		!reflect.DeepEqual(success.Calls.Resolve[0].Accounts, []string{"account-a"}) ||
		!reflect.DeepEqual(success.Calls.Resolve[0].URLs,
			[]string{"lbry://local#aaa", "lbry://error#bbb"}) ||
		len(success.Calls.ClaimSearch) != 0 {
		t.Fatalf("claim-result calls = %+v", success.Calls)
	}
	if !reflect.DeepEqual(success.ResultLabels,
		[]string{"remote-claim", "local-error", "plain"}) ||
		!success.ResultIsRemote || !success.ErrorResultIsLocal || !success.PlainResultIsLocal {
		t.Fatalf("claim-result selection = labels %v, identities %t/%t/%t",
			success.ResultLabels, success.ResultIsRemote,
			success.ErrorResultIsLocal, success.PlainResultIsLocal)
	}
	resolved := success.Result[0]
	if !listResolutionOracleBool(resolved.IsInternalTransfer, true) ||
		!listResolutionOracleBool(resolved.IsSpent, false) ||
		!listResolutionOracleBool(resolved.IsMyOutput, true) ||
		!listResolutionOracleBool(resolved.IsMyInput, false) ||
		!listResolutionOracleInt(resolved.SentSupports, 11) ||
		!listResolutionOracleInt(resolved.SentTips, 22) ||
		!listResolutionOracleInt(resolved.ReceivedTips, 33) ||
		!listResolutionOracleString(resolved.Channel, "local-channel") ||
		!listResolutionOracleString(resolved.PrivateKey, "local-private-key") ||
		!listResolutionOracleString(resolved.PurchaseReceipt, "remote-receipt") ||
		!listResolutionOracleString(resolved.PurchasedClaim, "remote-purchased") ||
		!listResolutionOracleString(resolved.RepostedClaim, "remote-repost") ||
		!listResolutionOracleStrings(resolved.Claims, []*string{listResolutionString("remote-member")}) ||
		resolved.Meta["remote"] != "preserved" {
		t.Fatalf("resolved claim annotations = %+v", resolved)
	}
	if success.LocalAfter.Meta["local"] != "preserved" ||
		!listResolutionOracleString(success.LocalAfter.PurchaseReceipt, "local-receipt") {
		t.Fatalf("local annotation source was mutated: %+v", success.LocalAfter)
	}
	nested, ok := success.Result[1].Meta["error"].(map[string]any)
	if !ok || nested["name"] != "NOT_FOUND" || nested["text"] != "missing from hub" ||
		success.Result[1].Meta["existing"] != float64(1) {
		t.Fatalf("local resolve error metadata = %#v", success.Result[1].Meta)
	}
	failure := oracle.ClaimResults.Failure
	if failure.Error.Type != "ProbeFailure" || failure.Error.Message != "resolve exploded" ||
		!reflect.DeepEqual(failure.Calls.Events, []string{"db:get_txos", "resolve"}) ||
		len(failure.Calls.Resolve) != 1 {
		t.Fatalf("claim resolve failure = %+v", failure)
	}
}

func assertListResolutionSupportOracle(t *testing.T, oracle listResolutionOracleResponse) {
	t.Helper()
	success := oracle.SupportResults.Success
	if !reflect.DeepEqual(success.Calls.Events,
		[]string{"db:get_txos", "resolve", "claim_search"}) ||
		len(success.Calls.Resolve) != 1 || len(success.Calls.Resolve[0].URLs) != 0 ||
		len(success.Calls.ClaimSearch) != 1 ||
		!reflect.DeepEqual(success.Calls.ClaimSearch[0].Accounts,
			[]string{"account-support"}) ||
		!reflect.DeepEqual(success.Calls.ClaimSearch[0].ClaimIDs,
			[]string{"channel-1"}) ||
		!reflect.DeepEqual(success.Calls.ClaimSearch[0].ConstraintKeys,
			[]string{"claim_ids"}) {
		t.Fatalf("support resolution calls = %+v", success.Calls)
	}
	if !reflect.DeepEqual(success.ResultLabels,
		[]string{"support-first", "support-unsigned", "support-second"}) ||
		!reflect.DeepEqual(success.IdentitiesPreserved, []bool{true, true, true}) ||
		!listResolutionOracleStrings(success.Channels, []*string{
			listResolutionString("channel-last"),
			listResolutionString("unsigned-existing"),
			listResolutionString("channel-last"),
		}) {
		t.Fatalf("support resolution result = labels %v channels %v identities %v",
			success.ResultLabels, success.Channels, success.IdentitiesPreserved)
	}
	failure := oracle.SupportResults.Failure
	if failure.Error.Type != "ProbeFailure" ||
		failure.Error.Message != "support lookup exploded" ||
		!reflect.DeepEqual(failure.Calls.Events,
			[]string{"db:get_txos", "resolve", "claim_search"}) {
		t.Fatalf("support resolution failure = %+v", failure)
	}
}

func assertListResolutionPurchaseOracle(t *testing.T, oracle listResolutionOracleResponse) {
	t.Helper()
	success := oracle.Purchases.Success
	if !reflect.DeepEqual(success.Calls.Events,
		[]string{"db:get_purchases", "claim_search"}) ||
		len(success.Calls.ClaimSearch) != 1 ||
		len(success.Calls.ClaimSearch[0].Accounts) != 0 ||
		!reflect.DeepEqual(success.Calls.ClaimSearch[0].ClaimIDs,
			[]string{"claim-a", "claim-missing", "claim-a"}) ||
		!listResolutionOracleStrings(success.PurchasedClaims, []*string{
			listResolutionString("claim-a-last"), nil,
			listResolutionString("claim-a-last"),
		}) {
		t.Fatalf("purchase resolution = calls %+v claims %v",
			success.Calls, success.PurchasedClaims)
	}
	failure := oracle.Purchases.Failure
	if !reflect.DeepEqual(failure.ResultLabels, []string{"purchase-failed"}) ||
		len(failure.PurchasedClaims) != 1 || failure.PurchasedClaims[0] != nil ||
		!reflect.DeepEqual(failure.Logs,
			[]string{"Resolve failed while looking up purchased claim ids:"}) {
		t.Fatalf("swallowed purchase resolution failure = %+v", failure)
	}
}

func assertListResolutionCollectionOracle(t *testing.T, oracle listResolutionOracleResponse) {
	t.Helper()
	list := oracle.Collections.List
	if !reflect.DeepEqual(list.Calls.Events,
		[]string{"db:get_collections", "resolve", "claim_search", "claim_search"}) ||
		len(list.Calls.Resolve) != 1 ||
		!reflect.DeepEqual(list.Calls.Resolve[0].URLs, []string{
			"lbry://collection-first#111", "lbry://collection-second#222",
		}) || len(list.Calls.ClaimSearch) != 2 ||
		!reflect.DeepEqual(list.Calls.ClaimSearch[0].ClaimIDs,
			[]string{"claim-a", "missing", "claim-a"}) ||
		!reflect.DeepEqual(list.Calls.ClaimSearch[1].ClaimIDs,
			[]string{"claim-b", "claim-a"}) ||
		len(list.Calls.ClaimSearch[0].Accounts) != 0 ||
		len(list.Calls.ClaimSearch[1].Accounts) != 0 {
		t.Fatalf("collection resolution calls = %+v", list.Calls)
	}
	if !reflect.DeepEqual(list.ResultLabels,
		[]string{"remote-collection-first", "remote-collection-second"}) ||
		!reflect.DeepEqual(list.ResultIsRemote, []bool{true, true}) ||
		len(list.Claims) != 2 ||
		!listResolutionOracleStrings(list.Claims[0], []*string{
			listResolutionString("claim-a-first"), nil,
			listResolutionString("claim-a-first"),
		}) || list.Claims[1] == nil || len(list.Claims[1]) != 0 ||
		!reflect.DeepEqual(list.Logs,
			[]string{"Resolve failed while looking up collection claim ids:"}) {
		t.Fatalf("collection resolution result = labels %v claims %v logs %v",
			list.ResultLabels, list.Claims, list.Logs)
	}
	if !listResolutionOracleString(list.Annotations[0].Channel, "local-channel") ||
		!listResolutionOracleString(list.Annotations[0].PrivateKey, "local-private-key") ||
		!listResolutionOracleInt(list.Annotations[0].ReceivedTips, 33) {
		t.Fatalf("collection local annotations = %+v", list.Annotations[0])
	}
	sliced := oracle.Collections.Slice
	if len(sliced.Calls.ClaimSearch) != 1 ||
		!reflect.DeepEqual(sliced.Calls.ClaimSearch[0].ClaimIDs,
			[]string{"claim-b", "claim-a", "missing"}) ||
		!listResolutionOracleStrings(sliced.Claims, []*string{
			listResolutionString("claim-b"), listResolutionString("claim-a-first"), nil,
		}) {
		t.Fatalf("collection slice result = calls %+v claims %v", sliced.Calls, sliced.Claims)
	}
}

func assertGoListResolutionHelpers(t *testing.T, oracle listResolutionOracleResponse) {
	t.Helper()
	firstA := transactionResolvedEnrichmentClaim(t, 0x81)
	lastA := transactionResolvedEnrichmentClaim(t, 0x81)
	claimB := transactionResolvedEnrichmentClaim(t, 0x82)
	other := transactionResolvedEnrichmentClaim(t, 0x83)
	claimAID := transactionResolvedEnrichmentClaimID(t, firstA)
	claimBID := transactionResolvedEnrichmentClaimID(t, claimB)

	collection, err := mapTransactionCollectionClaims(
		[]string{claimBID, claimAID, "missing"},
		[]*TransactionOutput{firstA, lastA, claimB},
	)
	if err != nil {
		t.Fatal(err)
	}
	labels := listResolutionGoLabels(collection, map[*TransactionOutput]string{
		firstA: "claim-a-first", lastA: "claim-a-last", claimB: "claim-b",
	})
	if !listResolutionOracleStrings(labels, oracle.Collections.Slice.Claims) {
		t.Fatalf("Go collection mapping = %v, Python %v", labels, oracle.Collections.Slice.Claims)
	}

	purchased, err := mapTransactionResolvedClaims(
		[]string{claimAID, "missing", claimAID},
		[]*TransactionOutput{firstA, other, lastA},
	)
	if err != nil {
		t.Fatal(err)
	}
	labels = listResolutionGoLabels(purchased, map[*TransactionOutput]string{
		firstA: "claim-a-first", lastA: "claim-a-last", other: "claim-other",
	})
	if !listResolutionOracleStrings(labels, oracle.Purchases.Success.PurchasedClaims) {
		t.Fatalf("Go purchase mapping = %v, Python %v",
			labels, oracle.Purchases.Success.PurchasedClaims)
	}

	channel := &TransactionOutput{}
	privateKey := &keys.PrivateKey{}
	source := &TransactionOutput{
		IsInternalTransfer: listResolutionBool(true),
		IsSpent:            listResolutionBool(false),
		IsMyOutput:         listResolutionBool(true),
		IsMyInput:          listResolutionBool(false),
		SentSupports:       listResolutionInt(11),
		SentTips:           listResolutionInt(22),
		ReceivedTips:       listResolutionInt(33),
		Channel:            channel,
		PrivateKey:         privateKey,
		PurchaseReceipt:    &TransactionOutput{},
	}
	remoteReceipt := &TransactionOutput{}
	remoteRelated := &TransactionOutput{}
	target := &TransactionOutput{
		PurchaseReceipt: remoteReceipt, PurchasedClaim: remoteRelated,
		RepostedClaim: remoteRelated, Claims: []*TransactionOutput{remoteRelated},
		Meta: map[string]any{"remote": "preserved"},
	}
	copyLocalTransactionOutputAnnotations(target, source)
	want := oracle.ClaimResults.Success.Result[0]
	if !listResolutionOracleBool(target.IsInternalTransfer, *want.IsInternalTransfer) ||
		!listResolutionOracleBool(target.IsSpent, *want.IsSpent) ||
		!listResolutionOracleBool(target.IsMyOutput, *want.IsMyOutput) ||
		!listResolutionOracleBool(target.IsMyInput, *want.IsMyInput) ||
		!listResolutionOracleInt(target.SentSupports, *want.SentSupports) ||
		!listResolutionOracleInt(target.SentTips, *want.SentTips) ||
		!listResolutionOracleInt(target.ReceivedTips, *want.ReceivedTips) ||
		target.Channel != channel || target.PrivateKey != privateKey ||
		target.PurchaseReceipt != remoteReceipt || target.PurchasedClaim != remoteRelated ||
		target.RepostedClaim != remoteRelated || len(target.Claims) != 1 ||
		target.Claims[0] != remoteRelated || target.Meta["remote"] != "preserved" {
		t.Fatalf("Go local annotation copy = %+v, Python %+v", target, want)
	}
}

func listResolutionGoLabels(
	outputs []*TransactionOutput, labels map[*TransactionOutput]string,
) []*string {
	result := make([]*string, len(outputs))
	for index, output := range outputs {
		if output != nil {
			result[index] = listResolutionString(labels[output])
		}
	}
	return result
}

func listResolutionBool(value bool) *bool       { return &value }
func listResolutionInt(value int64) *int64      { return &value }
func listResolutionString(value string) *string { return &value }

func listResolutionOracleBool(value *bool, want bool) bool {
	return value != nil && *value == want
}

func listResolutionOracleInt(value *int64, want int64) bool {
	return value != nil && *value == want
}

func listResolutionOracleString(value *string, want string) bool {
	return value != nil && *value == want
}

func listResolutionOracleStrings(got, want []*string) bool {
	return reflect.DeepEqual(got, want)
}

func runListResolutionOracle(t *testing.T) listResolutionOracleResponse {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate list resolution oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot := os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	script := filepath.Join(daemonRoot, "compat", "list_resolution_oracle.py")
	for _, path := range []string{sdkRoot, script} {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			t.Skipf("list resolution oracle dependency is unavailable: %s", path)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	for relative := range listResolutionOracleSources {
		path := filepath.Join(sdkRoot, filepath.FromSlash(relative))
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			t.Skipf("list resolution oracle source is unavailable: %s", path)
		} else if err != nil {
			t.Fatal(err)
		}
	}

	command := exec.Command(python, script, "--sdk-root", sdkRoot)
	command.Env = append(os.Environ(), "PYTHONHASHSEED=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Python list resolution oracle failed: %v\n%s", err, output)
	}
	var oracle listResolutionOracleResponse
	if err := json.Unmarshal(output, &oracle); err != nil {
		t.Fatalf("decode Python list resolution oracle: %v\n%s", err, output)
	}
	return oracle
}
