package rpc

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	walletpkg "lbry/daemon/wallet"
)

type resultOutputsProductionInflateFixture struct {
	transactions     []*walletpkg.Transaction
	transactionLabel map[*walletpkg.Transaction]string
	outputLabel      map[*walletpkg.TransactionOutput]string
}

type resultOutputsProductionOutputSpec struct {
	label     string
	isChannel bool
}

func TestResultOutputsInflateMatchesPinnedOracle(t *testing.T) {
	oracle := runResultOutputsDecodeOracle(t)
	for _, fixture := range oracle.Cases {
		fixture := fixture
		if fixture.DecodeError != nil {
			continue
		}
		t.Run(fixture.Name, func(t *testing.T) {
			outputs, err := walletpkg.DecodeHubOutputsBase64(fixture.InputBase64)
			if err != nil {
				t.Fatalf("decode production fixture: %v", err)
			}
			transactions := resultOutputsProductionTransactions(t, fixture.Name)
			gotTXOs, gotBlocked, err := outputs.Inflate(transactions.transactions)
			if fixture.InflateError != nil {
				assertResultOutputsProductionInflateError(t, err, fixture.InflateError)
				assertResultOutputsProductionFailureSideEffects(t, fixture.Name, transactions)
				return
			}
			if err != nil {
				t.Fatalf("production inflate failed: %v", err)
			}

			got := resultOutputsNormalizeProductionInflation(
				t, gotTXOs, gotBlocked, transactions,
			)
			want := resultOutputsJSONCanonical(t, fixture.Inflated)
			if !reflect.DeepEqual(got, want) {
				gotJSON, _ := json.Marshal(got)
				wantJSON, _ := json.Marshal(want)
				t.Fatalf("production inflated Outputs = %s, want %s", gotJSON, wantJSON)
			}

			assertResultOutputsProductionAliases(
				t, fixture.Name, gotTXOs, gotBlocked, transactions,
			)
		})
	}
}

func TestResultOutputsInflateProcessesPinnedExtrasBeforePageFailure(t *testing.T) {
	oracle := runResultOutputsDecodeOracle(t)
	var canonical *resultOutputsDecodeOracleCase
	for index := range oracle.Cases {
		if oracle.Cases[index].Name == "canonical_relationship_graph" {
			canonical = &oracle.Cases[index]
			break
		}
	}
	if canonical == nil {
		t.Fatal("pinned canonical relationship graph is unavailable")
	}
	outputs, err := walletpkg.DecodeHubOutputsBase64(canonical.InputBase64)
	if err != nil {
		t.Fatal(err)
	}
	fixture := resultOutputsProductionTransactions(t, canonical.Name)
	fixture.transactions[0].Outputs = fixture.transactions[0].Outputs[:1]
	_, _, err = outputs.Inflate(fixture.transactions)
	var inflateError *walletpkg.HubOutputsInflateError
	if !errors.As(err, &inflateError) || inflateError.PythonErrorName() != "IndexError" ||
		err.Error() != "list index out of range" {
		t.Fatalf("page failure = %T %v, want pinned IndexError", err, err)
	}
	channel := &fixture.transactions[1].Outputs[0]
	reposted := &fixture.transactions[2].Outputs[2]
	if channel.Meta == nil || channel.Meta["short_url"] != "lbry://@channel#c" ||
		reposted.Meta == nil || reposted.Meta["short_url"] != "lbry://original#o" {
		t.Fatalf("extra outputs were not mutated before page failure: channel=%#v repost=%#v",
			channel.Meta, reposted.Meta)
	}
}

