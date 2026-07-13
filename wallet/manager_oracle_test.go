package wallet

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

type walletManagerOracleAction struct {
	Action   string   `json:"action"`
	Password *string  `json:"password,omitempty"`
	Key      string   `json:"key,omitempty"`
	Value    any      `json:"value,omitempty"`
	Now      *float64 `json:"now,omitempty"`
	ID       *string  `json:"id,omitempty"`
	IDs      []string `json:"ids,omitempty"`
}

type walletManagerOracleWalletCase struct {
	Name        string                      `json:"name"`
	Document    json.RawMessage             `json:"document,omitempty"`
	Path        string                      `json:"path,omitempty"`
	Mode        int                         `json:"mode,omitempty"`
	Ledgers     *Object                     `json:"ledgers,omitempty"`
	Now         float64                     `json:"now,omitempty"`
	InitVectors map[string]string           `json:"init_vectors,omitempty"`
	Actions     []walletManagerOracleAction `json:"actions,omitempty"`
}

type walletManagerOracleFile struct {
	Path     string          `json:"path"`
	Document json.RawMessage `json:"document"`
	Mode     int             `json:"mode,omitempty"`
}

type walletManagerOracleManagerCase struct {
	Name                  string                    `json:"name"`
	Kind                  string                    `json:"kind,omitempty"`
	Root                  string                    `json:"root"`
	Ledgers               *Object                   `json:"ledgers,omitempty"`
	Wallets               []string                  `json:"wallets"`
	Files                 []walletManagerOracleFile `json:"files,omitempty"`
	Now                   float64                   `json:"now,omitempty"`
	BlockchainName        string                    `json:"blockchain_name,omitempty"`
	HubTimeout            float64                   `json:"hub_timeout,omitempty"`
	LBryumServers         any                       `json:"lbryum_servers,omitempty"`
	KnownHubs             any                       `json:"known_hubs,omitempty"`
	Jurisdiction          any                       `json:"jurisdiction,omitempty"`
	ConcurrentHubRequests int                       `json:"concurrent_hub_requests,omitempty"`
	TransactionCacheSize  int                       `json:"transaction_cache_size,omitempty"`
	CoinSelectionStrategy any                       `json:"coin_selection_strategy,omitempty"`
}

type walletManagerOracleClock struct {
	now float64
}

func (clock *walletManagerOracleClock) preferenceTime() float64 { return clock.now }

func (clock *walletManagerOracleClock) accountTime() time.Time {
	seconds, fraction := math.Modf(clock.now)
	return time.Unix(int64(seconds), int64(fraction*1_000_000_000))
}

