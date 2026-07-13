package wallet

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"lbry/daemon/wallet/keys"
)

const (
	EncryptOnDisk   = "encrypt-on-disk"
	ENCRYPT_ON_DISK = EncryptOnDisk
)

var (
	ErrLockedWalletSerialization = errors.New("cannot serialize a wallet with locked/encrypted accounts")
	ErrLockedWalletSync          = errors.New("cannot operate on a locked wallet")
	ErrLockedWalletPack          = errors.New("Cannot pack a wallet with locked/encrypted accounts.")
	ErrLockedWalletMerge         = errors.New("Cannot sync apply on a locked wallet.")
	ErrWalletPasswordUnavailable = errors.New("wallet encryption password is unavailable")
	ErrBlankWalletPassword       = errors.New("cannot encrypt with blank password")
	ErrNilWalletAccount          = errors.New("wallet contains a nil account")
)

// WalletLedgerResolver supplies the ledger-specific key network and preserves
// Account.__init__'s registration order when loading: ledger first, wallet
// second. A later wallet manager can implement this without coupling Wallet to
// ledger databases or network services.
type WalletLedgerResolver interface {
	NetworkForLedger(ledgerID string) (keys.Network, error)
	RegisterAccount(ledgerID string, account *Account) error
}

type WalletOption func(*walletOptions)

type walletOptions struct {
	name              string
	accounts          []*Account
	storage           *WalletStorage
	preferences       *Object
	preferenceOptions []PreferenceOption
	accountOptions    []AccountOption
	syncEntropy       io.Reader
}

func defaultWalletOptions() walletOptions {
	return walletOptions{name: "Wallet", syncEntropy: rand.Reader}
}

func WithWalletName(name string) WalletOption {
	return func(options *walletOptions) {
		options.name = name
	}
}

func WithWalletAccounts(accounts []*Account) WalletOption {
	return func(options *walletOptions) {
		options.accounts = accounts
	}
}

func WithWalletStorage(storage *WalletStorage) WalletOption {
	return func(options *walletOptions) {
		options.storage = storage
	}
}

func WithWalletPreferences(preferences *Object) WalletOption {
	return func(options *walletOptions) {
		options.preferences = preferences
	}
}

func WithWalletPreferenceOptions(preferenceOptions ...PreferenceOption) WalletOption {
	return func(options *walletOptions) {
		options.preferenceOptions = append(options.preferenceOptions, preferenceOptions...)
	}
}

// WithWalletAccountOptions applies to accounts created by WalletFromStorage or
// Merge. It is useful for deterministic clock and IV fixtures.
func WithWalletAccountOptions(accountOptions ...AccountOption) WalletOption {
	return func(options *walletOptions) {
		options.accountOptions = append(options.accountOptions, accountOptions...)
	}
}

// WithWalletSyncEntropy controls the 16-byte IV source used by Pack. It is a
// deterministic fixture hook; production wallets use crypto/rand.Reader.
func WithWalletSyncEntropy(entropy io.Reader) WalletOption {
	return func(options *walletOptions) {
		if entropy != nil {
			options.syncEntropy = entropy
		}
	}
}

type Wallet struct {
	Name               string
	Accounts           []*Account
	Storage            *WalletStorage
	Preferences        *TimestampedPreferences
	EncryptionPassword *string
	ID                 string

	accountOptions []AccountOption
	syncEntropy    io.Reader
}

func NewWallet(options ...WalletOption) *Wallet {
	settings := defaultWalletOptions()
	applyWalletOptions(&settings, options)
	return newWallet(settings)
}

func applyWalletOptions(settings *walletOptions, options []WalletOption) {
	for _, option := range options {
		if option != nil {
			option(settings)
		}
	}
}

func newWallet(settings walletOptions) *Wallet {
	storage := settings.storage
	if storage == nil {
		storage = NewMemoryWalletStorage()
	}
	accounts := settings.accounts
	if len(accounts) == 0 {
		// Python's `accounts or []` replaces every empty input with a new list.
		accounts = make([]*Account, 0)
	}
	wallet := &Wallet{
		Name:           settings.name,
		Accounts:       accounts,
		Storage:        storage,
		Preferences:    NewTimestampedPreferences(settings.preferences, settings.preferenceOptions...),
		accountOptions: append([]AccountOption(nil), settings.accountOptions...),
		syncEntropy:    settings.syncEntropy,
	}
	for _, account := range wallet.Accounts {
		if account != nil && account.wallet == nil {
			account.wallet = wallet
		}
	}
	wallet.ID = walletID(storage, wallet.Name)
	return wallet
}

