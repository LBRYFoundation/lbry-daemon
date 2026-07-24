package wallet

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

const (
	transactionShowResolvedEnrichmentOracleCommit  = "e7666f489418e96b6d2104974e93915b539235c5"
	transactionShowResolvedEnrichmentOracleVersion = "0.113.0"
)

var transactionShowResolvedEnrichmentOracleMethods = map[string]string{
	"Daemon.jsonrpc_transaction_show":       "10ec4201cf4cce44bf3442ff6654732bc2d99a1634e905e5d65cc1741d898ad6",
	"Database.get_purchases":                "30771b54e4dd985ef5aa714f7a88534aed3362999d3d541631ce98e00bf0b901",
	"Database.get_transactions":             "5c5bff04bc5b5d0a3e3f402d421226ede46c59f9ba481137b9d6de2120efdf2f",
	"Database.tx_to_row":                    "631c38db61f579f5e4fc4f934da89e17fe5f86097646a758ed83c7b6f68b9c8b",
	"JSONResponseEncoder.__init__":          "bf1a658c1eed62bbae283ebe132f8067f986e534771ebf3417685536472fdb1e",
	"JSONResponseEncoder.default":           "298986ed087ef927a948ecc2d8f55730ca2e57a9c6ec032255d30bc92448c4a8",
	"JSONResponseEncoder.encode_claim":      "c537d439cc940682b1954615726587d125615e5d4bda62f26b9e78085c5ed088",
	"JSONResponseEncoder.encode_claim_meta": "7998df829f2f3a45d3f851ec1fa08910d4c6106c58a0a5a22690390ff8371c05",
	"JSONResponseEncoder.encode_output":     "fc124a8362451a2449d83b06e252d9c3d85ec6b006b5f9d0dc5dfd60b5db92be",
	"Ledger._inflate_outputs":               "2eb53ed61cabd4456010c5c3c23ec848c5888ca749acb68ec864fc1e92be5cfe",
	"Ledger.get_purchases":                  "a10b63da0d141f7f094eb0d85f8734f4743dcbb76b5fecb5928d692cb6fe2bbb",
	"Ledger.resolve_collection":             "9692e89042901f82f8a5f5cb06b300bf7ced49f5aadd4718da7ad2cc3c6c7ef3",
	"Output.purchased_claim_id":             "f2737848aa8850ab501dbbe429204a0b5f4d1bf9bae37f17dd91c0e4739375bf",
	"Outputs.message_to_txo":                "4369696def2c977a904df2db3d397219bf2b2e1a6e0c3550f3ad184b286d1ce5",
	"WalletManager.get_transaction":         "b71d91ee306c7fe80dbab674633b55b7a07adf314b2d4943e5414ef3641ad2aa",
}

type transactionShowResolvedEnrichmentOracleResponse struct {
	Reference struct {
		Commit       string            `json:"commit"`
		Version      string            `json:"version"`
		SourceSHA256 map[string]string `json:"source_sha256"`
		MethodSHA256 map[string]string `json:"method_sha256"`
	} `json:"reference"`
	Metadata struct {
		PythonVersion                              string            `json:"python_version"`
		ProtobufVersion                            string            `json:"protobuf_version"`
		RealV2ClaimObjectsExecuted                 bool              `json:"real_v2_claim_objects_executed"`
		ExtractedEncoderMethodsExecuted            bool              `json:"extracted_encoder_methods_executed"`
		ExtractedLedgerRelationshipMethodsExecuted bool              `json:"extracted_ledger_relationship_methods_executed"`
		ExternalNetworkUsed                        bool              `json:"external_network_used"`
		CaseCount                                  int               `json:"case_count"`
		SuccessCaseCount                           int               `json:"success_case_count"`
		ErrorCaseCount                             int               `json:"error_case_count"`
		TransactionShowContract                    map[string]string `json:"transaction_show_contract"`
		RelationshipProbes                         struct {
			Collection struct {
				AllReferenceIDs   []string  `json:"all_reference_ids"`
				RequestedClaimIDs []string  `json:"requested_claim_ids"`
				ResultLabels      []*string `json:"result_labels"`
				Offset            int       `json:"offset"`
				PageSize          int       `json:"page_size"`
				Selection         string    `json:"selection"`
			} `json:"collection"`
			PurchasedClaim struct {
				RequestedClaimIDs []string       `json:"requested_claim_ids"`
				ResultLabels      []*string      `json:"result_labels"`
				DBConstraints     map[string]any `json:"db_constraints"`
				Selection         string         `json:"selection"`
			} `json:"purchased_claim"`
			PurchaseReceipt struct {
				RequestedClaimIDs []string       `json:"requested_claim_ids"`
				ResultLabels      []*string      `json:"result_labels"`
				Offset            int            `json:"offset"`
				Total             int            `json:"total"`
				Blocked           map[string]any `json:"blocked"`
				Selection         string         `json:"selection"`
			} `json:"purchase_receipt"`
			Annotations struct {
				CopyCreated      bool           `json:"copy_created"`
				ChannelPreserved bool           `json:"channel_preserved"`
				Result           map[string]any `json:"result"`
				SourceAfter      map[string]any `json:"source_after"`
				Calls            []struct {
					Method      string         `json:"method"`
					Constraints map[string]any `json:"constraints"`
				} `json:"calls"`
				ReceivedOnly map[string]any `json:"received_only"`
			} `json:"annotations"`
		} `json:"relationship_probes"`
	} `json:"metadata"`
	AnnotationProbe struct {
		EncodedOutput          map[string]any `json:"encoded_output"`
		SourceMetaAfterEncode  map[string]any `json:"source_meta_after_encode"`
		FixtureMetaAfterEncode map[string]any `json:"fixture_meta_after_encode"`
	} `json:"annotation_probe"`
	Cases []transactionShowResolvedEnrichmentOracleCase `json:"cases"`
}

