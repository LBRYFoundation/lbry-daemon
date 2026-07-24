package wallet

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"lbry/daemon/wallet/keys"
)

type walletSyncOracleMergeFixture struct {
	name     string
	document *Object
	password *string
	payload  map[string]any
}

func TestWalletSyncMatchesPinnedPythonOracle(t *testing.T) {
	mainLedger := NewObject(Member{Key: keys.MainNet.ID(), Value: NewObject(
		Member{Key: "data_path", Value: "/tmp/wallet-sync-oracle"},
	)})
	emptyDocument := walletSyncOracleDocument("Wallet")
	unicodeDocument := walletSyncOracleDocument(
		"Zażółć gęślą jaźń",
		Member{Key: "preferences", Value: NewObject(Member{Key: "język", Value: NewObject(
			Member{Key: "value", Value: "polski"}, Member{Key: "ts", Value: 7},
		)})},
	)
	oneAccountDocument := walletSyncOracleDocument(
		"One Account",
		Member{Key: "accounts", Value: []any{walletTestMergeRecord(
			fixedAccountXPub, keys.MainNet.ID(), "Read Only", 11,
		)}},
	)

	goPackCases := []struct {
		name     string
		password string
		wallet   *Wallet
	}{
		{
			name: "Go empty", password: "go password",
			wallet: NewWallet(WithWalletSyncEntropy(bytes.NewReader(bytes.Repeat([]byte{'g'}, 16)))),
		},
		{
			name: "Go Unicode", password: "pässwörd 🔑",
			wallet: NewWallet(
				WithWalletName("Zażółć gęślą jaźń"),
				WithWalletPreferences(NewObject(Member{Key: "język", Value: NewObject(
					Member{Key: "value", Value: "polski"}, Member{Key: "ts", Value: 7},
				)})),
				WithWalletSyncEntropy(bytes.NewReader(bytes.Repeat([]byte{'u'}, 16))),
			),
		},
	}
	goPacked := make([][]byte, len(goPackCases))
	goUnpacked := make([]any, len(goPackCases))
	unpackCases := make([]any, len(goPackCases))
	for index, fixture := range goPackCases {
		packed, err := fixture.wallet.Pack(fixture.password)
		if err != nil {
			t.Fatal(err)
		}
		goPacked[index] = packed
		goUnpacked[index], err = UnpackWallet(fixture.password, packed)
		if err != nil {
			t.Fatal(err)
		}
		unpackCases[index] = map[string]any{
			"name": fixture.name, "password": fixture.password, "encrypted": string(packed),
		}
	}

	localDocument := walletSyncOracleDocument(
		"Local",
		Member{Key: "accounts", Value: []any{walletTestMergeRecord(
			fixedAccountXPub, keys.MainNet.ID(), "local", 10,
		)}},
	)
	basicIncoming := walletSyncOracleDocument(
		"Remote",
		Member{Key: "preferences", Value: NewObject(Member{Key: "theme", Value: NewObject(
			Member{Key: "value", Value: "dark"}, Member{Key: "ts", Value: 8},
		)})},
		Member{Key: "accounts", Value: []any{
			walletTestMergeRecord(fixedAccountXPub, keys.MainNet.ID(), "updated local", 20),
			walletTestMergeRecord(mismatchedAccountXPub, keys.MainNet.ID(), "new", 1),
			walletTestMergeRecord(mismatchedAccountXPub, keys.MainNet.ID(), "updated new", 2),
		}},
	)
	emptyPassword := ""
	lazyLocalDocument := walletSyncOracleDocument(
		"Lazy Keys",
		Member{Key: "accounts", Value: []any{NewObject(
			Member{Key: "ledger", Value: keys.MainNet.ID()},
			Member{Key: "seed", Value: accountEncryptionSeed},
			Member{Key: "modified_on", Value: 10},
		)}},
	)
	mergeFixtures := []walletSyncOracleMergeFixture{
		{
			name: "clear duplicate merge", document: localDocument,
			payload: map[string]any{
				"name": "clear duplicate merge", "document": localDocument,
				"ledgers": mainLedger, "incoming": basicIncoming, "encoding": "json",
			},
		},
		{
			name: "encrypted empty password", document: localDocument, password: &emptyPassword,
			payload: map[string]any{
				"name": "encrypted empty password", "document": localDocument,
				"ledgers": mainLedger, "incoming": basicIncoming, "encoding": "pack",
				"password": "", "urandom": []string{strings.Repeat("65", 16)},
			},
		},
		{
			name: "preference before missing accounts", document: localDocument,
			payload: map[string]any{
				"name": "preference before missing accounts", "document": localDocument,
				"ledgers": mainLedger, "incoming": NewObject(Member{Key: "preferences", Value: NewObject(
					Member{Key: "raw", Value: "stored without timestamp validation"},
					Member{Key: "partial", Value: NewObject(Member{Key: "value", Value: true}, Member{Key: "ts", Value: 9})},
				)}), "encoding": "json",
			},
		},
		{
			name: "account partial mutation", document: localDocument,
			payload: map[string]any{
				"name": "account partial mutation", "document": localDocument,
				"ledgers": mainLedger, "incoming": NewObject(Member{Key: "accounts", Value: []any{NewObject(
					Member{Key: "ledger", Value: keys.MainNet.ID()}, Member{Key: "public_key", Value: fixedAccountXPub},
					Member{Key: "name", Value: "partially changed"}, Member{Key: "modified_on", Value: 30},
					Member{Key: "address_generator", Value: NewObject(Member{Key: "name", Value: SingleAddressGenerator})},
				)}}), "encoding": "json",
			},
		},
		{
			name: "lazy seed key precedence", document: lazyLocalDocument,
			payload: map[string]any{
				"name": "lazy seed key precedence", "document": lazyLocalDocument,
				"ledgers": mainLedger, "incoming": NewObject(Member{Key: "accounts", Value: []any{
					NewObject(
						Member{Key: "ledger", Value: keys.MainNet.ID()}, Member{Key: "seed", Value: accountEncryptionSeed},
						Member{Key: "private_key", Value: 123}, Member{Key: "public_key", Value: "ignored and invalid"},
						Member{Key: "modified_on", Value: 1},
						Member{Key: "certificates", Value: NewObject(Member{Key: "lazy", Value: true})},
					),
					NewObject(
						Member{Key: "ledger", Value: keys.MainNet.ID()}, Member{Key: "seed", Value: 123},
						Member{Key: "private_key", Value: 456}, Member{Key: "encrypted", Value: "yes"},
						Member{Key: "public_key", Value: accountEncryptionXPrv}, Member{Key: "modified_on", Value: 1},
						Member{Key: "certificates", Value: NewObject(Member{Key: "xprv", Value: true})},
					),
				}}), "encoding": "json",
			},
		},
	}
	mergeCases := make([]any, len(mergeFixtures))
	for index := range mergeFixtures {
		mergeCases[index] = mergeFixtures[index].payload
	}

	payload := map[string]any{
		"better_aes_cases": []any{
			map[string]any{
				"name": "fixed encryption", "operation": "encrypt", "password": "super secret",
				"value_base64": base64.StdEncoding.EncodeToString([]byte("valuable value")),
				"urandom":      []string{strings.Repeat("64", 16)},
			},
			map[string]any{
				"name": "pinned decrypt", "operation": "decrypt", "password": "super secret",
				"encrypted": "czo4MTkyOjE2OjE6VrwsN8FSJlegxHVEQePoyjWT1k8yAXBCUbbGCFKcsNY=",
			},
		},
		"pack_cases": []any{
			map[string]any{
				"name": "Python empty", "document": emptyDocument, "ledgers": mainLedger,
				"password": "password", "urandom": []string{strings.Repeat("30", 16)},
			},
			map[string]any{
				"name": "Python Unicode", "document": unicodeDocument, "ledgers": mainLedger,
				"password": "pässwörd 🔑", "urandom": []string{strings.Repeat("75", 16)},
			},
			map[string]any{
				"name": "Python account", "document": oneAccountDocument, "ledgers": mainLedger,
				"password": "", "urandom": []string{strings.Repeat("61", 16)},
			},
		},
		"unpack_cases": unpackCases,
		"merge_cases":  mergeCases,
	}
	oracle := runWalletSyncOracle(t, payload)
	reference := oracle["reference"].(map[string]any)
	if got := reference["commit"]; got != accountOraclePinnedCommit {
		t.Fatalf("sync oracle commit = %v, want %s", got, accountOraclePinnedCommit)
	}
	if got := reference["version"]; got != accountOraclePinnedVersion {
		t.Fatalf("sync oracle version = %v, want %s", got, accountOraclePinnedVersion)
	}
	metadata := oracle["metadata"].(map[string]any)
	if metadata["python_debug"] != true {
		t.Fatal("wallet sync oracle ran with Python assertions disabled")
	}
	if want := os.Getenv("LBRY_ORACLE_PYTHON_VERSION"); want != "" && metadata["python_version"] != want {
		t.Fatalf("oracle Python version = %v, want %s", metadata["python_version"], want)
	}

	betterCases := oracle["better_aes_cases"].([]any)
	fixedEncryption := betterCases[0].(map[string]any)
	goFixed, err := betterAESEncrypt(
		"super secret", []byte("valuable value"), bytes.NewReader(bytes.Repeat([]byte{'d'}, 16)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fixedEncryption["encrypted"], string(goFixed); got != want {
		t.Fatalf("fixed better-AES differs: Python %v, Go %s", got, want)
	}
	pinnedDecrypt := betterCases[1].(map[string]any)
	if got, want := pinnedDecrypt["value_base64"], base64.StdEncoding.EncodeToString([]byte("valuable value")); got != want {
		t.Fatalf("pinned decrypt = %v, want %s", got, want)
	}

	for _, value := range oracle["pack_cases"].([]any) {
		result := value.(map[string]any)
		if result["error_type"] != nil || result["unpack_error_type"] != nil {
			t.Fatalf("Python pack %q failed: %v / %v", result["name"], result["error"], result["unpack_error"])
		}
		decoded, err := UnpackWallet(walletSyncOraclePassword(result["name"].(string)), []byte(result["packed"].(string)))
		if err != nil {
			t.Fatalf("Go unpack of %q failed: %v", result["name"], err)
		}
		assertWalletManagerOracleEqual(t, "Python pack -> Go unpack "+result["name"].(string), decoded, result["unpacked"])
	}

	for index, value := range oracle["unpack_cases"].([]any) {
		result := value.(map[string]any)
		if result["error_type"] != nil {
			t.Fatalf("Python unpack of Go case %q failed: %v", result["name"], result["error"])
		}
		assertWalletManagerOracleEqual(t, "Go pack -> Python unpack "+goPackCases[index].name, result["result"], goUnpacked[index])
	}

	for index, value := range oracle["merge_cases"].([]any) {
		result := value.(map[string]any)
		fixture := mergeFixtures[index]
		goResult := executeGoWalletSyncMerge(t, fixture, result["data"].(string))
		pythonErrored := result["error_type"] != nil
		if goResult["error"] != pythonErrored {
			t.Fatalf("merge %q error parity = Go %v, Python %v (%v)", fixture.name, goResult["error"], pythonErrored, result["error_type"])
		}
		assertWalletManagerOracleEqual(t, fixture.name+" added IDs", goResult["added_ids"], result["added_ids"])
		assertWalletManagerOracleEqual(t, fixture.name+" merged IDs", goResult["merged_ids"], result["merged_ids"])
		pythonAfter := result["after"].(map[string]any)
		assertWalletManagerOracleEqual(t, fixture.name+" wallet state", goResult["to_dict"], pythonAfter["to_dict"])
		pythonManager := result["manager"].(map[string]any)
		assertWalletManagerOracleEqual(t, fixture.name+" ledger state", goResult["ledgers"], pythonManager["ledgers"])
	}
}

func walletSyncOraclePassword(name string) string {
	switch name {
	case "Python empty":
		return "password"
	case "Python Unicode":
		return "pässwörd 🔑"
	case "Python account":
		return ""
	default:
		panic("unknown wallet sync oracle pack case: " + name)
	}
}

func walletSyncOracleDocument(name string, replacements ...Member) *Object {
	document := NewObject(
		Member{Key: "version", Value: LatestVersion},
		Member{Key: "name", Value: name},
		Member{Key: "preferences", Value: NewObject()},
		Member{Key: "accounts", Value: []any{}},
	)
	for _, replacement := range replacements {
		document.Set(replacement.Key, replacement.Value)
	}
	return document
}

func executeGoWalletSyncMerge(
	t *testing.T, fixture walletSyncOracleMergeFixture, data string,
) map[string]any {
	t.Helper()
	manager := NewWalletManager()
	if _, err := manager.GetOrCreateLedger(
		keys.MainNet.ID(), LedgerConfig{"data_path": t.TempDir()},
	); err != nil {
		t.Fatal(err)
	}
	wallet, err := WalletFromStorage(NewMemoryWalletStorage(fixture.document), manager)
	if err != nil {
		t.Fatal(err)
	}
	added, merged, mergeErr := wallet.Merge(manager, fixture.password, data)
	object, objectErr := wallet.ToObject(nil)
	if objectErr != nil {
		t.Fatal(objectErr)
	}
	return map[string]any{
		"error":      mergeErr != nil,
		"added_ids":  walletSyncOracleAccountIDs(added),
		"merged_ids": walletSyncOracleAccountIDs(merged),
		"to_dict":    object,
		"ledgers":    walletSyncOracleLedgers(manager),
	}
}

func walletSyncOracleAccountIDs(accounts []*Account) any {
	if accounts == nil {
		return nil
	}
	result := make([]any, len(accounts))
	for index, account := range accounts {
		result[index] = account.ID
	}
	return result
}

func walletSyncOracleLedgers(manager *WalletManager) []any {
	ledgers := manager.OrderedLedgers()
	result := make([]any, len(ledgers))
	for index, ledger := range ledgers {
		result[index] = map[string]any{
			"id": ledger.ID(), "account_ids": walletSyncOracleAccountIDs(ledger.Accounts),
		}
	}
	return result
}

func runWalletSyncOracle(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	sdkRoot, script := walletSyncOraclePaths(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(python, script, "--sdk-root", sdkRoot)
	command.Stdin = bytes.NewReader(encoded)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("Python wallet sync oracle failed: %v\n%s", err, stderr.String())
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.UseNumber()
	var result map[string]any
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("decode Python wallet sync oracle: %v\n%s", err, output)
	}
	return result
}

func walletSyncOraclePaths(t *testing.T) (sdkRoot, script string) {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate wallet sync oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot = os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	script = filepath.Join(daemonRoot, "compat", "wallet_sync_oracle.py")
	for _, required := range []string{
		filepath.Join(sdkRoot, "lbry", "wallet", "wallet.py"),
		filepath.Join(sdkRoot, "lbry", "crypto", "crypt.py"),
		script,
	} {
		if _, err := os.Stat(required); errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned local Python SDK wallet sync source is unavailable: %s", required)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	return sdkRoot, script
}
