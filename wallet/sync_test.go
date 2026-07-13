package wallet

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"errors"
	"io"
	"reflect"
	"testing"

	"lbry/daemon/wallet/keys"
)

const (
	pythonPackedEmptyWallet         = "czo4MTkyOjE2OjE6MDAwMDAwMDAwMDAwMDAwMGu7uUtX7Ntc99ROgIbG5uJ8w2udEpxWtws29WXMN0qkQ3/FMeBh83P4+mN0lGf6zpx78M3emQ2L0kPSp34tycMTxWINJXFAoEpEq9uYsR6Y"
	pythonPackedEmptyPasswordWallet = "czo4MTkyOjE2OjE6MDAwMDAwMDAwMDAwMDAwMMyPxC8/3XScfT39fteaxvopA49O/phrV7BAOJfiT/8fEahIs3CRDOcbmDmD2MTeoc3T3ugdXfp+xI1+HAcw9VsRr7l31/nCJRt+Kc6/N6kt"
)

func TestWalletUnpackAcceptsPinnedPythonZlibAndAESPayloads(t *testing.T) {
	for _, test := range []struct {
		name     string
		password string
		packed   string
	}{
		{name: "password", password: "password", packed: pythonPackedEmptyWallet},
		{name: "empty password", password: "", packed: pythonPackedEmptyPasswordWallet},
	} {
		t.Run(test.name, func(t *testing.T) {
			decoded, err := UnpackWallet(test.password, []byte(test.packed))
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := encodePreferenceJSON(decoded)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := string(encoded), `{"version": 1, "name": "Wallet", "preferences": {}, "accounts": []}`; got != want {
				t.Fatalf("unpacked JSON = %q, want %q", got, want)
			}
		})
	}
}

func TestWalletPackRoundTripUsesFreshInitializationVectors(t *testing.T) {
	entropy := append(bytes.Repeat([]byte{'a'}, 16), bytes.Repeat([]byte{'b'}, 16)...)
	wallet := NewWallet(WithWalletSyncEntropy(bytes.NewReader(entropy)))
	first, err := wallet.Pack("")
	if err != nil {
		t.Fatal(err)
	}
	second, err := wallet.Pack("")
	if err != nil {
		t.Fatal(err)
	}
	for index, packed := range [][]byte{first, second} {
		decodedEnvelope, err := base64.StdEncoding.DecodeString(string(packed))
		if err != nil {
			t.Fatal(err)
		}
		wantIV := entropy[index*16 : (index+1)*16]
		if got := decodedEnvelope[len("s:8192:16:1:") : len("s:8192:16:1:")+16]; !bytes.Equal(got, wantIV) {
			t.Fatalf("pack %d IV = %q, want %q", index, got, wantIV)
		}
		unpacked, err := UnpackWallet("", packed)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := encodePreferenceJSON(unpacked)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := string(encoded), `{"version": 1, "name": "Wallet", "preferences": {}, "accounts": []}`; got != want {
			t.Fatalf("pack %d JSON = %q, want %q", index, got, want)
		}
	}
	if bytes.Equal(first, second) {
		t.Fatal("two packs with different IVs produced identical envelopes")
	}
}

func TestWalletPackRejectsLockedWalletBeforeReadingEntropy(t *testing.T) {
	reader := &walletCountingReader{data: bytes.Repeat([]byte{'x'}, 16)}
	account := walletTestSeedAccount(t, bytes.NewReader(bytes.Repeat([]byte{'0'}, 64)))
	if err := account.Encrypt("password"); err != nil {
		t.Fatal(err)
	}
	wallet := NewWallet(
		WithWalletAccounts([]*Account{account}),
		WithWalletSyncEntropy(reader),
	)
	if _, err := wallet.Pack("password"); !errors.Is(err, ErrLockedWalletPack) ||
		err.Error() != "Cannot pack a wallet with locked/encrypted accounts." {
		t.Fatalf("locked pack error = %v, want ErrLockedWalletPack", err)
	}
	if reader.calls != 0 {
		t.Fatalf("locked pack read entropy %d times", reader.calls)
	}
	if _, _, err := wallet.Merge(nil, nil, "not JSON"); !errors.Is(err, ErrLockedWalletMerge) ||
		err.Error() != "Cannot sync apply on a locked wallet." {
		t.Fatalf("locked merge error = %v, want ErrLockedWalletMerge", err)
	}
}