func TestWalletAndManagerMatchPinnedPythonOracle(t *testing.T) {
	password, emptyPassword, wrongPassword := "password", "", "wrong"
	defaultID := "bbmkLZJvGdu6WFaRCZjZBgvagbRWjr5Xew"
	missingID := "missing"
	fixedIVs := map[string]string{
		"private_key": "30303030303030303030303030303030",
		"seed":        "30303030303030303030303030303030",
	}
	workRoot := t.TempDir()
	pythonRoot := filepath.Join(workRoot, "python")
	goRoot := filepath.Join(workRoot, "go")

	plainAccount := `{"ledger":"lbc_mainnet","name":"Primary","seed":"` + accountEncryptionSeed + `","modified_on":123,"address_generator":{"name":"single-address"},"certificates":{"zeta":"one","alpha":"two"}}`
	plainWallet := rawAccountJSON(`{"version":1,"name":"Main Wallet","preferences":{},"accounts":[` + plainAccount + `]}`)
	resetWallet := rawAccountJSON(`{"version":1,"name":"Reset Wallet","preferences":{"encrypt-on-disk":{"value":true,"ts":1}},"accounts":[` + plainAccount + `]}`)
	lockedAccount := `{"ledger":"lbc_mainnet","name":"Locked","seed":"` + encryptedAccountSeed + `","private_key":"` + encryptedAccountXPrv + `","encrypted":true,"public_key":"` + fixedAccountXPub + `","modified_on":124,"address_generator":{"name":"single-address"},"certificates":{}}`
	lockedWallet := rawAccountJSON(`{"version":1,"name":"Locked Wallet","preferences":{},"accounts":[` + lockedAccount + `]}`)

	mainLedger := NewObject(Member{Key: "lbc_mainnet", Value: NewObject(
		Member{Key: "data_path", Value: "/tmp/wallet-manager-oracle"},
	)})
	twoLedgers := NewObject(
		Member{Key: "lbc_testnet", Value: NewObject(Member{Key: "data_path", Value: "/tmp/wallet-manager-oracle"})},
		Member{Key: "lbc_mainnet", Value: NewObject(Member{Key: "data_path", Value: "/tmp/wallet-manager-oracle"})},
	)
	twoNetworkWallet := rawAccountJSON(`{"version":1,"name":"Sorted Wallet","preferences":{},"accounts":[{"ledger":"lbc_testnet","name":"Test","seed":"` + accountEncryptionSeed + `","modified_on":77,"address_generator":{"name":"single-address"}},{"ledger":"lbc_mainnet","name":"Main","seed":"` + accountEncryptionSeed + `","modified_on":78,"address_generator":{"name":"single-address"}}]}`)
	walletCases := []walletManagerOracleWalletCase{
		{
			Name: "load lookup and memory save", Document: plainWallet, Ledgers: mainLedger,
			Now: 1_700_000_000.75,
			Actions: []walletManagerOracleAction{
				{Action: "account", ID: &defaultID},
				{Action: "account", ID: &missingID},
				{Action: "account_or_default"},
				{Action: "accounts_or_all", IDs: []string{defaultID, defaultID}},
				{Action: "save"},
			},
		},
		{
			Name: "save resets unavailable disk encryption", Document: resetWallet,
			Path: filepath.Join(pythonRoot, "wallet-actions", "reset-wallet"), Mode: 0o640,
			Ledgers: mainLedger, Now: 10,
			Actions: []walletManagerOracleAction{{Action: "save", Now: floatPointer(20.9)}},
		},
		{
			Name: "empty password takes encryption branch", Document: resetWallet,
			Ledgers: mainLedger, Now: 30, InitVectors: fixedIVs,
			Actions: []walletManagerOracleAction{
				{Action: "set_password", Password: &emptyPassword},
				{Action: "save"},
			},
		},
		{
			Name: "encrypt lock unlock decrypt lifecycle", Document: plainWallet,
			Ledgers: mainLedger, Now: 100, InitVectors: fixedIVs,
			Actions: []walletManagerOracleAction{
				{Action: "encrypt", Password: &password},
				{Action: "lock"},
				{Action: "unlock", Password: &wrongPassword},
				{Action: "unlock", Password: &password},
				{Action: "decrypt", Now: floatPointer(101)},
			},
		},
		{
			Name: "loaded locked wallet", Document: lockedWallet, Ledgers: mainLedger,
			Path: filepath.Join(pythonRoot, "wallet-actions", "locked-wallet"), Mode: 0o640, Now: 200,
			Actions: []walletManagerOracleAction{
				{Action: "save"},
				{Action: "unlock", Password: &password},
			},
		},
		{
			Name: "wallet hash sorts accounts across networks", Document: twoNetworkWallet,
			Ledgers: twoLedgers, Now: 300,
		},
	}

	secondAccount := `{"ledger":"lbc_mainnet","name":"Existing","seed":"` + accountEncryptionSeed + `","modified_on":50}`
	secondWallet := rawAccountJSON(`{"version":1,"name":"Second","preferences":{},"accounts":[` + secondAccount + `]}`)
	regAccount := `{"ledger":"lbc_regtest","name":"Regtest","seed":"` + accountEncryptionSeed + `","modified_on":51,"address_generator":{"name":"single-address"}}`
	regWallet := rawAccountJSON(`{"version":1,"name":"Reg Wallet","preferences":{},"accounts":[` + regAccount + `]}`)
	managerCases := []walletManagerOracleManagerCase{
		{
			Name: "from config ordered ledgers and wallets", Root: filepath.Join(pythonRoot, "from-config"),
			Ledgers: NewObject(
				Member{Key: "lbc_regtest", Value: NewObject(Member{Key: "data_path", Value: "<ROOT>"}, Member{Key: "marker", Value: "first"})},
				Member{Key: "lbc_mainnet", Value: NewObject(Member{Key: "data_path", Value: "<ROOT>"}, Member{Key: "marker", Value: "second"})},
			),
			Wallets: []string{"wallets/reg", "wallets/main"},
			Files: []walletManagerOracleFile{
				{Path: "wallets/reg", Document: regWallet, Mode: 0o640},
				{Path: "wallets/main", Document: secondWallet},
			},
			Now: 1_700_000_000.75,
		},
		{
			Name: "lbrynet creates and saves default account", Kind: "lbrynet",
			Root: filepath.Join(pythonRoot, "empty"), Wallets: []string{"default_wallet"},
			Now: 1_700_000_000.75,
		},
		{
			Name: "lbrynet first empty wallet registers generated account last", Kind: "lbrynet",
			Root: filepath.Join(pythonRoot, "registration"), Wallets: []string{"default_wallet", "second"},
			Files: []walletManagerOracleFile{{Path: "wallets/second", Document: secondWallet}},
			Now:   1_700_000_000.75,
		},
		{
			Name: "lbrynet locked wallet initializes disk preference", Kind: "lbrynet",
			Root: filepath.Join(pythonRoot, "locked"), Wallets: []string{"default_wallet"},
			Files: []walletManagerOracleFile{{Path: "wallets/default_wallet", Document: lockedWallet, Mode: 0o640}},
			Now:   1_700_000_010.25,
		},
		{
			Name: "lbrynet without configured wallets fails after directory creation", Kind: "lbrynet",
			Root: filepath.Join(pythonRoot, "no-wallets"), Wallets: []string{},
			Now: 1_700_000_000.75,
		},
	}

	oracle := runWalletManagerOracle(t, walletCases, managerCases)
	reference := oracle["reference"].(map[string]any)
	if reference["commit"] != accountOraclePinnedCommit || reference["version"] != accountOraclePinnedVersion {
		t.Fatalf("oracle reference = %#v", reference)
	}
	metadata := oracle["metadata"].(map[string]any)
	if metadata["python_debug"] != true {
		t.Fatal("wallet manager oracle ran with Python assertions disabled")
	}
	if want := os.Getenv("LBRY_ORACLE_PYTHON_VERSION"); want != "" && metadata["python_version"] != want {
		t.Fatalf("oracle Python version = %v, want %s", metadata["python_version"], want)
	}

	goWalletCases := make([]walletManagerOracleWalletCase, len(walletCases))
	copy(goWalletCases, walletCases)
	for index := range goWalletCases {
		if goWalletCases[index].Path != "" {
			goWalletCases[index].Path = strings.Replace(goWalletCases[index].Path, pythonRoot, goRoot, 1)
		}
	}
	goWalletResults := make([]any, len(goWalletCases))
	for index, fixture := range goWalletCases {
		goWalletResults[index] = executeGoWalletManagerWalletCase(t, fixture)
	}
	pythonWalletResults := oracle["wallet_cases"].([]any)
	assertWalletManagerOracleEqual(t, "wallet object orchestration",
		normalizeWalletManagerOracle(goWalletResults, goRoot),
		normalizeWalletManagerOracle(pythonWalletResults, pythonRoot))

	goManagerCases := make([]walletManagerOracleManagerCase, len(managerCases))
	copy(goManagerCases, managerCases)
	for index := range goManagerCases {
		goManagerCases[index].Root = strings.Replace(managerCases[index].Root, pythonRoot, goRoot, 1)
	}
	goManagerResults := make([]any, len(goManagerCases))
	withoutEnvironment(t, "LBRY_FEE_PER_NAME_CHAR", func() {
		for index, fixture := range goManagerCases {
			goManagerResults[index] = executeGoWalletManagerManagerCase(t, fixture)
		}
	})
	pythonManagerResults := oracle["manager_cases"].([]any)
	assertWalletManagerOracleEqual(t, "wallet manager orchestration",
		normalizeWalletManagerOracle(goManagerResults, goRoot),
		normalizeWalletManagerOracle(pythonManagerResults, pythonRoot))
}

