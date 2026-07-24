package wallet

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"reflect"
	"testing"
	"time"

	"lbry/daemon/wallet/keys"
)

const (
	fixedAccountXPub = "xpub661MyMwAqRbcGWtPvbWh9sc2BCfw2cTeVDYF23o3N1t6UZ5wv3EMmDgp66FxH" +
		"uDtWdft3B5eL5xQtyzAtkdmhhC95gjRjLzSTdkho95asu9"
	mismatchedAccountXPub = "xpub661MyMwAqRbcFwwe67Bfjd53h5WXmKm6tqfBJZZH3pQLoy8Nb6mKUMJFc7" +
		"UbpVNzmwFPN2evn3YHnig1pkKVYcvCV8owTd2yAcEkJfCX53g"
	fixedReceiveZeroAccountXPrv = "xprv9vwXVierUTT4hmoe3dtTeBfbNv1ph2mm8RWXARU6HsZjBaAoFaS2FRQu4fptR" +
		"AyJWhJW42dmsEaC1nKnVKKTMhq3TVEHsNj1ca3ciZMKktT"
)

func TestNewAccountSeedPrecedenceDefaultsAndOrderedSerialization(t *testing.T) {
	clock := func() time.Time { return time.Unix(1_700_000_000, 900_000_000) }
	account, err := NewAccount(keys.MainNet, NewObject(
		Member{Key: "seed", Value: accountEncryptionSeed},
		Member{Key: "private_key", Value: "ignored invalid private key"},
		Member{Key: "public_key", Value: "ignored invalid public key"},
	), WithAccountClock(clock))
	if err != nil {
		t.Fatal(err)
	}
	if account.ID != "bbmkLZJvGdu6WFaRCZjZBgvagbRWjr5Xew" {
		t.Fatalf("ID = %q", account.ID)
	}
	if account.Name != "Account #"+account.ID {
		t.Fatalf("default name = %q", account.Name)
	}
	if account.ModifiedOn.String() != "1700000000" {
		t.Fatalf("modified_on = %s", account.ModifiedOn)
	}
	if account.PrivateKey == nil || account.PrivateKey.ExtendedKeyString() != accountEncryptionXPrv {
		t.Fatalf("private key = %v", account.PrivateKey)
	}
	if account.PublicKey.ExtendedKeyString() != fixedAccountXPub {
		t.Fatalf("public key = %q", account.PublicKey.ExtendedKeyString())
	}
	if account.GeneratorName != DeterministicChainGenerator || account.Receiving == account.Change {
		t.Fatalf("default managers = %q, %p, %p", account.GeneratorName, account.Receiving, account.Change)
	}
	if account.Receiving.Gap != 20 || account.Receiving.MaximumUsesPerAddress != 1 ||
		account.Change.Gap != 6 || account.Change.MaximumUsesPerAddress != 1 {
		t.Fatalf("default gaps = receive(%v,%v), change(%v,%v)",
			account.Receiving.Gap, account.Receiving.MaximumUsesPerAddress,
			account.Change.Gap, account.Change.MaximumUsesPerAddress)
	}

	object, err := account.ToObject()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{
		"ledger", "name", "seed", "encrypted", "private_key", "public_key",
		"address_generator", "modified_on", "certificates",
	}; !reflect.DeepEqual(object.Keys(), want) {
		t.Fatalf("account keys = %v, want %v", object.Keys(), want)
	}
	generator := mustAccountObject(t, object, "address_generator")
	if want := []string{"name", "receiving", "change"}; !reflect.DeepEqual(generator.Keys(), want) {
		t.Fatalf("generator keys = %v, want %v", generator.Keys(), want)
	}
	for _, chainName := range []string{"receiving", "change"} {
		chain := mustAccountObject(t, generator, chainName)
		if want := []string{"gap", "maximum_uses_per_address"}; !reflect.DeepEqual(chain.Keys(), want) {
			t.Fatalf("%s keys = %v, want %v", chainName, chain.Keys(), want)
		}
	}
	privateValue, _ := object.Get("private_key")
	publicValue, _ := object.Get("public_key")
	if privateValue != accountEncryptionXPrv || publicValue != fixedAccountXPub {
		t.Fatalf("serialized key precedence = (%q, %q)", privateValue, publicValue)
	}
}