func walletID(storage *WalletStorage, name string) string {
	if storage == nil {
		return name
	}
	path, hasPath := storage.Path()
	if !hasPath || path == "" {
		return name
	}
	// On the pinned Linux SDK os.path.basename keeps the empty suffix of a
	// trailing slash, unlike filepath.Base.
	if separator := strings.LastIndexByte(path, '/'); separator >= 0 {
		return path[separator+1:]
	}
	return path
}

// WalletFromStorage loads accounts in document order. Each successful account
// is registered with its ledger before it is appended to the wallet. Earlier
// registrations remain observable when a later record fails, matching Python's
// partial construction behavior.
func WalletFromStorage(
	storage *WalletStorage, resolver WalletLedgerResolver, options ...WalletOption,
) (*Wallet, error) {
	if storage == nil {
		return nil, errors.New("wallet storage is nil")
	}
	document, err := storage.Read()
	if err != nil {
		return nil, err
	}

	settings := defaultWalletOptions()
	applyWalletOptions(&settings, options)
	settings.storage = storage
	settings.accounts = nil

	if value, exists := document.Get("name"); exists {
		name, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("wallet name has type %T, want string", value)
		}
		settings.name = name
	}
	if value, exists := document.Get("preferences"); exists && pythonJSONTruthy(value) {
		preferences, ok := value.(*Object)
		if !ok || preferences == nil {
			return nil, fmt.Errorf("wallet preferences has type %T, want object", value)
		}
		settings.preferences = preferences
	} else {
		settings.preferences = NewObject()
	}

	wallet := newWallet(settings)
	accountsValue, exists := document.Get("accounts")
	if !exists {
		return wallet, nil
	}
	records, err := walletAccountRecords(accountsValue)
	if err != nil {
		return nil, err
	}
	for index, record := range records {
		ledgerID, err := accountRequiredString(record, "ledger")
		if err != nil {
			return nil, fmt.Errorf("load wallet account %d: %w", index, err)
		}
		if resolver == nil {
			return nil, errors.New("wallet ledger resolver is nil")
		}
		network, err := resolver.NetworkForLedger(ledgerID)
		if err != nil {
			return nil, fmt.Errorf("resolve wallet account %d ledger %q: %w", index, ledgerID, err)
		}
		account, err := NewAccount(network, record, settings.accountOptions...)
		if err != nil {
			return nil, fmt.Errorf("load wallet account %d: %w", index, err)
		}
		account.wallet = wallet
		if err := resolver.RegisterAccount(ledgerID, account); err != nil {
			return nil, fmt.Errorf("register wallet account %d with ledger %q: %w", index, ledgerID, err)
		}
		wallet.AddAccount(account)
	}
	return wallet, nil
}

func walletAccountRecords(value any) ([]*Object, error) {
	if value == nil {
		return nil, errors.New("wallet accounts has type <nil>, want array")
	}
	reflected := reflect.ValueOf(value)
	if reflected.Kind() != reflect.Slice && reflected.Kind() != reflect.Array {
		return nil, fmt.Errorf("wallet accounts has type %T, want array", value)
	}
	records := make([]*Object, reflected.Len())
	for index := 0; index < reflected.Len(); index++ {
		record, ok := reflected.Index(index).Interface().(*Object)
		if !ok || record == nil {
			return nil, fmt.Errorf("wallet account %d has type %T, want object", index, reflected.Index(index).Interface())
		}
		records[index] = record
	}
	return records, nil
}

func (wallet *Wallet) AddAccount(account *Account) {
	if wallet != nil && account != nil && account.wallet == nil {
		account.wallet = wallet
	}
	wallet.Accounts = append(wallet.Accounts, account)
}

func (wallet *Wallet) DefaultAccount() *Account {
	if wallet == nil || len(wallet.Accounts) == 0 {
		return nil
	}
	return wallet.Accounts[0]
}

func (wallet *Wallet) AccountOrDefault(accountID *string) (*Account, error) {
	if accountID == nil {
		return wallet.DefaultAccount(), nil
	}
	return wallet.Account(*accountID)
}

func (wallet *Wallet) Account(accountID string) (*Account, error) {
	if wallet != nil {
		for _, account := range wallet.Accounts {
			if account == nil {
				return nil, ErrNilWalletAccount
			}
			if account.ID == accountID {
				return account, nil
			}
		}
	}
	return nil, fmt.Errorf("Couldn't find account: %s.", accountID)
}

