package rpc

import (
	"encoding/hex"
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