func TestNewAccountPrivateEncryptedAndReadOnlyKeyPrecedence(t *testing.T) {
	privateAccount, err := NewAccount(keys.MainNet, NewObject(
		Member{Key: "private_key", Value: accountEncryptionXPrv},
		Member{Key: "public_key", Value: "ignored invalid public key"},
		Member{Key: "modified_on", Value: 1},
	))
	if err != nil {
		t.Fatal(err)
	}
	if privateAccount.PublicKey.ExtendedKeyString() != fixedAccountXPub {
		t.Fatalf("private-key precedence public key = %q", privateAccount.PublicKey.ExtendedKeyString())
	}

	encryptedAccount, err := NewAccount(keys.MainNet, NewObject(
		Member{Key: "seed", Value: "opaque seed ciphertext"},
		Member{Key: "private_key", Value: "opaque private ciphertext"},
		Member{Key: "encrypted", Value: true},
		Member{Key: "public_key", Value: mismatchedAccountXPub},
		Member{Key: "modified_on", Value: 2},
	))
	if err != nil {
		t.Fatal(err)
	}
	if encryptedAccount.PrivateKey != nil || encryptedAccount.PublicKey.ExtendedKeyString() != mismatchedAccountXPub {
		t.Fatalf("encrypted keys = private %v, public %q", encryptedAccount.PrivateKey, encryptedAccount.PublicKey.ExtendedKeyString())
	}

	readOnly, err := NewAccount(keys.MainNet, NewObject(
		Member{Key: "public_key", Value: fixedAccountXPub},
		Member{Key: "modified_on", Value: json.Number("123456789012345678901234567890")},
	))
	if err != nil {
		t.Fatal(err)
	}
	if readOnly.Seed != "" || readOnly.PrivateKeyString != "" || readOnly.PrivateKey != nil {
		t.Fatalf("read-only secrets = %q, %q, %v", readOnly.Seed, readOnly.PrivateKeyString, readOnly.PrivateKey)
	}
	if readOnly.ModifiedOn.String() != "123456789012345678901234567890" {
		t.Fatalf("arbitrary modified_on = %s", readOnly.ModifiedOn)
	}
}

func TestAccountAddressGeneratorRecordsAndKeySelection(t *testing.T) {
	deterministic, err := NewAccount(keys.MainNet, NewObject(
		Member{Key: "seed", Value: accountEncryptionSeed},
		Member{Key: "modified_on", Value: 123},
		Member{Key: "address_generator", Value: NewObject(
			Member{Key: "name", Value: DeterministicChainGenerator},
			Member{Key: "receiving", Value: NewObject(
				Member{Key: "gap", Value: 17},
				Member{Key: "maximum_uses_per_address", Value: 2},
			)},
			Member{Key: "change", Value: NewObject(
				Member{Key: "gap", Value: 10},
				Member{Key: "maximum_uses_per_address", Value: 3},
			)},
		)},
	))
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := deterministic.Receiving.GetPrivateKey(0)
	if err != nil {
		t.Fatal(err)
	}
	if privateKey.ExtendedKeyString() != fixedReceiveZeroAccountXPrv {
		t.Fatalf("receive private key = %q", privateKey.ExtendedKeyString())
	}
	publicKey, err := deterministic.Receiving.GetPublicKey(0)
	if err != nil {
		t.Fatal(err)
	}
	if publicKey.Address() != "bCqJrLHdoiRqEZ1whFZ3WHNb33bP34SuGx" {
		t.Fatalf("receive address = %q", publicKey.Address())
	}

	single, err := NewAccount(keys.MainNet, NewObject(
		Member{Key: "seed", Value: accountEncryptionSeed},
		Member{Key: "modified_on", Value: 123},
		Member{Key: "address_generator", Value: NewObject(
			Member{Key: "name", Value: SingleAddressGenerator},
		)},
	))
	if err != nil {
		t.Fatal(err)
	}
	if single.Receiving != single.Change {
		t.Fatal("single-address receiving and change managers do not share identity")
	}
	gotPublic, err := single.Change.GetPublicKey(-123)
	if err != nil || gotPublic != single.PublicKey {
		t.Fatalf("single public key = %p, %v", gotPublic, err)
	}
	gotPrivate, err := single.Change.GetPrivateKey(1 << 40)
	if err != nil || gotPrivate != single.PrivateKey {
		t.Fatalf("single private key = %p, %v", gotPrivate, err)
	}
	object, err := single.ToObject()
	if err != nil {
		t.Fatal(err)
	}
	if generator := mustAccountObject(t, object, "address_generator"); !reflect.DeepEqual(generator.Keys(), []string{"name"}) {
		t.Fatalf("single generator keys = %v", generator.Keys())
	}
}

