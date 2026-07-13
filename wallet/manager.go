package wallet

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"
	"sync"

	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/ledgerdb"
	"lbry/daemon/wallet/mnemonic"
)

var (
	ErrInvalidLedgerConfig              = errors.New("invalid ledger config")
	ErrLegacyWalletMigrationUnsupported = errors.New("legacy lbryum wallet migration is unsupported")
	ErrDefaultWalletMissing             = errors.New("default wallet is missing")
)

// LedgerConfig is the persistence-independent subset of the dictionary passed
// to the Python Ledger constructor. Values remain dynamically typed because
// callers and future ledger milestones add configuration fields over time.
type LedgerConfig map[string]any

// Ledger preserves the manager-visible state of a Python ledger. Persistence
// is opened explicitly; SPV networking and full ledger readiness remain a
// separate compatibility boundary.
type Ledger struct {
	Network               keys.Network
	Config                LedgerConfig
	Accounts              []*Account
	accountsMu            sync.RWMutex
	CoinSelectionStrategy any
	Headers               *Headers
	Database              *ledgerdb.DB
	SPVNetwork            LedgerSPVNetwork
	spvSync               ledgerSPVSync
	addressState          ledgerAddressState
	transactionEvents     transactionEventListeners
	FeePerByte            any
	FeePerNameCharacter   any
	transactionFeesSet    bool
	utxoReservationMu     contextMutex
	transactionCacheOnce  sync.Once
	transactionCache      *ledgerTransactionCache
	hubOutputsInflateMu   sync.Mutex
}

type contextMutex struct {
	once  sync.Once
	token chan struct{}
}

func (mutex *contextMutex) Lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	mutex.once.Do(func() {
		mutex.token = make(chan struct{}, 1)
		mutex.token <- struct{}{}
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-mutex.token:
		return nil
	}
}

func (mutex *contextMutex) Unlock() {
	mutex.token <- struct{}{}
}

func newLedger(network keys.Network, config LedgerConfig) (*Ledger, error) {
	if config == nil {
		config = LedgerConfig{}
	}
	feePerByte := any(int64(50))
	if configured, exists := config["fee_per_byte"]; exists {
		feePerByte = configured
	}
	feePerNameCharacter := any(int64(0))
	if configured, exists := config["fee_per_name_char"]; exists {
		feePerNameCharacter = configured
	}
	ledger := &Ledger{
		Network: network, Config: config,
		FeePerByte: feePerByte, FeePerNameCharacter: feePerNameCharacter,
		transactionFeesSet: true,
	}
	// Python constructs the default Database and Headers with self.path during
	// Ledger.__init__, so a missing or non-string data_path fails immediately.
	path, err := ledger.Path()
	if err != nil {
		return nil, err
	}
	ledger.Headers = NewHeadersForNetwork(pythonPathJoin(path, "headers"), network)
	ledger.Database = ledgerdb.New(pythonPathJoin(path, ledgerdb.Filename))
	ledger.ledgerTransactionCache()
	return ledger, nil
}

func (ledger *Ledger) ID() string {
	if ledger == nil {
		return ""
	}
	return ledger.Network.ID()
}

func (ledger *Ledger) Path() (string, error) {
	if ledger == nil {
		return "", fmt.Errorf("%w: ledger is nil", ErrInvalidLedgerConfig)
	}
	value, exists := ledger.Config["data_path"]
	if !exists {
		return "", fmt.Errorf("%w: data_path is missing", ErrInvalidLedgerConfig)
	}
	dataPath, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%w: data_path has type %T, want string", ErrInvalidLedgerConfig, value)
	}
	return pythonPathJoin(dataPath, ledger.ID()), nil
}

func (ledger *Ledger) DatabasePath() (string, error) {
	path, err := ledger.Path()
	if err != nil {
		return "", err
	}
	return pythonPathJoin(path, "blockchain.db"), nil
}

func (ledger *Ledger) HeadersPath() (string, error) {
	path, err := ledger.Path()
	if err != nil {
		return "", err
	}
	return pythonPathJoin(path, "headers"), nil
}

func (ledger *Ledger) addAccount(account *Account) {
	if account != nil {
		account.ledger = ledger
	}
	ledger.accountsMu.Lock()
	ledger.Accounts = append(ledger.Accounts, account)
	ledger.accountsMu.Unlock()
}

