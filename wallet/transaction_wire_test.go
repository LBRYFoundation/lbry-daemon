package wallet

import (
	"bytes"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"

	"lbry/daemon/wallet/keys"
)

func TestLegacyTransactionJSONResolvedInputAmountsOutputsAndAnnotations(t *testing.T) {
	headers := newTransactionExecutionHeaders(t,
		strings.Repeat("00", 32), strings.Repeat("11", 32), strings.Repeat("22", 32),
	)
	ledger := &Ledger{Network: keys.MainNet, Headers: headers}
	parent := transactionHistoryUnitCoinbase(
		t, 8_001, NewPayPubKeyHashOutput(150_000_000, bytes.Repeat([]byte{0x31}, 20)),
	)
	parent.Height = 1
	input, err := NewSpendInput(&parent.Outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	transaction := NewTransaction()
	transaction.LockTime = 8_002
	transaction.AddInputs([]TransactionInput{input})
	transaction.AddOutputs([]TransactionOutput{
		NewPayPubKeyHashOutput(100_000_000, bytes.Repeat([]byte{0x32}, 20)),
		NewReturnDataOutput([]byte("wire data")),
	})
	if err := transaction.RebuildDerived(); err != nil {
		t.Fatal(err)
	}
	transaction.Height = 2
	spent, mineOutput, mineInput, internal := true, false, true, false
	transaction.Outputs[0].IsSpent = &spent
	transaction.Outputs[0].IsMyOutput = &mineOutput
	transaction.Outputs[0].IsMyInput = &mineInput
	transaction.Outputs[0].IsInternalTransfer = &internal

	parentAddress, err := parent.Outputs[0].Address(keys.MainNet)
	if err != nil {
		t.Fatal(err)
	}
	outputAddress, err := transaction.Outputs[0].Address(keys.MainNet)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"txid":   transaction.ID,
		"height": int64(2),
		"inputs": []any{map[string]any{
			"txid": parent.ID, "nout": uint32(0), "height": int64(1),
			"amount": "1.5", "address": parentAddress, "confirmations": int64(2),
			"timestamp": int64(2), "type": "payment",
		}},
		"outputs": []any{
			map[string]any{
				"txid": transaction.ID, "nout": uint32(0), "height": int64(2),
				"amount": "1.0", "address": outputAddress, "confirmations": int64(1),
				"timestamp": int64(3), "type": "payment",
				"is_spent": true, "is_my_output": false, "is_my_input": true,
				"is_internal_transfer": false,
			},
			map[string]any{
				"txid": transaction.ID, "nout": uint32(1), "height": int64(2),
				"amount": "0.0", "address": nil, "confirmations": int64(1),
				"timestamp": int64(3), "type": "data",
			},
		},
		"total_input": "1.5", "total_output": "1.0", "total_fee": "0.5",
		"hex": hex.EncodeToString(transaction.Raw),
	}

	got, err := ledger.LegacyTransactionJSON(transaction)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy transaction JSON =\n%#v\nwant\n%#v", got, want)
	}
}

func TestLegacyTransactionJSONSupportTipAnnotationsPreserveNullability(t *testing.T) {
	headers := newTransactionExecutionHeaders(t, strings.Repeat("00", 32))
	ledger := &Ledger{Network: keys.MainNet, Headers: headers}
	transaction := transactionHistoryUnitCoinbase(
		t, 8_101, NewPayPubKeyHashOutput(1, bytes.Repeat([]byte{0x71}, 20)),
	)
	sentSupports, sentTips, receivedTips := int64(0), int64(123_456_789), int64(-1)
	transaction.Outputs[0].SentSupports = &sentSupports
	transaction.Outputs[0].SentTips = &sentTips
	transaction.Outputs[0].ReceivedTips = &receivedTips

	encoded, err := ledger.LegacyTransactionJSON(transaction)
	if err != nil {
		t.Fatal(err)
	}
	output := encoded["outputs"].([]any)[0].(map[string]any)
	if output["sent_supports"] != "0.0" || output["sent_tips"] != "1.23456789" ||
		output["received_tips"] != "-0.00000001" {
		t.Fatalf("support/tip annotations = %#v", output)
	}

	transaction.Outputs[0].SentSupports = nil
	transaction.Outputs[0].SentTips = nil
	transaction.Outputs[0].ReceivedTips = nil
	encoded, err = ledger.LegacyTransactionJSON(transaction)
	if err != nil {
		t.Fatal(err)
	}
	output = encoded["outputs"].([]any)[0].(map[string]any)
	for _, key := range []string{"sent_supports", "sent_tips", "received_tips"} {
		if _, exists := output[key]; exists {
			t.Fatalf("nil annotation emitted %s: %#v", key, output)
		}
	}
}