func TestAccountDeterministicGeneratorUsesWholeRecordDefaults(t *testing.T) {
	base := func(generator *Object) *Object {
		return NewObject(
			Member{Key: "public_key", Value: fixedAccountXPub},
			Member{Key: "modified_on", Value: 1},
			Member{Key: "address_generator", Value: generator},
		)
	}
	account, err := NewAccount(keys.MainNet, base(NewObject(
		Member{Key: "name", Value: DeterministicChainGenerator},
	)))
	if err != nil {
		t.Fatal(err)
	}
	if account.Receiving.Gap != 20 || account.Receiving.MaximumUsesPerAddress != 1 {
		t.Fatalf("whole-record defaults = %v, %v", account.Receiving.Gap, account.Receiving.MaximumUsesPerAddress)
	}

	for _, invalid := range []*Object{
		NewObject(Member{Key: "gap", Value: 20}),
		NewObject(
			Member{Key: "gap", Value: 20},
			Member{Key: "maximum_uses_per_address", Value: 1},
			Member{Key: "extra", Value: true},
		),
	} {
		_, err := NewAccount(keys.MainNet, base(NewObject(
			Member{Key: "name", Value: DeterministicChainGenerator},
			Member{Key: "receiving", Value: invalid},
		)))
		if !errors.Is(err, ErrInvalidAccountData) {
			t.Fatalf("partial/extra record error = %v", err)
		}
	}
}

func TestAccountMergePreservesTimestampAndPartialMutationSemantics(t *testing.T) {
	certificates := NewObject(
		Member{Key: "existing", Value: "old"},
		Member{Key: "stable", Value: "value"},
	)
	account, err := NewAccount(keys.MainNet, NewObject(
		Member{Key: "name", Value: "Original"},
		Member{Key: "seed", Value: accountEncryptionSeed},
		Member{Key: "modified_on", Value: 123},
		Member{Key: "certificates", Value: certificates},
		Member{Key: "address_generator", Value: deterministicGenerator(5, 2, 5, 2)},
	))
	if err != nil {
		t.Fatal(err)
	}
	incomingCertificates := NewObject(
		Member{Key: "existing", Value: "new"},
		Member{Key: "added", Value: "certificate"},
	)
	err = account.Merge(NewObject(
		Member{Key: "name", Value: "Changed"},
		Member{Key: "modified_on", Value: 123.75},
		Member{Key: "address_generator", Value: deterministicGenerator(8, 9, 6, 7)},
		Member{Key: "certificates", Value: incomingCertificates},
	))
	if err != nil {
		t.Fatal(err)
	}
	if account.Name != "Changed" || account.ModifiedOn.String() != "123" {
		t.Fatalf("float timestamp merge = %q, %s", account.Name, account.ModifiedOn)
	}
	if account.Change.Gap != 6 || account.Change.MaximumUsesPerAddress != 7 ||
		account.Receiving.Gap != 8 || account.Receiving.MaximumUsesPerAddress != 9 {
		t.Fatalf("merged generator = receive(%v,%v), change(%v,%v)",
			account.Receiving.Gap, account.Receiving.MaximumUsesPerAddress,
			account.Change.Gap, account.Change.MaximumUsesPerAddress)
	}
	if want := []string{"existing", "stable", "added"}; !reflect.DeepEqual(account.ChannelKeys.Keys(), want) {
		t.Fatalf("certificate order = %v, want %v", account.ChannelKeys.Keys(), want)
	}
	if value, _ := certificates.Get("existing"); value != "new" {
		t.Fatalf("constructor certificate alias was not updated: %v", value)
	}

	// Comparison happens before int(): another fractional value is newer than
	// the stored integer even though it truncates back to the same integer.
	err = account.Merge(NewObject(
		Member{Key: "name", Value: "Changed Again"},
		Member{Key: "modified_on", Value: 123.25},
		Member{Key: "address_generator", Value: deterministicGenerator(10, 11, 12, 13)},
	))
	if err != nil || account.Name != "Changed Again" || account.ModifiedOn.String() != "123" {
		t.Fatalf("second fractional merge = %q, %s, %v", account.Name, account.ModifiedOn, err)
	}

	// Generator assertion happens after name and timestamp assignments, and
	// certificate update is skipped when it fails.
	err = account.Merge(NewObject(
		Member{Key: "name", Value: "Partially Applied"},
		Member{Key: "modified_on", Value: 200},
		Member{Key: "address_generator", Value: NewObject(Member{Key: "name", Value: SingleAddressGenerator})},
		Member{Key: "certificates", Value: NewObject(Member{Key: "not-added", Value: true})},
	))
	if !errors.Is(err, ErrAddressGeneratorMismatch) {
		t.Fatalf("generator mismatch error = %v", err)
	}
	if account.Name != "Partially Applied" || account.ModifiedOn.String() != "200" {
		t.Fatalf("partial mutation = %q, %s", account.Name, account.ModifiedOn)
	}
	if _, exists := account.ChannelKeys.Get("not-added"); exists {
		t.Fatal("certificates merged after generator mismatch")
	}

	// Certificates merge regardless of an older modified_on value.
	err = account.Merge(NewObject(
		Member{Key: "modified_on", Value: 1},
		Member{Key: "certificates", Value: NewObject(Member{Key: "old-record-cert", Value: true})},
	))
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := account.ChannelKeys.Get("old-record-cert"); !exists {
		t.Fatal("older record certificate was not merged")
	}
}