func (ledger *Ledger) AccountsSnapshot() []*Account {
	if ledger == nil {
		return []*Account{}
	}
	ledger.accountsMu.RLock()
	defer ledger.accountsMu.RUnlock()
	return append([]*Account(nil), ledger.Accounts...)
}

type WalletNotLoadedError struct {
	WalletID string
}

func (err *WalletNotLoadedError) Error() string {
	return fmt.Sprintf("Wallet %s is not loaded.", err.WalletID)
}

type LedgerSpec struct {
	ID     string
	Config LedgerConfig
}

// ManagerConfig is the isolated equivalent of WalletManager.from_config's
// two supported configuration entries. Ledgers is a slice because Python
// dictionaries preserve insertion order and ledger construction can fail.
type ManagerConfig struct {
	Ledgers []LedgerSpec
	Wallets []string
}

// WalletManager owns wallets and one ledger instance per registered network.
// Its Running flag is reserved for the eventual full Python Start/Stop
// lifecycle, not the persistence-prefix methods.
type WalletManager struct {
	Wallets     []*Wallet
	Ledgers     map[keys.Network]*Ledger
	Running     bool
	lifecycleMu sync.RWMutex

	walletOptions  []WalletOption
	ledgerOrder    []*Ledger
	lbryNetConfig  func() (LBRYNetConfig, error)
	reconfigureSPV func(context.Context, *Ledger, LBRYNetConfig) error
}

func NewWalletManager(walletOptions ...WalletOption) *WalletManager {
	return &WalletManager{
		Ledgers:       make(map[keys.Network]*Ledger),
		walletOptions: append([]WalletOption(nil), walletOptions...),
	}
}

func WalletManagerFromConfig(
	config ManagerConfig, walletOptions ...WalletOption,
) (*WalletManager, error) {
	manager := NewWalletManager(walletOptions...)
	for _, ledgerSpec := range config.Ledgers {
		if _, err := manager.GetOrCreateLedger(ledgerSpec.ID, ledgerSpec.Config); err != nil {
			return nil, err
		}
	}
	for _, walletPath := range config.Wallets {
		if _, err := manager.ImportWallet(walletPath); err != nil {
			return nil, err
		}
	}
	return manager, nil
}

// GetOrCreateLedger returns the existing network instance without applying a
// later config. Python keys this map by ledger class; keys.Network is its
// closed Go equivalent.
func (manager *WalletManager) GetOrCreateLedger(
	ledgerID string, config LedgerConfig,
) (*Ledger, error) {
	network, err := keys.ParseNetwork(ledgerID)
	if err != nil {
		return nil, err
	}
	if manager.Ledgers == nil {
		manager.Ledgers = make(map[keys.Network]*Ledger)
	}
	if ledger := manager.Ledgers[network]; ledger != nil {
		return ledger, nil
	}
	ledger, err := newLedger(network, config)
	if err != nil {
		return nil, err
	}
	manager.Ledgers[network] = ledger
	manager.ledgerOrder = append(manager.ledgerOrder, ledger)
	return ledger, nil
}

// OrderedLedgers returns ledger instances in their first-construction order,
// matching iteration over Python's insertion-ordered manager.ledgers dict.
func (manager *WalletManager) OrderedLedgers() []*Ledger {
	if manager == nil {
		return []*Ledger{}
	}
	return append([]*Ledger(nil), manager.ledgerOrder...)
}

// NetworkForLedger and RegisterAccount implement WalletLedgerResolver.
func (manager *WalletManager) NetworkForLedger(ledgerID string) (keys.Network, error) {
	ledger, err := manager.GetOrCreateLedger(ledgerID, nil)
	if err != nil {
		return 0, err
	}
	return ledger.Network, nil
}

func (manager *WalletManager) RegisterAccount(ledgerID string, account *Account) error {
	ledger, err := manager.GetOrCreateLedger(ledgerID, nil)
	if err != nil {
		return err
	}
	ledger.addAccount(account)
	return nil
}

func (manager *WalletManager) UnregisterAccount(account *Account) {
	if manager == nil || account == nil {
		return
	}
	ledger := manager.Ledgers[account.Network]
	if ledger == nil {
		return
	}
	ledger.accountsMu.Lock()
	kept := ledger.Accounts[:0]
	for _, candidate := range ledger.Accounts {
		if candidate != account {
			kept = append(kept, candidate)
		}
	}
	ledger.Accounts = kept
	ledger.accountsMu.Unlock()
}

