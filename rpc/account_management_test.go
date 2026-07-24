package rpc

import (
	"encoding/json"
	"testing"

	walletpkg "lbry/daemon/wallet"
)

func TestAccountListDetailsAndSpecificPagination(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	result := fileMutationRPCResult(t, fixture.server, "account_list", map[string]any{
		"account_id": fixture.account.ID, "show_seed": true,
	}).(map[string]any)
	items := result["items"].([]any)
	item := items[0].(map[string]any)
	if len(items) != 1 || result["page"] != json.Number("1") || result["page_size"] != json.Number("1") ||
		item["id"] != fixture.account.ID || item["satoshis"] != json.Number("500000000") ||
		item["coins"] != json.Number("5.0") || item["seed"] != fixture.account.Seed ||
		item["certificates"] != json.Number("0") || item["address_generator"] == nil {
		t.Fatalf("account_list = %#v", result)
	}
}

func TestAccountManagementLifecycle(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	fixture.ledger.SPVNetwork = nil
	selectedWallet := fixture.server.walletManagerProvider().DefaultWallet()
	created := fileMutationRPCResult(t, fixture.server, "account_create", map[string]any{
		"account_name": "created", "single_key": true,
	}).(map[string]any)
	createdID := created["id"].(string)
	if created["name"] != "created" || created["is_default"] != false || len(selectedWallet.Accounts) != 2 {
		t.Fatalf("account_create = %#v, accounts=%d", created, len(selectedWallet.Accounts))
	}

	importSource, err := walletpkg.GenerateAccount(
		fixture.account.Network, "source", walletpkg.DeterministicChainGenerator,
	)
	if err != nil {
		t.Fatal(err)
	}
	added := fileMutationRPCResult(t, fixture.server, "account_add", map[string]any{
		"account_name": "imported", "seed": importSource.Seed,
	}).(map[string]any)
	addedID := added["id"].(string)
	if added["name"] != "imported" || len(selectedWallet.Accounts) != 3 {
		t.Fatalf("account_add = %#v, accounts=%d", added, len(selectedWallet.Accounts))
	}

	updated := fileMutationRPCResult(t, fixture.server, "account_set", map[string]any{
		"account_id": addedID, "new_name": "primary", "default": true,
		"change_gap": 9, "change_max_uses": 4, "receiving_gap": 11, "receiving_max_uses": 5,
	}).(map[string]any)
	account, err := selectedWallet.Account(addedID)
	if err != nil {
		t.Fatal(err)
	}
	if updated["name"] != "primary" || updated["is_default"] != true || selectedWallet.DefaultAccount() != account ||
		account.Change.Gap != int64(9) || account.Change.MaximumUsesPerAddress != int64(4) ||
		account.Receiving.Gap != int64(11) || account.Receiving.MaximumUsesPerAddress != int64(5) {
		t.Fatalf("account_set = %#v, account=%#v", updated, account)
	}

	gaps := fileMutationRPCResult(t, fixture.server, "account_max_address_gap", map[string]any{
		"account_id": createdID,
	}).(map[string]any)
	if gaps["max_change_gap"] != json.Number("0") || gaps["max_receiving_gap"] != json.Number("0") {
		t.Fatalf("single-address gaps = %#v", gaps)
	}

	removed := fileMutationRPCResult(t, fixture.server, "account_remove", map[string]any{
		"account_id": createdID,
	}).(map[string]any)
	if removed["id"] != createdID || removed["is_default"] != false || len(selectedWallet.Accounts) != 2 {
		t.Fatalf("account_remove = %#v, accounts=%d", removed, len(selectedWallet.Accounts))
	}
	if _, err := selectedWallet.Account(createdID); err == nil {
		t.Fatal("removed account remains in wallet")
	}
	if account.GeneratorName != walletpkg.DeterministicChainGenerator {
		t.Fatalf("imported generator = %q", account.GeneratorName)
	}
}