func TestAccountMergeMissingModifiedOnUsesClockAfterZeroComparison(t *testing.T) {
	clock := func() time.Time { return time.Unix(999, 800_000_000) }
	account, err := NewAccount(keys.MainNet, NewObject(
		Member{Key: "name", Value: "Negative"},
		Member{Key: "public_key", Value: fixedAccountXPub},
		Member{Key: "modified_on", Value: -1},
	), WithAccountClock(clock))
	if err != nil {
		t.Fatal(err)
	}
	err = account.Merge(NewObject(
		Member{Key: "name", Value: "Clocked"},
		Member{Key: "address_generator", Value: deterministicGenerator(20, 1, 6, 1)},
	))
	if err != nil || account.Name != "Clocked" || account.ModifiedOn.String() != "999" {
		t.Fatalf("missing modified_on merge = %q, %s, %v", account.Name, account.ModifiedOn, err)
	}
}

func TestAccountHashMatchesPinnedJSONAndCertificateKeyRules(t *testing.T) {
	certificates := NewObject(
		Member{Key: "zeta", Value: "ignored"},
		Member{Key: "alpha", Value: "value"},
	)
	account, err := NewAccount(keys.MainNet, NewObject(
		Member{Key: "name", Value: "My Account"},
		Member{Key: "seed", Value: accountEncryptionSeed},
		Member{Key: "modified_on", Value: 123},
		Member{Key: "address_generator", Value: NewObject(Member{Key: "name", Value: SingleAddressGenerator})},
		Member{Key: "certificates", Value: certificates},
	))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := account.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(digest[:]); got != "8e5c1710623e0f8007b6d1ebbd7432152606f5532ea999b2f65e29cd7e219aca" {
		t.Fatalf("account hash = %s", got)
	}
	certificates.Set("zeta", NewObject(Member{Key: "completely", Value: "different"}))
	certificates.Set("alpha", nil)
	sameDigest, err := account.Hash()
	if err != nil || sameDigest != digest {
		t.Fatalf("hash changed with certificate values: %x, %v", sameDigest, err)
	}
	certificates.Set("beta", "new key")
	changedDigest, err := account.Hash()
	if err != nil || changedDigest == digest {
		t.Fatalf("hash did not change with certificate key: %x, %v", changedDigest, err)
	}
}

