package wallet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

var testIV = []byte("0123456789abcdef")

func writeWallet(t *testing.T, dir, file string, data walletFile) {
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
	writeWallet(t, dir, "default_wallet", walletFile{Name: "My Wallet", Accounts: []account{{Name: "plain", Seed: "seed words", PrivateKey: "xprv-plain"}}})
	writeWallet(t, dir, "secret", walletFile{Name: "Secret", Accounts: []account{{
		Name: "enc", Encrypted: true,
		Seed:       aesEncrypt("hunter2", "seed words", testIV),
		PrivateKey: aesEncrypt("hunter2", "xprv-secret", testIV),
	}}})
	os.WriteFile(filepath.Join(dir, "junk"), []byte("not json"), 0o600)
	manager, err := NewManager(dir)
	if err != nil {
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
	manager, err := NewManager(filepath.Join(t.TempDir(), "nope"))
	if err != nil || len(manager.List(nil)) != 0 {
		t.Fatalf("expected empty manager, got %v %v", manager, err)
	}
}

func TestUnlockLock(t *testing.T) {
	manager := fixture(t)
	if manager.Unlock(nil, "secret", "wrong") {
		t.Fatal("unlocked with the wrong password")
	}
	if !manager.Unlock(nil, "secret", "hunter2") {
		t.Fatal("did not unlock with the right password")
	}
	if manager.find("secret").IsLocked() {
		t.Fatal("wallet still locked")
	}
	if !manager.Lock(nil, "secret") || !manager.find("secret").IsLocked() {
		t.Fatal("lock failed")
	}
	if manager.Lock(nil, "default_wallet") {
		t.Fatal("locked an unencrypted wallet")
	}
	if !manager.Unlock(nil, "default_wallet", "anything") {
		t.Fatal("unencrypted wallet should report unlocked")
	}
	if manager.Unlock(nil, "missing", "x") {
		t.Fatal("unlocked a wallet that does not exist")
	}
}

func TestAESRoundTrip(t *testing.T) {
	for _, value := range []string{"a", "exactly16bytes!!", "a longer value that spans several blocks"} {
		out, err := aesDecrypt("pw", aesEncrypt("pw", value, testIV))
		if err != nil || out != value {
			t.Fatalf("round trip of %q gave %q, %v", value, out, err)
		}
	}
}

// Produced by lbry-sdk's lbry.crypto.crypt.aes_encrypt("hunter2", "seed words", iv=b"0123456789abcdef").
func TestAESPythonVector(t *testing.T) {
	out, err := aesDecrypt("hunter2", "MDEyMzQ1Njc4OWFiY2RlZoNseIwLAkeUPKSB6Wmt4vU=")
	if err != nil || out != "seed words" {
		t.Fatalf("got %q, %v", out, err)
	}
}
