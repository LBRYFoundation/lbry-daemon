package wallet

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"testing"

	"lbry/daemon/wallet/keys"
)

// These fixtures are the exact claim payloads exercised by
// compat/transaction_show_claim_oracle.py against SDK 0.113.0.
const (
	claimWireOracleLiveTXID    = "4efd7e17bdc0664b02834f7c743917de4834b79ef2ce725778bafc5554fb3158"
	claimWireOracleLiveClaimID = "bb9a6185f017956711c05f44d71f0bc4ae20a27a"
	claimWireOracleLiveAddress = "bTMNi3y5tYPZrLRjeuqMXtvcXrA97wAUAZ"
	claimWireOracleLiveName    = "T4NG3RIN-ASMR--02"
	claimWireOracleLiveProto   = "000aa7010a8d010a30727c8f4f681de1cee70903ccfbef38dac5d39104e247ec4d7cc597fdafc84fd1d8f89333207d01c" +
		"16280e1bd4380dd0512175244545f32303236303731315f3130333132352e6d703418a4d18d602209766964656f2f6d7034" +
		"323036d72ae4e3d594fe090a17e881f53fd2a1acde20dcb64cc495b72c2f1a0f2cd838517b3eb21b54132367e68e4d601" +
		"a581a044e6f6e65289b90c9d2065a0908800a10ce0518d305421154344e473352314e2041534d522023303352412a3f6874" +
		"7470733a2f2f7468756d62732e6f647963646e2e636f6d2f6363376164623531363638306531343630346131396462306164" +
		"6639626562612e776562705a0461736d725a0b656172206c69636b696e675a0a65617220656174696e675a12633a64697361" +
		"626c652d636f6d6d656e74735a0f64697361626c652d737570706f727462020801"
	claimWireOracleFixtureProto = "000a00420746697874757265"
)

func TestLegacyTransactionJSONLiveClaimMatchesPinnedOracle(t *testing.T) {
	addressPayload, err := keys.DecodeBase58Check(claimWireOracleLiveAddress)
	if err != nil || len(addressPayload) != 21 {
		t.Fatalf("decode live address = %x, %v", addressPayload, err)
	}
	claim := NewClaimNameOutput(
		100_000, claimWireOracleLiveName,
		claimWireOracleMustHex(t, claimWireOracleLiveProto), addressPayload[1:],
	)
	transaction := claimWireOracleTransaction(
		t, claimWireOracleLiveTXID, 2_088_350, claim,
	)
	if claimID, err := transaction.Outputs[0].ClaimID(); err != nil || claimID != claimWireOracleLiveClaimID {
		t.Fatalf("live fixture claim ID = %q, %v", claimID, err)
	}
	ledger := &Ledger{
		Network: keys.MainNet,
		Headers: claimWireOracleHeaders(t, 2_088_350, 1_783_777_424),
	}

	for _, includeProtobuf := range []bool{false, true} {
		t.Run(map[bool]string{false: "without protobuf", true: "with protobuf"}[includeProtobuf], func(t *testing.T) {
			output := map[string]any{
				"txid": claimWireOracleLiveTXID, "nout": 0, "height": 2_088_350,
				"amount": "0.001", "address": claimWireOracleLiveAddress,
				"confirmations": 1, "timestamp": 1_783_777_424, "type": "claim",
				"claim_op": "create", "name": claimWireOracleLiveName,
				"normalized_name": "t4ng3rin-asmr--02", "claim_id": claimWireOracleLiveClaimID,
				"permanent_url": "lbry://" + claimWireOracleLiveName + "#" + claimWireOracleLiveClaimID,
				"meta":          map[string]any{}, "value_type": "stream",
				"value": claimWireOracleLiveValue(),
			}
			if includeProtobuf {
				output["protobuf"] = claimWireOracleLiveProto
			}
			want := claimWireOracleTransactionMap(transaction, []any{output}, "0.001", "-0.001")
			got, err := ledger.LegacyTransactionJSONWithOptions(
				transaction, LegacyTransactionJSONOptions{IncludeProtobuf: includeProtobuf},
			)
			if err != nil {
				t.Fatal(err)
			}
			claimWireOracleEqual(t, got, want)
		})
	}
}