type transactionShowResolvedEnrichmentOracleCase struct {
	Name            string                              `json:"name"`
	SourceMode      string                              `json:"source_mode"`
	IncludeProtobuf bool                                `json:"include_protobuf"`
	CheckSignature  bool                                `json:"check_signature"`
	EncodedOutput   map[string]any                      `json:"encoded_output"`
	FieldOrder      map[string][]string                 `json:"field_order"`
	SignatureChecks []string                            `json:"signature_checks"`
	Error           *transactionShowResolvedOracleError `json:"error"`
}

type transactionShowResolvedOracleError struct {
	Stage   string `json:"stage"`
	Type    string `json:"type"`
	Module  string `json:"module"`
	Message string `json:"message"`
}

func TestTransactionShowResolvedEnrichmentPinnedOracle(t *testing.T) {
	oracle := runTransactionShowResolvedEnrichmentOracle(t)
	if oracle.Reference.Commit != transactionShowResolvedEnrichmentOracleCommit ||
		oracle.Reference.Version != transactionShowResolvedEnrichmentOracleVersion ||
		!reflect.DeepEqual(oracle.Reference.MethodSHA256, transactionShowResolvedEnrichmentOracleMethods) {
		t.Fatalf("resolved enrichment oracle reference = %+v", oracle.Reference)
	}
	if len(oracle.Reference.SourceSHA256) != 14 {
		t.Fatalf("resolved enrichment source count = %d, want 14", len(oracle.Reference.SourceSHA256))
	}
	metadata := oracle.Metadata
	if metadata.PythonVersion == "" || metadata.ProtobufVersion == "" ||
		!metadata.RealV2ClaimObjectsExecuted || !metadata.ExtractedEncoderMethodsExecuted ||
		!metadata.ExtractedLedgerRelationshipMethodsExecuted ||
		metadata.ExternalNetworkUsed || metadata.CaseCount != 18 ||
		metadata.SuccessCaseCount != 15 || metadata.ErrorCaseCount != 3 ||
		len(metadata.TransactionShowContract) != 8 || len(oracle.Cases) != 18 {
		t.Fatalf("resolved enrichment metadata = %+v, cases %d", metadata, len(oracle.Cases))
	}
	assertTransactionShowRelationshipProbes(t, oracle)
	assertTransactionShowAnnotationProbe(t, oracle)

	wantNames := []string{
		"repost_local_transaction_show_unresolved_plain",
		"repost_remote_resolved_protobuf",
		"repost_check_signature_false_nested_checked",
		"repost_nested_decode_error_suppressed",
		"collection_local_transaction_show_unresolved_plain",
		"collection_remote_resolved_ordered_protobuf",
		"collection_remote_resolved_empty",
		"collection_signed_channel_absent_fallback",
		"purchase_remote_transaction_show_unlinked_payment",
		"purchase_local_linked_unresolved",
		"purchase_resolved_claim_protobuf",
		"purchase_check_signature_false_nested_checked",
		"purchase_receipt_local_absent",
		"purchase_receipt_resolved_protobuf",
		"purchase_nested_decode_error_suppressed",
		"signature_error_propagates",
		"repost_cycle_recursion_error",
		"collection_invalid_entry_error",
	}
	byName := make(map[string]transactionShowResolvedEnrichmentOracleCase, len(oracle.Cases))
	for index, fixture := range oracle.Cases {
		if fixture.Name != wantNames[index] {
			t.Fatalf("resolved enrichment case %d = %q, want %q", index, fixture.Name, wantNames[index])
		}
		byName[fixture.Name] = fixture
	}

	assertTransactionShowResolvedReposts(t, byName)
	assertTransactionShowResolvedCollections(t, byName)
	assertTransactionShowResolvedPurchases(t, byName)
	assertTransactionShowResolvedFailures(t, byName)
}

