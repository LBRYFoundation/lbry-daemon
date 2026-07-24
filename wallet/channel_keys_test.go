package wallet

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"lbry/daemon/wallet/keys"
)

const channelKeyLegacyPEM = "-----BEGIN EC PRIVATE KEY-----\n" +
	"MHQCAQEEIBZRTZ7tHnYCH3IE9mCo95466L/ShYFhXGrjmSMFJw8eoAcGBSuBBAAK\n" +
	"oUQDQgAEmucoPz9nI+ChZrfhnh0RZ/bcX0r2G0pYBmoNKovtKzXGa8y07D66MWsW\n" +
	"qXptakqO/9KddIkBu5eJNSUZzQCxPQ==\n" +
	"-----END EC PRIVATE KEY-----\n"

const channelKeyCanonicalPEM = "-----BEGIN PRIVATE KEY-----\n" +
	"MIGEAgEAMBAGByqGSM49AgEGBSuBBAAKBG0wawIBAQQgFlFNnu0edgIfcgT2YKj3\n" +
	"njrov9KFgWFcauOZIwUnDx6hRANCAASa5yg/P2cj4KFmt+GeHRFn9txfSvYbSlgG\n" +
	"ag0qi+0rNcZrzLTsProxaxapem1qSo7/0p10iQG7l4k1JRnNALE9\n" +
	"-----END PRIVATE KEY-----\n"

func TestAccountAddAndGetImportedChannelPrivateKey(t *testing.T) {
	account := channelKeyTestAccount(t, keys.RegTest)
	privateKey, err := keys.PrivateKeyFromPEM(keys.RegTest, channelKeyLegacyPEM)
	if err != nil {
		t.Fatal(err)
	}
	if err := account.AddChannelPrivateKey(privateKey); err != nil {
		t.Fatal(err)
	}
	if got, want := account.ChannelKeys.Keys(), []string{"mqs77XbdnuxWN4cXrjKbSoGLkvAHa4f4B8"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("channel-key names = %v, want %v", got, want)
	}
	stored, _ := account.ChannelKeys.Get(privateKey.Address())
	if stored != channelKeyCanonicalPEM {
		t.Fatalf("stored PEM differs\n%s", stored)
	}

	fallbackCalls := 0
	loaded, err := account.GetChannelPrivateKey(
		privateKey.PublicKey().CompressedBytes(),
		func(string) (*keys.PrivateKey, error) {
			fallbackCalls++
			return nil, errors.New("fallback must not run")
		},
	)
	if err != nil || loaded == nil || !bytes.Equal(loaded.PrivateKeyBytes(), privateKey.PrivateKeyBytes()) {
		t.Fatalf("loaded imported key = %v, %v", loaded, err)
	}
	if fallbackCalls != 0 {
		t.Fatalf("imported lookup called fallback %d times", fallbackCalls)
	}

	// Existing insertion position is retained on overwrite.
	account.ChannelKeys.Set("later", "value")
	if err := account.AddChannelPrivateKey(privateKey); err != nil {
		t.Fatal(err)
	}
	if got := account.ChannelKeys.Keys(); !reflect.DeepEqual(got, []string{privateKey.Address(), "later"}) {
		t.Fatalf("overwrite order = %v", got)
	}
}