func (wallet *Wallet) AccountsOrAll(accountIDs []string) ([]*Account, error) {
	if len(accountIDs) == 0 {
		return wallet.Accounts, nil
	}
	accounts := make([]*Account, len(accountIDs))
	for index, accountID := range accountIDs {
		account, err := wallet.Account(accountID)
		if err != nil {
			return nil, err
		}
		accounts[index] = account
	}
	return accounts, nil
}

// ToObject returns Wallet.to_dict's insertion-ordered representation. A nil
// password is Python None; a pointer to an empty string remains distinguishable
// at the wallet save boundary even though Account.to_dict treats both as false.
func (wallet *Wallet) ToObject(encryptPassword *string) (*Object, error) {
	if wallet == nil || wallet.Preferences == nil {
		return nil, errors.New("wallet is not initialized")
	}
	password := ""
	if encryptPassword != nil {
		password = *encryptPassword
	}
	accounts := make([]any, len(wallet.Accounts))
	for index, account := range wallet.Accounts {
		if account == nil {
			return nil, ErrNilWalletAccount
		}
		serialized, err := account.ToDict(password, true)
		if err != nil {
			return nil, fmt.Errorf("serialize wallet account %d: %w", index, err)
		}
		accounts[index] = serialized
	}
	return NewObject(
		Member{Key: "version", Value: LatestVersion},
		Member{Key: "name", Value: wallet.Name},
		Member{Key: "preferences", Value: wallet.Preferences.Data()},
		Member{Key: "accounts", Value: accounts},
	), nil
}

func (wallet *Wallet) ToJSON() ([]byte, error) {
	if wallet.IsLocked() {
		return nil, ErrLockedWalletSerialization
	}
	object, err := wallet.ToObject(nil)
	if err != nil {
		return nil, err
	}
	return encodePreferenceJSON(object)
}

// Pack returns the legacy scrypt/AES-CBC envelope around zlib-compressed
// Wallet.to_json output.
func (wallet *Wallet) Pack(password string) ([]byte, error) {
	if wallet == nil {
		return nil, errors.New("wallet is nil")
	}
	if wallet.IsLocked() {
		return nil, ErrLockedWalletPack
	}
	serialized, err := wallet.ToJSON()
	if err != nil {
		return nil, err
	}
	var compressed bytes.Buffer
	compressor := zlib.NewWriter(&compressed)
	if _, err := compressor.Write(serialized); err != nil {
		_ = compressor.Close()
		return nil, err
	}
	if err := compressor.Close(); err != nil {
		return nil, err
	}
	return betterAESEncrypt(password, compressed.Bytes(), wallet.syncEntropy)
}

// UnpackWallet decrypts and decompresses a legacy wallet synchronization
// envelope. The decoded JSON value is returned without requiring an object,
// matching Wallet.unpack's classmethod boundary.
func UnpackWallet(password string, encrypted []byte) (any, error) {
	decrypted, err := BetterAESDecrypt(password, encrypted)
	if err != nil {
		return nil, err
	}
	decompressor, err := zlib.NewReader(bytes.NewReader(decrypted))
	if err != nil {
		if errors.Is(err, zlib.ErrHeader) {
			return nil, ErrInvalidWalletPassword
		}
		return nil, err
	}
	decompressed, readErr := io.ReadAll(decompressor)
	closeErr := decompressor.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return decodeOrderedJSON(decompressed)
}