func assertTransactionShowAnnotationProbe(
	t *testing.T, oracle transactionShowResolvedEnrichmentOracleResponse,
) {
	t.Helper()
	probe := oracle.AnnotationProbe
	output := probe.EncodedOutput
	if output["sent_supports"] != "0.0" || output["sent_tips"] != "1.23456789" ||
		output["received_tips"] != "-0.00000001" ||
		output["short_url"] != "lbry://annotation#1" ||
		output["canonical_url"] != "lbry://@channel#2/annotation#1" {
		t.Fatalf("resolved annotation probe output = %#v", output)
	}
	meta, ok := output["meta"].(map[string]any)
	if !ok || meta["effective_amount"] != "1.23456789" ||
		meta["support_amount"] != "0.0000001" || meta["truthy_amount"] != "0.00000001" ||
		meta["floating_amount"] != float64(2) || meta["text_amount"] != "3" ||
		meta["creation_height"] != float64(3) || meta["creation_timestamp"] != float64(10_003) {
		t.Fatalf("resolved annotation probe meta = %#v", meta)
	}
	if _, exists := meta["short_url"]; exists {
		t.Fatalf("resolved annotation meta retained short_url: %#v", meta)
	}
	if _, exists := meta["canonical_url"]; exists {
		t.Fatalf("resolved annotation meta retained canonical_url: %#v", meta)
	}
	if !reflect.DeepEqual(probe.SourceMetaAfterEncode, probe.FixtureMetaAfterEncode) ||
		probe.FixtureMetaAfterEncode["short_url"] != "lbry://annotation#1" ||
		probe.FixtureMetaAfterEncode["effective_amount"] != float64(123_456_789) ||
		probe.FixtureMetaAfterEncode["creation_timestamp"] != float64(999) {
		t.Fatalf("resolved annotation source meta mutated: before %#v after %#v",
			probe.SourceMetaAfterEncode, probe.FixtureMetaAfterEncode)
	}
}

