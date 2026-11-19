package rpc

import (
	"encoding/json"
	"testing"

	"lbry/daemon/wallet"
)

func TestAccountDetails(t *testing.T) {
	account := &wallet.Account{
		Name: "main", Locked: true, Seed: "enc-seed", PublicKey: "not a key",
		Certificates: map[string]json.RawMessage{"a": nil, "b": nil},
	}
	details := accountDetails(account, false)
	if details["ledger"] != "lbc_mainnet" || details["name"] != "main" || details["encrypted"] != true ||
		details["certificates"] != 2 || details["satoshis"] != 0 || details["coins"] != 0 {
		t.Errorf("unexpected details %v", details)
	}
	if _, has := details["seed"]; has {
		t.Error("the seed must only be shown on request")
	}
	if got := accountDetails(account, true)["seed"]; got != "enc-seed" {
		t.Errorf("seed = %v", got)
	}
	account.Ledger = "lbc_testnet"
	if got := accountDetails(account, false)["ledger"]; got != "lbc_testnet" {
		t.Errorf("ledger = %v", got)
	}
}
