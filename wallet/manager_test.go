package wallet

import "encoding/json"
import "os"
import "path/filepath"
import "testing"

import "lbry/daemon/crypt"

var testIV = []byte("0123456789abcdef")

func writeWallet(t *testing.T, dir, file string, data WalletFile) {
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
	writeWallet(t, dir, "default_wallet", WalletFile{Name: "My Wallet", Accounts: []Account{{Name: "plain", Seed: "seed words", PrivateKey: "xprv-plain"}}})
	writeWallet(t, dir, "secret", WalletFile{Name: "Secret", Accounts: []Account{{
		Name: "enc", Encrypted: true,
		Seed:       crypt.Encrypt("hunter2", "seed words", testIV),
		PrivateKey: crypt.Encrypt("hunter2", "xprv-secret", testIV),
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
	list := manager.List(nil)
	if len(list) != 2 || list[0].ID != "default_wallet" || list[0].Name != "My Wallet" || list[1].ID != "secret" {
		t.Fatalf("unexpected list: %+v", list)
	}
	if manager.Get(nil, "secret") == nil || manager.Get(nil, "missing") != nil {
		t.Fatal("Get returned the wrong result")
	}
}

func TestMissingDirectory(t *testing.T) {
	manager := NewDefaultWalletManager(filepath.Join(t.TempDir(), "nope"))
	if err := manager.(*DefaultWalletManager).Load(); err != nil || len(manager.List(nil)) != 0 {
		t.Fatalf("expected empty manager, got %v %v", manager, err)
	}
}

// Lock state must be visible through the public API (List and Get), including
// for an encrypted wallet that has never been unlocked.
func TestLockStateThroughPublicAPI(t *testing.T) {
	manager := fixture(t)
	if !manager.Get(nil, "secret").IsLocked() {
		t.Fatal("encrypted wallet must report locked before any unlock")
	}
	if manager.Get(nil, "default_wallet").IsLocked() {
		t.Fatal("unencrypted wallet must not report locked")
	}
	if list := manager.List(nil); list[0].IsLocked() || !list[1].IsLocked() {
		t.Fatalf("List lost the lock state: %+v", list)
	}

	if manager.Unlock(nil, "secret", "wrong") {
		t.Fatal("unlocked with the wrong password")
	}
	if !manager.Get(nil, "secret").IsLocked() {
		t.Fatal("a failed unlock must leave the wallet locked")
	}
	if !manager.Unlock(nil, "secret", "hunter2") {
		t.Fatal("did not unlock with the right password")
	}
	if manager.Get(nil, "secret").IsLocked() {
		t.Fatal("wallet still locked after unlock")
	}
	if !manager.Lock(nil, "secret") || !manager.Get(nil, "secret").IsLocked() {
		t.Fatal("lock failed")
	}
}

func TestUnlockRetainsDecryptedMaterialUntilLock(t *testing.T) {
	manager := fixture(t)
	account := &manager.(*DefaultWalletManager).find("secret").accounts[0]

	if manager.Unlock(nil, "secret", "wrong"); account.decryptedSeed != "" || account.decryptedPrivateKey != "" {
		t.Fatal("a failed unlock must not retain anything")
	}
	if !manager.Unlock(nil, "secret", "hunter2") {
		t.Fatal("unlock failed")
	}
	if account.decryptedSeed != "seed words" || account.decryptedPrivateKey != "xprv-secret" {
		t.Fatalf("decrypted material not retained: %q %q", account.decryptedSeed, account.decryptedPrivateKey)
	}
	if account.Seed == "seed words" {
		t.Fatal("the encrypted original must be kept as is")
	}
	if !manager.Lock(nil, "secret") {
		t.Fatal("lock failed")
	}
	if account.decryptedSeed != "" || account.decryptedPrivateKey != "" {
		t.Fatal("lock must forget the decrypted material")
	}
}

func TestUnlockIsAllOrNothing(t *testing.T) {
	dir := t.TempDir()
	writeWallet(t, dir, "two", WalletFile{Name: "Two", Accounts: []Account{
		{Encrypted: true, Seed: crypt.Encrypt("pw", "seed one", testIV), PrivateKey: crypt.Encrypt("pw", "key one", testIV)},
		{Encrypted: true, Seed: crypt.Encrypt("other", "seed two", testIV), PrivateKey: crypt.Encrypt("other", "key two", testIV)},
	}})
	manager := NewDefaultWalletManager(dir)
	manager.(*DefaultWalletManager).Load()
	if manager.Unlock(nil, "two", "pw") {
		t.Fatal("unlock must fail when one account does not decrypt")
	}
	if got := manager.(*DefaultWalletManager).find("two").accounts[0].decryptedSeed; got != "" {
		t.Fatalf("first account was left decrypted: %q", got)
	}
}

func TestUnencryptedAndMissingWallets(t *testing.T) {
	manager := fixture(t)
	if manager.Lock(nil, "default_wallet") {
		t.Fatal("locked an unencrypted wallet")
	}
	if !manager.Unlock(nil, "default_wallet", "anything") {
		t.Fatal("unencrypted wallet should report unlocked")
	}
	if manager.Unlock(nil, "missing", "x") || manager.Lock(nil, "missing") {
		t.Fatal("operations on a missing wallet must fail")
	}
}

func TestNewDefaultWalletManagerReadsNothingUntilLoad(t *testing.T) {
	dir := t.TempDir()
	writeWallet(t, dir, "default_wallet", WalletFile{Name: "My Wallet"})
	manager := NewDefaultWalletManager(dir)
	if len(manager.List(nil)) != 0 {
		t.Fatal("NewDefaultWalletManager must not read the directory")
	}
	if err := manager.(*DefaultWalletManager).Load(); err != nil || len(manager.List(nil)) != 1 {
		t.Fatalf("Load did not pick up the wallet: %v", err)
	}
}