func assertTransactionShowRelationshipProbes(
	t *testing.T, oracle transactionShowResolvedEnrichmentOracleResponse,
) {
	t.Helper()
	probes := oracle.Metadata.RelationshipProbes
	claimA, claimB, claimC := strings.Repeat("11", 20), strings.Repeat("22", 20), strings.Repeat("33", 20)
	collection := probes.Collection
	if !reflect.DeepEqual(collection.AllReferenceIDs, []string{claimC, claimA, claimB, claimA, claimC}) ||
		!reflect.DeepEqual(collection.RequestedClaimIDs, []string{claimA, claimB, claimA, claimC}) ||
		collection.Offset != 1 || collection.PageSize != 4 ||
		collection.Selection != "first matching resolve result is reused for repeated ids" {
		t.Fatalf("collection relationship probe = %+v", collection)
	}
	assertResolvedOracleOptionalStrings(t, "collection probe", collection.ResultLabels,
		[]*string{resolvedOracleString("claim-a-first"), resolvedOracleString("claim-b"), resolvedOracleString("claim-a-first"), nil})

	purchased := probes.PurchasedClaim
	if !reflect.DeepEqual(purchased.RequestedClaimIDs, []string{claimA, claimC}) ||
		purchased.Selection != "claim-id lookup dict is last-result-wins" {
		t.Fatalf("purchased claim relationship probe = %+v", purchased)
	}
	assertResolvedOracleOptionalStrings(t, "purchased claim probe", purchased.ResultLabels,
		[]*string{resolvedOracleString("claim-a-second"), nil})

	receipt := probes.PurchaseReceipt
	if !reflect.DeepEqual(receipt.RequestedClaimIDs, []string{claimA, claimB}) ||
		receipt.Offset != 7 || receipt.Total != 19 ||
		receipt.Selection != "receipt lookup dict is last-result-wins" {
		t.Fatalf("purchase receipt relationship probe = %+v", receipt)
	}
	assertResolvedOracleOptionalStrings(t, "purchase receipt probe", receipt.ResultLabels,
		[]*string{resolvedOracleString("receipt-a-last"), resolvedOracleString("receipt-b")})

	annotations := probes.Annotations
	if !annotations.CopyCreated || !annotations.ChannelPreserved ||
		annotations.Result["is_spent"] != nil || annotations.Result["is_my_output"] != true ||
		annotations.Result["is_my_input"] != nil || annotations.Result["is_internal_transfer"] != nil ||
		annotations.Result["sent_supports"] != float64(11) ||
		annotations.Result["sent_tips"] != float64(22) ||
		annotations.Result["received_tips"] != float64(33) ||
		annotations.Result["private_key"] != nil || annotations.Result["purchase_receipt"] != nil {
		t.Fatalf("inflate annotation result = %+v", annotations)
	}
	if annotations.SourceAfter["sent_supports"] != float64(901) ||
		annotations.SourceAfter["sent_tips"] != float64(902) ||
		annotations.SourceAfter["received_tips"] != float64(903) ||
		annotations.SourceAfter["private_key"] != "cached-private-key" ||
		annotations.SourceAfter["purchase_receipt"] != "receipt-a-first" {
		t.Fatalf("inflate annotation source = %+v", annotations.SourceAfter)
	}
	if len(annotations.Calls) != 4 || annotations.Calls[0].Method != "get_txo_count" {
		t.Fatalf("inflate annotation calls = %+v", annotations.Calls)
	}
	wantDirections := [][3]any{
		{true, true, false},
		{true, false, nil},
		{false, true, nil},
	}
	for index, want := range wantDirections {
		call := annotations.Calls[index+1]
		constraints := call.Constraints
		if call.Method != "get_txo_sum" || constraints["is_my_input"] != want[0] ||
			constraints["is_my_output"] != want[1] || constraints["is_spent"] != want[2] ||
			constraints["txo_type"] != float64(TransactionOutputTypeSupport) {
			t.Fatalf("inflate annotation sum call %d = %+v, want direction %+v", index, call, want)
		}
	}
	if annotations.ReceivedOnly["received_tips"] != nil ||
		annotations.ReceivedOnly["source_received_tips"] != float64(904) ||
		annotations.ReceivedOnly["call_count"] != float64(0) {
		t.Fatalf("inflate received-only gate = %+v", annotations.ReceivedOnly)
	}
}

func resolvedOracleString(value string) *string {
	return &value
}

func assertResolvedOracleOptionalStrings(t *testing.T, label string, got, want []*string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
	for index := range want {
		if (got[index] == nil) != (want[index] == nil) ||
			(got[index] != nil && *got[index] != *want[index]) {
			t.Fatalf("%s = %v, want %v", label, got, want)
		}
	}
}