func TestLegacyTransactionJSONClaimMetaConversionAndURLHoisting(t *testing.T) {
	headers := newTransactionExecutionHeaders(
		t, strings.Repeat("00", 32), strings.Repeat("11", 32), strings.Repeat("22", 32),
	)
	ledger := &Ledger{Network: keys.MainNet, Headers: headers}
	claim := NewClaimNameOutput(
		1, "meta", claimWireOracleMustHex(t, claimWireOracleFixtureProto),
		bytes.Repeat([]byte{0x72}, 20),
	)
	transaction := transactionHistoryUnitCoinbase(t, 8_102, claim)
	transaction.Height = 2
	meta := map[string]any{
		"short_url":          "lbry://meta#1",
		"canonical_url":      "lbry://@channel#2/meta#1",
		"effective_amount":   int64(123_456_789),
		"support_amount":     uint64(10),
		"truthy_amount":      true,
		"floating_amount":    2.0,
		"text_amount":        "3",
		"creation_height":    int64(1),
		"creation_timestamp": int64(999),
		"reposted":           uint32(7),
	}
	transaction.Outputs[0].Meta = meta

	encoded, err := ledger.LegacyTransactionJSON(transaction)
	if err != nil {
		t.Fatal(err)
	}
	output := encoded["outputs"].([]any)[0].(map[string]any)
	if output["short_url"] != "lbry://meta#1" ||
		output["canonical_url"] != "lbry://@channel#2/meta#1" {
		t.Fatalf("hoisted claim URLs = %#v", output)
	}
	wantMeta := map[string]any{
		"effective_amount":   "1.23456789",
		"support_amount":     "0.0000001",
		"truthy_amount":      "0.00000001",
		"floating_amount":    2.0,
		"text_amount":        "3",
		"creation_height":    int64(1),
		"creation_timestamp": int64(2),
		"reposted":           uint32(7),
	}
	if !reflect.DeepEqual(output["meta"], wantMeta) {
		t.Fatalf("claim meta = %#v, want %#v", output["meta"], wantMeta)
	}
	if meta["short_url"] != "lbry://meta#1" || meta["effective_amount"] != int64(123_456_789) ||
		meta["creation_timestamp"] != int64(999) || len(meta) != 10 {
		t.Fatalf("claim meta source was mutated: %#v", meta)
	}
}

func TestLegacyTransactionJSONUnresolvedInputHasOnlyOutpoint(t *testing.T) {
	headers := newTransactionExecutionHeaders(t, strings.Repeat("00", 32))
	ledger := &Ledger{Network: keys.MainNet, Headers: headers}
	var previousHash [32]byte
	copy(previousHash[:], bytes.Repeat([]byte{0x11}, 32))
	transaction := NewTransaction()
	transaction.LockTime = 8_003
	transaction.AddInputs([]TransactionInput{{
		PreviousHash: previousHash, PreviousTxID: strings.Repeat("11", 32),
		PreviousIndex: 7, Sequence: ^uint32(0), Script: TransactionInputScript{Source: []byte{0x51}},
	}})
	transaction.AddOutputs([]TransactionOutput{
		NewPayPubKeyHashOutput(25_000_000, bytes.Repeat([]byte{0x33}, 20)),
	})
	if err := transaction.RebuildDerived(); err != nil {
		t.Fatal(err)
	}
	transaction.Height = -1

	got, err := ledger.LegacyTransactionJSON(transaction)
	if err != nil {
		t.Fatal(err)
	}
	inputs, ok := got["inputs"].([]any)
	if !ok || len(inputs) != 1 {
		t.Fatalf("unresolved inputs = %#v", got["inputs"])
	}
	wantInput := map[string]any{"txid": strings.Repeat("11", 32), "nout": uint32(7)}
	if !reflect.DeepEqual(inputs[0], wantInput) {
		t.Fatalf("unresolved input = %#v, want %#v", inputs[0], wantInput)
	}
	if got["total_input"] != "0.0" || got["total_output"] != "0.25" ||
		got["total_fee"] != "-0.25" {
		t.Fatalf("unresolved totals = input %v output %v fee %v",
			got["total_input"], got["total_output"], got["total_fee"])
	}
	outputs := got["outputs"].([]any)
	output := outputs[0].(map[string]any)
	if output["height"] != int64(-1) || output["confirmations"] != int64(-1) ||
		output["timestamp"] != nil || output["type"] != "payment" || len(output) != 8 {
		t.Fatalf("unconfirmed output = %#v", output)
	}
}

