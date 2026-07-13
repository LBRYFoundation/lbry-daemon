package rpc

import (
	"encoding/hex"
	"testing"

	walletpkg "lbry/daemon/wallet"
)

func TestSyncHashAndApplyEncryptedRoundTrip(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	selectedWallet := fixture.server.walletManagerProvider().DefaultWallet()
	hash, err := selectedWallet.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if result := fileMutationRPCResult(t, fixture.server, "sync_hash", map[string]any{}); result != hex.EncodeToString(hash[:]) {
		t.Fatalf("sync_hash = %#v, want %x", result, hash)
	}

	result := fileMutationRPCResult(t, fixture.server, "sync_apply", map[string]any{
		"password": "sync-password",
	}).(map[string]any)
	decoded, err := walletpkg.UnpackWallet("sync-password", []byte(result["data"].(string)))
	if err != nil || decoded == nil {
		t.Fatalf("unpack sync data = %#v, %v", decoded, err)
	}
	if result["hash"] != hex.EncodeToString(hash[:]) {
		t.Fatalf("sync_apply hash = %#v, want %x", result["hash"], hash)
	}
}

func TestSyncApplyMergesDataAdoptsEncryptionPasswordAndPersists(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	selectedWallet := fixture.server.walletManagerProvider().DefaultWallet()
	selectedWallet.Preferences.Set(walletpkg.EncryptOnDisk, true)
	remote, err := selectedWallet.Pack("new-password")
	if err != nil {
		t.Fatal(err)
	}
	result := fileMutationRPCResult(t, fixture.server, "sync_apply", map[string]any{
		"password": "new-password", "data": string(remote), "blocking": true,
	}).(map[string]any)
	if selectedWallet.EncryptionPassword == nil || *selectedWallet.EncryptionPassword != "new-password" {
		t.Fatalf("encryption password = %#v", selectedWallet.EncryptionPassword)
	}
	hash, err := selectedWallet.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if result["hash"] != hex.EncodeToString(hash[:]) {
		t.Fatalf("sync_apply = %#v, want hash %x", result, hash)
	}
	if _, err := walletpkg.UnpackWallet("new-password", []byte(result["data"].(string))); err != nil {
		t.Fatalf("returned pack = %v", err)
	}
}
