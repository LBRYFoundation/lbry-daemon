package rpc

import (
	"slices"
	"testing"

	"lbry/daemon/wallet"
)

func TestStringList(t *testing.T) {
	if list, ok := stringList(nil); !ok || list != nil {
		t.Errorf("nil: %v %v", list, ok)
	}
	if list, ok := stringList("a"); !ok || !slices.Equal(list, []string{"a"}) {
		t.Errorf("string: %v %v", list, ok)
	}
	if list, ok := stringList([]any{"a", "b"}); !ok || !slices.Equal(list, []string{"a", "b"}) {
		t.Errorf("list: %v %v", list, ok)
	}
	if _, ok := stringList([]any{"a", 1.0}); ok {
		t.Error("a list with a number is refused")
	}
	if _, ok := stringList(3.0); ok {
		t.Error("a number is refused")
	}
}

func TestClaimIDOf(t *testing.T) {
	hash := make([]byte, 20)
	pay := append(append([]byte{0x76, 0xa9, 0x14}, hash...), 0x88, 0xac)

	// A new claim: the id comes from its transaction and position (computed with an independent script).
	created := append([]byte{opClaimName, 4, 'n', 'a', 'm', 'e', 2, 'c', 'd', 0x6d, 0x75}, pay...)
	id, name, ok := claimIDOf(wallet.ClaimOutput{TxID: "0000000000000000000000000000000000000000000000000000000000000007", Index: 2, Script: created})
	if !ok || name != "name" || id != "0e3e05aec96604bcdaedaf359694f79a518d3c45" {
		t.Errorf("created: %s %s %v", id, name, ok)
	}

	// An update carries the id in its script, in the byte order the wallet server stores it.
	raw := make([]byte, 20)
	raw[0], raw[19] = 0x01, 0xff
	updated := append([]byte{opUpdateClaim, 4, 'n', 'a', 'm', 'e', 20}, raw...)
	updated = append(append(updated, 2, 'c', 'd', 0x6d, 0x6d), pay...)
	id, name, ok = claimIDOf(wallet.ClaimOutput{TxID: "00", Script: updated})
	if !ok || name != "name" || id != "ff00000000000000000000000000000000000001" {
		t.Errorf("updated: %s %s %v", id, name, ok)
	}

	if _, _, ok := claimIDOf(wallet.ClaimOutput{Script: pay}); ok {
		t.Error("a payment is not a claim")
	}
}
