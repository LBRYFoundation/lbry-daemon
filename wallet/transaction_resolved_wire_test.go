package wallet

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"lbry/daemon/wallet/keys"
)

const (
	transactionResolvedWireStream     = "000a00420746697874757265"
	transactionResolvedWireCollection = "001a20080212040a02010212160a1400000000000000000000000000000000000000004a0a436f6c6c656374696f6e"
	transactionResolvedWireRepost     = "0022160a14000102030405060708090a0b0c0d0e0f1011121342065265706f7374"
)

func TestLegacyTransactionJSONResolvedRepostRecursesWithSignatureAndProtobuf(t *testing.T) {
	ledger := transactionResolvedWireLedger(t)
	rootPayload := transactionResolvedWireSignedPayload(
		t, transactionResolvedWireRepost, 0x91,
	)
	nestedPayload := transactionResolvedWireSignedPayload(
		t, transactionResolvedWireStream, 0x92,
	)
	nested := transactionResolvedWireClaimTransaction(
		t, 0x92, "nested", nestedPayload,
	)
	root := transactionResolvedWireClaimTransaction(t, 0x91, "repost", rootPayload)
	root.Outputs[0].RepostedClaim = &nested.Outputs[0]

	encoded, err := ledger.legacyTransactionOutputJSON(
		&root.Outputs[0], LegacyTransactionJSONOptions{IncludeProtobuf: true}, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := encoded["signing_channel"]; exists {
		t.Fatalf("outer check_signature=false checked root: %#v", encoded)
	}
	if encoded["protobuf"] != hex.EncodeToString(rootPayload) {
		t.Fatalf("root protobuf = %#v", encoded["protobuf"])
	}
	reposted, ok := encoded["reposted_claim"].(map[string]any)
	if !ok || reposted["protobuf"] != hex.EncodeToString(nestedPayload) ||
		reposted["is_channel_signature_valid"] != false {
		t.Fatalf("recursively encoded repost = %#v", encoded["reposted_claim"])
	}
	if _, ok := reposted["signing_channel"].(map[string]any); !ok {
		t.Fatalf("nested repost signature stub = %#v", reposted)
	}
}

func TestLegacyTransactionJSONRecursivelyEncodesBlockedCensorMetadata(t *testing.T) {
	ledger := transactionResolvedWireLedger(t)
	root := transactionResolvedWireClaimTransaction(
		t, 0x95, "blocked", claimWireOracleMustHex(t, transactionResolvedWireStream),
	)
	censor := transactionResolvedWireClaimTransaction(
		t, 0x96, "censor", claimWireOracleMustHex(t, transactionResolvedWireStream),
	)
	root.Outputs[0].Meta = map[string]any{"error": map[string]any{
		"name": "BLOCKED", "text": "blocked", "censor": &censor.Outputs[0],
	}}

	encoded, err := ledger.legacyTransactionOutputJSON(
		&root.Outputs[0], LegacyTransactionJSONOptions{IncludeProtobuf: true}, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	errorMeta := encoded["meta"].(map[string]any)["error"].(map[string]any)
	censorWire, ok := errorMeta["censor"].(map[string]any)
	if !ok || censorWire["txid"] != censor.ID || censorWire["name"] != "censor" ||
		censorWire["protobuf"] == nil {
		t.Fatalf("blocked censor metadata = %#v", errorMeta["censor"])
	}
	if _, leaked := censorWire["TransactionID"]; leaked {
		t.Fatalf("blocked censor leaked Go struct fields: %#v", censorWire)
	}

	root.Outputs[0].Meta = map[string]any{"error": map[string]any{
		"name": "BLOCKED", "censor": &root.Outputs[0],
	}}
	if _, err := ledger.legacyTransactionOutputJSON(
		&root.Outputs[0], LegacyTransactionJSONOptions{}, true,
	); !errors.Is(err, ErrTransactionWireRelationCycle) {
		t.Fatalf("cyclic censor metadata error = %v", err)
	}
}

func TestLegacyTransactionJSONResolvedCollectionStatesAndNestedDecodeError(t *testing.T) {
	ledger := transactionResolvedWireLedger(t)
	collection := transactionResolvedWireClaimTransaction(
		t, 0xa1, "collection", claimWireOracleMustHex(t, transactionResolvedWireCollection),
	)
	encoded, err := ledger.legacyTransactionOutputJSON(
		&collection.Outputs[0], LegacyTransactionJSONOptions{}, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := encoded["claims"]; exists {
		t.Fatalf("unresolved collection emitted claims: %#v", encoded)
	}

	collection.Outputs[0].Claims = make([]*TransactionOutput, 0)
	encoded, err = ledger.legacyTransactionOutputJSON(
		&collection.Outputs[0], LegacyTransactionJSONOptions{}, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if claims, ok := encoded["claims"].([]any); !ok || len(claims) != 0 {
		t.Fatalf("resolved empty collection claims = %#v", encoded["claims"])
	}

	nested := transactionResolvedWireClaimTransaction(
		t, 0xa2, "nested", claimWireOracleMustHex(t, transactionResolvedWireStream),
	)
	malformed := transactionResolvedWireClaimTransaction(
		t, 0xa3, "malformed", []byte{0, 0x80},
	)
	collection.Outputs[0].Claims = []*TransactionOutput{
		&nested.Outputs[0], nil, &malformed.Outputs[0],
	}
	encoded, err = ledger.legacyTransactionOutputJSON(
		&collection.Outputs[0], LegacyTransactionJSONOptions{IncludeProtobuf: true}, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	claims := encoded["claims"].([]any)
	if len(claims) != 3 || claims[1] != nil {
		t.Fatalf("resolved ordered collection claims = %#v", claims)
	}
	first := claims[0].(map[string]any)
	if first["value_type"] != "stream" || first["protobuf"] == nil {
		t.Fatalf("resolved collection member = %#v", first)
	}
	third := claims[2].(map[string]any)
	for _, absent := range []string{"value", "value_type", "protobuf"} {
		if _, exists := third[absent]; exists {
			t.Fatalf("malformed nested member contains %s: %#v", absent, third)
		}
	}
}

func TestLegacyTransactionJSONResolvedPurchaseAndReceiptSemantics(t *testing.T) {
	ledger := transactionResolvedWireLedger(t)
	const purchasedClaimID = "1111111111111111111111111111111111111111"
	resolvedClaim := transactionResolvedWireClaimTransaction(
		t, 0xb1, "purchased", claimWireOracleMustHex(t, transactionResolvedWireStream),
	)
	purchaseData, err := NewPurchaseDataOutput(purchasedClaimID)
	if err != nil {
		t.Fatal(err)
	}
	purchase := claimWireOracleTransaction(
		t, strings.Repeat("b2", 32), 7,
		NewPayPubKeyHashOutput(100_000_000, bytes.Repeat([]byte{0xb2}, 20)),
		purchaseData,
	)
	purchase.Outputs[0].PurchasedClaim = &resolvedClaim.Outputs[0]

	encoded, err := ledger.legacyTransactionOutputJSON(
		&purchase.Outputs[0], LegacyTransactionJSONOptions{IncludeProtobuf: true}, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if encoded["type"] != "payment" {
		t.Fatalf("unlinked purchased claim changed type: %#v", encoded)
	}
	if _, exists := encoded["claim"]; exists {
		t.Fatalf("unlinked payment emitted purchased claim: %#v", encoded)
	}

	purchase.Outputs[0].Purchase = &purchase.Outputs[1]
	encoded, err = ledger.legacyTransactionOutputJSON(
		&purchase.Outputs[0], LegacyTransactionJSONOptions{IncludeProtobuf: true}, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	claim, ok := encoded["claim"].(map[string]any)
	if encoded["type"] != "purchase" || encoded["claim_id"] != purchasedClaimID ||
		!ok || claim["value_type"] != "stream" || claim["protobuf"] == nil {
		t.Fatalf("resolved purchase = %#v", encoded)
	}
	if _, exists := encoded["protobuf"]; exists {
		t.Fatalf("purchase payment emitted protobuf: %#v", encoded)
	}

	receiptPurchase := claimWireOracleTransaction(
		t, strings.Repeat("b3", 32), 7,
		NewPayPubKeyHashOutput(100_000_000, bytes.Repeat([]byte{0xb3}, 20)),
		purchaseData,
	)
	receiptPurchase.Outputs[0].Purchase = &receiptPurchase.Outputs[1]
	claimTransaction := transactionResolvedWireClaimTransaction(
		t, 0xb4, "priced", claimWireOracleMustHex(t, transactionResolvedWireStream),
	)
	claimTransaction.Outputs[0].PurchaseReceipt = &receiptPurchase.Outputs[0]
	encoded, err = ledger.legacyTransactionOutputJSON(
		&claimTransaction.Outputs[0],
		LegacyTransactionJSONOptions{IncludeProtobuf: true}, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	receipt, ok := encoded["purchase_receipt"].(map[string]any)
	if !ok || receipt["type"] != "purchase" || receipt["claim_id"] != purchasedClaimID ||
		encoded["protobuf"] == nil {
		t.Fatalf("resolved purchase receipt = %#v", encoded)
	}
	if _, exists := receipt["protobuf"]; exists {
		t.Fatalf("purchase receipt payment emitted protobuf: %#v", receipt)
	}
}

func TestLegacyTransactionJSONResolvedRelationCycleMatchesPythonError(t *testing.T) {
	ledger := transactionResolvedWireLedger(t)
	repost := transactionResolvedWireClaimTransaction(
		t, 0xc1, "cycle", claimWireOracleMustHex(t, transactionResolvedWireRepost),
	)
	repost.Outputs[0].RepostedClaim = &repost.Outputs[0]
	encoded, err := ledger.legacyTransactionOutputJSON(
		&repost.Outputs[0], LegacyTransactionJSONOptions{}, true,
	)
	if encoded != nil || !errors.Is(err, ErrTransactionWireRelationCycle) ||
		transactionWirePythonErrorName(err) != "RecursionError" ||
		err.Error() != "maximum recursion depth exceeded" {
		t.Fatalf("resolved relation cycle = %#v, %T %v", encoded, err, err)
	}
}

func transactionResolvedWireLedger(t *testing.T) *Ledger {
	t.Helper()
	return &Ledger{
		Network: keys.MainNet,
		Headers: claimWireOracleHeaders(t, 20, 10_012),
	}
}

func transactionResolvedWireClaimTransaction(
	t *testing.T, marker byte, name string, payload []byte,
) *Transaction {
	t.Helper()
	return claimWireOracleTransaction(
		t, strings.Repeat(hex.EncodeToString([]byte{marker}), 32), 12,
		NewClaimNameOutput(
			100_000_000, name, payload, bytes.Repeat([]byte{marker}, 20),
		),
	)
}

func transactionResolvedWireSignedPayload(t *testing.T, unsignedHex string, marker byte) []byte {
	t.Helper()
	unsigned := claimWireOracleMustHex(t, unsignedHex)
	channelHash := bytes.Repeat([]byte{marker}, 20)
	signature := bytes.Repeat([]byte{marker + 1}, keys.CompactSignatureLength)
	payload := make([]byte, 0, 1+len(channelHash)+len(signature)+len(unsigned)-1)
	payload = append(payload, 1)
	payload = append(payload, channelHash...)
	payload = append(payload, signature...)
	return append(payload, unsigned[1:]...)
}