func TestAccountImportedChannelKeyPrecedenceAndFallback(t *testing.T) {
	account := channelKeyTestAccount(t, keys.RegTest)
	requested, err := keys.PrivateKeyFromPEM(keys.RegTest, channelKeyCanonicalPEM)
	if err != nil {
		t.Fatal(err)
	}
	address, err := keys.AddressFromPublicKeyBytes(keys.RegTest, requested.PublicKey().CompressedBytes())
	if err != nil {
		t.Fatal(err)
	}
	fallbackKey, err := keys.NewPrivateKey(keys.RegTest, bytes.Repeat([]byte{2}, 32), make([]byte, 32), 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	fallbackCalls := 0
	fallback := func(gotAddress string) (*keys.PrivateKey, error) {
		fallbackCalls++
		if gotAddress != address {
			t.Fatalf("fallback address = %q, want %q", gotAddress, address)
		}
		return fallbackKey, nil
	}

	account.ChannelKeys.Set(address, "")
	loaded, err := account.GetChannelPrivateKey(requested.PublicKey().CompressedBytes(), fallback)
	if err != nil || loaded != fallbackKey || fallbackCalls != 1 {
		t.Fatalf("falsey fallback = %p, calls %d, %v", loaded, fallbackCalls, err)
	}
	account.ChannelKeys.Set(address, "not PEM")
	if _, err := account.GetChannelPrivateKey(requested.PublicKey().CompressedBytes(), fallback); err == nil {
		t.Fatal("truthy malformed PEM used fallback")
	}
	if fallbackCalls != 1 {
		t.Fatalf("malformed PEM called fallback: %d", fallbackCalls)
	}

	// The mapping key controls selection; decoded key material is not checked
	// against the requested public key.
	account.ChannelKeys.Set(address, channelKeyCanonicalPEM)
	loaded, err = account.GetChannelPrivateKey([]byte("arbitrary non-point bytes"), func(gotAddress string) (*keys.PrivateKey, error) {
		return nil, nil
	})
	if err != nil || loaded != nil {
		t.Fatalf("arbitrary public-key bytes = %v, %v", loaded, err)
	}
	other, err := keys.NewPrivateKey(keys.RegTest, bytes.Repeat([]byte{4}, 32), make([]byte, 32), 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	otherAddress, err := keys.AddressFromPublicKeyBytes(keys.RegTest, other.PublicKey().CompressedBytes())
	if err != nil {
		t.Fatal(err)
	}
	account.ChannelKeys.Set(otherAddress, channelKeyCanonicalPEM)
	loaded, err = account.GetChannelPrivateKey(other.PublicKey().CompressedBytes(), fallback)
	if err != nil || loaded == nil || bytes.Equal(loaded.PrivateKeyBytes(), other.PrivateKeyBytes()) {
		t.Fatalf("mismatched mapped PEM = %v, %v", loaded, err)
	}
}

func TestChannelPEMRemainsPlaintextInEncryptedWalletSave(t *testing.T) {
	account := walletTestSeedAccount(t, bytes.NewReader(bytes.Repeat([]byte{'0'}, 64)))
	channelKey, err := keys.PrivateKeyFromPEM(keys.MainNet, channelKeyLegacyPEM)
	if err != nil {
		t.Fatal(err)
	}
	if err := account.AddChannelPrivateKey(channelKey); err != nil {
		t.Fatal(err)
	}
	wallet := NewWallet(WithWalletAccounts([]*Account{account}))
	password := "password"
	wallet.EncryptionPassword = &password
	wallet.Preferences.SetAt(EncryptOnDisk, true, 1)
	encoded, err := wallet.Save()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeOrderedJSON(encoded)
	if err != nil {
		t.Fatal(err)
	}
	document := decoded.(*Object)
	accountsValue, _ := document.Get("accounts")
	record := accountsValue.([]any)[0].(*Object)
	certificatesValue, _ := record.Get("certificates")
	certificates := certificatesValue.(*Object)
	stored, exists := certificates.Get(channelKey.Address())
	if !exists || stored != channelKeyCanonicalPEM {
		t.Fatalf("encrypted-save channel PEM = %v, %v", stored, exists)
	}
	if encrypted, _ := record.Get("encrypted"); encrypted != true {
		t.Fatalf("encrypted-save account flag = %v", encrypted)
	}
	if account.Encrypted {
		t.Fatal("transient encrypted save locked the in-memory account")
	}
}

func TestWalletAccountOwnershipIsAttachedOnce(t *testing.T) {
	account := channelKeyTestAccount(t, keys.RegTest)
	first := NewWallet(WithWalletAccounts([]*Account{account}))
	if account.wallet != first {
		t.Fatal("first wallet did not attach an unowned account")
	}
	second := NewWallet(WithWalletAccounts([]*Account{account}))
	if account.wallet != first || second.Accounts[0] != account {
		t.Fatal("second wallet silently reparented the account")
	}

	added := channelKeyTestAccount(t, keys.RegTest)
	second.AddAccount(added)
	if added.wallet != second {
		t.Fatal("AddAccount did not attach an unowned account")
	}
}

func TestAccountMigrateChannelKeysRekeysFiltersAndPersists(t *testing.T) {
	account := channelKeyTestAccount(t, keys.RegTest)
	directory := t.TempDir()
	path := filepath.Join(directory, "wallet")
	wallet := NewWallet(
		WithWalletName("Migration"),
		WithWalletAccounts([]*Account{account}),
		WithWalletStorage(NewWalletStorage(path)),
	)
	if account.wallet != wallet {
		t.Fatal("wallet construction did not attach the account owner")
	}
	account.ChannelKeys = NewObject(
		Member{Key: "not-pem", Value: "not valid key"},
		Member{Key: "wrong-address", Value: channelKeyLegacyPEM},
		Member{Key: "duplicate", Value: channelKeyLegacyPEM},
		Member{Key: "non-string", Value: 1},
		Member{Key: "leading-space", Value: " " + channelKeyLegacyPEM},
	)
	changed, err := account.MigrateChannelKeys()
	if err != nil || !changed {
		t.Fatalf("migration = changed %v, %v", changed, err)
	}
	if got, want := account.ChannelKeys.Keys(), []string{"mqs77XbdnuxWN4cXrjKbSoGLkvAHa4f4B8"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("migrated keys = %v, want %v", got, want)
	}
	value, _ := account.ChannelKeys.Get("mqs77XbdnuxWN4cXrjKbSoGLkvAHa4f4B8")
	if value != channelKeyLegacyPEM {
		t.Fatal("migration canonicalized the preserved PEM value")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "mqs77XbdnuxWN4cXrjKbSoGLkvAHa4f4B8") ||
		strings.Contains(string(contents), "wrong-address") {
		t.Fatalf("persisted migration contents = %s", contents)
	}
}

func TestAccountMigrateChannelKeysRollbackEqualityAndSaveFailure(t *testing.T) {
	account := channelKeyTestAccount(t, keys.RegTest)
	wallet := NewWallet(WithWalletAccounts([]*Account{account}))
	if account.wallet != wallet {
		t.Fatal("wallet construction did not attach the account owner")
	}
	original := NewObject(
		Member{Key: "first", Value: channelKeyLegacyPEM},
		Member{Key: "bad", Value: "-----BEGIN malformed"},
	)
	account.ChannelKeys = original
	if changed, err := account.MigrateChannelKeys(); err == nil || changed || account.ChannelKeys != original {
		t.Fatalf("parse-failure migration = changed %v error %v same %v", changed, err, account.ChannelKeys == original)
	}

	equal := NewObject(
		Member{Key: "other", Value: "discarded"},
		Member{Key: "mqs77XbdnuxWN4cXrjKbSoGLkvAHa4f4B8", Value: channelKeyLegacyPEM},
	)
	// Removing the skipped entry leaves a mapping equal to the rebuilt result.
	equal.Delete("other")
	account.ChannelKeys = equal
	account.wallet = NewWallet(WithWalletStorage(NewWalletStorage(filepath.Join(t.TempDir(), "missing", "wallet"))))
	if changed, err := account.MigrateChannelKeys(); err != nil || changed || account.ChannelKeys != equal {
		t.Fatalf("equal migration = changed %v error %v same %v", changed, err, account.ChannelKeys == equal)
	}

	account.ChannelKeys = NewObject(Member{Key: "wrong", Value: channelKeyLegacyPEM})
	changed, err := account.MigrateChannelKeys()
	if err == nil || !changed {
		t.Fatalf("save-failure migration = changed %v, %v", changed, err)
	}
	if got := account.ChannelKeys.Keys(); !reflect.DeepEqual(got, []string{"mqs77XbdnuxWN4cXrjKbSoGLkvAHa4f4B8"}) {
		t.Fatalf("save-failure mapping = %v", got)
	}
}

func channelKeyTestAccount(t *testing.T, network keys.Network) *Account {
	t.Helper()
	account, err := NewAccount(network, NewObject(
		Member{Key: "public_key", Value: channelKeyAccountPublicKey(t, network)},
		Member{Key: "modified_on", Value: 1},
	))
	if err != nil {
		t.Fatal(err)
	}
	return account
}

func channelKeyAccountPublicKey(t *testing.T, network keys.Network) string {
	t.Helper()
	privateKey, err := keys.NewPrivateKey(network, bytes.Repeat([]byte{3}, 32), make([]byte, 32), 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	return privateKey.PublicKey().ExtendedKeyString()
}