func resultOutputsProductionTransactions(
	t *testing.T, name string,
) resultOutputsProductionInflateFixture {
	t.Helper()
	fixture := resultOutputsProductionInflateFixture{
		transactionLabel: make(map[*walletpkg.Transaction]string),
		outputLabel:      make(map[*walletpkg.TransactionOutput]string),
	}
	add := func(label string, firstHashByte byte, specs ...resultOutputsProductionOutputSpec) {
		transaction := &walletpkg.Transaction{
			Hash:    resultOutputsProductionHash(firstHashByte),
			Outputs: make([]walletpkg.TransactionOutput, len(specs)),
		}
		for index, spec := range specs {
			transaction.Outputs[index].Position = uint32(index)
			transaction.Outputs[index].TransactionHash = transaction.Hash
			if spec.isChannel {
				claim := []byte{0x00, 0x12, 0x23, 0x0a, 0x21}
				claim = append(claim, make([]byte, 33)...)
				transaction.Outputs[index].Script = walletpkg.TransactionOutputScript{
					Template: walletpkg.TransactionScriptClaimPubKeyHash,
					Claim:    claim,
				}
			}
			fixture.outputLabel[&transaction.Outputs[index]] = spec.label
		}
		fixture.transactions = append(fixture.transactions, transaction)
		fixture.transactionLabel[transaction] = label
	}
	output := func(label string) resultOutputsProductionOutputSpec {
		return resultOutputsProductionOutputSpec{label: label}
	}
	channel := func(label string) resultOutputsProductionOutputSpec {
		return resultOutputsProductionOutputSpec{label: label, isChannel: true}
	}

	switch name {
	case "canonical_relationship_graph":
		add("main-tx", 0, output("main-0"), output("root-repost"))
		add("channel-tx", 32, channel("signing-channel"))
		add("repost-tx", 64,
			output("repost-0"), output("repost-1"), output("reposted-stream"),
		)
		add("bare-tx", 96, output("bare-output"))
	case "duplicate_scalar_fields_last_value_wins":
		add("bare-two", 96, output("bare-two-0"), output("bare-two-1"))
	case "claim_then_error_oneof_error_wins",
		"unknown_error_enum_decodes_then_inflate_fails",
		"empty_payload_defaults", "non_alphabet_base64_noise_decodes_empty":
	case "error_then_claim_oneof_claim_wins":
		add("oneof-tx", 0, output("oneof-output"))
	case "repeated_same_claim_member_merges":
		add("merge-tx", 0, output("merge-output"))
	case "unknown_fields_preserved_and_ignored":
		add("unknown-tx", 96, output("unknown-output"))
	case "known_field_wrong_wire_type_is_unknown":
		add("wrong-wire-tx", 96, output("wrong-wire-output"))
	case "output_index_out_of_range_fails_inflate":
		add("short-tx", 0, output("only-output"))
	case "missing_relationship_transaction_fails_inflate":
		add("relationship-tx", 0, output("relationship-output"))
	case "duplicate_supplied_transaction_hash_last_value_wins":
		add("duplicate-first", 0, output("first-output"))
		add("duplicate-last", 0, output("last-output"))
	default:
		t.Fatalf("missing production transaction fixture for %q", name)
	}
	return fixture
}

func resultOutputsProductionHash(first byte) [32]byte {
	var hash [32]byte
	for index := range hash {
		hash[index] = first + byte(index)
	}
	return hash
}

func resultOutputsNormalizeProductionInflation(
	t *testing.T,
	txos []walletpkg.HubInflatedResult,
	blocked walletpkg.HubBlockedSummary,
	fixture resultOutputsProductionInflateFixture,
) any {
	t.Helper()
	normalizedTXOs := make([]any, len(txos))
	for index, txo := range txos {
		normalizedTXOs[index] = resultOutputsNormalizeProductionResult(t, txo, fixture)
	}
	channels := make([]any, len(blocked.Channels))
	for index, entry := range blocked.Channels {
		channels[index] = map[string]any{
			"channel": resultOutputsNormalizeProductionResult(t, entry.Channel, fixture),
			"blocked": entry.Blocked,
		}
	}
	transactionOutputs := make(map[string]any, len(fixture.transactions))
	for _, transaction := range fixture.transactions {
		outputs := make([]any, len(transaction.Outputs))
		for index := range transaction.Outputs {
			outputs[index] = resultOutputsNormalizeProductionOutput(
				t, &transaction.Outputs[index], fixture,
			)
		}
		transactionOutputs[fixture.transactionLabel[transaction]] = outputs
	}
	return resultOutputsJSONCanonical(t, map[string]any{
		"txos": normalizedTXOs,
		"blocked": map[string]any{
			"total": blocked.Total, "channels": channels,
		},
		"transaction_outputs": transactionOutputs,
	})
}

func resultOutputsNormalizeProductionResult(
	t *testing.T,
	result walletpkg.HubInflatedResult,
	fixture resultOutputsProductionInflateFixture,
) any {
	t.Helper()
	if result.Output != nil && result.Error != nil {
		t.Fatal("production inflated result has both output and error variants")
	}
	if result.Error != nil {
		errorValue := map[string]any{
			"name": result.Error.Name,
			"text": result.Error.Text,
		}
		if result.Error.Censor != nil {
			errorValue["censor"] = resultOutputsNormalizeProductionResult(
				t, *result.Error.Censor, fixture,
			)
		}
		return map[string]any{"error": errorValue}
	}
	return resultOutputsNormalizeProductionOutput(t, result.Output, fixture)
}