func TestLegacyTransactionJSONDeterministicClaimOutputsMatchPinnedOracle(t *testing.T) {
	const updateClaimID = "2222222222222222222222222222222222222222"
	claimPayload := claimWireOracleMustHex(t, claimWireOracleFixtureProto)
	pubKeyHash := bytes.Repeat([]byte{0x42}, 20)
	update, err := NewUpdateClaimOutput(200_000_000, "MiXeD", updateClaimID, claimPayload, pubKeyHash)
	if err != nil {
		t.Fatal(err)
	}
	plainSupport, err := NewSupportOutput(300_000_000, "MiXeD", updateClaimID, pubKeyHash)
	if err != nil {
		t.Fatal(err)
	}
	supportData, err := NewSupportDataOutput(
		400_000_000, "MiXeD", updateClaimID,
		claimWireOracleMustHex(t, "000a023a291206737465616479"), pubKeyHash,
	)
	if err != nil {
		t.Fatal(err)
	}
	transaction := claimWireOracleTransaction(
		t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 7,
		NewClaimNameOutput(100_000_000, "MiXeD", claimPayload, pubKeyHash),
		update, plainSupport, supportData,
	)
	ledger := &Ledger{Network: keys.MainNet, Headers: claimWireOracleHeaders(t, 10, 7_007)}
	address, err := transaction.Outputs[0].Address(keys.MainNet)
	if err != nil {
		t.Fatal(err)
	}
	createClaimID, err := transaction.Outputs[0].ClaimID()
	if err != nil {
		t.Fatal(err)
	}

	for _, includeProtobuf := range []bool{false, true} {
		t.Run(map[bool]string{false: "without protobuf", true: "with protobuf"}[includeProtobuf], func(t *testing.T) {
			create := claimWireOracleBaseOutput(transaction, 0, "1.0", address, "claim", 7_007)
			claimWireOracleAddEnvelope(create, "MiXeD", "mixed", createClaimID)
			create["claim_op"] = "create"
			create["value"] = map[string]any{"title": "Fixture"}
			create["value_type"] = "stream"

			update := claimWireOracleBaseOutput(transaction, 1, "2.0", address, "claim", 7_007)
			claimWireOracleAddEnvelope(update, "MiXeD", "mixed", updateClaimID)
			update["claim_op"] = "update"
			update["value"] = map[string]any{"title": "Fixture"}
			update["value_type"] = "stream"

			plainSupport := claimWireOracleBaseOutput(transaction, 2, "3.0", address, "support", 7_007)
			claimWireOracleAddEnvelope(plainSupport, "MiXeD", "mixed", updateClaimID)

			supportData := claimWireOracleBaseOutput(transaction, 3, "4.0", address, "support", 7_007)
			claimWireOracleAddEnvelope(supportData, "MiXeD", "mixed", updateClaimID)
			supportData["value"] = map[string]any{"emoji": ":)", "comment": "steady"}

			if includeProtobuf {
				create["protobuf"] = claimWireOracleFixtureProto
				update["protobuf"] = claimWireOracleFixtureProto
				supportData["protobuf"] = "000a023a291206737465616479"
			}
			want := claimWireOracleTransactionMap(
				transaction, []any{create, update, plainSupport, supportData}, "10.0", "-10.0",
			)
			got, err := ledger.LegacyTransactionJSONWithOptions(
				transaction, LegacyTransactionJSONOptions{IncludeProtobuf: includeProtobuf},
			)
			if err != nil {
				t.Fatal(err)
			}
			claimWireOracleEqual(t, got, want)
		})
	}
}

