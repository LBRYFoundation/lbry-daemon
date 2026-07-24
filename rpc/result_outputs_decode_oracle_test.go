package rpc

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"testing"

	walletpkg "lbry/daemon/wallet"
)

const (
	resultOutputsDecodeOracleCommit  = "e7666f489418e96b6d2104974e93915b539235c5"
	resultOutputsDecodeOracleVersion = "0.113.0"
)

var resultOutputsDecodeOracleSources = map[string]string{
	"lbry/__init__.py":                   "eb88d95a4a3d876da9815eb3bdb97b536958bf51edddcde17c04ffcebffd9238",
	"lbry/schema/result.py":              "b5a506fedc9f40c5e9ea1b0691e1e36f9559acaabafe9e3599ed7db52031a4cf",
	"lbry/schema/types/v2/result_pb2.py": "05e396ccaf0bd2385582292d26a5554619d3d1b4a04a915c513c2e187368d096",
}

var resultOutputsDecodeOracleMethods = map[string]string{
	"Outputs.__init__":        "a36513bf64852324e9a3061f002cda3e44403f5cfdc431b0eddc4533df0b55c5",
	"Outputs.inflate":         "61bfff753fc883560eb1982a08316fc2b0bd8e5aa7fe2ca143dd8a50b71d5870",
	"Outputs.inflate_blocked": "e66de7986c315fd7982f58ba68ca5c110282a279ee18e75a6246d25ee8734343",
	"Outputs.message_to_txo":  "4369696def2c977a904df2db3d397219bf2b2e1a6e0c3550f3ad184b286d1ce5",
	"Outputs.from_base64":     "ba9757279c4464d989292e744f4c35c421d8b09c4d188b6e6bc9affcc44b901d",
	"Outputs.from_bytes":      "c0419687f777e73f8017df2d8c8b4048d5beb64f2db5edfb5e5ece39b2b353a6",
}

type resultOutputsDecodeOracleResponse struct {
	Reference struct {
		Commit       string            `json:"commit"`
		Version      string            `json:"version"`
		SourceSHA256 map[string]string `json:"source_sha256"`
		MethodSHA256 map[string]string `json:"method_sha256"`
	} `json:"reference"`
	Metadata struct {
		PythonVersion                   string            `json:"python_version"`
		ProtobufVersion                 string            `json:"protobuf_version"`
		GeneratedResultPB2Executed      bool              `json:"generated_result_pb2_executed"`
		ExtractedOutputsMethodsExecuted bool              `json:"extracted_outputs_methods_executed"`
		ExternalNetworkUsed             bool              `json:"external_network_used"`
		CaseCount                       int               `json:"case_count"`
		DecodeErrorCount                int               `json:"decode_error_count"`
		InflateErrorCount               int               `json:"inflate_error_count"`
		ProposedGoContract              map[string]string `json:"proposed_go_contract"`
		ContractNotes                   map[string]string `json:"contract_notes"`
	} `json:"metadata"`
	Cases []resultOutputsDecodeOracleCase `json:"cases"`
}

type resultOutputsDecodeOracleCase struct {
	Name                      string                    `json:"name"`
	InputBase64               string                    `json:"input_base64"`
	Decoded                   map[string]any            `json:"decoded"`
	Inflated                  map[string]any            `json:"inflated"`
	DecodeError               *resultOutputsOracleError `json:"decode_error"`
	InflateError              *resultOutputsOracleError `json:"inflate_error"`
	MessageReserializedBase64 string                    `json:"message_reserialized_base64"`
	WireRoundTripIdentical    bool                      `json:"wire_round_trip_identical"`
}

type resultOutputsOracleError struct {
	Stage   string `json:"stage"`
	Type    string `json:"type"`
	Module  string `json:"module"`
	Message string `json:"message"`
}

