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

func fixture(t *testing.T) *WalletManager {
	dir := t.TempDir()
	writeWallet(t, dir, "default_wallet", WalletFile{Name: "My Wallet", Accounts: []Account{{Name: "plain", Seed: "seed words", PrivateKey: "xprv-plain"}}})
	writeWallet(t, dir, "secret", WalletFile{Name: "Secret", Accounts: []Account{{
		Name: "enc", Encrypted: true,
		Seed:       crypt.Encrypt("hunter2", "seed words", testIV),
		PrivateKey: crypt.Encrypt("hunter2", "xprv-secret", testIV),
	}}})
	os.WriteFile(filepath.Join(dir, "junk"), []byte("not json"), 0o600)
	manager := NewManager(dir)
	if err := manager.Load(); err != nil {
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
	manager := NewManager(filepath.Join(t.TempDir(), "nope"))
	if err := manager.Load(); err != nil || len(manager.List(nil)) != 0 {
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
	account := &manager.find("secret").accounts[0]

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
	manager := NewManager(dir)
	manager.Load()
	if manager.Unlock(nil, "two", "pw") {
		t.Fatal("unlock must fail when one account does not decrypt")
	}
	if got := manager.find("two").accounts[0].decryptedSeed; got != "" {
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

func TestDefaultDirectory(t *testing.T) {
	home := func() (string, error) { return "/home/u", nil }
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	dirs := func(existing ...string) func(string) bool {
		return func(p string) bool {
			for _, e := range existing {
				if p == e {
					return true
				}
			}
			return false
		}
	}
	for _, tc := range []struct {
		name, goos string
		env        map[string]string
		existing   []string
		want       string
	}{
		{"macOS", "darwin", nil, nil, "/home/u/.lbryum/wallets"},
		{"linux legacy .lbryum", "linux", nil, []string{"/home/u/.lbryum"}, "/home/u/.lbryum/wallets"},
		{"linux legacy .lbrynet", "linux", nil, []string{"/home/u/.lbrynet"}, "/home/u/.lbryum/wallets"},
		{"linux new default", "linux", nil, nil, "/home/u/.local/share/lbry/lbryum/wallets"},
		{"linux XDG_DATA_HOME", "linux", map[string]string{"XDG_DATA_HOME": "/data"}, nil, "/data/lbry/lbryum/wallets"},
		{"windows legacy", "windows", map[string]string{"APPDATA": "/r"}, []string{"/r/lbryum"}, "/r/lbryum/wallets"},
		{"windows new", "windows", map[string]string{"APPDATA": "/r", "LOCALAPPDATA": "/l"}, nil, "/l/lbry/lbryum/wallets"},
	} {
		if got := defaultDirectory(tc.goos, home, env(tc.env), dirs(tc.existing...)); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestNewManagerReadsNothingUntilLoad(t *testing.T) {
	dir := t.TempDir()
	writeWallet(t, dir, "default_wallet", WalletFile{Name: "My Wallet"})
	manager := NewManager(dir)
	if len(manager.List(nil)) != 0 {
		t.Fatal("NewManager must not read the directory")
	}
	if err := manager.Load(); err != nil || len(manager.List(nil)) != 1 {
		t.Fatalf("Load did not pick up the wallet: %v", err)
	}
}