func assertTransactionShowResolvedReposts(
	t *testing.T, fixtures map[string]transactionShowResolvedEnrichmentOracleCase,
) {
	t.Helper()
	local := fixtures["repost_local_transaction_show_unresolved_plain"]
	assertResolvedOracleSuccess(t, local)
	assertResolvedOracleString(t, local.EncodedOutput, "value_type", "repost")
	assertResolvedOracleAbsent(t, local.EncodedOutput, "reposted_claim", "protobuf")

	remote := fixtures["repost_remote_resolved_protobuf"]
	assertResolvedOracleSuccess(t, remote)
	reposted := assertResolvedOracleMap(t, remote.EncodedOutput, "reposted_claim")
	assertResolvedOracleString(t, reposted, "name", "original")
	assertResolvedOracleString(t, reposted, "value_type", "stream")
	assertResolvedOracleTruthyString(t, remote.EncodedOutput, "protobuf")
	assertResolvedOracleTruthyString(t, reposted, "protobuf")
	channel := assertResolvedOracleMap(t, reposted, "signing_channel")
	assertResolvedOracleTruthyString(t, channel, "protobuf")
	assertResolvedOracleBool(t, reposted, "is_channel_signature_valid", true)
	if !reflect.DeepEqual(remote.SignatureChecks, []string{"original"}) {
		t.Fatalf("remote repost signature checks = %v", remote.SignatureChecks)
	}
	assertResolvedOracleOrderBefore(t, remote.FieldOrder["$"], "reposted_claim", "value")
	assertResolvedOracleOrderBefore(t, remote.FieldOrder["$"], "value", "protobuf")

	noCheck := fixtures["repost_check_signature_false_nested_checked"]
	assertResolvedOracleSuccess(t, noCheck)
	assertResolvedOracleAbsent(t, noCheck.EncodedOutput, "signing_channel", "is_channel_signature_valid")
	nested := assertResolvedOracleMap(t, noCheck.EncodedOutput, "reposted_claim")
	assertResolvedOracleBool(t, nested, "is_channel_signature_valid", true)
	if noCheck.CheckSignature || !reflect.DeepEqual(noCheck.SignatureChecks, []string{"nested-original"}) {
		t.Fatalf("repost check_signature recursion = check %v, calls %v", noCheck.CheckSignature, noCheck.SignatureChecks)
	}

	malformed := fixtures["repost_nested_decode_error_suppressed"]
	assertResolvedOracleSuccess(t, malformed)
	malformedNested := assertResolvedOracleMap(t, malformed.EncodedOutput, "reposted_claim")
	assertResolvedOracleAbsent(t, malformedNested, "value", "value_type", "protobuf")
	assertResolvedOracleTruthyString(t, malformed.EncodedOutput, "protobuf")
}

func assertTransactionShowResolvedCollections(
	t *testing.T, fixtures map[string]transactionShowResolvedEnrichmentOracleCase,
) {
	t.Helper()
	local := fixtures["collection_local_transaction_show_unresolved_plain"]
	assertResolvedOracleSuccess(t, local)
	assertResolvedOracleAbsent(t, local.EncodedOutput, "claims")
	value := assertResolvedOracleMap(t, local.EncodedOutput, "value")
	if claimIDs := assertResolvedOracleList(t, value, "claims"); len(claimIDs) != 3 {
		t.Fatalf("unresolved collection value references = %v", claimIDs)
	}

	resolved := fixtures["collection_remote_resolved_ordered_protobuf"]
	assertResolvedOracleSuccess(t, resolved)
	claims := assertResolvedOracleList(t, resolved.EncodedOutput, "claims")
	if len(claims) != 3 || claims[1] != nil {
		t.Fatalf("resolved collection claims = %#v", claims)
	}
	first, ok := claims[0].(map[string]any)
	if !ok {
		t.Fatalf("resolved collection first claim = %#v", claims[0])
	}
	third, ok := claims[2].(map[string]any)
	if !ok {
		t.Fatalf("resolved collection third claim = %#v", claims[2])
	}
	assertResolvedOracleString(t, first, "name", "alpha")
	assertResolvedOracleString(t, third, "name", "beta")
	assertResolvedOracleTruthyString(t, resolved.EncodedOutput, "protobuf")
	assertResolvedOracleTruthyString(t, first, "protobuf")
	assertResolvedOracleTruthyString(t, third, "protobuf")
	if !reflect.DeepEqual(resolved.SignatureChecks, []string{"alpha"}) {
		t.Fatalf("collection signature checks = %v", resolved.SignatureChecks)
	}
	assertResolvedOracleOrderBefore(t, resolved.FieldOrder["$"], "claims", "value")

	empty := fixtures["collection_remote_resolved_empty"]
	assertResolvedOracleSuccess(t, empty)
	if claims := assertResolvedOracleList(t, empty.EncodedOutput, "claims"); len(claims) != 0 {
		t.Fatalf("empty resolved collection claims = %#v", claims)
	}

	fallback := fixtures["collection_signed_channel_absent_fallback"]
	assertResolvedOracleSuccess(t, fallback)
	orphan := assertResolvedOracleList(t, fallback.EncodedOutput, "claims")[0].(map[string]any)
	stub := assertResolvedOracleMap(t, orphan, "signing_channel")
	assertResolvedOracleString(t, stub, "channel_id", strings.Repeat("aa", 20))
	assertResolvedOracleBool(t, orphan, "is_channel_signature_valid", false)
	if len(fallback.SignatureChecks) != 0 {
		t.Fatalf("unhydrated collection claim checked signature: %v", fallback.SignatureChecks)
	}
}