func TestLegacyTransactionJSONUnavailableBoundaries(t *testing.T) {
	transaction := transactionHistoryUnitCoinbase(
		t, 8_004, NewReturnDataOutput([]byte("data")),
	)
	if result, err := (*Ledger)(nil).LegacyTransactionJSON(transaction); result != nil ||
		!errors.Is(err, ErrTransactionWireUnavailable) {
		t.Fatalf("nil ledger encoding = %#v, %v", result, err)
	}
	if result, err := (&Ledger{}).LegacyTransactionJSON(transaction); result != nil ||
		!errors.Is(err, ErrTransactionWireUnavailable) {
		t.Fatalf("header-less encoding = %#v, %v", result, err)
	}
	headers := newTransactionExecutionHeaders(t, strings.Repeat("00", 32))
	if result, err := (&Ledger{Headers: headers}).LegacyTransactionJSON(nil); result != nil ||
		!errors.Is(err, ErrTransactionWireUnavailable) {
		t.Fatalf("nil transaction encoding = %#v, %v", result, err)
	}
}

func TestLegacyTransactionJSONPreservesMissingRemoteHeight(t *testing.T) {
	headers := newTransactionExecutionHeaders(t, strings.Repeat("00", 32))
	ledger := &Ledger{Headers: headers}
	transaction := transactionHistoryUnitCoinbase(t, 8_005)
	transaction.HeightMissing = true

	got, err := ledger.LegacyTransactionJSON(transaction)
	if err != nil || got["height"] != nil {
		t.Fatalf("output-less missing-height transaction = %#v, %v", got, err)
	}

	transaction.AddOutputs([]TransactionOutput{NewReturnDataOutput([]byte("data"))})
	if err := transaction.RebuildDerived(); err != nil {
		t.Fatal(err)
	}
	transaction.HeightMissing = true
	if _, err := ledger.LegacyTransactionJSON(transaction); err == nil ||
		err.Error() != "'>' not supported between instances of 'NoneType' and 'int'" {
		t.Fatalf("missing-height output error = %v", err)
	}
}

func TestLegacyTransactionJSONPlainSupportEnvelope(t *testing.T) {
	headers := newTransactionExecutionHeaders(t, strings.Repeat("00", 32), strings.Repeat("11", 32))
	ledger := &Ledger{Network: keys.MainNet, Headers: headers}
	claimID := "00112233445566778899aabbccddeeff00112233"
	support, err := NewSupportOutput(
		25_000_000, "Straße", claimID, bytes.Repeat([]byte{0x61}, 20),
	)
	if err != nil {
		t.Fatal(err)
	}
	transaction := transactionHistoryUnitCoinbase(t, 8_006, support)
	transaction.Height = 1

	got, err := ledger.LegacyTransactionJSON(transaction)
	if err != nil {
		t.Fatal(err)
	}
	output := got["outputs"].([]any)[0].(map[string]any)
	if output["type"] != "support" || output["name"] != "Straße" ||
		output["normalized_name"] != "strasse" || output["claim_id"] != claimID ||
		output["permanent_url"] != "lbry://Straße#"+claimID ||
		!reflect.DeepEqual(output["meta"], map[string]any{}) {
		t.Fatalf("plain support envelope = %#v", output)
	}
	for _, absent := range []string{"value", "protobuf", "claim_op", "value_type"} {
		if _, exists := output[absent]; exists {
			t.Fatalf("plain support unexpectedly contains %s: %#v", absent, output)
		}
	}
}

