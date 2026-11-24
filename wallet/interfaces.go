package wallet

import (
	"encoding/json"
	"errors"
	"lbry/daemon/auth"
	"lbry/daemon/crypto"
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

type Wallet struct {
	ID          string                 `json:"-"`
	Name        *string                `json:"name"`
	Accounts    []*Account             `json:"accounts"`
	Preferences map[string]*Preference `json:"preferences"`

	locked    bool
	encrypted bool   // lbry-sdk Wallet.is_encrypted: locked, or unlocked with a password
	password  string // set while unlocked with a password, enables Lock
}

func (wallet *Wallet) GetDefaultAccount() *Account {
	if len(wallet.Accounts) > 0 {
		return wallet.Accounts[0]
	}
	return nil
}

func (wallet *Wallet) IsEncrypted() bool {
	return wallet.encrypted
}

func (wallet *Wallet) Lock() bool {
	// NO-OP
	return false
}

func (wallet *Wallet) Unlock(password string) bool {
	// NO-OP
	return false
}

func (wallet *Wallet) IsLocked() bool {
	return wallet.locked
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
