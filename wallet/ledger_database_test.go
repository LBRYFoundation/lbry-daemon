package wallet

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/ledgerdb"
)

func TestLedgerChannelUsageAndUnlockPrimeDeterministicCache(t *testing.T) {
	ctx := context.Background()
	ledger, err := newLedger(keys.MainNet, LedgerConfig{"data_path": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ledgerPath, err := ledger.Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(ledgerPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Database.Open(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := ledger.Database.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})

	account := channelManagerSeedAccount(t)
	ledger.addAccount(account)
	if err := ledger.Database.AddKeys(ctx, account.PublicKey.Address(), []ledgerdb.AddressKey{{
		Address: "owned-output", PublicKey: []byte{1}, ChainCode: []byte{2},
	}}); err != nil {
		t.Fatal(err)
	}
	channelRoot, err := account.PrivateKey.Child(ChannelChain)
	if err != nil {
		t.Fatal(err)
	}
	first, err := channelRoot.Child(0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := channelRoot.Child(1)
	if err != nil {
		t.Fatal(err)
	}

	if err := ledger.Database.Close(ctx); err != nil {
		t.Fatal(err)
	}
	seedLedgerChannelOutput(t, ledger.Database.Path(), "first", 1, 0, first.PublicKey().CompressedBytes())
	seedLedgerChannelOutput(t, ledger.Database.Path(), "second", 2, 0, second.PublicKey().CompressedBytes())
	if err := ledger.Database.Open(ctx); err != nil {
		t.Fatal(err)
	}

	used, err := ledger.ChannelKeyUsage(ctx)(account, first.PublicKey())
	if err != nil || !used {
		t.Fatalf("direct channel usage = %t, %v", used, err)
	}
	if err := account.Encrypt("password"); err != nil {
		t.Fatal(err)
	}
	wallet := NewWallet(WithWalletAccounts([]*Account{account}))
	unlocked, err := wallet.Unlock("password")
	if err != nil || !unlocked {
		t.Fatalf("unlock = %t, %v", unlocked, err)
	}
	manager := account.DeterministicChannelKeys
	if manager.LastKnown != 2 || len(manager.Cache) != 3 {
		t.Fatalf("primed manager = last_known %d cache %d", manager.LastKnown, len(manager.Cache))
	}
	for _, privateKey := range []*keys.PrivateKey{first, second} {
		if cached := manager.GetPrivateKey(privateKey.Address()); cached == nil ||
			!equalBytes(cached.PrivateKeyBytes(), privateKey.PrivateKeyBytes()) {
			t.Fatalf("missing cached channel key %s", privateKey.Address())
		}
	}
}

func TestWalletUnlockSkipsPrimingUntilLedgerDatabaseOpens(t *testing.T) {
	account := channelManagerSeedAccount(t)
	ledger, err := newLedger(keys.MainNet, LedgerConfig{"data_path": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ledger.addAccount(account)
	if err := account.Encrypt("password"); err != nil {
		t.Fatal(err)
	}
	wallet := NewWallet(WithWalletAccounts([]*Account{account}))
	if unlocked, err := wallet.Unlock("password"); err != nil || !unlocked {
		t.Fatalf("isolated unlock = %t, %v", unlocked, err)
	}
	if manager := account.DeterministicChannelKeys; manager.LastKnown != 0 || len(manager.Cache) != 0 {
		t.Fatalf("unopened DB priming = last_known %d cache %d", manager.LastKnown, len(manager.Cache))
	}
}

func seedLedgerChannelOutput(
	t *testing.T, path, txid string, height, position int, publicKey []byte,
) {
	t.Helper()
	connection, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.Exec(`INSERT INTO tx
        (txid, raw, height, position, is_verified) VALUES (?, x'00', ?, ?, 0)`,
		txid, height, position,
	); err != nil {
		t.Fatal(err)
	}
	claim := makeV2ChannelClaim(publicKey)
	script := makeChannelClaimScript(channelClaimNameOpcode, claim, false)
	if _, err := connection.Exec(`INSERT INTO txo
        (txid, txoid, address, position, amount, script, txo_type)
        VALUES (?, ?, 'owned-output', 0, 1, ?, 2)`,
		txid, txid+":0", script,
	); err != nil {
		t.Fatal(err)
	}
}

func TestLedgerOwnsUnopenedDatabaseAtPinnedPath(t *testing.T) {
	ledger, err := newLedger(keys.TestNet, LedgerConfig{"data_path": filepath.FromSlash("/tmp/wallet")})
	if err != nil {
		t.Fatal(err)
	}
	want := "/tmp/wallet/lbc_testnet/blockchain.db"
	if ledger.Database == nil || ledger.Database.Path() != want || ledger.Database.IsOpen() {
		t.Fatalf("ledger DB = %#v, want unopened %q", ledger.Database, want)
	}
}
