package wallet

import (
	"bytes"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"

	"lbry/daemon/wallet/keys"
)

type walletTestResolver struct {
	networks       map[string]keys.Network
	events         []string
	registered     []*Account
	registerCalls  int
	failRegisterAt int
}

func (resolver *walletTestResolver) NetworkForLedger(ledgerID string) (keys.Network, error) {
	resolver.events = append(resolver.events, "network:"+ledgerID)
	network, exists := resolver.networks[ledgerID]
	if !exists {
		return 0, errors.New("unknown test ledger")
	}
	return network, nil
}

func (resolver *walletTestResolver) RegisterAccount(ledgerID string, account *Account) error {
	resolver.registerCalls++
	resolver.events = append(resolver.events, "register:"+ledgerID)
	if resolver.failRegisterAt == resolver.registerCalls {
		return errors.New("test registration failure")
	}
	resolver.registered = append(resolver.registered, account)
	return nil
}

func TestWalletConstructionIDDefaultAndLookupSemantics(t *testing.T) {
	wallet := NewWallet()
	if wallet.Name != "Wallet" || wallet.ID != "Wallet" || len(wallet.Accounts) != 0 ||
		wallet.Storage == nil || wallet.Preferences == nil || wallet.EncryptionPassword != nil {
		t.Fatalf("default wallet = %#v", wallet)
	}

	for _, test := range []struct {
		path string
		want string
	}{
		{"", "Named"},
		{"/", ""},
		{"/tmp/wallet", "wallet"},
		{"/tmp/wallet/", ""},
		{`C:\\wallet`, `C:\\wallet`},
	} {
		got := NewWallet(WithWalletName("Named"), WithWalletStorage(NewWalletStorage(test.path)))
		if got.ID != test.want {
			t.Fatalf("wallet ID for %q = %q, want %q", test.path, got.ID, test.want)
		}
		got.Name = "Changed"
		if got.ID != test.want {
			t.Fatalf("wallet ID changed with name: %q", got.ID)
		}
	}

	first := walletTestReadOnlyAccount(t, fixedAccountXPub, "first", 1)
	second := walletTestReadOnlyAccount(t, mismatchedAccountXPub, "second", 2)
	provided := []*Account{first, second}
	wallet = NewWallet(WithWalletAccounts(provided))
	provided[0] = second
	if wallet.DefaultAccount() != second {
		t.Fatal("non-empty account list was copied instead of shared")
	}
	provided[0] = first

	if got, err := wallet.AccountOrDefault(nil); err != nil || got != first {
		t.Fatalf("default account = %p, %v", got, err)
	}
	if got, err := wallet.Account(second.ID); err != nil || got != second {
		t.Fatalf("account lookup = %p, %v", got, err)
	}
	emptyID := ""
	if _, err := wallet.AccountOrDefault(&emptyID); err == nil {
		t.Fatal("explicit empty account ID selected the default")
	}

	wallet.AddAccount(first)
	if got, err := wallet.Account(first.ID); err != nil || got != first {
		t.Fatalf("duplicate lookup did not return first match: %p, %v", got, err)
	}
	selected, err := wallet.AccountsOrAll([]string{second.ID, first.ID, second.ID})
	if err != nil || !reflect.DeepEqual(selected, []*Account{second, first, second}) {
		t.Fatalf("selected accounts = %v, %v", selected, err)
	}
	all, err := wallet.AccountsOrAll(nil)
	if err != nil {
		t.Fatal(err)
	}
	all[0] = second
	if wallet.Accounts[0] != second {
		t.Fatal("empty account selection returned a copy")
	}

	empty := make([]*Account, 0, 1)
	emptyWallet := NewWallet(WithWalletAccounts(empty))
	empty = append(empty, first)
	if len(emptyWallet.Accounts) != 0 {
		t.Fatal("empty input account list was retained")
	}
}