func executeGoWalletManagerWalletCase(t *testing.T, fixture walletManagerOracleWalletCase) map[string]any {
	t.Helper()
	clock := &walletManagerOracleClock{now: fixture.Now}
	options := walletManagerOracleOptions(clock)
	manager := NewWalletManager(options...)
	for _, member := range fixture.Ledgers.Members() {
		config := walletManagerOracleLedgerConfig(t, member.Value, "")
		if _, err := manager.GetOrCreateLedger(member.Key, config); err != nil {
			t.Fatal(err)
		}
	}
	var storage *WalletStorage
	if fixture.Path != "" {
		writeGoWalletManagerOracleDocument(t, fixture.Path, fixture.Document, fixture.Mode)
		storage = NewWalletStorage(fixture.Path)
	} else {
		var defaults []*Object
		if len(fixture.Document) > 0 {
			defaults = []*Object{mustAccountObjectFromJSON(t, fixture.Document)}
		}
		storage = NewMemoryWalletStorage(defaults...)
	}
	wallet, err := WalletFromStorage(storage, manager, options...)
	result := map[string]any{
		"name": fixture.Name, "error_type": walletManagerOracleErrorType(err),
		"error": walletManagerOracleErrorMessage(err), "initial": nil, "actions": []any{},
	}
	if err != nil {
		return result
	}
	setWalletManagerOracleIVs(t, wallet, fixture.InitVectors)
	result["initial"] = goWalletManagerWalletView(wallet)
	actions := make([]any, 0, len(fixture.Actions))
	for _, action := range fixture.Actions {
		if action.Now != nil {
			clock.now = *action.Now
		}
		actions = append(actions, executeGoWalletManagerAction(wallet, action))
	}
	result["actions"] = actions
	return result
}