func TestWalletUnpackZlibErrorClassificationAndArbitraryJSON(t *testing.T) {
	invalidHeader, err := betterAESEncrypt("password", []byte("not zlib"), bytes.NewReader(bytes.Repeat([]byte{1}, 16)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UnpackWallet("password", invalidHeader); !errors.Is(err, ErrInvalidWalletPassword) {
		t.Fatalf("zlib header error = %v, want ErrInvalidWalletPassword", err)
	}

	compressed := walletTestZlib(t, []byte(`[]`))
	compressed[len(compressed)-1] ^= 0xff
	badChecksum, err := betterAESEncrypt("password", compressed, bytes.NewReader(bytes.Repeat([]byte{2}, 16)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UnpackWallet("password", badChecksum); err == nil || errors.Is(err, ErrInvalidWalletPassword) {
		t.Fatalf("zlib checksum error = %v", err)
	}

	arbitrary, err := betterAESEncrypt("password", walletTestZlib(t, []byte(`[]`)), bytes.NewReader(bytes.Repeat([]byte{3}, 16)))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := UnpackWallet("password", arbitrary)
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := decoded.([]any); !ok || len(value) != 0 {
		t.Fatalf("arbitrary unpacked JSON = %#v", decoded)
	}
}

func TestWalletMergeClearPreferencesAddsAndDuplicateMatches(t *testing.T) {
	manager, ledger := walletTestSyncManager(t, keys.MainNet)
	local := walletTestReadOnlyAccount(t, fixedAccountXPub, "local", 10)
	ledger.addAccount(local)
	wallet := NewWallet(WithWalletAccounts([]*Account{local}))
	document := NewObject(
		Member{Key: "preferences", Value: NewObject(Member{Key: "theme", Value: NewObject(
			Member{Key: "value", Value: "dark"}, Member{Key: "ts", Value: 5},
		)})},
		Member{Key: "accounts", Value: []any{
			walletTestMergeRecord(fixedAccountXPub, keys.MainNet.ID(), "updated local", 20),
			walletTestMergeRecord(mismatchedAccountXPub, keys.MainNet.ID(), "new", 1),
			walletTestMergeRecord(mismatchedAccountXPub, keys.MainNet.ID(), "updated new", 2),
		}},
	)
	added, merged, err := wallet.Merge(manager, nil, walletTestSyncJSON(t, document))
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 || len(merged) != 2 {
		t.Fatalf("merge results = %d added, %d merged", len(added), len(merged))
	}
	if merged[0] != local || merged[1] != added[0] || wallet.Accounts[1] != added[0] {
		t.Fatal("duplicate incoming account identities were not preserved")
	}
	if local.Name != "updated local" || added[0].Name != "updated new" {
		t.Fatalf("merged names = %q and %q", local.Name, added[0].Name)
	}
	if len(wallet.Accounts) != 2 || len(ledger.Accounts) != 2 {
		t.Fatalf("registered account counts = wallet %d, ledger %d", len(wallet.Accounts), len(ledger.Accounts))
	}
	if value, exists, err := wallet.Preferences.Get("theme"); err != nil || !exists || value != "dark" {
		t.Fatalf("merged preference = %v, %v, %v", value, exists, err)
	}
}

func TestWalletMergeEncryptedEmptyPasswordAndIdentityOnlyValidation(t *testing.T) {
	manager, ledger := walletTestSyncManager(t, keys.MainNet)
	local := walletTestReadOnlyAccount(t, fixedAccountXPub, "local", 10)
	ledger.addAccount(local)
	wallet := NewWallet(WithWalletAccounts([]*Account{local}))
	remote := NewWallet(
		WithWalletAccounts([]*Account{walletTestReadOnlyAccount(t, fixedAccountXPub, "remote", 1)}),
		WithWalletSyncEntropy(bytes.NewReader(bytes.Repeat([]byte{'e'}, 16))),
	)
	remote.Accounts[0].GeneratorName = "future-generator"
	packed, err := remote.Pack("")
	if err == nil {
		t.Fatal("invalid local generator state unexpectedly serialized")
	}

	// Build the incoming object directly: keys_from_dict must match before the
	// invalid generator is considered, and the older record skips it entirely.
	document := NewObject(
		Member{Key: "accounts", Value: []any{NewObject(
			Member{Key: "ledger", Value: keys.MainNet.ID()},
			Member{Key: "public_key", Value: fixedAccountXPub},
			Member{Key: "name", Value: "ignored"},
			Member{Key: "modified_on", Value: 1},
			Member{Key: "address_generator", Value: NewObject(Member{Key: "name", Value: "future-generator"})},
			Member{Key: "certificates", Value: NewObject(Member{Key: "new", Value: "certificate"})},
		)}},
	)
	clear := walletTestSyncJSON(t, document)
	compressed := walletTestZlib(t, []byte(clear))
	packed, err = betterAESEncrypt("", compressed, bytes.NewReader(bytes.Repeat([]byte{'e'}, 16)))
	if err != nil {
		t.Fatal(err)
	}
	emptyPassword := ""
	added, merged, err := wallet.Merge(manager, &emptyPassword, string(packed))
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 0 || !reflect.DeepEqual(merged, []*Account{local}) {
		t.Fatalf("encrypted identity-only merge = added %v merged %v", added, merged)
	}
	if value, exists := local.ChannelKeys.Get("new"); !exists || value != "certificate" {
		t.Fatalf("certificate merge = %v, %v", value, exists)
	}
	if _, _, err := wallet.Merge(manager, &emptyPassword, "\u00a0"+string(packed)); !errors.Is(err, ErrInvalidSyncEnvelope) {
		t.Fatalf("non-ASCII encrypted string error = %v, want ErrInvalidSyncEnvelope", err)
	}

	fresh := NewWallet(WithWalletAccounts([]*Account{walletTestReadOnlyAccount(t, fixedAccountXPub, "fresh", 10)}))
	if _, _, err := fresh.Merge(manager, nil, string(packed)); err == nil {
		t.Fatal("nil password parsed an encrypted payload")
	}
}

func TestWalletMergeMatchesAddressAcrossLedgers(t *testing.T) {
	manager := NewWalletManager()
	testLedger, err := manager.GetOrCreateLedger(keys.TestNet.ID(), LedgerConfig{"data_path": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	regtestLedger, err := manager.GetOrCreateLedger(keys.RegTest.ID(), LedgerConfig{"data_path": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	local, err := NewAccount(keys.TestNet, NewObject(
		Member{Key: "seed", Value: accountEncryptionSeed},
		Member{Key: "modified_on", Value: 10},
	))
	if err != nil {
		t.Fatal(err)
	}
	testLedger.addAccount(local)
	wallet := NewWallet(WithWalletAccounts([]*Account{local}))
	document := NewObject(Member{Key: "accounts", Value: []any{NewObject(
		Member{Key: "ledger", Value: keys.RegTest.ID()},
		Member{Key: "public_key", Value: local.PublicKey.ExtendedKeyString()},
		Member{Key: "modified_on", Value: 1},
		Member{Key: "certificates", Value: NewObject(Member{Key: "cross", Value: true})},
	)}})
	added, merged, err := wallet.Merge(manager, nil, walletTestSyncJSON(t, document))
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 0 || len(merged) != 1 || merged[0] != local {
		t.Fatalf("cross-ledger merge = added %v merged %v", added, merged)
	}
	if len(regtestLedger.Accounts) != 0 || len(testLedger.Accounts) != 1 {
		t.Fatalf("cross-ledger registration = testnet %d regtest %d", len(testLedger.Accounts), len(regtestLedger.Accounts))
	}
}

func TestWalletMergeIdentityUsesLazyPythonKeyPrecedence(t *testing.T) {
	manager, ledger := walletTestSyncManager(t, keys.MainNet)
	local, err := NewAccount(keys.MainNet, NewObject(
		Member{Key: "seed", Value: accountEncryptionSeed},
		Member{Key: "modified_on", Value: 10},
	))
	if err != nil {
		t.Fatal(err)
	}
	ledger.addAccount(local)
	wallet := NewWallet(WithWalletAccounts([]*Account{local}))

	seedWins := NewObject(Member{Key: "accounts", Value: []any{NewObject(
		Member{Key: "ledger", Value: keys.MainNet.ID()},
		Member{Key: "seed", Value: accountEncryptionSeed},
		Member{Key: "private_key", Value: 123},
		Member{Key: "public_key", Value: "ignored and invalid"},
		Member{Key: "modified_on", Value: 1},
		Member{Key: "certificates", Value: NewObject(Member{Key: "seed_won", Value: true})},
	)}})
	added, merged, err := wallet.Merge(manager, nil, walletTestSyncJSON(t, seedWins))
	if err != nil || len(added) != 0 || len(merged) != 1 || merged[0] != local {
		t.Fatalf("seed-precedence merge = added %v merged %v err %v", added, merged, err)
	}

	// Python treats the non-empty encrypted value as true, ignores malformed
	// secrets, and accepts an xprv in public_key for identity lookup.
	xprvPublicIdentity := NewObject(Member{Key: "accounts", Value: []any{NewObject(
		Member{Key: "ledger", Value: keys.MainNet.ID()},
		Member{Key: "seed", Value: 123},
		Member{Key: "private_key", Value: 456},
		Member{Key: "encrypted", Value: "yes"},
		Member{Key: "public_key", Value: local.PrivateKey.ExtendedKeyString()},
		Member{Key: "modified_on", Value: 1},
		Member{Key: "certificates", Value: NewObject(Member{Key: "xprv_won", Value: true})},
	)}})
	added, merged, err = wallet.Merge(manager, nil, walletTestSyncJSON(t, xprvPublicIdentity))
	if err != nil || len(added) != 0 || len(merged) != 1 || merged[0] != local {
		t.Fatalf("encrypted-xprv identity merge = added %v merged %v err %v", added, merged, err)
	}
	for _, key := range []string{"seed_won", "xprv_won"} {
		if value, exists := local.ChannelKeys.Get(key); !exists || value != true {
			t.Fatalf("certificate %q = %v, %v", key, value, exists)
		}
	}
}

func TestWalletMergePreservesPreferenceAndAccountPartialMutations(t *testing.T) {
	manager, ledger := walletTestSyncManager(t, keys.MainNet)
	local := walletTestReadOnlyAccount(t, fixedAccountXPub, "before", 10)
	ledger.addAccount(local)
	wallet := NewWallet(WithWalletAccounts([]*Account{local}))

	missingAccounts := NewObject(Member{Key: "preferences", Value: NewObject(
		Member{Key: "applied", Value: NewObject(Member{Key: "value", Value: true}, Member{Key: "ts", Value: 1})},
	)})
	added, merged, err := wallet.Merge(manager, nil, walletTestSyncJSON(t, missingAccounts))
	if err == nil || added != nil || merged != nil {
		t.Fatalf("missing accounts result = added %v merged %v err %v", added, merged, err)
	}
	if value, exists, getErr := wallet.Preferences.Get("applied"); getErr != nil || !exists || value != true {
		t.Fatalf("preference partial mutation = %v, %v, %v", value, exists, getErr)
	}

	partialAccount := NewObject(Member{Key: "accounts", Value: []any{NewObject(
		Member{Key: "ledger", Value: keys.MainNet.ID()},
		Member{Key: "public_key", Value: fixedAccountXPub},
		Member{Key: "name", Value: "partially changed"},
		Member{Key: "modified_on", Value: 20},
		Member{Key: "address_generator", Value: NewObject(Member{Key: "name", Value: SingleAddressGenerator})},
	)}})
	added, merged, err = wallet.Merge(manager, nil, walletTestSyncJSON(t, partialAccount))
	if !errors.Is(err, ErrAddressGeneratorMismatch) || added != nil || merged != nil {
		t.Fatalf("partial account result = added %v merged %v err %v", added, merged, err)
	}
	if local.Name != "partially changed" || local.ModifiedOn.Int64() != 20 {
		t.Fatalf("partial account state = name %q modified %v", local.Name, local.ModifiedOn)
	}

	newRecord := walletTestMergeRecord(mismatchedAccountXPub, keys.MainNet.ID(), "added before error", 1)
	badRecord := NewObject(Member{Key: "ledger", Value: keys.MainNet.ID()}, Member{Key: "public_key", Value: "invalid"})
	earlierAdd := NewObject(Member{Key: "accounts", Value: []any{newRecord, badRecord}})
	added, merged, err = wallet.Merge(manager, nil, walletTestSyncJSON(t, earlierAdd))
	if err == nil || added != nil || merged != nil {
		t.Fatalf("later failure result = added %v merged %v err %v", added, merged, err)
	}
	if len(wallet.Accounts) != 2 || len(ledger.Accounts) != 2 || wallet.Accounts[1].Name != "added before error" {
		t.Fatalf("earlier add did not persist: wallet %d ledger %d", len(wallet.Accounts), len(ledger.Accounts))
	}
}

type walletCountingReader struct {
	data  []byte
	calls int
}

func (reader *walletCountingReader) Read(destination []byte) (int, error) {
	reader.calls++
	if len(reader.data) == 0 {
		return 0, io.EOF
	}
	count := copy(destination, reader.data)
	reader.data = reader.data[count:]
	return count, nil
}

func walletTestSyncManager(t *testing.T, network keys.Network) (*WalletManager, *Ledger) {
	t.Helper()
	manager := NewWalletManager()
	ledger, err := manager.GetOrCreateLedger(network.ID(), LedgerConfig{"data_path": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return manager, ledger
}

func walletTestMergeRecord(publicKey, ledgerID, name string, modifiedOn int) *Object {
	return NewObject(
		Member{Key: "ledger", Value: ledgerID},
		Member{Key: "public_key", Value: publicKey},
		Member{Key: "name", Value: name},
		Member{Key: "modified_on", Value: modifiedOn},
		Member{Key: "address_generator", Value: NewObject(Member{Key: "name", Value: DeterministicChainGenerator})},
	)
}

func walletTestSyncJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := encodePreferenceJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func walletTestZlib(t *testing.T, value []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(value); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}
