package rpc

import (
	"encoding/hex"
	"slices"
	"testing"
)

// claimTransaction is a transaction with one output, a claim script holding the claim data.
func claimTransaction(t *testing.T, name string, data []byte) string {
	t.Helper()
	script := []byte{opClaimName, byte(len(name))}
	script = append(script, name...)
	script = append(script, 0x4c, byte(len(data)))
	script = append(script, data...)
	script = append(script, 0x6d, 0x75, 0x76, 0xa9, 0x14)
	script = append(script, make([]byte, 20)...)
	script = append(script, 0x88, 0xac)
	raw := []byte{1, 0, 0, 0} // version
	raw = append(raw, 1)      // one input: a hash, an index, an empty script and a sequence
	raw = append(raw, make([]byte, 32+4)...)
	raw = append(raw, 0)
	raw = append(raw, 0xff, 0xff, 0xff, 0xff)
	raw = append(raw, 1)                      // one output
	raw = append(raw, 1, 0, 0, 0, 0, 0, 0, 0) // amount
	raw = append(raw, byte(len(script)))
	raw = append(raw, script...)
	raw = append(raw, 0, 0, 0, 0) // lock time
	return hex.EncodeToString(raw)
}

func TestConvertProtobufToClaimReadsUnsignedClaims(t *testing.T) {
	// An unsigned claim: a version byte of 0, then the protobuf.
	txHash := make([]byte, 32)
	for i := range txHash {
		txHash[i] = byte(i + 1)
	}
	// As the handlers pass it: reversed in place, so it is the txid as written.
	txid := append([]byte(nil), txHash...)
	for i, j := 0, len(txid)-1; i < j; i, j = i+1, j-1 {
		txid[i], txid[j] = txid[j], txid[i]
	}
	displayed := hex.EncodeToString(txid)

	claim := convertProtobufToClaim(map[int]any{1: txid}, map[string]any{
		displayed: []any{claimTransaction(t, "@name", []byte{0, 0x12, 0x00}), map[string]any{}},
	})
	if claim["txid"] != displayed {
		t.Errorf("txid %v, want %s", claim["txid"], displayed)
	}
	if claim["nout"] != uint64(0) {
		t.Errorf("nout %v, want 0", claim["nout"])
	}
	id, _ := claim["claim_id"].(string)
	if len(id) != 40 {
		t.Errorf("an unsigned claim has a claim id, got %q", id)
	}
	if want := hex.EncodeToString(ComputeClaimID(txHash, 0)); id != want {
		t.Errorf("claim id %s, want %s", id, want)
	}
}

func TestConvertProtobufToClaimLeavesOldSchemesAlone(t *testing.T) {
	txid := make([]byte, 32)
	displayed := hex.EncodeToString(txid)
	claim := convertProtobufToClaim(map[int]any{1: txid}, map[string]any{
		displayed: []any{claimTransaction(t, "name", []byte{7, 1, 2}), map[string]any{}},
	})
	if _, has := claim["claim_id"]; has {
		t.Error("claim data of an unknown version is not read")
	}
}