func executeGoWalletManagerAction(wallet *Wallet, action walletManagerOracleAction) map[string]any {
	var result any
	var err error
	switch action.Action {
	case "save":
		var encoded []byte
		encoded, err = wallet.Save()
		if encoded != nil {
			result = string(encoded)
		}
	case "set_password":
		wallet.EncryptionPassword = copyStringPointer(action.Password)
	case "set_preference":
		wallet.Preferences.Set(action.Key, action.Value)
	case "encrypt":
		password := valueOrEmpty(action.Password)
		err = wallet.Encrypt(password)
		if err == nil {
			result = true
		}
	case "decrypt":
		err = wallet.Decrypt()
		if err == nil {
			result = true
		}
	case "lock":
		err = wallet.Lock()
		if err == nil {
			result = true
		}
	case "unlock":
		result, err = wallet.Unlock(valueOrEmpty(action.Password))
	case "account":
		var account *Account
		account, err = wallet.Account(valueOrEmpty(action.ID))
		if err == nil {
			result = account.ID
		}
	case "account_or_default":
		var account *Account
		account, err = wallet.AccountOrDefault(action.ID)
		if err == nil && account != nil {
			result = account.ID
		}
	case "accounts_or_all":
		var accounts []*Account
		accounts, err = wallet.AccountsOrAll(action.IDs)
		if err == nil {
			result = walletManagerOracleAccountIDs(accounts)
		}
	default:
		err = errors.New("unknown wallet action")
	}
	var file any
	if path, exists := wallet.Storage.Path(); exists {
		file = goWalletManagerFileView(path)
	}
	view := map[string]any{
		"action": action.Action, "result": result,
		"error_type": walletManagerOracleErrorType(err), "error": walletManagerOracleErrorMessage(err),
		"wallet": goWalletManagerWalletView(wallet), "file": file,
	}
	return view
}

