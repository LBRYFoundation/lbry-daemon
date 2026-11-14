package wallet

import "encoding/json"
import "errors"
import "lbry/daemon/auth"
import "lbry/daemon/crypt"

type WalletManager interface {
	List(user *auth.User) []Wallet
	Get(user *auth.User, id string) *Wallet
	Remove(user *auth.User, id string)
	Lock(user *auth.User, id string) bool
	Unlock(user *auth.User, id string, password string) bool
	// SetAccount applies the changes to one account of a wallet and saves the wallet file.
	SetAccount(user *auth.User, walletID string, accountID string, change AccountChange) (*Account, error)
}

var ErrWalletNotFound = errors.New("Wallet does not exist.")

// AccountChange holds the settings of lbry-sdk's account_set. A nil field is left as it is.
type AccountChange struct {
	Default          bool
	NewName          *string
	ChangeGap        *int
	ChangeMaxUses    *int
	ReceivingGap     *int
	ReceivingMaxUses *int
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

	Ledger           string                     `json:"ledger,omitempty"`
	AddressGenerator json.RawMessage            `json:"address_generator,omitempty"`
	ModifiedOn       float64                    `json:"modified_on,omitempty"`
	Certificates     map[string]json.RawMessage `json:"certificates,omitempty"`
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

// ID is the account id: the address of its extended public key (lbry-sdk Account.id).
func (account *Account) ID() string {
	id, _ := AddressFromExtendedPublicKey(account.PublicKey)
	return id
}

// AddressGeneratorName is the generator lbry-sdk uses when a wallet does not name one.
const AddressGeneratorName = "deterministic-chain"

// AddressGeneratorDetails returns the account's address generator the way lbry-sdk reports it:
// the name and, for the deterministic chain, the gap and maximum uses of both chains, filling in defaults.
func (account *Account) AddressGeneratorDetails() map[string]any {
	stored := map[string]any{}
	if len(account.AddressGenerator) > 0 {
		json.Unmarshal(account.AddressGenerator, &stored)
	}
	name, _ := stored["name"].(string)
	if name == "" {
		name = AddressGeneratorName
	}
	details := map[string]any{"name": name}
	if name != AddressGeneratorName {
		return details
	}
	for chain, defaultGap := range map[string]int{"receiving": 20, "change": 6} {
		values := map[string]any{"gap": defaultGap, "maximum_uses_per_address": 1}
		if given, ok := stored[chain].(map[string]any); ok {
			for key := range values {
				if value, ok := given[key]; ok {
					values[key] = value
				}
			}
		}
		details[chain] = values
	}
	return details
}