func TestLegacyTransactionJSONSignedClaimUsesRemoteChannelStub(t *testing.T) {
	unsigned := claimWireOracleMustHex(t, claimWireOracleFixtureProto)
	channelHash := make([]byte, 20)
	for index := range channelHash {
		channelHash[index] = byte(index + 1)
	}
	signature := make([]byte, 64)
	for index := range signature {
		signature[index] = byte(index + 21)
	}
	signed := append([]byte{1}, channelHash...)
	signed = append(signed, signature...)
	signed = append(signed, unsigned[1:]...)
	transaction := claimWireOracleTransaction(
		t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 7,
		NewClaimNameOutput(100_000_000, "MiXeD", signed, bytes.Repeat([]byte{0x43}, 20)),
	)
	ledger := &Ledger{Network: keys.MainNet, Headers: claimWireOracleHeaders(t, 10, 7_007)}
	address, err := transaction.Outputs[0].Address(keys.MainNet)
	if err != nil {
		t.Fatal(err)
	}
	claimID, err := transaction.Outputs[0].ClaimID()
	if err != nil {
		t.Fatal(err)
	}
	output := claimWireOracleBaseOutput(transaction, 0, "1.0", address, "claim", 7_007)
	claimWireOracleAddEnvelope(output, "MiXeD", "mixed", claimID)
	output["claim_op"] = "create"
	output["value"] = map[string]any{"title": "Fixture"}
	output["value_type"] = "stream"
	output["protobuf"] = hex.EncodeToString(signed)
	output["signing_channel"] = map[string]any{
		"channel_id": "14131211100f0e0d0c0b0a090807060504030201",
	}
	output["is_channel_signature_valid"] = false

	got, err := ledger.LegacyTransactionJSONWithOptions(
		transaction, LegacyTransactionJSONOptions{IncludeProtobuf: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := claimWireOracleTransactionMap(transaction, []any{output}, "1.0", "-1.0")
	claimWireOracleEqual(t, got, want)
}

func TestLegacyTransactionJSONChannelClaimProjection(t *testing.T) {
	const payload = "00124a0a2102111111111111111111111111111111111111111111111111111111111111111112036140622a20080212160a14000102030405060708090a0b0c0d0e0f1011121312040a02aabb42074368616e6e656c"
	transaction := claimWireOracleTransaction(
		t, "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", 7,
		NewClaimNameOutput(
			100_000_000, "@channel", claimWireOracleMustHex(t, payload),
			bytes.Repeat([]byte{0x46}, 20),
		),
	)
	ledger := &Ledger{Network: keys.MainNet, Headers: claimWireOracleHeaders(t, 10, 7_007)}
	address, err := transaction.Outputs[0].Address(keys.MainNet)
	if err != nil {
		t.Fatal(err)
	}
	claimID, err := transaction.Outputs[0].ClaimID()
	if err != nil {
		t.Fatal(err)
	}
	output := claimWireOracleBaseOutput(transaction, 0, "1.0", address, "claim", 7_007)
	claimWireOracleAddEnvelope(output, "@channel", "@channel", claimID)
	output["claim_op"] = "create"
	output["value_type"] = "channel"
	output["has_signing_key"] = false
	output["protobuf"] = payload
	output["value"] = map[string]any{
		"title":         "Channel",
		"email":         "a@b",
		"public_key":    "021111111111111111111111111111111111111111111111111111111111111111",
		"public_key_id": "bUbEYvQUQQWgkUUQa9kBRjJJwrnKVUMhy2",
		"featured": []any{
			"131211100f0e0d0c0b0a09080706050403020100", "bbaa",
		},
	}

	got, err := ledger.LegacyTransactionJSONWithOptions(
		transaction, LegacyTransactionJSONOptions{IncludeProtobuf: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := claimWireOracleTransactionMap(transaction, []any{output}, "1.0", "-1.0")
	claimWireOracleEqual(t, got, want)
}

func TestLegacyTransactionJSONClaimDecodeErrorKeepsEnvelope(t *testing.T) {
	transaction := claimWireOracleTransaction(
		t, "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", 7,
		NewClaimNameOutput(100_000_000, "MiXeD", []byte{0, 0x80}, bytes.Repeat([]byte{0x44}, 20)),
	)
	ledger := &Ledger{Network: keys.MainNet, Headers: claimWireOracleHeaders(t, 10, 7_007)}
	address, err := transaction.Outputs[0].Address(keys.MainNet)
	if err != nil {
		t.Fatal(err)
	}
	claimID, err := transaction.Outputs[0].ClaimID()
	if err != nil {
		t.Fatal(err)
	}
	output := claimWireOracleBaseOutput(transaction, 0, "1.0", address, "claim", 7_007)
	claimWireOracleAddEnvelope(output, "MiXeD", "mixed", claimID)
	output["claim_op"] = "create"

	got, err := ledger.LegacyTransactionJSONWithOptions(
		transaction, LegacyTransactionJSONOptions{IncludeProtobuf: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := claimWireOracleTransactionMap(transaction, []any{output}, "1.0", "-1.0")
	claimWireOracleEqual(t, got, want)
}

func TestLegacyTransactionJSONPurchaseHydrationMatchesPinnedClaimOracle(t *testing.T) {
	const purchaseClaimID = "3333333333333333333333333333333333333333"
	purchaseData, err := NewPurchaseDataOutput(purchaseClaimID)
	if err != nil {
		t.Fatal(err)
	}
	transaction := claimWireOracleTransaction(
		t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 7,
		NewPayPubKeyHashOutput(500_000_000, bytes.Repeat([]byte{0x45}, 20)), purchaseData,
	)
	ledger := &Ledger{Network: keys.MainNet, Headers: claimWireOracleHeaders(t, 10, 7_007)}
	address, err := transaction.Outputs[0].Address(keys.MainNet)
	if err != nil {
		t.Fatal(err)
	}

	for _, hydrated := range []bool{false, true} {
		t.Run(map[bool]string{false: "remote unhydrated", true: "local hydrated"}[hydrated], func(t *testing.T) {
			transaction.Outputs[0].Purchase = nil
			paymentType := "payment"
			if hydrated {
				transaction.Outputs[0].Purchase = &transaction.Outputs[1]
				paymentType = "purchase"
			}
			payment := claimWireOracleBaseOutput(transaction, 0, "5.0", address, paymentType, 7_007)
			if hydrated {
				payment["claim_id"] = purchaseClaimID
			}
			data := claimWireOracleBaseOutput(transaction, 1, "0.0", nil, "data", 7_007)
			want := claimWireOracleTransactionMap(transaction, []any{payment, data}, "5.0", "-5.0")
			got, err := ledger.LegacyTransactionJSONWithOptions(
				transaction, LegacyTransactionJSONOptions{IncludeProtobuf: true},
			)
			if err != nil {
				t.Fatal(err)
			}
			claimWireOracleEqual(t, got, want)
		})
	}
}

func claimWireOracleLiveValue() map[string]any {
	return map[string]any{
		"source": map[string]any{
			"hash": "727c8f4f681de1cee70903ccfbef38dac5d39104e247ec4d7cc597fdafc84fd1d8f89333207d01c16280e1bd4380dd05",
			"name": "RDT_20260711_103125.mp4", "size": "201549988", "media_type": "video/mp4",
			"sd_hash": "36d72ae4e3d594fe090a17e881f53fd2a1acde20dcb64cc495b72c2f1a0f2cd838517b3eb21b54132367e68e4d601a58",
		},
		"license": "None", "release_time": "1783777307",
		"video": map[string]any{"width": 1280, "height": 718, "duration": 723},
		"title": "T4NG3R1N ASMR #03",
		"thumbnail": map[string]any{
			"url": "https://thumbs.odycdn.com/cc7adb516680e14604a19db0adf9beba.webp",
		},
		"tags":      []any{"asmr", "ear licking", "ear eating", "c:disable-comments", "disable-support"},
		"languages": []any{"en"}, "stream_type": "video",
	}
}

func claimWireOracleTransaction(
	t *testing.T, transactionID string, height int64, outputs ...TransactionOutput,
) *Transaction {
	t.Helper()
	transaction := NewTransaction()
	transaction.AddOutputs(outputs)
	transaction.Height = height
	if transactionID == "" {
		return transaction
	}
	displayHash := claimWireOracleMustHex(t, transactionID)
	if len(displayHash) != len(transaction.Hash) {
		t.Fatalf("transaction ID length = %d", len(displayHash))
	}
	internalHash := reverseTransactionBytes(displayHash)
	copy(transaction.Hash[:], internalHash)
	transaction.ID = transactionID
	for index := range transaction.Outputs {
		transaction.Outputs[index].TransactionID = transactionID
		transaction.Outputs[index].TransactionHash = transaction.Hash
	}
	return transaction
}

func claimWireOracleHeaders(t *testing.T, bestHeight int, timestamp int64) *Headers {
	t.Helper()
	headers := newTransactionExecutionHeaders(t)
	headers.mu.Lock()
	headers.size = bestHeight + 1
	headers.firstBlockTimestamp = timestamp
	headers.timestampAverageOffset = 0
	headers.mu.Unlock()
	return headers
}

func claimWireOracleBaseOutput(
	transaction *Transaction, position uint32, amount string, address any, outputType string, timestamp int64,
) map[string]any {
	return map[string]any{
		"txid": transaction.ID, "nout": position, "height": transaction.Height,
		"amount": amount, "address": address,
		"confirmations": int64(4), "timestamp": timestamp, "type": outputType,
	}
}

func claimWireOracleAddEnvelope(
	output map[string]any, name, normalizedName, claimID string,
) {
	output["name"] = name
	output["normalized_name"] = normalizedName
	output["claim_id"] = claimID
	output["permanent_url"] = "lbry://" + name + "#" + claimID
	output["meta"] = map[string]any{}
}

func claimWireOracleTransactionMap(
	transaction *Transaction, outputs []any, totalOutput, totalFee string,
) map[string]any {
	return map[string]any{
		"txid": transaction.ID, "height": transaction.Height,
		"inputs": []any{}, "outputs": outputs,
		"total_input": "0.0", "total_output": totalOutput, "total_fee": totalFee,
		"hex": hex.EncodeToString(transaction.Raw),
	}
}

func claimWireOracleMustHex(t *testing.T, encoded string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func claimWireOracleEqual(t *testing.T, got, want any) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("legacy claim wire JSON =\n%s\nwant\n%s", gotJSON, wantJSON)
	}
}
