package wallet

import (
	"encoding/json"
	"testing"
)

func updateScript(hash []byte) []byte {
	return append([]byte{opUpdateClaim, 4, 'n', 'a', 'm', 'e', 20}, append(append(make([]byte, 20), 2, 'c', 'd', 0x6d, 0x6d), p2pkh(hash)...)...)
}

func TestClaimOutputsListsClaimsAndUpdatesOnly(t *testing.T) {
	hub := &fakeHub{}
	plain := keyAt(t, publicKeyG, ReceivingChain, 0)
	created := keyAt(t, publicKeyG, ReceivingChain, 1)
	updated := keyAt(t, publicKeyG, ChangeChain, 0)
	supported := keyAt(t, publicKeyG, ReceivingChain, 2)
	hub.fund(plain, p2pkh(plain.Hash160()), 1_000, 5)
	hub.fund(created, claimScript(created.Hash160()), 20_000, 5)
	hub.fund(updated, updateScript(updated.Hash160()), 30_000, 6)
	hub.fund(supported, supportScript(supported.Hash160()), 40_000, 6)

	account := &Account{ID: "bAccountID", PublicKey: publicKeyG}
	claims, err := ClaimOutputs(hub, []*Account{account}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 2 {
		t.Fatalf("got %d claim outputs, want 2: %+v", len(claims), claims)
	}
	amounts := map[int64]ClaimOutput{}
	for _, claim := range claims {
		amounts[claim.Amount] = claim
	}
	if got := amounts[20_000]; got.Address != created.Address() || got.AccountID != account.ID || got.Script[0] != opClaimName || got.Index != 0 {
		t.Errorf("created claim: %+v, address want %s", got, created.Address())
	}
	if got := amounts[30_000]; got.Address != updated.Address() || got.Script[0] != opUpdateClaim {
		t.Errorf("updated claim: %+v", got)
	}
}

func TestClaimOutputsNeedsNoTransactionsWhenNothingIsUnspent(t *testing.T) {
	hub := &fakeHub{}
	claims, err := ClaimOutputs(hub, []*Account{{PublicKey: publicKeyG}}, 0)
	if err != nil || len(claims) != 0 || hub.calls["blockchain.transaction.get_batch"] != 0 {
		t.Errorf("%v %v %v", claims, err, hub.calls)
	}
}

func TestClaimOutputsHubFailuresAreErrors(t *testing.T) {
	hub := &fakeHub{failOn: "blockchain.scripthash.get_history"}
	if _, err := ClaimOutputs(hub, []*Account{{PublicKey: publicKeyG}}, 0); err == nil {
		t.Error("a hub that is down is an error, not an empty list")
	}
	if _, err := ClaimOutputs(&fakeHub{}, []*Account{{PublicKey: publicKeyG}}, 2); err != ErrUnsupportedConfirmations {
		t.Errorf("confirmations above 1: %v", err)
	}
}

func TestAccountHasKey(t *testing.T) {
	account := Account{Certificates: map[string]json.RawMessage{"bAddress": json.RawMessage(`"pem"`)}}
	if !account.HasKey("bAddress") || account.HasKey("bOther") {
		t.Error("a key is held for the address it is stored under")
	}
	if (&Account{}).HasKey("bAddress") {
		t.Error("no certificates, no keys")
	}
}