func goWalletManagerWalletView(wallet *Wallet) map[string]any {
	var storagePath any
	if path, exists := wallet.Storage.Path(); exists {
		storagePath = path
	}
	var password any
	if wallet.EncryptionPassword != nil {
		password = *wallet.EncryptionPassword
	}
	var defaultAccount any
	if account := wallet.DefaultAccount(); account != nil {
		defaultAccount = account.ID
	}
	encrypted, encryptedErr := wallet.IsEncrypted()
	object, objectErr := wallet.ToObject(nil)
	dict := map[string]any{
		"result": object, "json": nil, "key_order": nil,
		"error_type": walletManagerOracleErrorType(objectErr), "error": walletManagerOracleErrorMessage(objectErr),
	}
	if objectErr == nil {
		encoded, err := encodePreferenceJSON(object)
		if err != nil {
			panic(err)
		}
		dict["json"], dict["key_order"] = string(encoded), object.Keys()
	}
	jsonBytes, jsonErr := wallet.ToJSON()
	var jsonResult any
	if jsonBytes != nil {
		jsonResult = string(jsonBytes)
	}
	digest, hashErr := wallet.Hash()
	var hash any
	if hashErr == nil {
		hash = hex.EncodeToString(digest[:])
	}
	view := map[string]any{
		"id": wallet.ID, "name": wallet.Name, "storage_path": storagePath,
		"account_ids":        walletManagerOracleAccountIDs(wallet.Accounts),
		"default_account_id": defaultAccount,
		"preferences":        wallet.Preferences.Data(), "encryption_password": password,
		"is_locked": wallet.IsLocked(), "is_encrypted": encrypted,
		"is_encrypted_error_type": walletManagerOracleErrorType(encryptedErr),
		"is_encrypted_error":      walletManagerOracleErrorMessage(encryptedErr),
		"to_dict":                 dict, "to_json": jsonResult,
		"to_json_error_type": walletManagerOracleErrorType(jsonErr),
		"to_json_error":      walletManagerOracleErrorMessage(jsonErr),
		"hash":               hash, "hash_error_type": walletManagerOracleErrorType(hashErr),
		"hash_error": walletManagerOracleErrorMessage(hashErr),
	}
	return cloneWalletManagerOracleMap(view)
}

func cloneWalletManagerOracleMap(value map[string]any) map[string]any {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var result map[string]any
	if err := decoder.Decode(&result); err != nil {
		panic(err)
	}
	return result
}

