package wallet

import "encoding/json"
import "os"
import "path/filepath"
import "testing"

import "lbry/daemon/crypto"

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

func TestUnlockDecryptsInPlaceAndLockEncryptsAgain(t *testing.T) {
	manager := fixture(t)
	account := &manager.(*DefaultWalletManager).find("secret").Accounts[0]
	original := account.Seed

	if manager.Unlock(nil, "secret", "wrong"); !account.Encrypted || account.Seed != original {
		t.Fatal("a failed unlock must leave the account as it was")
	}
	if !manager.Unlock(nil, "secret", "hunter2") {
		t.Fatal("unlock failed")
	}
	if account.Encrypted || account.Seed != "seed words" || account.PrivateKey != "xprv-secret" {
		t.Fatalf("account not decrypted: %+v", account)
	}
	if !manager.Lock(nil, "secret") {
		t.Fatal("lock failed")
	}
	if !account.Encrypted || account.Seed == "seed words" || account.PrivateKey == "xprv-secret" {
		t.Fatal("lock must encrypt the account again")
	}
	if !manager.Unlock(nil, "secret", "hunter2") || account.Seed != "seed words" {
		t.Fatal("the locked account must unlock again with the same password")
	}
}

func TestUnlockIsAllOrNothing(t *testing.T) {
	dir := t.TempDir()
	writeWallet(t, dir, "two", WalletFile{Name: "Two", Accounts: []Account{
		{Encrypted: true, Seed: crypto.Encrypt("pw", "seed one", testIV), PrivateKey: crypto.Encrypt("pw", "key one", testIV)},
		{Encrypted: true, Seed: crypto.Encrypt("other", "seed two", testIV), PrivateKey: crypto.Encrypt("other", "key two", testIV)},
	}})
	manager := NewDefaultWalletManager(dir)
	manager.(*DefaultWalletManager).Load()
	if manager.Unlock(nil, "two", "pw") {
		t.Fatal("unlock must fail when one account does not decrypt")
	}
	if first := manager.(*DefaultWalletManager).find("two").Accounts[0]; !first.Encrypted {
		t.Fatalf("first account was left decrypted: %q", first.Seed)
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