func assertTransactionShowResolvedPurchases(
	t *testing.T, fixtures map[string]transactionShowResolvedEnrichmentOracleCase,
) {
	t.Helper()
	remote := fixtures["purchase_remote_transaction_show_unlinked_payment"]
	assertResolvedOracleSuccess(t, remote)
	assertResolvedOracleString(t, remote.EncodedOutput, "type", "payment")
	assertResolvedOracleAbsent(t, remote.EncodedOutput, "claim_id", "claim", "protobuf")

	local := fixtures["purchase_local_linked_unresolved"]
	assertResolvedOracleSuccess(t, local)
	assertResolvedOracleString(t, local.EncodedOutput, "type", "purchase")
	assertResolvedOracleString(t, local.EncodedOutput, "claim_id", strings.Repeat("11", 20))
	assertResolvedOracleAbsent(t, local.EncodedOutput, "claim")

	resolved := fixtures["purchase_resolved_claim_protobuf"]
	assertResolvedOracleSuccess(t, resolved)
	claim := assertResolvedOracleMap(t, resolved.EncodedOutput, "claim")
	assertResolvedOracleString(t, claim, "name", "purchased-stream")
	assertResolvedOracleTruthyString(t, claim, "protobuf")
	assertResolvedOracleAbsent(t, resolved.EncodedOutput, "protobuf")
	if !reflect.DeepEqual(resolved.SignatureChecks, []string{"purchased-stream"}) {
		t.Fatalf("resolved purchase signature checks = %v", resolved.SignatureChecks)
	}
	assertResolvedOracleOrderBefore(t, resolved.FieldOrder["$"], "claim_id", "claim")

	noCheck := fixtures["purchase_check_signature_false_nested_checked"]
	assertResolvedOracleSuccess(t, noCheck)
	nested := assertResolvedOracleMap(t, noCheck.EncodedOutput, "claim")
	assertResolvedOracleBool(t, nested, "is_channel_signature_valid", true)
	if noCheck.CheckSignature || !reflect.DeepEqual(noCheck.SignatureChecks, []string{"nested-purchased"}) {
		t.Fatalf("purchase check_signature recursion = check %v, calls %v", noCheck.CheckSignature, noCheck.SignatureChecks)
	}

	absent := fixtures["purchase_receipt_local_absent"]
	assertResolvedOracleSuccess(t, absent)
	assertResolvedOracleAbsent(t, absent.EncodedOutput, "purchase_receipt")

	receiptCase := fixtures["purchase_receipt_resolved_protobuf"]
	assertResolvedOracleSuccess(t, receiptCase)
	receipt := assertResolvedOracleMap(t, receiptCase.EncodedOutput, "purchase_receipt")
	assertResolvedOracleString(t, receipt, "type", "purchase")
	assertResolvedOracleString(t, receipt, "claim_id", strings.Repeat("11", 20))
	assertResolvedOracleAbsent(t, receipt, "claim", "protobuf")
	assertResolvedOracleTruthyString(t, receiptCase.EncodedOutput, "protobuf")
	order := receiptCase.FieldOrder["$"]
	assertResolvedOracleOrderBefore(t, order, "protobuf", "purchase_receipt")
	assertResolvedOracleOrderBefore(t, order, "purchase_receipt", "value_type")

	malformed := fixtures["purchase_nested_decode_error_suppressed"]
	assertResolvedOracleSuccess(t, malformed)
	malformedClaim := assertResolvedOracleMap(t, malformed.EncodedOutput, "claim")
	assertResolvedOracleAbsent(t, malformedClaim, "value", "value_type", "protobuf")
}