func (manager *WalletManager) ImportWallet(walletPath string) (*Wallet, error) {
	wallet, err := WalletFromStorage(
		NewWalletStorage(walletPath), manager, manager.walletOptions...,
	)
	if err != nil {
		return nil, err
	}
	manager.Wallets = append(manager.Wallets, wallet)
	return wallet, nil
}

func (manager *WalletManager) DefaultWallet() *Wallet {
	if manager == nil || len(manager.Wallets) == 0 {
		return nil
	}
	return manager.Wallets[0]
}

func (manager *WalletManager) DefaultAccount() *Account {
	wallet := manager.DefaultWallet()
	if wallet == nil {
		return nil
	}
	return wallet.DefaultAccount()
}

// DefaultLedger mirrors WalletManager.ledger, which is resolved through the
// first account of the first loaded wallet rather than ledger registration
// order.
func (manager *WalletManager) DefaultLedger() *Ledger {
	account := manager.DefaultAccount()
	if account == nil {
		return nil
	}
	return account.ledger
}

func (manager *WalletManager) Accounts() []*Account {
	if manager == nil {
		return []*Account{}
	}
	var accounts []*Account
	for _, wallet := range manager.Wallets {
		accounts = append(accounts, wallet.Accounts...)
	}
	return accounts
}

func (manager *WalletManager) GetWalletOrDefault(walletID *string) (*Wallet, error) {
	if walletID == nil {
		return manager.DefaultWallet(), nil
	}
	return manager.GetWalletOrError(*walletID)
}

func (manager *WalletManager) GetWalletOrError(walletID string) (*Wallet, error) {
	for _, wallet := range manager.Wallets {
		if wallet.ID == walletID {
			return wallet, nil
		}
	}
	return nil, &WalletNotLoadedError{WalletID: walletID}
}

// AccountFactory is injectable so empty-wallet initialization can be tested
// without weakening production entropy.
type AccountFactory func(ledger *Ledger, wallet *Wallet) (*Account, error)

type LBRYNetConfig struct {
	BlockchainName        string
	WalletDir             string
	Wallets               []string
	HubTimeout            float64
	DefaultServers        any
	KnownHubs             any
	Jurisdiction          any
	ConcurrentHubRequests int
	TransactionCacheSize  int
	CoinSelectionStrategy any
	// DefaultServerDefaults is Config.lbryum_servers.default. DefaultServers
	// remains the effective startup value, which may be explicitly configured.
	DefaultServerDefaults     any
	LBryumServersSet          bool
	LBryumServersSetToDefault bool
	ClearLBryumServers        func() error
	Reload                    func() (LBRYNetConfig, error)
	ReconfigureSPV            func(context.Context, *Ledger, LBRYNetConfig) error
}