func TestWalletFromStorageRegistersBeforeAppendAndPreservesOrder(t *testing.T) {
	preferences := NewObject(Member{Key: "theme", Value: NewObject(
		Member{Key: "value", Value: "dark"}, Member{Key: "ts", Value: 10},
	)})
	document := NewObject(
		Member{Key: "version", Value: LatestVersion},
		Member{Key: "name", Value: "Loaded"},
		Member{Key: "preferences", Value: preferences},
		Member{Key: "accounts", Value: []any{
			walletTestAccountRecord(fixedAccountXPub, "first", 1),
			walletTestAccountRecord(mismatchedAccountXPub, "second", 2),
		}},
	)
	resolver := &walletTestResolver{networks: map[string]keys.Network{keys.MainNet.ID(): keys.MainNet}}
	wallet, err := WalletFromStorage(NewMemoryWalletStorage(document), resolver)
	if err != nil {
		t.Fatal(err)
	}
	if wallet.Name != "Loaded" || wallet.ID != "Loaded" || len(wallet.Accounts) != 2 {
		t.Fatalf("loaded wallet = %#v", wallet)
	}
	if !reflect.DeepEqual(resolver.events, []string{
		"network:lbc_mainnet", "register:lbc_mainnet",
		"network:lbc_mainnet", "register:lbc_mainnet",
	}) {
		t.Fatalf("resolver events = %v", resolver.events)
	}
	if resolver.registered[0] != wallet.Accounts[0] || resolver.registered[1] != wallet.Accounts[1] {
		t.Fatal("registered and wallet account identities differ")
	}
	if wallet.Accounts[0].Name != "first" || wallet.Accounts[1].Name != "second" {
		t.Fatalf("account order = %q, %q", wallet.Accounts[0].Name, wallet.Accounts[1].Name)
	}
	entryValue, _ := preferences.Get("theme")
	entryValue.(*Object).Set("value", "light")
	value, _, err := wallet.Preferences.Get("theme")
	if err != nil || value != "light" {
		t.Fatalf("preference constructor was not shallow: %v, %v", value, err)
	}

	failing := &walletTestResolver{
		networks:       map[string]keys.Network{keys.MainNet.ID(): keys.MainNet},
		failRegisterAt: 2,
	}
	if _, err := WalletFromStorage(NewMemoryWalletStorage(document), failing); err == nil {
		t.Fatal("registration failure was ignored")
	}
	if len(failing.registered) != 1 || failing.registerCalls != 2 {
		t.Fatalf("partial registrations = %d successful, %d calls", len(failing.registered), failing.registerCalls)
	}
}