// Merge applies a clear or encrypted remote wallet without transactionality.
// Mutations completed before a later error remain in memory, matching the
// pinned SDK's synchronization behavior. A nil password selects clear JSON;
// a non-nil empty password still selects the encrypted envelope.
func (wallet *Wallet) Merge(
	manager *WalletManager, password *string, data string,
) (addedAccounts, mergedAccounts []*Account, err error) {
	if wallet == nil {
		return nil, nil, errors.New("wallet is nil")
	}
	if wallet.IsLocked() {
		return nil, nil, ErrLockedWalletMerge
	}
	var decoded any
	if password == nil {
		decoded, err = decodeOrderedJSON([]byte(data))
	} else {
		if !isASCII(data) {
			return nil, nil, fmt.Errorf("%w: encrypted string contains non-ASCII characters", ErrInvalidSyncEnvelope)
		}
		decoded, err = UnpackWallet(*password, []byte(data))
	}
	if err != nil {
		return nil, nil, err
	}
	document, ok := decoded.(*Object)
	if !ok || document == nil {
		return nil, nil, fmt.Errorf("wallet synchronization JSON has type %T, want object", decoded)
	}

	remotePreferences := NewObject()
	if value, exists := document.Get("preferences"); exists {
		var valid bool
		remotePreferences, valid = value.(*Object)
		if !valid || remotePreferences == nil {
			return nil, nil, fmt.Errorf("wallet synchronization preferences has type %T, want object", value)
		}
	}
	if wallet.Preferences == nil {
		return nil, nil, errors.New("wallet preferences are not initialized")
	}
	if err := wallet.Preferences.Merge(remotePreferences); err != nil {
		return nil, nil, err
	}

	accountsValue, exists := document.Get("accounts")
	if !exists {
		return nil, nil, errors.New("wallet synchronization accounts are missing")
	}
	addedAccounts = make([]*Account, 0)
	mergedAccounts = make([]*Account, 0)
	err = forEachWalletMergeAccount(accountsValue, func(index int, record *Object) error {
		ledgerID, err := accountRequiredString(record, "ledger")
		if err != nil {
			return fmt.Errorf("merge wallet account %d: %w", index, err)
		}
		if manager == nil {
			return errors.New("wallet manager is nil")
		}
		ledger, err := manager.GetOrCreateLedger(ledgerID, nil)
		if err != nil {
			return fmt.Errorf("merge wallet account %d ledger %q: %w", index, ledgerID, err)
		}
		accountID, err := accountIDFromObject(ledger.Network, record)
		if err != nil {
			return fmt.Errorf("merge wallet account %d keys: %w", index, err)
		}
		var localMatch *Account
		for _, localAccount := range wallet.Accounts {
			if localAccount == nil {
				return ErrNilWalletAccount
			}
			if localAccount.ID == accountID {
				localMatch = localAccount
				break
			}
		}
		if localMatch != nil {
			if err := localMatch.Merge(record); err != nil {
				return fmt.Errorf("merge wallet account %d state: %w", index, err)
			}
			mergedAccounts = append(mergedAccounts, localMatch)
			return nil
		}

		newAccount, err := NewAccount(ledger.Network, record, wallet.accountOptions...)
		if err != nil {
			return fmt.Errorf("merge wallet account %d: %w", index, err)
		}
		newAccount.wallet = wallet
		ledger.addAccount(newAccount)
		wallet.AddAccount(newAccount)
		addedAccounts = append(addedAccounts, newAccount)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return addedAccounts, mergedAccounts, nil
}

func isASCII(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] >= 0x80 {
			return false
		}
	}
	return true
}

func forEachWalletMergeAccount(value any, visit func(int, *Object) error) error {
	if value == nil {
		return errors.New("wallet synchronization accounts has type <nil>, want iterable account objects")
	}
	reflected := reflect.ValueOf(value)
	if reflected.Kind() == reflect.Slice || reflected.Kind() == reflect.Array {
		for index := 0; index < reflected.Len(); index++ {
			item := reflected.Index(index).Interface()
			record, ok := item.(*Object)
			if !ok || record == nil {
				return fmt.Errorf("wallet synchronization account %d has type %T, want object", index, item)
			}
			if err := visit(index, record); err != nil {
				return err
			}
		}
		return nil
	}
	// Empty dictionaries and strings are iterable but contain no records in
	// Python. Non-empty variants fail on their first scalar item.
	switch typed := value.(type) {
	case *Object:
		if typed != nil && typed.Len() == 0 {
			return nil
		}
	case string:
		if typed == "" {
			return nil
		}
	}
	return fmt.Errorf("wallet synchronization accounts has type %T, want iterable account objects", value)
}

func (wallet *Wallet) Save() ([]byte, error) {
	if wallet == nil || wallet.Storage == nil || wallet.Preferences == nil {
		return nil, errors.New("wallet is not initialized")
	}
	encryptOnDisk, err := wallet.Preferences.GetOr(EncryptOnDisk, false)
	if err != nil {
		return nil, err
	}
	if pythonJSONTruthy(encryptOnDisk) {
		if wallet.EncryptionPassword != nil {
			object, err := wallet.ToObject(wallet.EncryptionPassword)
			if err != nil {
				return nil, err
			}
			return wallet.Storage.Write(object)
		}
		if !wallet.IsLocked() {
			wallet.Preferences.Set(EncryptOnDisk, false)
		}
	}
	object, err := wallet.ToObject(nil)
	if err != nil {
		return nil, err
	}
	return wallet.Storage.Write(object)
}

func (wallet *Wallet) IsLocked() bool {
	if wallet == nil {
		return false
	}
	for _, account := range wallet.Accounts {
		if account != nil && account.Encrypted {
			return true
		}
	}
	return false
}

