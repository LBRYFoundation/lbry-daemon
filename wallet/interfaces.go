package wallet

import "encoding/json"
import "errors"
import "fmt"
import "time"
import "lbry/daemon/auth"
import "lbry/daemon/crypto"

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
	Preferences map[string]*Preference

	locked    bool
	encrypted bool   // lbry-sdk Wallet.is_encrypted: locked, or unlocked with a password
	password  string // set while unlocked with a password, enables Lock
}

func (wallet *Wallet) IsEncrypted() bool {
	return wallet.encrypted
}

func (wallet *Wallet) IsLocked() bool {
	return wallet.locked
}

func (wallet *Wallet) IsSyncing() bool {
	return false
}

func (wallet *Wallet) Save() error {
	fmt.Println(json.Marshal(wallet))
	return errors.New("Failed saving wallet.")
}

type Account struct {
	Name       string `json:"name"`
	Locked     bool   `json:"encrypted"`
	Seed       string `json:"seed"`
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
}

func (account *Account) Lock(password string) error {
	if account.Locked {
		return errors.New("Account is already locked.")
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
		return errors.New("Account is already unlocked.")
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
	TimeStamp int64
	Value     any
}

func (preference *Preference) SetNewValue(value any) {
	preference.TimeStamp = time.Now().Unix()
	preference.Value = value
}