func TestResultOutputsDecodePinnedOracle(t *testing.T) {
	oracle := runResultOutputsDecodeOracle(t)
	if oracle.Reference.Commit != resultOutputsDecodeOracleCommit ||
		oracle.Reference.Version != resultOutputsDecodeOracleVersion ||
		!reflect.DeepEqual(oracle.Reference.SourceSHA256, resultOutputsDecodeOracleSources) ||
		!reflect.DeepEqual(oracle.Reference.MethodSHA256, resultOutputsDecodeOracleMethods) {
		t.Fatalf("result Outputs oracle reference = %+v", oracle.Reference)
	}
	metadata := oracle.Metadata
	if metadata.PythonVersion == "" || metadata.ProtobufVersion == "" ||
		!metadata.GeneratedResultPB2Executed || !metadata.ExtractedOutputsMethodsExecuted ||
		metadata.ExternalNetworkUsed || metadata.CaseCount != 15 ||
		metadata.DecodeErrorCount != 2 || metadata.InflateErrorCount != 3 ||
		len(oracle.Cases) != 15 {
		t.Fatalf("result Outputs oracle metadata = %+v, cases %d", metadata, len(oracle.Cases))
	}
	wantContract := map[string]string{
		"types":         "HubOutputs, HubOutput, HubClaimMeta, HubError, HubBlocked",
		"decode_bytes":  "DecodeHubOutputsBytes([]byte) (*HubOutputs, error)",
		"decode_base64": "DecodeHubOutputsBase64(string) (*HubOutputs, error)",
		"requests":      "(*HubOutputs).TransactionRequests() []HubTransactionRequest",
		"inflate":       "(*HubOutputs).Inflate([]*Transaction) ([]any, HubBlockedSummary, error)",
		"note":          "Names are advisory; fixture semantics and wire bytes are normative.",
	}
	if !reflect.DeepEqual(metadata.ProposedGoContract, wantContract) {
		t.Fatalf("result Outputs proposed Go contract = %#v", metadata.ProposedGoContract)
	}
	wantNotes := map[string]string{
		"transaction_requests": "Python stores a deduplicated set of top-level non-error tx/hash pairs; the oracle sorts that set only for deterministic JSON.",
		"nested_references":    "Channel, repost, censor, and blocked references do not add fetch requests; their transactions must also appear as top-level txos or extra_txos.",
		"unknown_fields":       "Outputs semantics ignore unknown fields. Generated result_pb2 preserves them when directly reserialized, but Outputs exposes no reserialize method.",
	}
	if !reflect.DeepEqual(metadata.ContractNotes, wantNotes) {
		t.Fatalf("result Outputs contract notes = %#v", metadata.ContractNotes)
	}

	wantNames := []string{
		"canonical_relationship_graph",
		"duplicate_scalar_fields_last_value_wins",
		"claim_then_error_oneof_error_wins",
		"error_then_claim_oneof_claim_wins",
		"repeated_same_claim_member_merges",
		"unknown_fields_preserved_and_ignored",
		"known_field_wrong_wire_type_is_unknown",
		"unknown_error_enum_decodes_then_inflate_fails",
		"empty_payload_defaults",
		"non_alphabet_base64_noise_decodes_empty",
		"missing_base64_padding_fails",
		"truncated_protobuf_fails",
		"output_index_out_of_range_fails_inflate",
		"missing_relationship_transaction_fails_inflate",
		"duplicate_supplied_transaction_hash_last_value_wins",
	}
	byName := make(map[string]resultOutputsDecodeOracleCase, len(oracle.Cases))
	for index, fixture := range oracle.Cases {
		if fixture.Name != wantNames[index] {
			t.Fatalf("result Outputs case %d = %q, want %q", index, fixture.Name, wantNames[index])
		}
		byName[fixture.Name] = fixture
	}

	assertResultOutputsGraphOracle(t, byName["canonical_relationship_graph"])
	assertResultOutputsWireSemanticsOracle(t, byName)
	assertResultOutputsFailureOracle(t, byName)
	assertGoResultOutputsDecodeOracle(t, oracle.Cases)
}

