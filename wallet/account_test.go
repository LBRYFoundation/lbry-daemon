package wallet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Public keys and ids computed with an independent implementation (Python hashlib, ripemd160), for the
// extended public keys built from these compressed keys with a zero depth, parent and child number.
const (
	publicKeyOne = "xpub661MyMwAqRbcEYSGagKuFUqExQV8d2eizDP5SamP9TcLeqAk9JsrNexcG8NnkpFr2HH9WwpV8rX9uQuw9EyNCep7Cdggp6X7TXujag2BbGo"
	idOne        = "bJXhaTKRsQTFmv6bZvB3vko8Q5X74p443s"
	publicKeyTwo = "xpub661MyMwAqRbcEYSGagKuFUqExQV8d2eizDP5SamP9TcLeqAk9JsrNexcG6jHgvc6q7ycbvQiFVYjNvsmkmXtRkUYR9hJTvRyaecM7iiBZen"
	idTwo        = "bYibmp63f4AVetNtCDqkUYQhGN2MnjGJNU"
)

func TestAccountID(t *testing.T) {
	for publicKey, want := range map[string]string{publicKeyOne: idOne, publicKeyTwo: idTwo} {
		account := Account{PublicKey: publicKey}
		if got := account.ID; got != want {
			t.Errorf("ID = %q, want %q", got, want)
		}
	}
	for name, publicKey := range map[string]string{"empty": "", "not base58": "xpub0OIl", "bad checksum": publicKeyOne[:len(publicKeyOne)-1] + "a"} {
		if got := (&Account{PublicKey: publicKey}).ID; got != "" {
			t.Errorf("%s: expected no id, got %q", name, got)
		}
	}
}

func TestAddressGeneratorDetails(t *testing.T) {
	// Nothing stored: the defaults of lbry-sdk's deterministic chain.
	got, _ := json.Marshal((&Account{}).AddressGeneratorDetails())
	want := `{"change":{"gap":6,"maximum_uses_per_address":1},"name":"deterministic-chain","receiving":{"gap":20,"maximum_uses_per_address":1}}`
	if string(got) != want {
		t.Errorf("defaults: %s", got)
	}
	stored := Account{AddressGenerator: json.RawMessage(`{"name":"deterministic-chain","receiving":{"gap":30,"maximum_uses_per_address":2}}`)}
	got, _ = json.Marshal(stored.AddressGeneratorDetails())
	want = `{"change":{"gap":6,"maximum_uses_per_address":1},"name":"deterministic-chain","receiving":{"gap":30,"maximum_uses_per_address":2}}`
	if string(got) != want {
		t.Errorf("stored: %s", got)
	}
	single := Account{AddressGenerator: json.RawMessage(`{"name":"single-address"}`)}
	if got := single.AddressGeneratorDetails(); len(got) != 1 || got["name"] != "single-address" {
		t.Errorf("single-address: %v", got)
	}
}

// writeRawWallet writes a wallet file with fields this daemon does not model, to check they survive.
func writeRawWallet(t *testing.T, dir string) string {
	t.Helper()
	raw := `{
  "version": 1,
  "name": "Raw",
  "preferences": {"shared": {"ts": 1, "value": {"x": 1}}},
  "accounts": [
    {"ledger": "lbc_mainnet", "name": "first", "seed": "enc-seed-1", "encrypted": true, "private_key": "enc-key-1", "public_key": "` + publicKeyOne + `",
     "address_generator": {"name": "deterministic-chain", "receiving": {"gap": 20, "maximum_uses_per_address": 1}, "change": {"gap": 6, "maximum_uses_per_address": 1}},
     "modified_on": 1700000000, "certificates": {"k": "v"}},
    {"ledger": "lbc_mainnet", "name": "second", "seed": "enc-seed-2", "encrypted": true, "private_key": "enc-key-2", "public_key": "` + publicKeyTwo + `"}
  ]
}`
	path := filepath.Join(dir, "raw")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func rawFile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]any
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	return file
}

func rawWalletManager(t *testing.T) (*DefaultWalletManager, string) {
	dir := t.TempDir()
	path := writeRawWallet(t, dir)
	manager := NewDefaultWalletManager(dir).(*DefaultWalletManager)
	if err := manager.Load(); err != nil {
		t.Fatal(err)
	}
	return manager, path
}

func ptr[T any](value T) *T { return &value }

