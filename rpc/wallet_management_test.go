package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"lbry/daemon/wallet/keys"
)

func TestWalletCreateRemoveAndAddLifecycle(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	fixture.ledger.SPVNetwork = nil
	walletDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(walletDir, "wallets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.server.settings.Set("wallet_dir", walletDir); err != nil {
		t.Fatal(err)
	}
	created := fileMutationRPCResult(t, fixture.server, "wallet_create", map[string]any{
		"wallet_id": "secondary", "create_account": true, "single_key": true,
	}).(map[string]any)
	manager := fixture.server.walletManagerProvider()
	secondary, err := manager.GetWalletOrError("secondary")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(walletDir, "wallets", "secondary")
	if created["id"] != "secondary" || created["name"] != "My Wallet" || len(created) != 2 ||
		len(secondary.Accounts) != 1 || secondary.Accounts[0].GeneratorName != "single-address" {
		t.Fatalf("wallet_create = %#v, wallet=%#v", created, secondary)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("created wallet file = %v", err)
	}
	configured := fixture.server.configuredWallets()
	if configured[len(configured)-1] != "secondary" {
		t.Fatalf("configured wallets = %v", configured)
	}

	removed := fileMutationRPCResult(t, fixture.server, "wallet_remove", map[string]any{
		"wallet_id": "secondary",
	}).(map[string]any)
	if removed["id"] != "secondary" || len(manager.Wallets) != 1 {
		t.Fatalf("wallet_remove = %#v, loaded=%d", removed, len(manager.Wallets))
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("wallet_remove deleted file: %v", err)
	}

	added := fileMutationRPCResult(t, fixture.server, "wallet_add", map[string]any{
		"wallet_id": "secondary",
	}).(map[string]any)
	if added["id"] != "secondary" || len(manager.Wallets) != 2 {
		t.Fatalf("wallet_add = %#v, loaded=%d", added, len(manager.Wallets))
	}
}

func TestWalletExportImportClearAndEncrypted(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	clear := fileMutationRPCResult(t, fixture.server, "wallet_export", map[string]any{}).(string)
	var document map[string]any
	if err := json.Unmarshal([]byte(clear), &document); err != nil || document["name"] != "paid-wallet" {
		t.Fatalf("clear wallet export = %#v, %v", document, err)
	}
	imported := fileMutationRPCResult(t, fixture.server, "wallet_import", map[string]any{
		"data": clear,
	}).(string)
	if err := json.Unmarshal([]byte(imported), &document); err != nil || len(document["accounts"].([]any)) != 1 {
		t.Fatalf("clear wallet import = %#v, %v", document, err)
	}

	encrypted := fileMutationRPCResult(t, fixture.server, "wallet_export", map[string]any{
		"password": "export-password",
	}).(string)
	result := fileMutationRPCResult(t, fixture.server, "wallet_import", map[string]any{
		"data": encrypted, "password": "export-password", "blocking": true,
	}).(string)
	if _, err := fixture.server.walletManagerProvider().DefaultWallet().Hash(); err != nil {
		t.Fatal(err)
	}
	if result == clear || len(result) < 32 {
		t.Fatalf("encrypted wallet import = %q", result)
	}
}

func TestWalletSendAcceptsScriptAddressAndReleasesPreview(t *testing.T) {
	fixture := newPaidGetFixture(t, false)
	payload := append([]byte{keys.MainNet.ScriptAddressPrefix()}, bytes.Repeat([]byte{0x42}, 20)...)
	address := keys.EncodeBase58Check(payload)
	result := fileMutationRPCResult(t, fixture.server, "wallet_send", map[string]any{
		"amount": "1.0", "addresses": address,
		"funding_account_ids": []any{fixture.account.ID},
		"change_account_id":   fixture.account.ID, "preview": true,
	}).(map[string]any)
	outputs := result["outputs"].([]any)
	if result["txid"] == "" || len(outputs) != 2 || outputs[0].(map[string]any)["amount"] != "1.0" ||
		outputs[0].(map[string]any)["address"] != address {
		t.Fatalf("wallet_send = %#v", result)
	}
	spendables, err := fixture.ledger.Database.ListSpendableOutputs(
		context.Background(), []string{fixture.account.ID},
	)
	if err != nil || len(spendables) != 1 {
		t.Fatalf("preview spendables = %#v, %v", spendables, err)
	}
}
