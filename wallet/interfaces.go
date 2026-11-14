package wallet

import "errors"
import "lbry/daemon/auth"
import "lbry/daemon/crypt"

type WalletManager interface {
	List(user *auth.User) []Wallet
	Get(user *auth.User, id string) *Wallet
	Remove(user *auth.User, id string)
	Lock(user *auth.User, id string) bool
	Unlock(user *auth.User, id string, password string) bool
}

type Wallet struct {
	ID          string
	Name        string
	Accounts    []Account
	Preferences map[string]Preference

	locked   bool
	password string // set while unlocked with a password, enables Lock
}

type Account struct {
	Name       string `json:"name"`
	Encrypted  bool   `json:"encrypted"`
	Seed       string `json:"seed"`
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
}

type Preference struct {
	TimeStamp int64
	Value     any
}

func (wallet *Wallet) IsEncrypted() bool {
	return false
}

func (wallet *Wallet) IsLocked() bool {
	return wallet.locked
}

func (wallet *Wallet) IsSyncing() bool {
	return false
}

func (account *Account) Encrypt(password string) error {
	if account.Encrypted {
		return errors.New("Account is already encrypted.")
	}
	var encryptedSeed string
	var encryptedPrivateKey string

	if account.Seed != "" {
		// TODO Cache Initialization Vector
		iv, _ := crypt.NewIV()
		encryptedSeed = crypt.Encrypt(password, account.Seed, iv[:])
	}
	if account.PrivateKey != "" {
		// TODO Cache Initialization Vector
		iv, _ := crypt.NewIV()
		encryptedPrivateKey = crypt.Encrypt(password, account.PrivateKey, iv[:])
	}

	account.Seed = encryptedSeed
	account.PrivateKey = encryptedPrivateKey
	account.Encrypted = true
	return nil
}

func (account *Account) Decrypt(password string) error {
	if !account.Encrypted {
		return errors.New("Account is not encrypted.")
	}
	var decryptedSeed string
	var decryptedPrivateKey string

	var err error
	if account.Seed != "" {
		if decryptedSeed, err = crypt.Decrypt(password, account.Seed); err != nil {
			return err
		}
	}

	if account.PrivateKey != "" {
		if decryptedPrivateKey, err = crypt.Decrypt(password, account.PrivateKey); err != nil {
			return err
		}
	}

	account.Seed = decryptedSeed
	account.PrivateKey = decryptedPrivateKey
	account.Encrypted = false
	return nil
}