func TestLegacyTransactionJSONPurchaseProjection(t *testing.T) {
	headers := newTransactionExecutionHeaders(t, strings.Repeat("00", 32))
	ledger := &Ledger{Network: keys.MainNet, Headers: headers}
	claimID := "00112233445566778899aabbccddeeff00112233"
	purchaseData, err := NewPurchaseDataOutput(claimID)
	if err != nil {
		t.Fatal(err)
	}
	transaction := transactionHistoryUnitCoinbase(
		t, 8_007,
		NewPayPubKeyHashOutput(100_000_000, bytes.Repeat([]byte{0x62}, 20)),
		purchaseData,
	)
	transaction.Outputs[0].Purchase = &transaction.Outputs[1]

	got, err := ledger.LegacyTransactionJSON(transaction)
	if err != nil {
		t.Fatal(err)
	}
	outputs := got["outputs"].([]any)
	payment := outputs[0].(map[string]any)
	data := outputs[1].(map[string]any)
	if payment["type"] != "purchase" || payment["claim_id"] != claimID ||
		data["type"] != "data" {
		t.Fatalf("purchase projection = %#v", outputs)
	}
}

func TestLegacyTransactionJSONSupportDataProjection(t *testing.T) {
	headers := newTransactionExecutionHeaders(t, strings.Repeat("00", 32), strings.Repeat("11", 32))
	ledger := &Ledger{Network: keys.MainNet, Headers: headers}
	claimID := "00112233445566778899aabbccddeeff00112233"
	payload := mustSupportValueHex(t, "000a023a291206737465616479")
	support, err := NewSupportDataOutput(
		400_000_000, "MiXeD", claimID, payload, bytes.Repeat([]byte{0x64}, 20),
	)
	if err != nil {
		t.Fatal(err)
	}
	transaction := transactionHistoryUnitCoinbase(t, 8_008, support)
	transaction.Height = 1

	for _, includeProtobuf := range []bool{false, true} {
		got, err := ledger.LegacyTransactionJSONWithOptions(
			transaction, LegacyTransactionJSONOptions{IncludeProtobuf: includeProtobuf},
		)
		if err != nil {
			t.Fatal(err)
		}
		output := got["outputs"].([]any)[0].(map[string]any)
		if output["type"] != "support" || output["name"] != "MiXeD" ||
			output["normalized_name"] != "mixed" || output["claim_id"] != claimID ||
			!reflect.DeepEqual(output["value"], map[string]any{"emoji": ":)", "comment": "steady"}) {
			t.Fatalf("support-data projection = %#v", output)
		}
		protobuf, exists := output["protobuf"]
		if exists != includeProtobuf || includeProtobuf && protobuf != hex.EncodeToString(payload) {
			t.Fatalf("support-data protobuf = %#v, exists %v", protobuf, exists)
		}
	}
}