func TestSetAccountRenamesAndKeepsEverythingElse(t *testing.T) {
	manager, path := rawWalletManager(t)
	account, err := manager.SetAccount(nil, "raw", idOne, AccountChange{NewName: ptr("renamed")})
	if err != nil || account.Name != "renamed" || account.ModifiedOn <= 1700000000 {
		t.Fatalf("unexpected result %+v %v", account, err)
	}

	file := rawFile(t, path)
	if file["version"] != float64(1) || file["name"] != "Raw" || file["preferences"] == nil {
		t.Errorf("wallet fields lost: %v", file)
	}
	first := file["accounts"].([]any)[0].(map[string]any)
	if first["name"] != "renamed" || first["seed"] != "enc-seed-1" || first["private_key"] != "enc-key-1" ||
		first["encrypted"] != true || first["certificates"] == nil || first["ledger"] != "lbc_mainnet" {
		t.Errorf("account fields lost or wrong: %v", first)
	}
	if got := manager.Read(nil, "raw").Accounts[0].Name; got != "renamed" {
		t.Errorf("in-memory name %q", got)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("permissions changed to %v", info.Mode().Perm())
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("temporary files left behind: %v", entries)
	}
	// The file still loads, and the locked state is untouched.
	again := NewDefaultWalletManager(filepath.Dir(path))
	if err := again.(*DefaultWalletManager).Load(); err != nil || !again.Read(nil, "raw").IsLocked() {
		t.Errorf("reload failed: %v", err)
	}
}

func TestSetAccountGaps(t *testing.T) {
	manager, path := rawWalletManager(t)
	if _, err := manager.SetAccount(nil, "raw", idOne, AccountChange{ReceivingGap: ptr(50), ChangeMaxUses: ptr(3)}); err != nil {
		t.Fatal(err)
	}
	generator := rawFile(t, path)["accounts"].([]any)[0].(map[string]any)["address_generator"].(map[string]any)
	receiving, change := generator["receiving"].(map[string]any), generator["change"].(map[string]any)
	if receiving["gap"] != float64(50) || receiving["maximum_uses_per_address"] != float64(1) ||
		change["gap"] != float64(6) || change["maximum_uses_per_address"] != float64(3) || generator["name"] != "deterministic-chain" {
		t.Errorf("unexpected generator %v", generator)
	}

	// The second account stores no generator: the defaults are written out with the change, as lbry-sdk does.
	if _, err := manager.SetAccount(nil, "raw", idTwo, AccountChange{ChangeGap: ptr(9)}); err != nil {
		t.Fatal(err)
	}
	generator = rawFile(t, path)["accounts"].([]any)[1].(map[string]any)["address_generator"].(map[string]any)
	if generator["change"].(map[string]any)["gap"] != float64(9) || generator["receiving"].(map[string]any)["gap"] != float64(20) {
		t.Errorf("unexpected generator %v", generator)
	}
}

func TestSetAccountDefaultMovesItFirst(t *testing.T) {
	manager, path := rawWalletManager(t)
	if _, err := manager.SetAccount(nil, "raw", idTwo, AccountChange{Default: true}); err != nil {
		t.Fatal(err)
	}
	accounts := rawFile(t, path)["accounts"].([]any)
	if accounts[0].(map[string]any)["name"] != "second" || accounts[1].(map[string]any)["name"] != "first" {
		t.Errorf("accounts not reordered: %v", accounts)
	}
	if got := manager.Read(nil, "raw").Accounts; got[0].ID != idTwo || got[1].ID != idOne {
		t.Errorf("in-memory order not updated")
	}
	// Already the default: nothing changes, nothing is written.
	before, _ := os.ReadFile(path)
	if _, err := manager.SetAccount(nil, "raw", idTwo, AccountChange{Default: true}); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Error("the file was rewritten without a change")
	}
}

func TestSetAccountWithNoChangeWritesNothing(t *testing.T) {
	manager, path := rawWalletManager(t)
	before, _ := os.ReadFile(path)
	if _, err := manager.SetAccount(nil, "raw", idOne, AccountChange{}); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Error("the file was rewritten without a change")
	}
}

func TestSetAccountErrors(t *testing.T) {
	manager, path := rawWalletManager(t)
	if _, err := manager.SetAccount(nil, "missing", idOne, AccountChange{NewName: ptr("x")}); err != ErrWalletNotFound {
		t.Errorf("unknown wallet: %v", err)
	}
	if _, err := manager.SetAccount(nil, "raw", "nope", AccountChange{NewName: ptr("x")}); err == nil {
		t.Error("unknown account must fail")
	}
	// The file changed on disk behind the manager's back.
	os.WriteFile(path, []byte(`{"name":"Raw","accounts":[]}`), 0o600)
	if _, err := manager.SetAccount(nil, "raw", idOne, AccountChange{NewName: ptr("x")}); err == nil {
		t.Error("a changed file must fail")
	}
}
