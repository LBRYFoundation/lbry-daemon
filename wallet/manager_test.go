package wallet

import (
	"encoding/json"
	"lbry/daemon/crypto"
	"os"
	"path/filepath"
	"testing"
)

var testIV = []byte("0123456789abcdef")

func writeWallet(t *testing.T, dir, file string, data Wallet) {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) WalletManager {
	dir := t.TempDir()
	writeWallet(t, dir, DEFAULT_WALLET, Wallet{Name: "My Wallet", Accounts: []*Account{{Name: "plain", Seed: "seed words", PrivateKey: "xprv-plain"}}})
	writeWallet(t, dir, "secret", Wallet{Name: "Secret", Accounts: []*Account{{
		Name: "enc", Locked: true,
		Seed:       crypto.Encrypt("hunter2", "seed words", testIV),
		PrivateKey: crypto.Encrypt("hunter2", "xprv-secret", testIV),
	}}})
	os.WriteFile(filepath.Join(dir, "junk"), []byte("not json"), 0o600)
	manager := NewDefaultWalletManager(dir)
	if err := manager.(*DefaultWalletManager).Load(); err != nil {
		t.Fatal(err)
	}
	return manager
}

func TestListAndGet(t *testing.T) {
	manager := fixture(t)
	list := manager.ReadList(nil)
	if len(list) != 2 || list[0].ID != DEFAULT_WALLET || list[0].Name != "My Wallet" || list[1].ID != "secret" {
		t.Fatalf("unexpected list: %+v", list)
	}
	if manager.Read(nil, "secret") == nil || manager.Read(nil, "missing") != nil {
		t.Fatal("Get returned the wrong result")
	}
}

// lbry-sdk keeps an empty wallet name as-is; the id is the file name. The list
// is ordered by id, not by the name in the file.
func TestLoadNameAndSorting(t *testing.T) {
	dir := t.TempDir()
	writeWallet(t, dir, "b", Wallet{Name: "Alpha"})
	writeWallet(t, dir, "a", Wallet{Name: ""})
	writeWallet(t, dir, "c", Wallet{Name: "Beta"})

	manager := NewDefaultWalletManager(dir)
	if err := manager.(*DefaultWalletManager).Load(); err != nil {
		t.Fatal(err)
	}

	list := manager.ReadList(nil)
	if len(list) != 3 {
		t.Fatalf("unexpected list: %+v", list)
	}
	for i, want := range []struct{ id, name string }{
		{"a", ""},
		{"b", "Alpha"},
		{"c", "Beta"},
	} {
		if list[i].ID != want.id || list[i].Name != want.name {
			t.Errorf("wallet %d = %+v, want id %q and name %q", i, list[i], want.id, want.name)
		}
	}
}

func TestMissingDirectory(t *testing.T) {
	manager := NewDefaultWalletManager(filepath.Join(t.TempDir(), "nope"))
	if err := manager.(*DefaultWalletManager).Load(); err != nil || len(manager.ReadList(nil)) != 0 {
		t.Fatalf("expected empty manager, got %v %v", manager, err)
	}
}

// Lock state must be visible through the public API (List and Get), including
// for an encrypted wallet that has never been unlocked.
func TestLockStateThroughPublicAPI(t *testing.T) {
	manager := fixture(t)
	if !manager.Read(nil, "secret").IsLocked() {
		t.Fatal("encrypted wallet must report locked before any unlock")
	}
	if manager.Read(nil, DEFAULT_WALLET).IsLocked() {
		t.Fatal("unencrypted wallet must not report locked")
	}
	if list := manager.ReadList(nil); list[0].IsLocked() || !list[1].IsLocked() {
		t.Fatalf("List lost the lock state: %+v", list)
	}

	if manager.Read(nil, "secret").Unlock("wrong") {
		t.Fatal("unlocked with the wrong password")
	}
	if !manager.Read(nil, "secret").IsLocked() {
		t.Fatal("a failed unlock must leave the wallet locked")
	}
	if !manager.Read(nil, "secret").Unlock("hunter2") {
		t.Fatal("did not unlock with the right password")
	}
	if manager.Read(nil, "secret").IsLocked() {
		t.Fatal("wallet still locked after unlock")
	}
	if !manager.Read(nil, "secret").Lock() || !manager.Read(nil, "secret").IsLocked() {
		t.Fatal("lock failed")
	}
}