func TestWalletExactOrderedObjectAndCompactJSON(t *testing.T) {
	certificates := NewObject(Member{Key: "cert", Value: "pem"})
	account, err := NewAccount(keys.MainNet, NewObject(
		Member{Key: "name", Value: "Read only"},
		Member{Key: "public_key", Value: fixedAccountXPub},
		Member{Key: "modified_on", Value: 123},
		Member{Key: "certificates", Value: certificates},
	))
	if err != nil {
		t.Fatal(err)
	}
	preferences := NewObject(Member{Key: "one", Value: NewObject(
		Member{Key: "value", Value: 1}, Member{Key: "ts", Value: 10},
	)})
	wallet := NewWallet(
		WithWalletName("Main Wallet"),
		WithWalletAccounts([]*Account{account}),
		WithWalletPreferences(preferences),
	)
	object, err := wallet.ToObject(nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"version", "name", "preferences", "accounts"}; !reflect.DeepEqual(object.Keys(), want) {
		t.Fatalf("wallet keys = %v, want %v", object.Keys(), want)
	}
	preferenceValue, _ := object.Get("preferences")
	if preferenceValue != wallet.Preferences.Data() {
		t.Fatal("wallet serialization copied preferences")
	}

	encoded, err := wallet.ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"version": 1, "name": "Main Wallet", "preferences": {"one": {"value": 1, "ts": 10}}, "accounts": [{"ledger": "lbc_mainnet", "name": "Read only", "seed": "", "encrypted": false, "private_key": "", "public_key": "` +
		fixedAccountXPub + `", "address_generator": {"name": "deterministic-chain", "receiving": {"gap": 20, "maximum_uses_per_address": 1}, "change": {"gap": 6, "maximum_uses_per_address": 1}}, "modified_on": 123, "certificates": {"cert": "pem"}}]}`
	if string(encoded) != want {
		t.Fatalf("compact wallet JSON differs\n got: %s\nwant: %s", encoded, want)
	}
	if strings.Contains(string(encoded), "\n") {
		t.Fatalf("compact JSON contains a newline: %q", encoded)
	}

	emptyJSON, err := NewWallet().ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(emptyJSON), `{"version": 1, "name": "Wallet", "preferences": {}, "accounts": []}`; got != want {
		t.Fatalf("empty wallet JSON = %q, want %q", got, want)
	}
}

func TestWalletSaveResetAndNilVersusEmptyPassword(t *testing.T) {
	clock := func() float64 { return 12_345.9 }
	account := walletTestSeedAccount(t, bytes.NewReader(bytes.Repeat([]byte{'0'}, 64)))
	wallet := NewWallet(
		WithWalletAccounts([]*Account{account}),
		WithWalletPreferenceOptions(WithPreferenceClock(clock)),
	)
	wallet.Preferences.SetAt(EncryptOnDisk, true, 1)
	plain, err := wallet.Save()
	if err != nil {
		t.Fatal(err)
	}
	value, _, err := wallet.Preferences.Get(EncryptOnDisk)
	if err != nil || value != false {
		t.Fatalf("reset preference = %v, %v", value, err)
	}
	entryValue, _ := wallet.Preferences.Data().Get(EncryptOnDisk)
	timestamp, _ := entryValue.(*Object).Get("ts")
	if timestamp != int64(12_345) {
		t.Fatalf("reset timestamp = %v, want 12345", timestamp)
	}
	if encrypted := walletTestSavedAccountField(t, plain, "encrypted"); encrypted != false {
		t.Fatalf("nil-password save encrypted account: %v", encrypted)
	}

	wallet.Preferences.SetAt(EncryptOnDisk, true, 2)
	emptyPassword := ""
	wallet.EncryptionPassword = &emptyPassword
	emptySaved, err := wallet.Save()
	if err != nil {
		t.Fatal(err)
	}
	value, _, _ = wallet.Preferences.Get(EncryptOnDisk)
	if value != true {
		t.Fatalf("empty password reset preference: %v", value)
	}
	if encrypted := walletTestSavedAccountField(t, emptySaved, "encrypted"); encrypted != false {
		t.Fatalf("empty-password save encrypted account: %v", encrypted)
	}

	password := "password"
	wallet.EncryptionPassword = &password
	encryptedSaved, err := wallet.Save()
	if err != nil {
		t.Fatal(err)
	}
	if got := walletTestSavedAccountField(t, encryptedSaved, "seed"); got != encryptedAccountSeed {
		t.Fatalf("saved encrypted seed = %q", got)
	}
	if got := walletTestSavedAccountField(t, encryptedSaved, "private_key"); got != encryptedAccountXPrv {
		t.Fatalf("saved encrypted private key = %q", got)
	}
	if got := walletTestSavedAccountField(t, encryptedSaved, "encrypted"); got != true {
		t.Fatalf("saved encrypted flag = %v", got)
	}
	if account.Encrypted || account.Seed != accountEncryptionSeed || account.PrivateKey == nil {
		t.Fatal("transient save mutated in-memory account lock state")
	}
}

func TestWalletPinnedHashesAndLockedHashFailures(t *testing.T) {
	emptyHash, err := NewWallet().Hash()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := hex.EncodeToString(emptyHash[:]), "c74f3008fdd2f7c5ae5446ab2e522629f63346f68a4026b4f72b91b393475ff6"; got != want {
		t.Fatalf("empty wallet hash = %s, want %s", got, want)
	}

	account := walletTestPinnedHashAccount(t)
	wallet := NewWallet(WithWalletName("Main Wallet"), WithWalletAccounts([]*Account{account}))
	hash, err := wallet.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := hex.EncodeToString(hash[:]), "869acc4660dde0f13784ed743796adf89562cdf79fdfc9e5c6dbea98d62ccf90"; got != want {
		t.Fatalf("pinned wallet hash = %s, want %s", got, want)
	}

	preferences := NewObject(Member{Key: EncryptOnDisk, Value: NewObject(
		Member{Key: "ts", Value: 1571762543.351794}, Member{Key: "value", Value: true},
	)})
	wallet = NewWallet(
		WithWalletName("Main Wallet"),
		WithWalletAccounts([]*Account{walletTestPinnedHashAccount(t)}),
		WithWalletPreferences(preferences),
	)
	encrypted, err := wallet.IsEncrypted()
	if err != nil || encrypted {
		t.Fatalf("preferred encryption without password = %v, %v", encrypted, err)
	}
	hash, err = wallet.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := hex.EncodeToString(hash[:]), "8cc6341885e6ad46f72a17364c65f8441f09e79996c55202196b399c75f8d751"; got != want {
		t.Fatalf("preferred-encryption wallet hash = %s, want %s", got, want)
	}

	if err := wallet.Accounts[0].Encrypt("password"); err != nil {
		t.Fatal(err)
	}
	if _, err := wallet.Hash(); !errors.Is(err, ErrWalletPasswordUnavailable) {
		t.Fatalf("locked nil-password hash error = %v, want ErrWalletPasswordUnavailable", err)
	}
	password := "password"
	wallet.EncryptionPassword = &password
	if _, err := wallet.Hash(); !errors.Is(err, ErrEncryptedAccountHash) {
		t.Fatalf("locked password hash error = %v, want ErrEncryptedAccountHash", err)
	}
	if _, err := wallet.ToJSON(); !errors.Is(err, ErrLockedWalletSerialization) {
		t.Fatalf("locked ToJSON error = %v", err)
	}
}

func TestWalletEncryptLockUnlockDecryptLifecycle(t *testing.T) {
	account := walletTestSeedAccount(t, bytes.NewReader(bytes.Repeat([]byte{'0'}, 64)))
	wallet := NewWallet(WithWalletAccounts([]*Account{account}))
	if err := wallet.Encrypt(""); !errors.Is(err, ErrBlankWalletPassword) {
		t.Fatalf("blank encryption error = %v", err)
	}
	if err := wallet.Lock(); !errors.Is(err, ErrWalletPasswordUnavailable) {
		t.Fatalf("passwordless lock error = %v", err)
	}
	if err := wallet.Encrypt("password"); err != nil {
		t.Fatal(err)
	}
	if account.Encrypted || wallet.EncryptionPassword == nil || *wallet.EncryptionPassword != "password" {
		t.Fatalf("post-encrypt state = encrypted %v password %v", account.Encrypted, wallet.EncryptionPassword)
	}
	if encrypted, err := wallet.IsEncrypted(); err != nil || !encrypted {
		t.Fatalf("post-encrypt status = %v, %v", encrypted, err)
	}
	if err := wallet.Lock(); err != nil {
		t.Fatal(err)
	}
	if !wallet.IsLocked() || !account.Encrypted {
		t.Fatal("lock did not encrypt account in memory")
	}
	if unlocked, err := wallet.Unlock("wrong"); err != nil || unlocked || !account.Encrypted {
		t.Fatalf("wrong-password unlock = %v, %v, encrypted %v", unlocked, err, account.Encrypted)
	}
	if unlocked, err := wallet.Unlock("password"); err != nil || !unlocked || account.Encrypted {
		t.Fatalf("correct unlock = %v, %v, encrypted %v", unlocked, err, account.Encrypted)
	}
	passwordPointer := wallet.EncryptionPassword
	if err := wallet.Decrypt(); err != nil {
		t.Fatal(err)
	}
	value, _, err := wallet.Preferences.Get(EncryptOnDisk)
	if err != nil || value != false || wallet.EncryptionPassword != passwordPointer {
		t.Fatalf("post-decrypt preference/password = %v, %v, %v", value, wallet.EncryptionPassword, err)
	}
	if encrypted, err := wallet.IsEncrypted(); err != nil || encrypted {
		t.Fatalf("post-decrypt status = %v, %v", encrypted, err)
	}
}

func TestWalletUnlockAndLockPreservePartialMutation(t *testing.T) {
	first := walletTestSeedAccount(t, bytes.NewReader(bytes.Repeat([]byte{'1'}, 64)))
	second := walletTestSeedAccount(t, bytes.NewReader(bytes.Repeat([]byte{'2'}, 64)))
	if err := first.Encrypt("first-password"); err != nil {
		t.Fatal(err)
	}
	if err := second.Encrypt("second-password"); err != nil {
		t.Fatal(err)
	}
	wallet := NewWallet(WithWalletAccounts([]*Account{first, second}))
	unlocked, err := wallet.Unlock("first-password")
	if err != nil || unlocked {
		t.Fatalf("partial unlock result = %v, %v", unlocked, err)
	}
	if first.Encrypted || !second.Encrypted || wallet.EncryptionPassword != nil {
		t.Fatalf("partial unlock state = first %v second %v password %v",
			first.Encrypted, second.Encrypted, wallet.EncryptionPassword)
	}

	lockFirst := walletTestSeedAccount(t, bytes.NewReader(bytes.Repeat([]byte{'3'}, 64)))
	lockSecond, err := NewAccount(keys.MainNet, NewObject(
		Member{Key: "seed", Value: accountEncryptionSeed},
		Member{Key: "modified_on", Value: 123},
	), WithAccountEntropy(walletErrorReader{}))
	if err != nil {
		t.Fatal(err)
	}
	password := "password"
	lockWallet := NewWallet(WithWalletAccounts([]*Account{lockFirst, lockSecond}))
	lockWallet.EncryptionPassword = &password
	if err := lockWallet.Lock(); err == nil {
		t.Fatal("lock entropy failure was ignored")
	}
	if !lockFirst.Encrypted || lockSecond.Encrypted {
		t.Fatalf("partial lock state = first %v second %v", lockFirst.Encrypted, lockSecond.Encrypted)
	}
}

type walletErrorReader struct{}

func (walletErrorReader) Read([]byte) (int, error) {
	return 0, errors.New("test entropy failure")
}

func walletTestReadOnlyAccount(t *testing.T, publicKey, name string, modifiedOn int) *Account {
	t.Helper()
	account, err := NewAccount(keys.MainNet, walletTestAccountRecord(publicKey, name, modifiedOn))
	if err != nil {
		t.Fatal(err)
	}
	return account
}

func walletTestAccountRecord(publicKey, name string, modifiedOn int) *Object {
	return NewObject(
		Member{Key: "ledger", Value: keys.MainNet.ID()},
		Member{Key: "name", Value: name},
		Member{Key: "public_key", Value: publicKey},
		Member{Key: "modified_on", Value: modifiedOn},
	)
}

func walletTestSeedAccount(t *testing.T, entropy interface{ Read([]byte) (int, error) }) *Account {
	t.Helper()
	account, err := NewAccount(keys.MainNet, NewObject(
		Member{Key: "seed", Value: accountEncryptionSeed},
		Member{Key: "modified_on", Value: 123},
	), WithAccountEntropy(entropy))
	if err != nil {
		t.Fatal(err)
	}
	if err := account.SetInitializationVector("seed", []byte("0000000000000000")); err != nil {
		t.Fatal(err)
	}
	if err := account.SetInitializationVector("private_key", []byte("0000000000000000")); err != nil {
		t.Fatal(err)
	}
	return account
}

func walletTestPinnedHashAccount(t *testing.T) *Account {
	t.Helper()
	account, err := NewAccount(keys.MainNet, NewObject(
		Member{Key: "certificates", Value: NewObject()},
		Member{Key: "name", Value: "An Account"},
		Member{Key: "ledger", Value: keys.MainNet.ID()},
		Member{Key: "modified_on", Value: 123},
		Member{Key: "seed", Value: accountEncryptionSeed},
		Member{Key: "encrypted", Value: false},
		Member{Key: "private_key", Value: accountEncryptionXPrv},
		Member{Key: "public_key", Value: fixedAccountXPub},
		Member{Key: "address_generator", Value: NewObject(
			Member{Key: "name", Value: DeterministicChainGenerator},
			Member{Key: "receiving", Value: NewObject(
				Member{Key: "gap", Value: 17}, Member{Key: "maximum_uses_per_address", Value: 3},
			)},
			Member{Key: "change", Value: NewObject(
				Member{Key: "gap", Value: 10}, Member{Key: "maximum_uses_per_address", Value: 3},
			)},
		)},
	))
	if err != nil {
		t.Fatal(err)
	}
	return account
}

func walletTestSavedAccountField(t *testing.T, encoded []byte, field string) any {
	t.Helper()
	decoded, err := decodeOrderedJSON(encoded)
	if err != nil {
		t.Fatal(err)
	}
	document := decoded.(*Object)
	accountsValue, _ := document.Get("accounts")
	accounts := accountsValue.([]any)
	value, exists := accounts[0].(*Object).Get(field)
	if !exists {
		t.Fatalf("saved account has no %q field", field)
	}
	return value
}