// WalletManagerFromLBRYNetConfig implements the filesystem and wallet-object
// portion of Python's from_lbrynet_config. Ledger services, legacy address
// migration, retaining the live Config object, and clearing a persisted
// default lbryum_servers value remain explicit boundaries.
func WalletManagerFromLBRYNetConfig(
	config LBRYNetConfig, accountFactory AccountFactory, walletOptions ...WalletOption,
) (*WalletManager, error) {
	ledgerID, err := ledgerIDForBlockchain(config.BlockchainName)
	if err != nil {
		return nil, err
	}
	ledgerConfig := LedgerConfig{
		"auto_connect":            true,
		"explicit_servers":        []any{},
		"hub_timeout":             config.HubTimeout,
		"default_servers":         config.DefaultServers,
		"known_hubs":              config.KnownHubs,
		"jurisdiction":            config.Jurisdiction,
		"concurrent_hub_requests": config.ConcurrentHubRequests,
		"data_path":               config.WalletDir,
		"tx_cache_size":           config.TransactionCacheSize,
	}
	if value, exists := os.LookupEnv("LBRY_FEE_PER_NAME_CHAR"); exists {
		fee, parseErr := pythonDecimalInteger(value)
		if parseErr != nil {
			return nil, fmt.Errorf("LBRY_FEE_PER_NAME_CHAR: %w", parseErr)
		}
		ledgerConfig["fee_per_name_char"] = fee
	}

	walletsDirectory := pythonPathJoin(config.WalletDir, "wallets")
	if _, statErr := os.Stat(walletsDirectory); errors.Is(statErr, os.ErrNotExist) {
		if mkdirErr := os.Mkdir(walletsDirectory, 0o777); mkdirErr != nil {
			return nil, mkdirErr
		}
	} else if statErr != nil {
		return nil, statErr
	}
	legacyPath := pythonPathJoin(walletsDirectory, "default_wallet")
	legacy, err := hasLegacyLBryumStructure(legacyPath)
	if err != nil {
		return nil, err
	}
	if legacy {
		return nil, fmt.Errorf("%w: %s", ErrLegacyWalletMigrationUnsupported, legacyPath)
	}
	if config.LBryumServersSetToDefault && config.ClearLBryumServers != nil {
		if clearErr := config.ClearLBryumServers(); clearErr != nil {
			return nil, clearErr
		}
	}

	walletPaths := make([]string, len(config.Wallets))
	for index, walletFile := range config.Wallets {
		walletPaths[index] = pythonPathJoin(walletsDirectory, walletFile)
	}
	manager, err := WalletManagerFromConfig(ManagerConfig{
		Ledgers: []LedgerSpec{{ID: ledgerID, Config: ledgerConfig}},
		Wallets: walletPaths,
	}, walletOptions...)
	if err != nil {
		return nil, err
	}
	capturedConfig := config
	manager.lbryNetConfig = func() (LBRYNetConfig, error) {
		if capturedConfig.Reload != nil {
			return capturedConfig.Reload()
		}
		return capturedConfig, nil
	}
	manager.reconfigureSPV = config.ReconfigureSPV
	ledger, err := manager.GetOrCreateLedger(ledgerID, nil)
	if err != nil {
		return nil, err
	}
	ledger.CoinSelectionStrategy = config.CoinSelectionStrategy

	defaultWallet := manager.DefaultWallet()
	if defaultWallet == nil {
		return nil, ErrDefaultWalletMissing
	}
	if defaultWallet.DefaultAccount() == nil {
		if accountFactory == nil {
			accountFactory = generateDefaultAccount
		}
		account, generateErr := accountFactory(ledger, defaultWallet)
		if generateErr != nil {
			return nil, generateErr
		}
		if account == nil {
			return nil, errors.New("account factory returned nil")
		}
		// Account.__init__ registers with the ledger before the wallet.
		account.wallet = defaultWallet
		ledger.addAccount(account)
		defaultWallet.AddAccount(account)
		if _, saveErr := defaultWallet.Save(); saveErr != nil {
			return nil, saveErr
		}
	}
	if defaultWallet.IsLocked() {
		value, exists, preferenceErr := defaultWallet.Preferences.Get(EncryptOnDisk)
		if preferenceErr != nil {
			return nil, preferenceErr
		}
		if !exists || value == nil {
			defaultWallet.Preferences.Set(EncryptOnDisk, true)
			if _, saveErr := defaultWallet.Save(); saveErr != nil {
				return nil, saveErr
			}
		}
	}
	return manager, nil
}

// Reset rebuilds the default ledger's live configuration and restarts its SPV
// network in the same mutation/stop/start order as WalletManager.reset.
func (manager *WalletManager) Reset(ctx context.Context) error {
	if manager == nil || manager.lbryNetConfig == nil {
		return errors.New("wallet manager live configuration is unavailable")
	}
	ledger := manager.DefaultLedger()
	if ledger == nil {
		return ErrDefaultWalletMissing
	}
	config, err := manager.lbryNetConfig()
	if err != nil {
		return err
	}
	defaultServers := config.DefaultServerDefaults
	if defaultServers == nil {
		defaultServers = config.DefaultServers
	}
	explicitServers := any([]any{})
	if config.LBryumServersSet {
		explicitServers = config.DefaultServers
	}
	ledger.Config = LedgerConfig{
		"auto_connect":            true,
		"explicit_servers":        explicitServers,
		"default_servers":         defaultServers,
		"known_hubs":              config.KnownHubs,
		"jurisdiction":            config.Jurisdiction,
		"hub_timeout":             config.HubTimeout,
		"concurrent_hub_requests": config.ConcurrentHubRequests,
		"data_path":               config.WalletDir,
	}
	if ctx == nil {
		ctx = context.Background()
	}
	checkpointSyncRunning := ledger.SPVSnapshot().Running
	if checkpointSyncRunning {
		if err := ledger.StopSPVCheckpointSync(ctx); err != nil {
			return err
		}
	} else if ledger.SPVNetwork != nil {
		if err := ledger.SPVNetwork.Stop(ctx); err != nil {
			return err
		}
	}
	if manager.reconfigureSPV != nil {
		if err := manager.reconfigureSPV(ctx, ledger, config); err != nil {
			return err
		}
	}
	if checkpointSyncRunning {
		return ledger.StartSPVCheckpointSync(ctx)
	}
	if ledger.SPVNetwork != nil {
		return ledger.SPVNetwork.Start(ctx)
	}
	return nil
}