func resultOutputsNormalizeProductionOutput(
	t *testing.T,
	output *walletpkg.TransactionOutput,
	fixture resultOutputsProductionInflateFixture,
) any {
	t.Helper()
	if output == nil {
		return nil
	}
	label, ok := fixture.outputLabel[output]
	if !ok {
		t.Fatalf("production inflater returned an output outside the supplied graph: %p", output)
	}
	return map[string]any{
		"label":          label,
		"meta":           output.Meta,
		"channel":        resultOutputsProductionOutputLabel(t, output.Channel, fixture),
		"reposted_claim": resultOutputsProductionOutputLabel(t, output.RepostedClaim, fixture),
	}
}

func resultOutputsProductionOutputLabel(
	t *testing.T,
	output *walletpkg.TransactionOutput,
	fixture resultOutputsProductionInflateFixture,
) any {
	t.Helper()
	if output == nil {
		return nil
	}
	label, ok := fixture.outputLabel[output]
	if !ok {
		t.Fatalf("production inflater related an output outside the supplied graph: %p", output)
	}
	return label
}

func resultOutputsJSONCanonical(t *testing.T, value any) any {
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

func assertResultOutputsProductionInflateError(
	t *testing.T, err error, want *resultOutputsOracleError,
) {
	t.Helper()
	if err == nil {
		t.Fatal("production inflate unexpectedly succeeded")
	}
	var inflateError *walletpkg.HubOutputsInflateError
	if !errors.As(err, &inflateError) {
		t.Fatalf("production inflate error = %T %v, want HubOutputsInflateError", err, err)
	}
	if got := inflateError.PythonErrorName(); got != want.Type || err.Error() != want.Message {
		t.Fatalf(
			"production inflate error = %s(%q), want %s(%q)",
			got, err.Error(), want.Type, want.Message,
		)
	}
}

func assertResultOutputsProductionAliases(
	t *testing.T,
	name string,
	txos []walletpkg.HubInflatedResult,
	blocked walletpkg.HubBlockedSummary,
	fixture resultOutputsProductionInflateFixture,
) {
	t.Helper()
	switch name {
	case "canonical_relationship_graph":
		main := fixture.transactions[0]
		channel := fixture.transactions[1]
		repost := fixture.transactions[2]
		bare := fixture.transactions[3]
		if txos[0].Output != &main.Outputs[1] || txos[1].Output != &bare.Outputs[0] ||
			txos[0].Output.Channel != &channel.Outputs[0] ||
			txos[0].Output.RepostedClaim != &repost.Outputs[2] ||
			txos[5].Error == nil || txos[5].Error.Censor == nil ||
			txos[5].Error.Censor.Output != &channel.Outputs[0] ||
			len(blocked.Channels) != 1 ||
			blocked.Channels[0].Channel.Output != &channel.Outputs[0] {
			t.Fatalf("canonical production graph does not preserve supplied-output identity")
		}
		if txos[2].Output != nil || txos[2].Error != nil ||
			channel.Outputs[0].Meta == nil || repost.Outputs[2].Meta == nil {
			t.Fatalf("canonical missing/extras-first semantics = %+v", txos)
		}
	case "duplicate_supplied_transaction_hash_last_value_wins":
		first := fixture.transactions[0]
		last := fixture.transactions[1]
		if txos[0].Output != &last.Outputs[0] || first.Outputs[0].Meta != nil ||
			last.Outputs[0].Meta == nil {
			t.Fatalf("duplicate supplied transaction hash did not preserve last-value semantics")
		}
	}
}

func assertResultOutputsProductionFailureSideEffects(
	t *testing.T, name string, fixture resultOutputsProductionInflateFixture,
) {
	t.Helper()
	if name != "missing_relationship_transaction_fails_inflate" {
		return
	}
	output := &fixture.transactions[0].Outputs[0]
	if output.Meta == nil || output.Meta["short_url"] != "lbry://missing-channel#1" ||
		output.Channel != nil {
		t.Fatalf("relationship failure side effects = %#v", output)
	}
}