func TestUnlockDecryptsInPlaceAndLockEncryptsAgain(t *testing.T) {
	manager := fixture(t)
	account := manager.(*DefaultWalletManager).Read(nil, "secret").Accounts[0]
	original := account.Seed

	if manager.Read(nil, "secret").Unlock("wrong"); !account.Locked || account.Seed != original {
		t.Fatal("a failed unlock must leave the account as it was")
	}
	if !manager.Read(nil, "secret").Unlock("hunter2") {
		t.Fatal("unlock failed")
	}
	if account.Locked || account.Seed != "seed words" || account.PrivateKey != "xprv-secret" {
		t.Fatalf("account not decrypted: %+v", account)
	}
	if !manager.Read(nil, "secret").Lock() {
		t.Fatal("lock failed")
	}
	if !account.Locked || account.Seed == "seed words" || account.PrivateKey == "xprv-secret" {
		t.Fatal("lock must encrypt the account again")
	}
	if !manager.Read(nil, "secret").Unlock("hunter2") || account.Seed != "seed words" {
		t.Fatal("the locked account must unlock again with the same password")
	}
}

func TestUnlockIsAllOrNothing(t *testing.T) {
	dir := t.TempDir()
	writeWallet(t, dir, "two", Wallet{Name: "Two", Accounts: []*Account{
		{Locked: true, Seed: crypto.Encrypt("pw", "seed one", testIV), PrivateKey: crypto.Encrypt("pw", "key one", testIV)},
		{Locked: true, Seed: crypto.Encrypt("other", "seed two", testIV), PrivateKey: crypto.Encrypt("other", "key two", testIV)},
	}})
	manager := NewDefaultWalletManager(dir)
	manager.(*DefaultWalletManager).Load()
	if manager.Read(nil, "two").Unlock("pw") {
		t.Fatal("unlock must fail when one account does not decrypt")
	}
	if first := manager.(*DefaultWalletManager).Read(nil, "two").Accounts[0]; !first.Locked {
		t.Fatalf("first account was left decrypted: %q", first.Seed)
	}
}

func TestUnencryptedAndMissingWallets(t *testing.T) {
	manager := fixture(t)
	if manager.Read(nil, DEFAULT_WALLET).Lock() {
		t.Fatal("locked an unencrypted wallet")
	}
	if !manager.Read(nil, DEFAULT_WALLET).Unlock("anything") {
		t.Fatal("unencrypted wallet should report unlocked")
	}
	if manager.Read(nil, "missing").Unlock("x") || manager.Read(nil, "missing").Lock() {
		t.Fatal("operations on a missing wallet must fail")
	}
}

func TestNewDefaultWalletManagerReadsNothingUntilLoad(t *testing.T) {
	dir := t.TempDir()
	writeWallet(t, dir, DEFAULT_WALLET, Wallet{Name: "My Wallet"})
	manager := NewDefaultWalletManager(dir)
	if len(manager.ReadList(nil)) != 0 {
		t.Fatal("NewDefaultWalletManager must not read the directory")
	}
	if err := manager.(*DefaultWalletManager).Load(); err != nil || len(manager.ReadList(nil)) != 1 {
		t.Fatalf("Load did not pick up the wallet: %v", err)
	}
}

// lbry-sdk Wallet.is_encrypted: the wallet is locked, or unlocked with a password.
func TestIsEncrypted(t *testing.T) {
	manager := fixture(t)
	if manager.Read(nil, DEFAULT_WALLET).IsEncrypted() {
		t.Fatal("an unencrypted wallet must not report encrypted")
	}
	if !manager.Read(nil, "secret").IsEncrypted() {
		t.Fatal("a locked wallet must report encrypted")
	}
	if manager.Read(nil, "secret").Unlock("wrong"); !manager.Read(nil, "secret").IsEncrypted() {
		t.Fatal("a failed unlock leaves the wallet encrypted")
	}
	if !manager.Read(nil, "secret").Unlock("hunter2") || !manager.Read(nil, "secret").IsEncrypted() {
		t.Fatal("a wallet unlocked with a password is still encrypted")
	}
	if !manager.Read(nil, "secret").Lock() || !manager.Read(nil, "secret").IsEncrypted() {
		t.Fatal("a locked wallet must report encrypted")
	}
	if list := manager.ReadList(nil); list[0].IsEncrypted() || !list[1].IsEncrypted() {
		t.Fatalf("List lost the encrypted state: %+v", list)
	}
}