func ledgerIDForBlockchain(blockchainName string) (string, error) {
	switch blockchainName {
	case "lbrycrd_main":
		return keys.MainNet.ID(), nil
	case "lbrycrd_testnet":
		return keys.TestNet.ID(), nil
	case "lbrycrd_regtest":
		return keys.RegTest.ID(), nil
	default:
		return "", fmt.Errorf("unknown blockchain name %q", blockchainName)
	}
}

func generateDefaultAccount(ledger *Ledger, _ *Wallet) (*Account, error) {
	words := mnemonic.NewEnglish()
	seed, err := words.MakeDefaultSeed()
	if err != nil {
		return nil, err
	}
	return NewAccount(ledger.Network, NewObject(
		Member{Key: "name", Value: nil},
		Member{Key: "seed", Value: seed},
		Member{Key: "address_generator", Value: NewObject()},
	))
}

func hasLegacyLBryumStructure(walletPath string) (bool, error) {
	contents, err := os.ReadFile(walletPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	decoded, err := decodeOrderedJSON(contents)
	if err != nil {
		return false, &WalletJSONDecodeError{Err: err}
	}
	document, ok := decoded.(*Object)
	if !ok {
		return false, nil
	}
	_, exists := document.Get("master_public_keys")
	return exists, nil
}

func pythonDecimalInteger(value string) (*big.Int, error) {
	return accountPythonIntString(value)
}

// pythonPathJoin mirrors posixpath.join rather than filepath.Join. In
// particular, it preserves trailing separators and dot segments, and an
// absolute later component replaces everything accumulated before it.
func pythonPathJoin(parts ...string) string {
	if len(parts) == 0 {
		return ""
	}
	joined := parts[0]
	for _, part := range parts[1:] {
		switch {
		case strings.HasPrefix(part, "/") || joined == "":
			joined = part
		case strings.HasSuffix(joined, "/"):
			joined += part
		default:
			joined += "/" + part
		}
	}
	return joined
}

// WalletFilePath mirrors os.path.join(wallet_dir, "wallets", wallet_id).
// In particular, an absolute wallet ID discards the preceding components.
func WalletFilePath(walletDir, walletID string) string {
	return pythonPathJoin(walletDir, "wallets", walletID)
}

// RemoveWallet detaches a loaded wallet and its accounts from ledger-visible
// inventories. The wallet object and its storage file remain untouched.
func (manager *WalletManager) RemoveWallet(wallet *Wallet) {
	if manager == nil || wallet == nil {
		return
	}
	for index, candidate := range manager.Wallets {
		if candidate == wallet {
			manager.Wallets = append(manager.Wallets[:index], manager.Wallets[index+1:]...)
			break
		}
	}
	for _, ledger := range manager.ledgerOrder {
		ledger.accountsMu.Lock()
		kept := ledger.Accounts[:0]
		for _, candidate := range ledger.Accounts {
			remove := false
			for _, account := range wallet.Accounts {
				if candidate == account {
					remove = true
					break
				}
			}
			if !remove {
				kept = append(kept, candidate)
			}
		}
		ledger.Accounts = kept
		ledger.accountsMu.Unlock()
	}
}

// pythonBaseName mirrors posixpath.basename without cleaning the path first.
func pythonBaseName(path string) string {
	if separator := strings.LastIndexByte(path, '/'); separator >= 0 {
		return path[separator+1:]
	}
	return path
}
