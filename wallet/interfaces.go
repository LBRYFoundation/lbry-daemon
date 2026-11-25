package wallet

import (
	"encoding/json"
	"errors"
	"lbry/daemon/auth"
	"lbry/daemon/crypto"
	"sync"
	"time"
)

const DEFAULT_WALLET = "default_wallet"

const ADDRESS_GENERATOR_NAME_DETERMINISTIC_CHAIN = "deterministic-chain"
const ADDRESS_GENERATOR_NAME_SINGLE_ADDRESS = "single-address"

type WalletManager interface {
	Create(*auth.User, *Wallet) *Wallet
	Read(*auth.User, string) *Wallet
	ReadList(*auth.User) []*Wallet
	Update(*auth.User, *Wallet) bool
	Delete(*auth.User, string) bool

	// Lock(user *auth.User, id string) bool
	// Unlock(user *auth.User, id string, password string) bool
}

// lockState guards the lock state of every wallet (the accounts' secrets and the password). Wallets are plain
// values that get copied and encoded, so it is not a field of one.
var lockState sync.Mutex

type Wallet struct {
	ID          string                 `json:"-"`
	Name        *string                `json:"name"`
	Accounts    []*Account             `json:"accounts"`
	Preferences map[string]*Preference `json:"preferences"`

	password string // set while unlocked with a password, enables Lock
}

func (wallet *Wallet) GetDefaultAccount() *Account {
	if len(wallet.Accounts) > 0 {
		return wallet.Accounts[0]
	}
	return nil
}

// IsEncrypted is lbry-sdk's Wallet.is_encrypted: the wallet is locked, or unlocked with a password.
func (wallet *Wallet) IsEncrypted() bool {
	if wallet == nil {
		return false
	}
	lockState.Lock()
	defer lockState.Unlock()
	return wallet.isLocked() || wallet.password != ""
}

// Lock encrypts every account again with the password the wallet was unlocked with. It fails for a wallet that
// is not unlocked with a password (an unencrypted wallet has none) and for one that is already locked.
func (wallet *Wallet) Lock() bool {
	if wallet == nil {
		return false
	}
	lockState.Lock()
	defer lockState.Unlock()
	if wallet.password == "" || wallet.isLocked() {
		return false
	}
	for _, account := range wallet.Accounts {
		if err := account.Lock(wallet.password); err != nil {
			return false
		}
	}
	return true
}

// Unlock decrypts every account with the password, all or nothing: with a wrong password nothing changes. A
// wallet that is not locked is already unlocked, so that succeeds without looking at the password.
func (wallet *Wallet) Unlock(password string) bool {
	if wallet == nil {
		return false
	}
	lockState.Lock()
	defer lockState.Unlock()
	if !wallet.isLocked() {
		return true
	}
	for _, account := range wallet.Accounts {
		if account.Locked && !account.canUnlockWith(password) {
			return false
		}
	}
	for _, account := range wallet.Accounts {
		if account.Locked {
			if err := account.Unlock(password); err != nil {
				return false
			}
		}
	}
	wallet.password = password
	return true
}

// IsLocked is true while any account is still encrypted.
func (wallet *Wallet) IsLocked() bool {
	if wallet == nil {
		return false
	}
	lockState.Lock()
	defer lockState.Unlock()
	return wallet.isLocked()
}

func (wallet *Wallet) isLocked() bool {
	for _, account := range wallet.Accounts {
		if account.Locked {
			return true
		}
	}
	return false
}

func (wallet *Wallet) IsSyncing() bool {
	return false
}

func (wallet *Wallet) GetAccountByID(id string) *Account {
	for _, account := range wallet.Accounts {
		if account.ID == id {
			return account
		}
	}
	return nil
}

type Account struct {
	ID         string `json:"-"`
	Name       string `json:"name"`
	Locked     bool   `json:"encrypted"`
	Seed       string `json:"seed"`
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`

	Ledger           string                     `json:"ledger,omitempty"`
	AddressGenerator AddressGenerator           `json:"address_generator"`
	ModifiedOn       int64                      `json:"modified_on,omitempty"`
	Certificates     map[string]json.RawMessage `json:"certificates,omitempty"`
}

func (account *Account) CalculateID() error {
	id, err := AddressFromExtendedPublicKey(account.PublicKey)
	if err != nil {
		return err
	}
	account.ID = id
	return nil
}

type AddressGenerator struct {
	Name      string                `json:"name"`
	Change    AddressGeneratorChain `json:"change"`
	Receiving AddressGeneratorChain `json:"receiving"`
}

type AddressGeneratorChain struct {
	Gap                   uint64 `json:"gap"`
	MaximumUsesPerAddress uint64 `json:"maximum_uses_per_address"`
}

// canUnlockWith tells whether the password decrypts the account's secrets, without changing it.
func (account *Account) canUnlockWith(password string) bool {
	for _, secret := range []string{account.Seed, account.PrivateKey} {
		if secret == "" {
			continue
		}
		if _, err := crypto.Decrypt(password, secret); err != nil {
			return false
		}
	}
	return true
}

func (account *Account) Lock(password string) error {
	if account.Locked {
		return errors.New("account is already locked")
	}
	var lockedSeed string
	var lockedPrivateKey string

	if account.Seed != "" {
		// TODO Cache Initialization Vector
		iv, _ := crypto.NewIV()
		lockedSeed = crypto.Encrypt(password, account.Seed, iv[:])
	}
	if account.PrivateKey != "" {
		// TODO Cache Initialization Vector
		iv, _ := crypto.NewIV()
		lockedPrivateKey = crypto.Encrypt(password, account.PrivateKey, iv[:])
	}

	account.Seed = lockedSeed
	account.PrivateKey = lockedPrivateKey
	account.Locked = true
	return nil
}

func (account *Account) Unlock(password string) error {
	if !account.Locked {
		return errors.New("account is already unlocked")
	}
	var unlockedSeed string
	var unlockedPrivateKey string

	if account.Seed != "" {
		var err error
		unlockedSeed, err = crypto.Decrypt(password, account.Seed)
		if err != nil {
			return err
		}
	}

	if account.PrivateKey != "" {
		var err error
		unlockedPrivateKey, err = crypto.Decrypt(password, account.PrivateKey)
		if err != nil {
			return err
		}
	}

	account.Seed = unlockedSeed
	account.PrivateKey = unlockedPrivateKey
	account.Locked = false
	return nil
}

type Preference struct {
	TimeStamp float64 `json:"ts"`
	Value     any     `json:"value"`
}

func (preference *Preference) SetNewValue(value any) {
	preference.TimeStamp = float64(time.Now().Unix())
	preference.Value = value
}
