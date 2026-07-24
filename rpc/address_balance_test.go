package rpc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestWalletAndAccountBalanceRPCDetailedShape(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	walletBalance := fileMutationRPCResult(t, fixture.server, "wallet_balance", map[string]any{}).(map[string]any)
	accountBalance := fileMutationRPCResult(t, fixture.server, "account_balance", map[string]any{
		"account_id": fixture.account.ID,
	}).(map[string]any)
	for name, balance := range map[string]map[string]any{"wallet": walletBalance, "account": accountBalance} {
		subtotals := balance["reserved_subtotals"].(map[string]any)
		if balance["total"] != "5.0" || balance["available"] != "5.0" || balance["reserved"] != "0.0" ||
			subtotals["claims"] != "0.0" || subtotals["supports"] != "0.0" || subtotals["tips"] != "0.0" {
			t.Fatalf("%s balance = %#v", name, balance)
		}
	}
	persistStreamMutationFixture(t, &fixture, "reserved-stream")
	reserved := fileMutationRPCResult(t, fixture.server, "account_balance", map[string]any{}).(map[string]any)
	subtotals := reserved["reserved_subtotals"].(map[string]any)
	if reserved["total"] != "7.0" || reserved["available"] != "5.0" || reserved["reserved"] != "2.0" ||
		subtotals["claims"] != "2.0" || subtotals["supports"] != "0.0" || subtotals["tips"] != "0.0" {
		t.Fatalf("reserved balance = %#v", reserved)
	}
}

func TestAddressRPCOwnershipListAndUnused(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	addresses, err := fixture.account.Receiving.GetAddresses(context.Background(), false)
	if err != nil || len(addresses) == 0 {
		t.Fatalf("fixture addresses = %v, %v", addresses, err)
	}
	address := addresses[0]
	if mine := fileMutationRPCResult(t, fixture.server, "address_is_mine", map[string]any{
		"address": address,
	}); mine != true {
		t.Fatalf("address_is_mine = %#v", mine)
	}
	if mine := fileMutationRPCResult(t, fixture.server, "address_is_mine", map[string]any{
		"address": "not-an-address",
	}); mine != false {
		t.Fatalf("unknown address_is_mine = %#v", mine)
	}
	listed := fileMutationRPCResult(t, fixture.server, "address_list", map[string]any{
		"address": address,
	}).(map[string]any)
	items := listed["items"].([]any)
	item := items[0].(map[string]any)
	if len(items) != 1 || listed["total_items"] != json.Number("1") || item["address"] != address ||
		item["account"] != fixture.account.ID || item["used_times"] == nil ||
		!strings.HasPrefix(item["pubkey"].(string), "xpub") {
		t.Fatalf("address list = %#v", listed)
	}
	unused := fileMutationRPCResult(t, fixture.server, "address_unused", map[string]any{
		"account_id": fixture.account.ID,
	})
	found := false
	for _, candidate := range addresses {
		if unused == candidate {
			found = true
		}
	}
	if !found {
		t.Fatalf("unused address = %#v, addresses %v", unused, addresses)
	}
}