func assertTransactionShowResolvedFailures(
	t *testing.T, fixtures map[string]transactionShowResolvedEnrichmentOracleCase,
) {
	t.Helper()
	wants := map[string]struct {
		typeName string
		message  string
	}{
		"signature_error_propagates":     {"RuntimeError", "fixture signature verification failed"},
		"repost_cycle_recursion_error":   {"RecursionError", "maximum recursion depth exceeded"},
		"collection_invalid_entry_error": {"AttributeError", "'object' object has no attribute 'tx_ref'"},
	}
	for name, want := range wants {
		fixture := fixtures[name]
		if fixture.Error == nil || fixture.EncodedOutput != nil ||
			fixture.Error.Stage != "JSONResponseEncoder.encode_output" ||
			fixture.Error.Type != want.typeName || fixture.Error.Module != "builtins" ||
			fixture.Error.Message != want.message {
			t.Fatalf("resolved enrichment failure %q = %+v", name, fixture)
		}
	}
	if got := fixtures["signature_error_propagates"].SignatureChecks; !reflect.DeepEqual(got, []string{"signature-failure-original"}) {
		t.Fatalf("signature failure calls = %v", got)
	}
}

func assertResolvedOracleSuccess(t *testing.T, fixture transactionShowResolvedEnrichmentOracleCase) {
	t.Helper()
	if fixture.Error != nil || fixture.EncodedOutput == nil || fixture.FieldOrder == nil {
		t.Fatalf("resolved enrichment case %q failed: output=%#v error=%+v", fixture.Name, fixture.EncodedOutput, fixture.Error)
	}
}

func assertResolvedOracleMap(t *testing.T, value map[string]any, key string) map[string]any {
	t.Helper()
	got, ok := value[key].(map[string]any)
	if !ok {
		t.Fatalf("resolved enrichment field %q = %#v, want object", key, value[key])
	}
	return got
}

func assertResolvedOracleList(t *testing.T, value map[string]any, key string) []any {
	t.Helper()
	got, ok := value[key].([]any)
	if !ok {
		t.Fatalf("resolved enrichment field %q = %#v, want list", key, value[key])
	}
	return got
}

func assertResolvedOracleString(t *testing.T, value map[string]any, key, want string) {
	t.Helper()
	if got, ok := value[key].(string); !ok || got != want {
		t.Fatalf("resolved enrichment field %q = %#v, want %q", key, value[key], want)
	}
}

func assertResolvedOracleTruthyString(t *testing.T, value map[string]any, key string) {
	t.Helper()
	if got, ok := value[key].(string); !ok || got == "" {
		t.Fatalf("resolved enrichment field %q = %#v, want non-empty string", key, value[key])
	}
}

func assertResolvedOracleBool(t *testing.T, value map[string]any, key string, want bool) {
	t.Helper()
	if got, ok := value[key].(bool); !ok || got != want {
		t.Fatalf("resolved enrichment field %q = %#v, want %v", key, value[key], want)
	}
}

func assertResolvedOracleAbsent(t *testing.T, value map[string]any, keys ...string) {
	t.Helper()
	for _, key := range keys {
		if _, exists := value[key]; exists {
			t.Fatalf("resolved enrichment field %q unexpectedly exists in %#v", key, value)
		}
	}
}

func assertResolvedOracleOrderBefore(t *testing.T, order []string, before, after string) {
	t.Helper()
	beforeIndex, afterIndex := -1, -1
	for index, field := range order {
		if field == before {
			beforeIndex = index
		}
		if field == after {
			afterIndex = index
		}
	}
	if beforeIndex < 0 || afterIndex < 0 || beforeIndex >= afterIndex {
		t.Fatalf("resolved enrichment field order %v does not place %q before %q", order, before, after)
	}
}

func runTransactionShowResolvedEnrichmentOracle(t *testing.T) transactionShowResolvedEnrichmentOracleResponse {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate transaction show resolved enrichment oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot := os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	script := filepath.Join(daemonRoot, "compat", "transaction_show_resolved_enrichment_oracle.py")
	for _, path := range []string{sdkRoot, script} {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			t.Skipf("transaction show resolved enrichment oracle dependency is unavailable: %s", path)
		} else if err != nil {
			t.Fatal(err)
		}
	}

	command := exec.Command(python, script, "--sdk-root", sdkRoot)
	command.Env = append(os.Environ(), "PROTOCOL_BUFFERS_PYTHON_IMPLEMENTATION=python")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Python transaction show resolved enrichment oracle failed: %v\n%s", err, output)
	}
	var oracle transactionShowResolvedEnrichmentOracleResponse
	if err := json.Unmarshal(output, &oracle); err != nil {
		t.Fatalf("decode transaction show resolved enrichment oracle: %v\n%s", err, output)
	}
	return oracle
}