func TestAccountEncryptionStateAndCachedIVMatchPinnedVectors(t *testing.T) {
	account, err := NewAccount(keys.MainNet, NewObject(
		Member{Key: "name", Value: "My Account"},
		Member{Key: "seed", Value: accountEncryptionSeed},
		Member{Key: "private_key", Value: accountEncryptionXPrv},
		Member{Key: "public_key", Value: mismatchedAccountXPub},
		Member{Key: "modified_on", Value: 123},
		Member{Key: "address_generator", Value: NewObject(Member{Key: "name", Value: SingleAddressGenerator})},
	))
	if err != nil {
		t.Fatal(err)
	}
	initializationVector := []byte("0000000000000000")
	for _, key := range []string{"seed", "private_key"} {
		if err := account.SetInitializationVector(key, initializationVector); err != nil {
			t.Fatal(err)
		}
	}

	transient, err := account.ToDict("password", true)
	if err != nil {
		t.Fatal(err)
	}
	assertAccountObjectValue(t, transient, "seed", encryptedAccountSeed)
	assertAccountObjectValue(t, transient, "private_key", encryptedAccountXPrv)
	assertAccountObjectValue(t, transient, "encrypted", true)
	if account.Encrypted || account.Seed != accountEncryptionSeed || account.PrivateKey == nil {
		t.Fatalf("transient encryption mutated state: encrypted=%v seed=%q private=%v", account.Encrypted, account.Seed, account.PrivateKey)
	}
	repeated, err := account.ToDict("password", true)
	if err != nil {
		t.Fatal(err)
	}
	assertAccountObjectValue(t, repeated, "seed", encryptedAccountSeed)
	assertAccountObjectValue(t, repeated, "private_key", encryptedAccountXPrv)

	if err := account.Encrypt("password"); err != nil {
		t.Fatal(err)
	}
	if !account.Encrypted || account.Seed != encryptedAccountSeed || account.PrivateKeyString != encryptedAccountXPrv || account.PrivateKey != nil {
		t.Fatalf("encrypted state = %v, %q, %q, %v", account.Encrypted, account.Seed, account.PrivateKeyString, account.PrivateKey)
	}
	if _, err := account.Hash(); !errors.Is(err, ErrEncryptedAccountHash) {
		t.Fatalf("encrypted hash error = %v", err)
	}
	if err := account.Encrypt("password"); !errors.Is(err, ErrAccountAlreadyEncrypted) {
		t.Fatalf("double encryption error = %v", err)
	}

	ok, err := account.Decrypt("password")
	if err != nil || !ok {
		t.Fatalf("Decrypt = %v, %v", ok, err)
	}
	if account.Encrypted || account.Seed != accountEncryptionSeed || account.PrivateKey == nil ||
		account.PrivateKey.ExtendedKeyString() != accountEncryptionXPrv || account.PrivateKeyString != "" {
		t.Fatalf("decrypted state = %v, %q, %v, %q", account.Encrypted, account.Seed, account.PrivateKey, account.PrivateKeyString)
	}
	for _, key := range []string{"seed", "private_key"} {
		got, exists := account.InitializationVector(key)
		if !exists || !bytes.Equal(got, initializationVector) {
			t.Fatalf("%s IV = %q, %v", key, got, exists)
		}
		got[0] = 'x'
		again, _ := account.InitializationVector(key)
		if !bytes.Equal(again, initializationVector) {
			t.Fatalf("%s IV getter exposed mutable cache: %q", key, again)
		}
	}
	if ok, err := account.Decrypt("password"); ok || !errors.Is(err, ErrAccountNotEncrypted) {
		t.Fatalf("unlocked Decrypt = %v, %v", ok, err)
	}
}

func TestAccountDecryptFailureAndReadOnlyRoundTrip(t *testing.T) {
	encrypted, err := NewAccount(keys.MainNet, NewObject(
		Member{Key: "seed", Value: encryptedAccountSeed},
		Member{Key: "private_key", Value: encryptedAccountXPrv},
		Member{Key: "encrypted", Value: true},
		Member{Key: "public_key", Value: mismatchedAccountXPub},
		Member{Key: "modified_on", Value: 1},
		Member{Key: "address_generator", Value: NewObject(Member{Key: "name", Value: SingleAddressGenerator})},
	))
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := encrypted.Decrypt("wrong"); err != nil || ok {
		t.Fatalf("wrong-password Decrypt = %v, %v", ok, err)
	}
	if !encrypted.Encrypted || encrypted.Seed != encryptedAccountSeed || encrypted.PrivateKeyString != encryptedAccountXPrv {
		t.Fatalf("failed decrypt mutated secrets: %v, %q, %q", encrypted.Encrypted, encrypted.Seed, encrypted.PrivateKeyString)
	}

	readOnly, err := NewAccount(keys.MainNet, NewObject(
		Member{Key: "public_key", Value: fixedAccountXPub},
		Member{Key: "modified_on", Value: 1},
		Member{Key: "address_generator", Value: NewObject(Member{Key: "name", Value: SingleAddressGenerator})},
	))
	if err != nil {
		t.Fatal(err)
	}
	if err := readOnly.Encrypt(""); err != nil {
		t.Fatal(err)
	}
	if !readOnly.Encrypted || readOnly.Seed != "" || readOnly.PrivateKeyString != "" {
		t.Fatalf("encrypted read-only state = %v, %q, %q", readOnly.Encrypted, readOnly.Seed, readOnly.PrivateKeyString)
	}
	if ok, err := readOnly.Decrypt(""); err != nil || !ok {
		t.Fatalf("read-only Decrypt = %v, %v", ok, err)
	}
	if readOnly.PrivateKey != nil || readOnly.PublicKey.ExtendedKeyString() != fixedAccountXPub {
		t.Fatalf("read-only decrypted keys = %v, %q", readOnly.PrivateKey, readOnly.PublicKey.ExtendedKeyString())
	}
	privateKey, err := readOnly.Receiving.GetPrivateKey(99)
	if err != nil || privateKey != nil {
		t.Fatalf("read-only single-address private key = %v, %v", privateKey, err)
	}
}

