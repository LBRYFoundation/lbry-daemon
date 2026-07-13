package rpc

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestPreferenceRPCSetGetAllAndMissing(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	set := fileMutationRPCResult(t, fixture.server, "preference_set", map[string]any{
		"key": "subscriptions", "value": `["one","two"]`,
	})
	if !reflect.DeepEqual(set, map[string]any{"subscriptions": []any{"one", "two"}}) {
		t.Fatalf("preference set = %#v", set)
	}
	got := fileMutationRPCResult(t, fixture.server, "preference_get", map[string]any{"key": "subscriptions"})
	if !reflect.DeepEqual(got, set) {
		t.Fatalf("preference get = %#v, want %#v", got, set)
	}
	all := fileMutationRPCResult(t, fixture.server, "preference_get", map[string]any{})
	if !reflect.DeepEqual(all, set) {
		t.Fatalf("all preferences = %#v", all)
	}
	missing := fileMutationRPCResult(t, fixture.server, "preference_get", map[string]any{"key": "missing"})
	if missing != nil {
		t.Fatalf("missing preference = %#v", missing)
	}
}

func TestWalletStateRPCEncryptionLifecycle(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	status := fileMutationRPCResult(t, fixture.server, "wallet_status", map[string]any{}).(map[string]any)
	if status["is_encrypted"] != false || status["is_locked"] != false || status["is_syncing"] != false {
		t.Fatalf("initial wallet status = %#v", status)
	}
	if result := fileMutationRPCResult(t, fixture.server, "wallet_encrypt", map[string]any{
		"new_password": "password",
	}); result != true {
		t.Fatalf("wallet encrypt = %#v", result)
	}
	status = fileMutationRPCResult(t, fixture.server, "wallet_status", map[string]any{}).(map[string]any)
	if status["is_encrypted"] != true || status["is_locked"] != false {
		t.Fatalf("encrypted wallet status = %#v", status)
	}
	if result := fileMutationRPCResult(t, fixture.server, "wallet_lock", map[string]any{}); result != true {
		t.Fatalf("wallet lock = %#v", result)
	}
	status = fileMutationRPCResult(t, fixture.server, "wallet_status", map[string]any{}).(map[string]any)
	if status["is_locked"] != true {
		t.Fatalf("locked wallet status = %#v", status)
	}
	if result := fileMutationRPCResult(t, fixture.server, "wallet_unlock", map[string]any{
		"password": "password",
	}); result != true {
		t.Fatalf("wallet unlock = %#v", result)
	}
	if result := fileMutationRPCResult(t, fixture.server, "wallet_decrypt", map[string]any{}); result != true {
		t.Fatalf("wallet decrypt = %#v", result)
	}
	status = fileMutationRPCResult(t, fixture.server, "wallet_status", map[string]any{}).(map[string]any)
	if status["is_encrypted"] != false || status["is_locked"] != false {
		t.Fatalf("decrypted wallet status = %#v", status)
	}
}

func TestWalletListRPCUsesPythonPaginationAndWalletShape(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	result := fileMutationRPCResult(t, fixture.server, "wallet_list", map[string]any{
		"page": 0, "page_size": 0,
	}).(map[string]any)
	items := result["items"].([]any)
	item := items[0].(map[string]any)
	if result["page"] != json.Number("1") || result["page_size"] != json.Number("1") ||
		result["total_pages"] != json.Number("1") || result["total_items"] != json.Number("1") ||
		len(items) != 1 || len(item) != 2 || item["id"] != "paid-wallet" || item["name"] != "paid-wallet" {
		t.Fatalf("wallet list = %#v", result)
	}
}
