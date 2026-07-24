package wallet

import (
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"lbry/daemon/wallet/keys"
)

func TestPythonWalletPathHelpersPreservePinnedPOSIXBehavior(t *testing.T) {
	t.Parallel()

	joinCases := []struct {
		name  string
		parts []string
		want  string
	}{
		{name: "ordinary", parts: []string{"/var/lib/lbry", "wallets", "default_wallet"}, want: "/var/lib/lbry/wallets/default_wallet"},
		{name: "absolute resets", parts: []string{"/var/lib/lbry", "wallets", "/tmp/wallet"}, want: "/tmp/wallet"},
		{name: "dot segment retained", parts: []string{"/var/lib/lbry", "wallets", "../wallet"}, want: "/var/lib/lbry/wallets/../wallet"},
		{name: "trailing separator retained", parts: []string{"/var/lib/lbry", "wallets", "named/"}, want: "/var/lib/lbry/wallets/named/"},
		{name: "empty final component", parts: []string{"/var/lib/lbry", "wallets", ""}, want: "/var/lib/lbry/wallets/"},
		{name: "empty first component", parts: []string{"", "wallets", "default_wallet"}, want: "wallets/default_wallet"},
		{name: "double leading separator retained", parts: []string{"//host/share", "wallets"}, want: "//host/share/wallets"},
	}
	for _, test := range joinCases {
		t.Run(test.name, func(t *testing.T) {
			if got := pythonPathJoin(test.parts...); got != test.want {
				t.Fatalf("pythonPathJoin(%q) = %q, want %q", test.parts, got, test.want)
			}
		})
	}

	baseCases := map[string]string{
		"":           "",
		"wallet":     "wallet",
		"a/wallet":   "wallet",
		"/":          "",
		"/a/":        "",
		"a/../named": "named",
	}
	for path, want := range baseCases {
		if got := pythonBaseName(path); got != want {
			t.Errorf("pythonBaseName(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestIsolatedLedgerPathsAndValidation(t *testing.T) {
	t.Parallel()

	config := LedgerConfig{"data_path": "/wallet/root/../root"}
	ledger, err := newLedger(keys.TestNet, config)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ledger.ID(), "lbc_testnet"; got != want {
		t.Fatalf("ledger ID = %q, want %q", got, want)
	}
	if got, err := ledger.Path(); err != nil || got != "/wallet/root/../root/lbc_testnet" {
		t.Fatalf("ledger path = %q, %v", got, err)
	}
	if got, err := ledger.DatabasePath(); err != nil || got != "/wallet/root/../root/lbc_testnet/blockchain.db" {
		t.Fatalf("database path = %q, %v", got, err)
	}
	if got, err := ledger.HeadersPath(); err != nil || got != "/wallet/root/../root/lbc_testnet/headers" {
		t.Fatalf("headers path = %q, %v", got, err)
	}
	if ledger.Headers == nil || ledger.Headers.path != "/wallet/root/../root/lbc_testnet/headers" {
		t.Fatalf("ledger headers = %#v", ledger.Headers)
	}
	if !ledger.Headers.validateDifficulty || ledger.Headers.opened {
		t.Fatalf("testnet headers state = validate %t, opened %t", ledger.Headers.validateDifficulty, ledger.Headers.opened)
	}
	if ledger.Database == nil || ledger.Database.Path() != "/wallet/root/../root/lbc_testnet/blockchain.db" {
		t.Fatalf("ledger database = %#v", ledger.Database)
	}
	regtest, err := newLedger(keys.RegTest, config)
	if err != nil {
		t.Fatal(err)
	}
	if regtest.Headers == nil || regtest.Headers.validateDifficulty || regtest.Headers.opened {
		t.Fatalf("regtest headers state = %#v", regtest.Headers)
	}

	for name, invalid := range map[string]LedgerConfig{
		"missing": {},
		"null":    {"data_path": nil},
		"number":  {"data_path": 1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := newLedger(keys.MainNet, invalid); !errors.Is(err, ErrInvalidLedgerConfig) {
				t.Fatalf("newLedger error = %v, want ErrInvalidLedgerConfig", err)
			}
		})
	}
}

func TestWalletManagerFromConfigRegistersAndFlattensInLoadOrder(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	firstPath := filepath.Join(directory, "first_wallet")
	secondPath := filepath.Join(directory, "second_wallet")
	writeManagerWalletFixture(t, firstPath, managerWalletDocument(
		managerAccountRecord("First", false),
	))
	writeManagerWalletFixture(t, secondPath, managerWalletDocument(
		managerAccountRecord("Second", false),
	))

	manager, err := WalletManagerFromConfig(ManagerConfig{
		Ledgers: []LedgerSpec{{
			ID: keys.MainNet.ID(), Config: LedgerConfig{"data_path": directory},
		}},
		Wallets: []string{firstPath, secondPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(manager.Wallets), 2; got != want {
		t.Fatalf("wallet count = %d, want %d", got, want)
	}
	if manager.DefaultWallet() != manager.Wallets[0] ||
		manager.DefaultAccount() != manager.Wallets[0].Accounts[0] {
		t.Fatal("default wallet/account did not use the first loaded wallet")
	}
	flattened := manager.Accounts()
	ledger := manager.Ledgers[keys.MainNet]
	if len(flattened) != 2 || len(ledger.Accounts) != 2 {
		t.Fatalf("account counts = manager %d, ledger %d", len(flattened), len(ledger.Accounts))
	}
	for index := range flattened {
		if flattened[index] != manager.Wallets[index].Accounts[0] ||
			ledger.Accounts[index] != flattened[index] {
			t.Fatalf("account %d was not shared in wallet/manager/ledger order", index)
		}
		if flattened[index].ledger != ledger {
			t.Fatalf("account %d ledger = %p, want %p", index, flattened[index].ledger, ledger)
		}
	}

	if got, err := manager.GetWalletOrDefault(nil); err != nil || got != manager.Wallets[0] {
		t.Fatalf("default lookup = %p, %v", got, err)
	}
	secondID := "second_wallet"
	if got, err := manager.GetWalletOrDefault(&secondID); err != nil || got != manager.Wallets[1] {
		t.Fatalf("explicit lookup = %p, %v", got, err)
	}
	emptyID := ""
	_, err = manager.GetWalletOrDefault(&emptyID)
	var notLoaded *WalletNotLoadedError
	if !errors.As(err, &notLoaded) || err.Error() != "Wallet  is not loaded." {
		t.Fatalf("empty ID lookup error = %T %v", err, err)
	}
}

func TestWalletManagerLedgerFirstConfigWins(t *testing.T) {
	t.Parallel()

	manager := NewWalletManager()
	firstConfig := LedgerConfig{"data_path": "/first"}
	first, err := manager.GetOrCreateLedger(keys.MainNet.ID(), firstConfig)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.GetOrCreateLedger(keys.MainNet.ID(), LedgerConfig{"data_path": "/second"})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("second get-or-create replaced the ledger instance")
	}
	firstConfig["data_path"] = "/mutated"
	if got, err := second.Path(); err != nil || got != "/mutated/lbc_mainnet" {
		t.Fatalf("aliased first config path = %q, %v", got, err)
	}
}

func TestWalletManagerPreservesFirstLedgerConstructionOrder(t *testing.T) {
	t.Parallel()

	manager, err := WalletManagerFromConfig(ManagerConfig{
		Ledgers: []LedgerSpec{
			{ID: keys.RegTest.ID(), Config: LedgerConfig{"data_path": "/wallets"}},
			{ID: keys.MainNet.ID(), Config: LedgerConfig{"data_path": "/wallets"}},
			{ID: keys.TestNet.ID(), Config: LedgerConfig{"data_path": "/wallets"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ordered := manager.OrderedLedgers()
	got := make([]string, len(ordered))
	for index, ledger := range ordered {
		got[index] = ledger.ID()
	}
	want := []string{"lbc_regtest", "lbc_mainnet", "lbc_testnet"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ordered ledgers = %v, want %v", got, want)
	}
	if _, err := manager.GetOrCreateLedger(keys.MainNet.ID(), LedgerConfig{"data_path": "/ignored"}); err != nil {
		t.Fatal(err)
	}
	if got := len(manager.OrderedLedgers()); got != 3 {
		t.Fatalf("existing ledger was appended again: %d", got)
	}
	ordered[0] = nil
	if manager.OrderedLedgers()[0] == nil {
		t.Fatal("OrderedLedgers exposed its backing slice")
	}
}

func TestWalletManagerPassesDeterministicOptionsToConfigAndLaterImports(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	firstPath := filepath.Join(directory, "first")
	secondPath := filepath.Join(directory, "second")
	firstRecord := managerAccountRecord("First", false)
	firstRecord.Delete("modified_on")
	secondRecord := managerAccountRecord("Second", false)
	secondRecord.Delete("modified_on")
	writeManagerWalletFixture(t, firstPath, managerWalletDocument(firstRecord))
	writeManagerWalletFixture(t, secondPath, managerWalletDocument(secondRecord))
	clock := func() time.Time { return time.Unix(77, 900_000_000) }

	manager, err := WalletManagerFromConfig(ManagerConfig{
		Ledgers: []LedgerSpec{{
			ID: keys.MainNet.ID(), Config: LedgerConfig{"data_path": directory},
		}},
		Wallets: []string{firstPath},
	}, WithWalletAccountOptions(WithAccountClock(clock)))
	if err != nil {
		t.Fatal(err)
	}
	if got := manager.DefaultAccount().ModifiedOn.String(); got != "77" {
		t.Fatalf("config-loaded modified_on = %s", got)
	}
	imported, err := manager.ImportWallet(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := imported.DefaultAccount().ModifiedOn.String(); got != "77" {
		t.Fatalf("later imported modified_on = %s", got)
	}
}

func TestWalletManagerImportRetainsEarlierLedgerRegistrationOnLaterFailure(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	manager := NewWalletManager()
	ledger, err := manager.GetOrCreateLedger(keys.MainNet.ID(), LedgerConfig{"data_path": directory})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "partial_wallet")
	writeManagerWalletFixture(t, path, managerWalletDocument(
		managerAccountRecord("Registered", false),
		NewObject(
			Member{Key: "ledger", Value: keys.MainNet.ID()},
			Member{Key: "name", Value: "Invalid"},
			Member{Key: "seed", Value: "ciphertext"},
			Member{Key: "encrypted", Value: true},
			Member{Key: "private_key", Value: "ciphertext"},
			Member{Key: "public_key", Value: "not-an-extended-key"},
			Member{Key: "modified_on", Value: 2},
		),
	))

	if _, err := manager.ImportWallet(path); err == nil {
		t.Fatal("ImportWallet accepted a malformed later account")
	}
	if len(manager.Wallets) != 0 {
		t.Fatalf("failed wallet was appended: %d", len(manager.Wallets))
	}
	if len(ledger.Accounts) != 1 || ledger.Accounts[0].Name != "Registered" {
		t.Fatalf("partial ledger registrations = %#v", ledger.Accounts)
	}
	if len(manager.Accounts()) != 0 {
		t.Fatal("orphan ledger registration leaked into manager wallet flattening")
	}
}

func TestWalletManagerLBRYNetInitializesOnlyFirstEmptyWallet(t *testing.T) {
	directory := t.TempDir()
	walletsDirectory := filepath.Join(directory, "wallets")
	if err := os.Mkdir(walletsDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	secondPath := filepath.Join(walletsDirectory, "second_wallet")
	writeManagerWalletFixture(t, secondPath, managerWalletDocument(
		managerAccountRecord("Existing", false),
	))
	t.Setenv("LBRY_FEE_PER_NAME_CHAR", "123_456")

	factoryCalls := 0
	manager, err := WalletManagerFromLBRYNetConfig(LBRYNetConfig{
		BlockchainName:        "lbrycrd_main",
		WalletDir:             directory,
		Wallets:               []string{"default_wallet", "second_wallet", "third_wallet"},
		HubTimeout:            30,
		DefaultServers:        []any{"hub.example:50001"},
		KnownHubs:             NewObject(),
		Jurisdiction:          "US",
		ConcurrentHubRequests: 32,
		TransactionCacheSize:  131072,
		CoinSelectionStrategy: "prefer_confirmed",
	}, func(ledger *Ledger, wallet *Wallet) (*Account, error) {
		factoryCalls++
		if ledger.Network != keys.MainNet || wallet.ID != "default_wallet" {
			t.Fatalf("factory context = %s, %q", ledger.ID(), wallet.ID)
		}
		return NewAccount(ledger.Network, NewObject(
			Member{Key: "ledger", Value: ledger.ID()},
			Member{Key: "name", Value: "Generated"},
			Member{Key: "seed", Value: accountEncryptionSeed},
			Member{Key: "modified_on", Value: 10},
		))
	})
	if err != nil {
		t.Fatal(err)
	}
	if factoryCalls != 1 {
		t.Fatalf("factory calls = %d, want 1", factoryCalls)
	}
	if got := []int{
		len(manager.Wallets[0].Accounts),
		len(manager.Wallets[1].Accounts),
		len(manager.Wallets[2].Accounts),
	}; !reflect.DeepEqual(got, []int{1, 1, 0}) {
		t.Fatalf("per-wallet account counts = %v", got)
	}
	ledger := manager.Ledgers[keys.MainNet]
	if got := []string{ledger.Accounts[0].Name, ledger.Accounts[1].Name}; !reflect.DeepEqual(got, []string{"Existing", "Generated"}) {
		t.Fatalf("ledger registration order = %v", got)
	}
	if got := []string{manager.Accounts()[0].Name, manager.Accounts()[1].Name}; !reflect.DeepEqual(got, []string{"Generated", "Existing"}) {
		t.Fatalf("wallet flattening order = %v", got)
	}
	if ledger.CoinSelectionStrategy != "prefer_confirmed" {
		t.Fatalf("coin selection strategy = %v", ledger.CoinSelectionStrategy)
	}
	fee, ok := ledger.Config["fee_per_name_char"].(*big.Int)
	if !ok || fee.String() != "123456" {
		t.Fatalf("fee_per_name_char = %T %v", ledger.Config["fee_per_name_char"], ledger.Config["fee_per_name_char"])
	}
	defaultDocument, err := NewWalletStorage(filepath.Join(walletsDirectory, "default_wallet")).Read()
	if err != nil {
		t.Fatal(err)
	}
	accounts, _ := defaultDocument.Get("accounts")
	if records, err := walletAccountRecords(accounts); err != nil || len(records) != 1 {
		t.Fatalf("saved generated accounts = %v, %v", records, err)
	}
	if _, err := os.Stat(filepath.Join(walletsDirectory, "third_wallet")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("later empty wallet was unexpectedly saved: %v", err)
	}
}

func TestWalletManagerLBRYNetInitializesLockedPreference(t *testing.T) {
	directory := t.TempDir()
	walletsDirectory := filepath.Join(directory, "wallets")
	if err := os.Mkdir(walletsDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(walletsDirectory, "default_wallet")
	writeManagerWalletFixture(t, path, managerWalletDocument(
		managerAccountRecord("Locked", true),
	))
	t.Setenv("LBRY_FEE_PER_NAME_CHAR", "0")

	manager, err := WalletManagerFromLBRYNetConfig(LBRYNetConfig{
		BlockchainName: "lbrycrd_main",
		WalletDir:      directory,
		Wallets:        []string{"default_wallet"},
	}, func(*Ledger, *Wallet) (*Account, error) {
		t.Fatal("factory called for a non-empty locked wallet")
		return nil, nil
	}, WithWalletPreferenceOptions(WithPreferenceClock(func() float64 { return 12345.75 })))
	if err != nil {
		t.Fatal(err)
	}
	wallet := manager.DefaultWallet()
	if !wallet.IsLocked() {
		t.Fatal("encrypted account did not lock wallet")
	}
	value, exists, err := wallet.Preferences.Get(EncryptOnDisk)
	if err != nil || !exists || value != true {
		t.Fatalf("encrypt-on-disk preference = %v, %t, %v", value, exists, err)
	}
	entryValue, _ := wallet.Preferences.Data().Get(EncryptOnDisk)
	entry := entryValue.(*Object)
	timestamp, _ := entry.Get("ts")
	if timestamp != int64(12345) {
		t.Fatalf("encrypt-on-disk timestamp = %T %v", timestamp, timestamp)
	}
	reloaded, err := WalletFromStorage(NewWalletStorage(path), manager)
	if err != nil {
		t.Fatal(err)
	}
	value, exists, err = reloaded.Preferences.Get(EncryptOnDisk)
	if err != nil || !exists || value != true || !reloaded.IsLocked() {
		t.Fatalf("persisted locked preference = %v, %t, %v; locked=%t", value, exists, err, reloaded.IsLocked())
	}
}

func TestWalletManagerRejectsLegacyLBryumWithoutMutation(t *testing.T) {
	directory := t.TempDir()
	walletsDirectory := filepath.Join(directory, "wallets")
	if err := os.Mkdir(walletsDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(walletsDirectory, "default_wallet")
	contents := []byte(`{"master_public_keys":{"x/":"legacy"},"accounts":{}}`)
	if err := os.WriteFile(path, contents, 0o640); err != nil {
		t.Fatal(err)
	}

	_, err := WalletManagerFromLBRYNetConfig(LBRYNetConfig{
		BlockchainName: "lbrycrd_main",
		WalletDir:      directory,
		Wallets:        []string{"default_wallet"},
	}, nil)
	if !errors.Is(err, ErrLegacyWalletMigrationUnsupported) {
		t.Fatalf("legacy wallet error = %v", err)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !reflect.DeepEqual(after, contents) {
		t.Fatalf("legacy wallet mutated: %q", after)
	}
	if matches, globErr := filepath.Glob(filepath.Join(walletsDirectory, "old_lbryum_wallet_*")); globErr != nil || len(matches) != 0 {
		t.Fatalf("legacy backups = %v, %v", matches, globErr)
	}
}

func managerWalletDocument(accounts ...*Object) *Object {
	values := make([]any, len(accounts))
	for index, account := range accounts {
		values[index] = account
	}
	return NewObject(
		Member{Key: "version", Value: LatestVersion},
		Member{Key: "name", Value: "My Wallet"},
		Member{Key: "preferences", Value: NewObject()},
		Member{Key: "accounts", Value: values},
	)
}

func managerAccountRecord(name string, encrypted bool) *Object {
	seed := accountEncryptionSeed
	privateKey := accountEncryptionXPrv
	if encrypted {
		seed = "encrypted-seed"
		privateKey = "encrypted-private-key"
	}
	return NewObject(
		Member{Key: "ledger", Value: keys.MainNet.ID()},
		Member{Key: "name", Value: name},
		Member{Key: "seed", Value: seed},
		Member{Key: "encrypted", Value: encrypted},
		Member{Key: "private_key", Value: privateKey},
		Member{Key: "public_key", Value: fixedAccountXPub},
		Member{Key: "address_generator", Value: NewObject(Member{Key: "name", Value: SingleAddressGenerator})},
		Member{Key: "modified_on", Value: 1},
		Member{Key: "certificates", Value: NewObject()},
	)
}

func writeManagerWalletFixture(t *testing.T, path string, document *Object) {
	t.Helper()
	if _, err := NewWalletStorage(path).Write(document); err != nil {
		t.Fatal(err)
	}
}