func TestLegacyTransactionJSONSignedSupportSignatureBoundary(t *testing.T) {
	headers := newTransactionExecutionHeaders(t, strings.Repeat("00", 32), strings.Repeat("11", 32))
	ledger := &Ledger{Network: keys.MainNet, Headers: headers}
	claimID := "00112233445566778899aabbccddeeff00112233"
	channelHash := make([]byte, 20)
	for index := range channelHash {
		channelHash[index] = byte(index + 1)
	}
	signature := make([]byte, 64)
	for index := range signature {
		signature[index] = byte(index + 21)
	}
	payload := append([]byte{1}, channelHash...)
	payload = append(payload, signature...)
	payload = append(payload, mustSupportValueHex(t, "1206737465616479")...)
	support, err := NewSupportDataOutput(
		1, "name", claimID, payload, bytes.Repeat([]byte{0x65}, 20),
	)
	if err != nil {
		t.Fatal(err)
	}
	transaction := transactionHistoryUnitCoinbase(t, 8_009, support)
	transaction.Height = 1

	got, err := ledger.LegacyTransactionJSON(transaction)
	if err != nil {
		t.Fatal(err)
	}
	output := got["outputs"].([]any)[0].(map[string]any)
	wantChannel := map[string]any{
		"channel_id": "14131211100f0e0d0c0b0a090807060504030201",
	}
	if !reflect.DeepEqual(output["signing_channel"], wantChannel) ||
		output["is_channel_signature_valid"] != false {
		t.Fatalf("signed support projection = %#v", output)
	}

	parent := transactionHistoryUnitCoinbase(t, 8_010, support)
	parent.Height = 1
	input, err := NewSpendInput(&parent.Outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	spend := NewTransaction()
	spend.AddInputs([]TransactionInput{input})
	if err := spend.RebuildDerived(); err != nil {
		t.Fatal(err)
	}
	got, err = ledger.LegacyTransactionJSON(spend)
	if err != nil {
		t.Fatal(err)
	}
	resolved := got["inputs"].([]any)[0].(map[string]any)
	if _, exists := resolved["signing_channel"]; exists {
		t.Fatalf("resolved input checked its support signature: %#v", resolved)
	}
	if _, exists := resolved["is_channel_signature_valid"]; exists {
		t.Fatalf("resolved input emitted signature validity: %#v", resolved)
	}
}

func TestLegacyTransactionJSONSupportDecodeErrorBoundary(t *testing.T) {
	headers := newTransactionExecutionHeaders(t, strings.Repeat("00", 32))
	ledger := &Ledger{Network: keys.MainNet, Headers: headers}
	claimID := "00112233445566778899aabbccddeeff00112233"

	unknownVersion, err := NewSupportDataOutput(
		1, "name", claimID, []byte{2}, bytes.Repeat([]byte{0x66}, 20),
	)
	if err != nil {
		t.Fatal(err)
	}
	transaction := transactionHistoryUnitCoinbase(t, 8_011, unknownVersion)
	got, err := ledger.LegacyTransactionJSONWithOptions(
		transaction, LegacyTransactionJSONOptions{IncludeProtobuf: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	output := got["outputs"].([]any)[0].(map[string]any)
	for _, absent := range []string{"value", "protobuf", "signing_channel", "is_channel_signature_valid"} {
		if _, exists := output[absent]; exists {
			t.Fatalf("decode-error support contains %s: %#v", absent, output)
		}
	}
	if output["type"] != "support" || output["claim_id"] != claimID {
		t.Fatalf("decode-error support lost envelope: %#v", output)
	}

	empty, err := NewSupportDataOutput(
		1, "name", claimID, nil, bytes.Repeat([]byte{0x67}, 20),
	)
	if err != nil {
		t.Fatal(err)
	}
	transaction = transactionHistoryUnitCoinbase(t, 8_012, empty)
	if _, err := ledger.LegacyTransactionJSON(transaction); err == nil {
		t.Fatal("empty support payload did not preserve Python IndexError")
	} else {
		var decodeError *SupportValueDecodeError
		if !errors.As(err, &decodeError) || decodeError.PythonErrorName() != "IndexError" {
			t.Fatalf("empty support payload error = %T %v", err, err)
		}
	}
}

func TestLegacyTransactionJSONSuppressesMalformedLegacyV1Claim(t *testing.T) {
	headers := newTransactionExecutionHeaders(t, strings.Repeat("00", 32))
	ledger := &Ledger{Network: keys.MainNet, Headers: headers}
	claim := NewClaimNameOutput(
		1, "name", []byte{2}, bytes.Repeat([]byte{0x63}, 20),
	)
	transaction := transactionHistoryUnitCoinbase(t, 8_013, claim)
	encoded, err := ledger.LegacyTransactionJSON(transaction)
	if err != nil {
		t.Fatal(err)
	}
	output := encoded["outputs"].([]any)[0].(map[string]any)
	for _, absent := range []string{"value", "value_type", "protobuf"} {
		if _, exists := output[absent]; exists {
			t.Fatalf("malformed legacy v1 claim contains %s: %#v", absent, output)
		}
	}
	if output["type"] != "claim" || output["claim_op"] != "create" {
		t.Fatalf("malformed legacy v1 claim lost envelope: %#v", output)
	}
}