// A claim the hub says was signed by a channel carries that channel, read from the channel's own transaction.
func TestConvertProtobufToClaimAddsTheSigningChannel(t *testing.T) {
	display := func(hash []byte) []byte { return reverseBytes(hash) }
	streamHash := make([]byte, 32)
	channelHash := make([]byte, 32)
	for i := range streamHash {
		streamHash[i] = byte(i + 1)
		channelHash[i] = byte(100 + i)
	}
	streamID, channelID := hex.EncodeToString(display(streamHash)), hex.EncodeToString(display(channelHash))

	signed := append([]byte{1}, make([]byte, 84)...)
	signed = append(signed, 0x0a, 0x03, 0x12, 0x01, 0x61)
	transactions := map[string]any{
		streamID:  []any{claimTransaction(t, "video", signed), map[string]any{}},
		channelID: []any{claimTransaction(t, "@creator", []byte{0, 0x12, 0x00}), map[string]any{}},
	}
	// As the hub sends it, and as hubClaimSearch leaves it once it has reversed the hashes in place.
	protobuf := map[int]any{
		1: append([]byte(nil), streamHash...),
		7: map[int]any{1: map[int]any{1: append([]byte(nil), channelHash...)}, 4: []byte("@creator#a/video#b")},
	}
	if got := channelReferenceTxID(protobuf); got != channelID {
		t.Fatalf("channel txid %q, want %q", got, channelID)
	}
	slices.Reverse(protobuf[1].([]byte))

	claim := convertProtobufToClaim(protobuf, transactions)
	if claim["name"] != "video" || claim["is_channel_signature_valid"] != true {
		t.Errorf("name %v, valid %v", claim["name"], claim["is_channel_signature_valid"])
	}
	channel, _ := claim["signing_channel"].(map[string]any)
	if channel == nil {
		t.Fatal("no signing_channel")
	}
	if channel["name"] != "@creator" || channel["txid"] != channelID || channel["value_type"] != "channel" {
		t.Errorf("unexpected channel: %v", channel)
	}
	if id, _ := channel["claim_id"].(string); len(id) != 40 || channel["permanent_url"] != "lbry://@creator#"+id {
		t.Errorf("channel claim id / url: %v", channel)
	}

	// Unsigned: no channel, and none is claimed.
	delete(protobuf, 7)
	if bare := convertProtobufToClaim(protobuf, transactions); bare["signing_channel"] != nil || bare["is_channel_signature_valid"] != nil {
		t.Errorf("a claim without a channel reference has none: %v", bare)
	}
	// A channel whose transaction was not fetched is left out, not a panic.
	delete(transactions, channelID)
	protobuf[7] = map[int]any{1: map[int]any{1: append([]byte(nil), channelHash...)}}
	if missing := convertProtobufToClaim(protobuf, transactions); missing["signing_channel"] != nil {
		t.Errorf("channel without a transaction: %v", missing["signing_channel"])
	}
}

// A text field whose bytes happen to parse as a protobuf message must still read as text, not panic.
func TestConvertProtobufToClaimReadsTextThatLooksLikeAMessage(t *testing.T) {
	txid := make([]byte, 32)
	txid[0] = 9
	displayed := hex.EncodeToString(txid)
	looksLikeAMessage := "\x08\x01" // field 1, varint 1
	data := []byte{0}
	data = append(data, 0x0a, 0x03, 0x12, 0x01, 'a')        // a stream by "a"
	data = append(data, 0x42, byte(len(looksLikeAMessage))) // field 8, the title
	data = append(data, looksLikeAMessage...)
	data = append(data, 0x5a, 0x01, 'x', 0x5a, 0x02, 0x08, 0x01) // field 11, two tags; the second parses as a message

	claim := convertProtobufToClaim(map[int]any{1: txid}, map[string]any{
		displayed: []any{claimTransaction(t, "video", data), map[string]any{}},
	})
	value, _ := claim["value"].(map[string]any)
	if value["title"] != looksLikeAMessage {
		t.Errorf("title %q, want %q", value["title"], looksLikeAMessage)
	}
	if tags, _ := value["tags"].([]string); len(tags) != 2 || tags[0] != "x" || tags[1] != "\x08\x01" {
		t.Errorf("tags %q", value["tags"])
	}
	if value["author"] != "a" {
		t.Errorf("author %v", value["author"])
	}
}

// A channel claim shows the public key it signs with, which is how the wallet finds the private key.
func TestConvertProtobufToClaimShowsTheChannelPublicKey(t *testing.T) {
	publicKey, _ := hex.DecodeString("02c9f0f75acebc5419d784b3f66f87e37464c8fbe63bbdda98f86d18a534711e67")
	channel := append([]byte{0x0a, byte(len(publicKey))}, publicKey...)
	data := append([]byte{0, 0x12, byte(len(channel))}, channel...)

	// As the handlers pass it: reversed in place, so it is the txid as written.
	txid := make([]byte, 32)
	txid[0] = 7
	displayed := hex.EncodeToString(txid)
	transactions := map[string]any{displayed: []any{claimTransaction(t, "@creator", data), map[string]any{}}}

	claim := convertProtobufToClaim(map[int]any{1: txid}, transactions)
	value, _ := claim["value"].(map[string]any)
	if claim["value_type"] != "channel" || value["public_key"] != hex.EncodeToString(publicKey) {
		t.Fatalf("value_type %v, value %v", claim["value_type"], value)
	}
}