func assertGoResultOutputsDecodeOracle(
	t *testing.T, fixtures []resultOutputsDecodeOracleCase,
) {
	t.Helper()
	for _, fixture := range fixtures {
		t.Run("Go decode "+fixture.Name, func(t *testing.T) {
			decoded, err := walletpkg.DecodeHubOutputsBase64(fixture.InputBase64)
			if fixture.DecodeError != nil {
				if err == nil {
					t.Fatal("Go decode unexpectedly succeeded")
				}
				switch fixture.DecodeError.Type {
				case "Error":
					if !errors.Is(err, walletpkg.ErrInvalidHubOutputsBase64) {
						t.Fatalf("Go base64 error = %v", err)
					}
				case "DecodeError":
					var decodeError *walletpkg.HubOutputsDecodeError
					if !errors.As(err, &decodeError) ||
						decodeError.PythonErrorName() != "DecodeError" {
						t.Fatalf("Go protobuf error = %#v", err)
					}
				default:
					t.Fatalf("unhandled pinned decode error = %+v", fixture.DecodeError)
				}
				return
			}
			if err != nil {
				t.Fatalf("Go decode failed: %v", err)
			}
			got := summarizeGoHubOutputs(t, decoded)
			want := make(map[string]any, len(fixture.Decoded)-1)
			for key, value := range fixture.Decoded {
				if key != "txo_reserialized_base64" {
					want[key] = value
				}
			}
			if !reflect.DeepEqual(got, want) {
				gotJSON, _ := json.Marshal(got)
				wantJSON, _ := json.Marshal(want)
				t.Fatalf("Go decoded Outputs = %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

func summarizeGoHubOutputs(t *testing.T, outputs *walletpkg.HubOutputs) map[string]any {
	t.Helper()
	requests := outputs.TransactionRequests()
	sort.Slice(requests, func(left, right int) bool {
		if requests[left].TxID != requests[right].TxID {
			return requests[left].TxID < requests[right].TxID
		}
		return requests[left].Height < requests[right].Height
	})
	transactions := make([]any, len(requests))
	for index, request := range requests {
		transactions[index] = map[string]any{
			"txid": request.TxID, "height": float64(request.Height),
		}
	}
	txos := make([]any, len(outputs.TXOs))
	for index, output := range outputs.TXOs {
		txos[index] = summarizeGoHubOutput(t, output)
	}
	extra := make([]any, len(outputs.ExtraTXOs))
	for index, output := range outputs.ExtraTXOs {
		extra[index] = summarizeGoHubOutput(t, output)
	}
	blocked := make([]any, len(outputs.Blocked))
	for index, entry := range outputs.Blocked {
		blocked[index] = map[string]any{
			"count":   float64(entry.Count),
			"channel": summarizeGoHubOutput(t, entry.Channel),
		}
	}
	return map[string]any{
		"offset":        float64(outputs.Offset),
		"total":         float64(outputs.Total),
		"blocked_total": float64(outputs.BlockedTotal),
		"txs":           transactions,
		"txos":          txos,
		"extra_txos":    extra,
		"blocked":       blocked,
	}
}

func summarizeGoHubOutput(t *testing.T, output *walletpkg.HubOutput) map[string]any {
	t.Helper()
	result := map[string]any{
		"tx_hash": hex.EncodeToString(output.TransactionHash),
		"nout":    float64(output.Position),
		"height":  float64(output.Height),
		"meta":    nil,
	}
	if output.Claim != nil {
		result["meta"] = "claim"
		result["claim"] = map[string]any{
			"short_url":         output.Claim.ShortURL,
			"canonical_url":     output.Claim.CanonicalURL,
			"is_controlling":    output.Claim.IsControlling,
			"take_over_height":  float64(output.Claim.TakeOverHeight),
			"creation_height":   float64(output.Claim.CreationHeight),
			"activation_height": float64(output.Claim.ActivationHeight),
			"expiration_height": float64(output.Claim.ExpirationHeight),
			"claims_in_channel": float64(output.Claim.ClaimsInChannel),
			"reposted":          float64(output.Claim.Reposted),
			"effective_amount":  float64(output.Claim.EffectiveAmount),
			"support_amount":    float64(output.Claim.SupportAmount),
			"has_channel":       output.Claim.Channel != nil,
			"has_repost":        output.Claim.Repost != nil,
		}
	} else if output.Error != nil {
		result["meta"] = "error"
		name, err := output.Error.Code.Name()
		var wireName any
		if err == nil {
			wireName = name
		}
		result["error"] = map[string]any{
			"code":        float64(output.Error.Code),
			"name":        wireName,
			"text":        output.Error.Text,
			"has_blocked": output.Error.Blocked != nil,
		}
	}
	return result
}

func assertResultOutputsGraphOracle(t *testing.T, fixture resultOutputsDecodeOracleCase) {
	t.Helper()
	if fixture.DecodeError != nil || fixture.InflateError != nil ||
		fixture.InputBase64 == "" || !fixture.WireRoundTripIdentical ||
		fixture.MessageReserializedBase64 != fixture.InputBase64 {
		t.Fatalf("canonical result Outputs graph envelope = %+v", fixture)
	}
	decoded := fixture.Decoded
	if resultOutputsNumber(t, decoded, "offset") != 7 ||
		resultOutputsNumber(t, decoded, "total") != 13 ||
		resultOutputsNumber(t, decoded, "blocked_total") != 4 {
		t.Fatalf("canonical result Outputs pagination = %#v", decoded)
	}
	txRequests := resultOutputsList(t, decoded, "txs")
	if len(txRequests) != 5 ||
		resultOutputsString(t, resultOutputsMapValue(t, txRequests[0]), "txid") !=
			"1f1e1d1c1b1a191817161514131211100f0e0d0c0b0a09080706050403020100" ||
		resultOutputsNumber(t, resultOutputsMapValue(t, txRequests[0]), "height") != 90 {
		t.Fatalf("canonical transaction requests = %#v", txRequests)
	}

	txos := resultOutputsList(t, decoded, "txos")
	if len(txos) != 6 {
		t.Fatalf("canonical decoded txos = %#v", txos)
	}
	wantMeta := []any{"claim", nil, "claim", "error", "error", "error"}
	for index, want := range wantMeta {
		if got := resultOutputsMapValue(t, txos[index])["meta"]; got != want {
			t.Fatalf("canonical decoded txo %d meta = %#v, want %#v", index, got, want)
		}
	}
	rootClaim := resultOutputsMap(t, resultOutputsMapValue(t, txos[0]), "claim")
	if !resultOutputsBool(t, rootClaim, "has_channel") ||
		!resultOutputsBool(t, rootClaim, "has_repost") ||
		resultOutputsString(t, rootClaim, "short_url") != "repost#r" ||
		resultOutputsNumber(t, rootClaim, "effective_amount") != 123456789 ||
		resultOutputsNumber(t, rootClaim, "claims_in_channel") != 99 {
		t.Fatalf("canonical decoded root claim = %#v", rootClaim)
	}
	if extras := resultOutputsList(t, decoded, "extra_txos"); len(extras) != 2 {
		t.Fatalf("canonical decoded extras = %#v", extras)
	}
	if blocked := resultOutputsList(t, decoded, "blocked"); len(blocked) != 1 ||
		resultOutputsNumber(t, resultOutputsMapValue(t, blocked[0]), "count") != 3 {
		t.Fatalf("canonical decoded blocked = %#v", blocked)
	}

	inflatedTXOs := resultOutputsList(t, fixture.Inflated, "txos")
	if len(inflatedTXOs) != 6 || inflatedTXOs[2] != nil {
		t.Fatalf("canonical inflated txos = %#v", inflatedTXOs)
	}
	root := resultOutputsMapValue(t, inflatedTXOs[0])
	rootMeta := resultOutputsMap(t, root, "meta")
	if resultOutputsString(t, root, "label") != "root-repost" ||
		resultOutputsString(t, root, "channel") != "signing-channel" ||
		resultOutputsString(t, root, "reposted_claim") != "reposted-stream" ||
		resultOutputsString(t, rootMeta, "short_url") != "lbry://repost#r" ||
		resultOutputsString(t, rootMeta, "canonical_url") != "lbry://@channel#c/repost#r" {
		t.Fatalf("canonical inflated root = %#v", root)
	}
	if _, exists := rootMeta["claims_in_channel"]; exists {
		t.Fatalf("non-channel root retained claims_in_channel: %#v", rootMeta)
	}
	if resultOutputsString(t, resultOutputsMap(t, resultOutputsMapValue(t, inflatedTXOs[3]), "error"), "name") != "NOT_FOUND" ||
		resultOutputsString(t, resultOutputsMap(t, resultOutputsMapValue(t, inflatedTXOs[4]), "error"), "name") != "INVALID" {
		t.Fatalf("canonical inflated errors = %#v", inflatedTXOs[3:5])
	}
	blockedError := resultOutputsMap(t, resultOutputsMapValue(t, inflatedTXOs[5]), "error")
	if resultOutputsString(t, blockedError, "name") != "BLOCKED" ||
		resultOutputsString(t, resultOutputsMap(t, blockedError, "censor"), "label") != "signing-channel" {
		t.Fatalf("canonical censored result = %#v", blockedError)
	}

	inflatedBlocked := resultOutputsMap(t, fixture.Inflated, "blocked")
	channels := resultOutputsList(t, inflatedBlocked, "channels")
	if resultOutputsNumber(t, inflatedBlocked, "total") != 4 || len(channels) != 1 ||
		resultOutputsNumber(t, resultOutputsMapValue(t, channels[0]), "blocked") != 3 ||
		resultOutputsString(t,
			resultOutputsMap(t, resultOutputsMapValue(t, channels[0]), "channel"), "label",
		) != "signing-channel" {
		t.Fatalf("canonical inflated blocked = %#v", inflatedBlocked)
	}
	transactions := resultOutputsMap(t, fixture.Inflated, "transaction_outputs")
	channelOutputs := resultOutputsList(t, transactions, "channel-tx")
	channelMeta := resultOutputsMap(t, resultOutputsMapValue(t, channelOutputs[0]), "meta")
	if resultOutputsNumber(t, channelMeta, "claims_in_channel") != 42 ||
		resultOutputsString(t, channelMeta, "canonical_url") != "lbry://@channel#c" {
		t.Fatalf("canonical inflated channel metadata = %#v", channelMeta)
	}
}

func assertResultOutputsWireSemanticsOracle(
	t *testing.T, fixtures map[string]resultOutputsDecodeOracleCase,
) {
	t.Helper()
	duplicate := fixtures["duplicate_scalar_fields_last_value_wins"]
	duplicateTXO := resultOutputsMapValue(t, resultOutputsList(t, duplicate.Decoded, "txos")[0])
	if duplicate.WireRoundTripIdentical ||
		resultOutputsString(t, duplicateTXO, "tx_hash") !=
			"606162636465666768696a6b6c6d6e6f707172737475767778797a7b7c7d7e7f" ||
		resultOutputsNumber(t, duplicateTXO, "nout") != 1 ||
		resultOutputsNumber(t, duplicateTXO, "height") != 6 ||
		resultOutputsString(t,
			resultOutputsMapValue(t, resultOutputsList(t, duplicate.Inflated, "txos")[0]), "label",
		) != "bare-two-1" {
		t.Fatalf("duplicate scalar semantics = %+v", duplicate)
	}

	errorWins := fixtures["claim_then_error_oneof_error_wins"]
	errorTXO := resultOutputsMapValue(t, resultOutputsList(t, errorWins.Decoded, "txos")[0])
	if errorWins.WireRoundTripIdentical || errorTXO["meta"] != "error" ||
		len(resultOutputsList(t, errorWins.Decoded, "txs")) != 0 ||
		resultOutputsString(t, resultOutputsMap(t, errorTXO, "error"), "name") != "NOT_FOUND" {
		t.Fatalf("claim then error oneof semantics = %+v", errorWins)
	}
	claimWins := fixtures["error_then_claim_oneof_claim_wins"]
	claimTXO := resultOutputsMapValue(t, resultOutputsList(t, claimWins.Decoded, "txos")[0])
	if claimWins.WireRoundTripIdentical || claimTXO["meta"] != "claim" ||
		len(resultOutputsList(t, claimWins.Decoded, "txs")) != 1 ||
		resultOutputsString(t,
			resultOutputsMapValue(t, resultOutputsList(t, claimWins.Inflated, "txos")[0]), "label",
		) != "oneof-output" {
		t.Fatalf("error then claim oneof semantics = %+v", claimWins)
	}

	merged := fixtures["repeated_same_claim_member_merges"]
	mergedClaim := resultOutputsMap(t,
		resultOutputsMapValue(t, resultOutputsList(t, merged.Decoded, "txos")[0]), "claim",
	)
	if merged.WireRoundTripIdentical ||
		resultOutputsString(t, mergedClaim, "short_url") != "merged-short#1" ||
		resultOutputsString(t, mergedClaim, "canonical_url") != "@merged#2/merged-short#1" ||
		resultOutputsNumber(t, mergedClaim, "effective_amount") != 2 {
		t.Fatalf("repeated claim merge semantics = %+v", merged)
	}

	unknown := fixtures["unknown_fields_preserved_and_ignored"]
	if !unknown.WireRoundTripIdentical ||
		unknown.MessageReserializedBase64 != unknown.InputBase64 ||
		resultOutputsMapValue(t, resultOutputsList(t, unknown.Decoded, "txos")[0])["meta"] != nil ||
		resultOutputsString(t,
			resultOutputsMapValue(t, resultOutputsList(t, unknown.Inflated, "txos")[0]), "label",
		) != "unknown-output" {
		t.Fatalf("unknown field semantics = %+v", unknown)
	}
	wrongWire := fixtures["known_field_wrong_wire_type_is_unknown"]
	if !wrongWire.WireRoundTripIdentical ||
		wrongWire.MessageReserializedBase64 != wrongWire.InputBase64 ||
		resultOutputsNumber(t, wrongWire.Decoded, "total") != 0 {
		t.Fatalf("wrong wire type semantics = %+v", wrongWire)
	}

	empty := fixtures["empty_payload_defaults"]
	noise := fixtures["non_alphabet_base64_noise_decodes_empty"]
	for _, fixture := range []resultOutputsDecodeOracleCase{empty, noise} {
		if fixture.DecodeError != nil || fixture.InflateError != nil ||
			resultOutputsNumber(t, fixture.Decoded, "offset") != 0 ||
			resultOutputsNumber(t, fixture.Decoded, "total") != 0 ||
			len(resultOutputsList(t, fixture.Decoded, "txos")) != 0 ||
			fixture.MessageReserializedBase64 != "" {
			t.Fatalf("empty result Outputs semantics for %q = %+v", fixture.Name, fixture)
		}
	}

	duplicateHash := fixtures["duplicate_supplied_transaction_hash_last_value_wins"]
	if resultOutputsString(t,
		resultOutputsMapValue(t, resultOutputsList(t, duplicateHash.Inflated, "txos")[0]), "label",
	) != "last-output" {
		t.Fatalf("duplicate transaction hash semantics = %+v", duplicateHash)
	}
}

func assertResultOutputsFailureOracle(
	t *testing.T, fixtures map[string]resultOutputsDecodeOracleCase,
) {
	t.Helper()
	unknownEnum := fixtures["unknown_error_enum_decodes_then_inflate_fails"]
	unknownError := resultOutputsMap(t,
		resultOutputsMapValue(t, resultOutputsList(t, unknownEnum.Decoded, "txos")[0]), "error",
	)
	if resultOutputsNumber(t, unknownError, "code") != 99 || unknownError["name"] != nil ||
		unknownEnum.InflateError == nil || unknownEnum.InflateError.Type != "ValueError" ||
		unknownEnum.InflateError.Message != "Enum Code has no name defined for value 99" {
		t.Fatalf("unknown enum semantics = %+v", unknownEnum)
	}

	decodeFailures := map[string]resultOutputsOracleError{
		"missing_base64_padding_fails": {
			Stage: "Outputs.from_base64", Type: "Error", Module: "binascii", Message: "Incorrect padding",
		},
		"truncated_protobuf_fails": {
			Stage: "Outputs.from_base64", Type: "DecodeError", Module: "google.protobuf.message", Message: "Truncated message.",
		},
	}
	for name, want := range decodeFailures {
		fixture := fixtures[name]
		if fixture.DecodeError == nil || *fixture.DecodeError != want ||
			fixture.Decoded != nil || fixture.Inflated != nil {
			t.Fatalf("decode failure %q = %+v, want %+v", name, fixture, want)
		}
	}

	inflateFailures := map[string]string{
		"output_index_out_of_range_fails_inflate":        "IndexError",
		"missing_relationship_transaction_fails_inflate": "KeyError",
	}
	for name, wantType := range inflateFailures {
		fixture := fixtures[name]
		if fixture.DecodeError != nil || fixture.Decoded == nil || fixture.InflateError == nil ||
			fixture.InflateError.Stage != "Outputs.inflate" ||
			fixture.InflateError.Module != "builtins" || fixture.InflateError.Type != wantType {
			t.Fatalf("inflate failure %q = %+v", name, fixture)
		}
	}
	if got := fixtures["output_index_out_of_range_fails_inflate"].InflateError.Message; got != "list index out of range" {
		t.Fatalf("out-of-range inflate message = %q", got)
	}
}

func resultOutputsMap(t *testing.T, value map[string]any, key string) map[string]any {
	t.Helper()
	got, ok := value[key].(map[string]any)
	if !ok {
		t.Fatalf("result Outputs field %q = %#v, want object", key, value[key])
	}
	return got
}

func resultOutputsMapValue(t *testing.T, value any) map[string]any {
	t.Helper()
	got, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("result Outputs value = %#v, want object", value)
	}
	return got
}

func resultOutputsList(t *testing.T, value map[string]any, key string) []any {
	t.Helper()
	got, ok := value[key].([]any)
	if !ok {
		t.Fatalf("result Outputs field %q = %#v, want list", key, value[key])
	}
	return got
}

func resultOutputsString(t *testing.T, value map[string]any, key string) string {
	t.Helper()
	got, ok := value[key].(string)
	if !ok {
		t.Fatalf("result Outputs field %q = %#v, want string", key, value[key])
	}
	return got
}

func resultOutputsNumber(t *testing.T, value map[string]any, key string) float64 {
	t.Helper()
	got, ok := value[key].(float64)
	if !ok {
		t.Fatalf("result Outputs field %q = %#v, want number", key, value[key])
	}
	return got
}

func resultOutputsBool(t *testing.T, value map[string]any, key string) bool {
	t.Helper()
	got, ok := value[key].(bool)
	if !ok {
		t.Fatalf("result Outputs field %q = %#v, want bool", key, value[key])
	}
	return got
}

func runResultOutputsDecodeOracle(t *testing.T) resultOutputsDecodeOracleResponse {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate result Outputs oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot := os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	script := filepath.Join(daemonRoot, "compat", "result_outputs_decode_oracle.py")
	for _, path := range []string{sdkRoot, script} {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			t.Skipf("result Outputs oracle dependency is unavailable: %s", path)
		} else if err != nil {
			t.Fatal(err)
		}
	}

	command := exec.Command(python, script, "--sdk-root", sdkRoot)
	command.Env = append(os.Environ(), "PROTOCOL_BUFFERS_PYTHON_IMPLEMENTATION=python")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Python result Outputs oracle failed: %v\n%s", err, output)
	}
	var oracle resultOutputsDecodeOracleResponse
	if err := json.Unmarshal(output, &oracle); err != nil {
		t.Fatalf("decode result Outputs oracle: %v\n%s", err, output)
	}
	return oracle
}