func executeGoWalletManagerManagerCase(t *testing.T, fixture walletManagerOracleManagerCase) map[string]any {
	t.Helper()
	if err := os.MkdirAll(fixture.Root, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, file := range fixture.Files {
		writeGoWalletManagerOracleFile(t, fixture.Root, file)
	}
	clock := &walletManagerOracleClock{now: fixture.Now}
	options := walletManagerOracleOptions(clock)
	var manager *WalletManager
	var err error
	if fixture.Kind == "lbrynet" {
		manager, err = WalletManagerFromLBRYNetConfig(
			walletManagerOracleLBRYNetConfig(fixture),
			func(ledger *Ledger, _ *Wallet) (*Account, error) {
				return NewAccount(ledger.Network, NewObject(
					Member{Key: "name", Value: nil},
					Member{Key: "seed", Value: accountEncryptionSeed},
					Member{Key: "address_generator", Value: NewObject()},
				), WithAccountClock(clock.accountTime))
			},
			options...,
		)
	} else {
		specs := make([]LedgerSpec, 0, fixture.Ledgers.Len())
		for _, member := range fixture.Ledgers.Members() {
			specs = append(specs, LedgerSpec{
				ID: member.Key, Config: walletManagerOracleLedgerConfig(t, member.Value, fixture.Root),
			})
		}
		paths := make([]string, len(fixture.Wallets))
		for index, path := range fixture.Wallets {
			paths[index] = pythonPathJoin(fixture.Root, path)
		}
		manager, err = WalletManagerFromConfig(ManagerConfig{Ledgers: specs, Wallets: paths}, options...)
	}
	result := map[string]any{
		"name": fixture.Name, "error_type": walletManagerOracleErrorType(err),
		"error": walletManagerOracleErrorMessage(err), "manager": nil,
		"files": goWalletManagerFiles(t, fixture.Root), "urandom_calls": []any{},
	}
	if manager != nil {
		result["manager"] = goWalletManagerManagerView(t, manager, fixture.Root)
	}
	return result
}

func goWalletManagerManagerView(t *testing.T, manager *WalletManager, root string) map[string]any {
	t.Helper()
	ledgers := make([]any, 0, len(manager.OrderedLedgers()))
	for _, ledger := range manager.OrderedLedgers() {
		config := make(map[string]any, len(ledger.Config))
		for key, value := range ledger.Config {
			if key == "data_path" {
				config[key] = "<ROOT>" + strings.TrimPrefix(value.(string), root)
			} else {
				config[key] = value
			}
		}
		path, err := ledger.Path()
		if err != nil {
			t.Fatal(err)
		}
		ledgers = append(ledgers, map[string]any{
			"id": ledger.ID(), "config": config,
			"account_ids":             walletManagerOracleAccountIDs(ledger.Accounts),
			"path":                    "<ROOT>" + strings.TrimPrefix(path, root),
			"coin_selection_strategy": ledger.CoinSelectionStrategy,
		})
	}
	wallets := make([]any, len(manager.Wallets))
	ids := make([]string, len(manager.Wallets))
	for index, wallet := range manager.Wallets {
		view := goWalletManagerWalletView(wallet)
		if path, ok := view["storage_path"].(string); ok && strings.HasPrefix(path, root) {
			view["storage_path"] = "<ROOT>" + strings.TrimPrefix(path, root)
		}
		wallets[index], ids[index] = view, wallet.ID
	}
	var defaultWallet, defaultAccount any
	if wallet := manager.DefaultWallet(); wallet != nil {
		defaultWallet = wallet.ID
	}
	if account := manager.DefaultAccount(); account != nil {
		defaultAccount = account.ID
	}
	return map[string]any{
		"wallets": wallets, "wallet_ids": ids,
		"default_wallet_id": defaultWallet, "default_account_id": defaultAccount,
		"account_ids": walletManagerOracleAccountIDs(manager.Accounts()),
		"ledgers":     ledgers, "running": manager.Running,
	}
}

func walletManagerOracleOptions(clock *walletManagerOracleClock) []WalletOption {
	return []WalletOption{
		WithWalletPreferenceOptions(WithPreferenceClock(clock.preferenceTime)),
		WithWalletAccountOptions(WithAccountClock(clock.accountTime)),
	}
}

func walletManagerOracleLBRYNetConfig(fixture walletManagerOracleManagerCase) LBRYNetConfig {
	blockchain := fixture.BlockchainName
	if blockchain == "" {
		blockchain = "lbrycrd_main"
	}
	hubTimeout := fixture.HubTimeout
	if hubTimeout == 0 {
		hubTimeout = 30
	}
	servers := fixture.LBryumServers
	if servers == nil {
		servers = []any{"hub.example:50001"}
	}
	known := fixture.KnownHubs
	if known == nil {
		known = NewObject()
	}
	concurrent := fixture.ConcurrentHubRequests
	if concurrent == 0 {
		concurrent = 32
	}
	cacheSize := fixture.TransactionCacheSize
	if cacheSize == 0 {
		cacheSize = 1024
	}
	strategy := fixture.CoinSelectionStrategy
	if strategy == nil {
		strategy = "prefer-confirmed"
	}
	return LBRYNetConfig{
		BlockchainName: blockchain, WalletDir: fixture.Root, Wallets: fixture.Wallets,
		HubTimeout: hubTimeout, DefaultServers: servers, KnownHubs: known,
		Jurisdiction: fixture.Jurisdiction, ConcurrentHubRequests: concurrent,
		TransactionCacheSize: cacheSize, CoinSelectionStrategy: strategy,
	}
}

func walletManagerOracleLedgerConfig(t *testing.T, value any, root string) LedgerConfig {
	t.Helper()
	object, ok := value.(*Object)
	if !ok {
		t.Fatalf("ledger config has type %T", value)
	}
	config := make(LedgerConfig, object.Len())
	for _, member := range object.Members() {
		config[member.Key] = member.Value
		if member.Value == "<ROOT>" {
			config[member.Key] = root
		}
	}
	return config
}

func setWalletManagerOracleIVs(t *testing.T, wallet *Wallet, vectors map[string]string) {
	t.Helper()
	for _, account := range wallet.Accounts {
		for key, value := range vectors {
			decoded, err := hex.DecodeString(value)
			if err != nil {
				t.Fatal(err)
			}
			if err := account.SetInitializationVector(key, decoded); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func writeGoWalletManagerOracleFile(t *testing.T, root string, fixture walletManagerOracleFile) {
	t.Helper()
	path := pythonPathJoin(root, fixture.Path)
	writeGoWalletManagerOracleDocument(t, path, fixture.Document, fixture.Mode)
}

func writeGoWalletManagerOracleDocument(t *testing.T, path string, document json.RawMessage, rawMode int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	value, err := decodeOrderedJSON(document)
	if err != nil {
		t.Fatal(err)
	}
	var encoded []byte
	if text, ok := value.(string); ok {
		encoded = []byte(text)
	} else {
		encoded, err = encodePreferenceJSON(value)
		if err != nil {
			t.Fatal(err)
		}
	}
	mode := os.FileMode(rawMode)
	if mode == 0 {
		mode = 0o600
	}
	if err := os.WriteFile(path, encoded, mode); err != nil {
		t.Fatal(err)
	}
}

func goWalletManagerFileView(path string) map[string]any {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{"exists": false, "contents": nil, "mode": nil}
	}
	if err != nil {
		panic(err)
	}
	var contents any
	if !info.IsDir() {
		data, err := os.ReadFile(path)
		if err != nil {
			panic(err)
		}
		contents = string(data)
	}
	return map[string]any{
		"exists": true, "contents": contents, "mode": int(info.Mode().Perm()),
	}
}

func goWalletManagerFiles(t *testing.T, root string) map[string]any {
	t.Helper()
	result := make(map[string]any)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		var contents any
		if !entry.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			contents = string(data)
		}
		result[filepath.ToSlash(relative)] = map[string]any{
			"exists": true, "contents": contents, "mode": int(info.Mode().Perm()),
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func walletManagerOracleAccountIDs(accounts []*Account) []string {
	result := make([]string, len(accounts))
	for index, account := range accounts {
		result[index] = account.ID
	}
	return result
}

func walletManagerOracleErrorType(err error) any {
	if err == nil {
		return nil
	}
	var notLoaded *WalletNotLoadedError
	switch {
	case errors.Is(err, ErrDefaultWalletMissing):
		return "AttributeError"
	case errors.As(err, &notLoaded):
		return "WalletNotLoadedError"
	case strings.HasPrefix(err.Error(), "Couldn't find account:"):
		return "ValueError"
	case errors.Is(err, ErrLockedWalletSerialization),
		errors.Is(err, ErrLockedWalletSync),
		errors.Is(err, ErrLockedWalletPack),
		errors.Is(err, ErrLockedWalletMerge),
		errors.Is(err, ErrWalletPasswordUnavailable),
		errors.Is(err, ErrBlankWalletPassword),
		errors.Is(err, ErrEncryptedAccountHash),
		errors.Is(err, ErrAccountAlreadyEncrypted),
		errors.Is(err, ErrAccountNotEncrypted):
		return "AssertionError"
	default:
		return "error"
	}
}

func walletManagerOracleErrorMessage(err error) any {
	if err == nil {
		return nil
	}
	return err.Error()
}

func copyStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func floatPointer(value float64) *float64 { return &value }

func normalizeWalletManagerOracle(value any, root string) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			if key == "error" || strings.HasSuffix(key, "_error") {
				continue
			}
			result[key] = normalizeWalletManagerOracle(item, root)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = normalizeWalletManagerOracle(item, root)
		}
		return result
	case string:
		if root != "" {
			return strings.ReplaceAll(typed, root, "<ROOT>")
		}
		return typed
	default:
		return value
	}
}

func assertWalletManagerOracleEqual(t *testing.T, name string, got, want any) {
	t.Helper()
	got = canonicalWalletManagerOracleValue(t, got)
	want = canonicalWalletManagerOracleValue(t, want)
	if path, left, right, differs := firstWalletManagerOracleDifference(got, want, "$"); differs {
		t.Fatalf("%s differs at %s\nGo:     %#v\nPython: %#v", name, path, left, right)
	}
}

func canonicalWalletManagerOracleValue(t *testing.T, value any) any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var result any
	if err := decoder.Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func firstWalletManagerOracleDifference(got, want any, path string) (string, any, any, bool) {
	if reflect.DeepEqual(got, want) {
		return "", nil, nil, false
	}
	gotMap, gotIsMap := got.(map[string]any)
	wantMap, wantIsMap := want.(map[string]any)
	if gotIsMap && wantIsMap {
		keys := make([]string, 0, len(gotMap)+len(wantMap))
		seen := make(map[string]struct{})
		for key := range gotMap {
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
		for key := range wantMap {
			if _, exists := seen[key]; !exists {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			left, leftExists := gotMap[key]
			right, rightExists := wantMap[key]
			if !leftExists || !rightExists {
				return path + "." + key, left, right, true
			}
			if childPath, childLeft, childRight, differs := firstWalletManagerOracleDifference(left, right, path+"."+key); differs {
				return childPath, childLeft, childRight, true
			}
		}
	}
	gotSlice, gotIsSlice := got.([]any)
	wantSlice, wantIsSlice := want.([]any)
	if gotIsSlice && wantIsSlice {
		if len(gotSlice) != len(wantSlice) {
			return path + ".length", len(gotSlice), len(wantSlice), true
		}
		for index := range gotSlice {
			if childPath, childLeft, childRight, differs := firstWalletManagerOracleDifference(gotSlice[index], wantSlice[index], fmt.Sprintf("%s[%d]", path, index)); differs {
				return childPath, childLeft, childRight, true
			}
		}
	}
	return path, got, want, true
}

func runWalletManagerOracle(
	t *testing.T, walletCases []walletManagerOracleWalletCase,
	managerCases []walletManagerOracleManagerCase,
) map[string]any {
	t.Helper()
	sdkRoot, script := walletManagerOraclePaths(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	payload, err := json.Marshal(map[string]any{
		"wallet_cases": walletCases, "manager_cases": managerCases,
	})
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(python, script, "--sdk-root", sdkRoot)
	command.Stdin = bytes.NewReader(payload)
	command.Env = environmentWithout(os.Environ(), "LBRY_FEE_PER_NAME_CHAR")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("Python wallet manager oracle failed: %v\n%s", err, stderr.String())
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.UseNumber()
	var result map[string]any
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("decode Python wallet manager oracle: %v\n%s", err, output)
	}
	return result
}

func walletManagerOraclePaths(t *testing.T) (sdkRoot, script string) {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate wallet manager oracle test source")
	}
	daemonRoot := filepath.Dir(filepath.Dir(sourceFile))
	sdkRoot = os.Getenv("LBRY_SDK_PATH")
	if sdkRoot == "" {
		sdkRoot = filepath.Join(filepath.Dir(daemonRoot), "lbry-sdk")
	} else if !filepath.IsAbs(sdkRoot) {
		sdkRoot = filepath.Join(daemonRoot, sdkRoot)
	}
	script = filepath.Join(daemonRoot, "compat", "wallet_manager_oracle.py")
	for _, required := range []string{
		filepath.Join(sdkRoot, "lbry", "wallet", "wallet.py"),
		filepath.Join(sdkRoot, "lbry", "wallet", "manager.py"),
		filepath.Join(sdkRoot, "lbry", "wallet", "account.py"),
		script,
	} {
		if _, err := os.Stat(required); errors.Is(err, os.ErrNotExist) {
			t.Skipf("pinned local Python SDK wallet manager source is unavailable: %s", required)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	return sdkRoot, script
}

func environmentWithout(environment []string, key string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment))
	for _, value := range environment {
		if !strings.HasPrefix(value, prefix) {
			result = append(result, value)
		}
	}
	return result
}

func withoutEnvironment(t *testing.T, key string, function func()) {
	t.Helper()
	value, exists := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if exists {
			_ = os.Setenv(key, value)
		} else {
			_ = os.Unsetenv(key)
		}
	}()
	function()
}