func TestAccountToDictCertificateSwitchAndPythonJSONSpacing(t *testing.T) {
	account, err := NewAccount(keys.MainNet, NewObject(
		Member{Key: "name", Value: "My Account"},
		Member{Key: "seed", Value: accountEncryptionSeed},
		Member{Key: "modified_on", Value: 123},
		Member{Key: "address_generator", Value: NewObject(Member{Key: "name", Value: SingleAddressGenerator})},
		Member{Key: "certificates", Value: NewObject(Member{Key: "key", Value: "value"})},
	))
	if err != nil {
		t.Fatal(err)
	}
	withoutCertificates, err := account.ToDict("", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := withoutCertificates.Get("certificates"); exists {
		t.Fatal("certificates included when disabled")
	}
	encoded, err := encodePreferenceJSON(withoutCertificates)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"ledger": "lbc_mainnet", "name": "My Account", "seed": "carbon smart garage balance margin twelve chest sword toast envelope bottom stomach absent", "encrypted": false, "private_key": "xprv9s21ZrQH143K42ovpZygnjfHdAqSd9jo7zceDfPRogM7bkkoNVv7DRNLEoB8HoirMgH969NrgL8jNzLEegqFzPRWM37GXd4uE8uuRkx4LAe", "public_key": "xpub661MyMwAqRbcGWtPvbWh9sc2BCfw2cTeVDYF23o3N1t6UZ5wv3EMmDgp66FxHuDtWdft3B5eL5xQtyzAtkdmhhC95gjRjLzSTdkho95asu9", "address_generator": {"name": "single-address"}, "modified_on": 123}`
	if string(encoded) != want {
		t.Fatalf("compact account JSON = %s\nwant = %s", encoded, want)
	}
}

func deterministicGenerator(receiveGap, receiveUses, changeGap, changeUses any) *Object {
	return NewObject(
		Member{Key: "name", Value: DeterministicChainGenerator},
		Member{Key: "receiving", Value: NewObject(
			Member{Key: "gap", Value: receiveGap},
			Member{Key: "maximum_uses_per_address", Value: receiveUses},
		)},
		Member{Key: "change", Value: NewObject(
			Member{Key: "gap", Value: changeGap},
			Member{Key: "maximum_uses_per_address", Value: changeUses},
		)},
	)
}

func mustAccountObject(t *testing.T, object *Object, key string) *Object {
	t.Helper()
	value, exists := object.Get(key)
	if !exists {
		t.Fatalf("%q is missing", key)
	}
	nested, ok := value.(*Object)
	if !ok {
		t.Fatalf("%q = %T, want *Object", key, value)
	}
	return nested
}

func assertAccountObjectValue(t *testing.T, object *Object, key string, want any) {
	t.Helper()
	got, exists := object.Get(key)
	if !exists || !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %T(%v), want %T(%v)", key, got, got, want, want)
	}
}

func TestAccountPythonIntCompatibility(t *testing.T) {
	tests := []struct {
		value any
		want  string
	}{
		{json.Number("1e20"), "100000000000000000000"},
		{123.9, "123"},
		{-123.9, "-123"},
		{"  +1_234  ", "1234"},
		{true, "1"},
		{new(big.Int).Lsh(big.NewInt(1), 200), "1606938044258990275541962092341162602522202993782792835301376"},
	}
	for _, test := range tests {
		got, err := accountPythonInt(test.value)
		if err != nil || got.String() != test.want {
			t.Errorf("accountPythonInt(%v) = %v, %v; want %s", test.value, got, err, test.want)
		}
	}
}