func (wallet *Wallet) IsEncrypted() (bool, error) {
	if wallet.IsLocked() {
		return true, nil
	}
	if wallet == nil || wallet.Preferences == nil {
		return false, errors.New("wallet is not initialized")
	}
	encryptOnDisk, err := wallet.Preferences.GetOr(EncryptOnDisk, false)
	if err != nil {
		return false, err
	}
	return pythonJSONTruthy(encryptOnDisk) && wallet.EncryptionPassword != nil, nil
}

func (wallet *Wallet) Hash() ([sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	if wallet == nil || wallet.Preferences == nil {
		return digest, errors.New("wallet is not initialized")
	}
	encrypted, err := wallet.IsEncrypted()
	if err != nil {
		return digest, err
	}
	hasher := sha256.New()
	if encrypted {
		if wallet.EncryptionPassword == nil {
			return digest, ErrWalletPasswordUnavailable
		}
		_, _ = hasher.Write([]byte(*wallet.EncryptionPassword))
	}
	preferenceHash, err := wallet.Preferences.Hash()
	if err != nil {
		return digest, err
	}
	_, _ = hasher.Write(preferenceHash[:])

	accounts := append([]*Account(nil), wallet.Accounts...)
	for _, account := range accounts {
		if account == nil {
			return digest, ErrNilWalletAccount
		}
	}
	sort.SliceStable(accounts, func(left, right int) bool {
		return accounts[left].ID < accounts[right].ID
	})
	for _, account := range accounts {
		accountHash, err := account.Hash()
		if err != nil {
			return digest, err
		}
		_, _ = hasher.Write(accountHash[:])
	}
	copy(digest[:], hasher.Sum(nil))
	return digest, nil
}

// Encrypt enables encrypted serialization on disk while leaving account
// secrets unlocked in memory.
func (wallet *Wallet) Encrypt(password string) error {
	if wallet.IsLocked() {
		return ErrLockedWalletSync
	}
	if password == "" {
		return ErrBlankWalletPassword
	}
	passwordCopy := password
	wallet.EncryptionPassword = &passwordCopy
	wallet.Preferences.Set(EncryptOnDisk, true)
	_, err := wallet.Save()
	return err
}

// Decrypt disables encrypted serialization on disk. Accounts must already be
// unlocked; the cached password intentionally remains available.
func (wallet *Wallet) Decrypt() error {
	if wallet.IsLocked() {
		return ErrLockedWalletSync
	}
	wallet.Preferences.Set(EncryptOnDisk, false)
	_, err := wallet.Save()
	return err
}

func (wallet *Wallet) Lock() error {
	if wallet == nil || wallet.EncryptionPassword == nil {
		return ErrWalletPasswordUnavailable
	}
	for _, account := range wallet.Accounts {
		if account == nil {
			return ErrNilWalletAccount
		}
		if !account.Encrypted {
			if err := account.Encrypt(*wallet.EncryptionPassword); err != nil {
				return err
			}
		}
	}
	return nil
}

// Unlock preserves Python's sequential, non-transactional behavior. Accounts
// decrypted before a later password or database failure remain decrypted, and
// each newly decrypted account primes deterministic channel keys when its
// ledger database is open.
func (wallet *Wallet) Unlock(password string) (bool, error) {
	if wallet == nil {
		return false, errors.New("wallet is nil")
	}
	for _, account := range wallet.Accounts {
		if account == nil {
			return false, ErrNilWalletAccount
		}
		if account.Encrypted {
			decrypted, err := account.Decrypt(password)
			if err != nil {
				return false, err
			}
			if !decrypted {
				return false, nil
			}
			if err := account.ensureDeterministicChannelCachePrimed(context.Background()); err != nil {
				return false, err
			}
		}
	}
	passwordCopy := password
	wallet.EncryptionPassword = &passwordCopy
	return true, nil
}

func pythonJSONTruthy(value any) bool {
	if value == nil {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return typed != ""
	case json.Number:
		text := string(typed)
		if integer, ok := new(big.Int).SetString(text, 10); ok {
			return integer.Sign() != 0
		}
		number, err := strconv.ParseFloat(text, 64)
		return err != nil || number != 0
	case *big.Int:
		return typed != nil && typed.Sign() != 0
	case big.Int:
		return typed.Sign() != 0
	case *Object:
		return typed != nil && typed.Len() != 0
	case Object:
		return typed.Len() != 0
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Bool:
		return reflected.Bool()
	case reflect.String, reflect.Array, reflect.Slice, reflect.Map:
		return reflected.Len() != 0
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflected.Int() != 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return reflected.Uint() != 0
	case reflect.Float32, reflect.Float64:
		return reflected.Float() != 0
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Pointer:
		return !reflected.IsNil()
	default:
		return true
	}
}
