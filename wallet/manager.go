package wallet

import "encoding/json"
import "errors"
import "os"
import "path/filepath"
import "sort"
import "sync"
import "unicode/utf8"

import "lbry/daemon/auth"
import "lbry/daemon/crypt"

// Account is one account of a wallet file.
type Account struct {
	Name       string `json:"name"`
	Encrypted  bool   `json:"encrypted"`
	Seed       string `json:"seed"`
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`

	// Filled by Unlock, cleared by Lock. Mirrors lbry-sdk's Account.decrypt,
	// which keeps the plain seed and private key on the account.
	decryptedSeed       string
	decryptedPrivateKey string
}

type WalletFile struct {
	Name     string    `json:"name"`
	Accounts []Account `json:"accounts"`
}

type DefaultWalletManager struct {
	mutex     sync.Mutex
	directory string
	wallets   []*Wallet
}

var _ WalletManager = (*DefaultWalletManager)(nil)

func NewDefaultWalletManager() WalletManager {
	return &DefaultWalletManager{
		directory: GetDefaultWalletDirectory(),
	}
}

func (walletManager *DefaultWalletManager) Load() error {
	entries, err := os.ReadDir(walletManager.directory)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var wallets []*Wallet
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(walletManager.directory, entry.Name()))
		if err != nil {
			continue
		}
		var parsed WalletFile
		if json.Unmarshal(data, &parsed) != nil {
			continue
		}
		name := parsed.Name
		if name == "" {
			name = entry.Name()
		}
		locked := false
		for _, account := range parsed.Accounts {
			if account.Encrypted {
				locked = true
			}
		}
		wallets = append(wallets, &Wallet{ID: entry.Name(), Name: name, accounts: parsed.Accounts, locked: locked})
	}
	sort.Slice(wallets, func(i, j int) bool { return wallets[i].ID < wallets[j].ID })
	walletManager.mutex.Lock()
	walletManager.wallets = wallets
	walletManager.mutex.Unlock()
	return nil
}

func (wallet *Wallet) snapshot() Wallet {
	return Wallet{ID: wallet.ID, Name: wallet.Name, locked: wallet.locked}
}

func (walletManager *DefaultWalletManager) List(user *auth.User) []Wallet {
	walletManager.mutex.Lock()
	defer walletManager.mutex.Unlock()
	result := make([]Wallet, 0, len(walletManager.wallets))
	for _, wallet := range walletManager.wallets {
		result = append(result, wallet.snapshot())
	}
	return result
}

func (walletManager *DefaultWalletManager) Get(user *auth.User, id string) *Wallet {
	walletManager.mutex.Lock()
	defer walletManager.mutex.Unlock()
	if wallet := walletManager.find(id); wallet != nil {
		snapshot := wallet.snapshot()
		return &snapshot
	}
	return nil
}

func (walletManager *DefaultWalletManager) Remove(user *auth.User, id string) {
	// TODO: Implement
}

func (walletManager *DefaultWalletManager) Lock(user *auth.User, id string) bool {
	walletManager.mutex.Lock()
	defer walletManager.mutex.Unlock()
	wallet := walletManager.find(id)
	if wallet == nil || wallet.password == "" {
		return false
	}
	for i := range wallet.accounts {
		wallet.accounts[i].decryptedSeed = ""
		wallet.accounts[i].decryptedPrivateKey = ""
	}
	wallet.locked = true
	wallet.password = ""
	return true
}

func (walletManager *DefaultWalletManager) Unlock(user *auth.User, id string, password string) bool {
	walletManager.mutex.Lock()
	defer walletManager.mutex.Unlock()
	wallet := walletManager.find(id)
	if wallet == nil {
		return false
	}
	type result struct{ seed, privateKey string }
	results := map[int]result{}
	for i, account := range wallet.accounts {
		if !account.Encrypted {
			continue
		}
		var plain [2]string
		for j, value := range []string{account.Seed, account.PrivateKey} {
			if value == "" {
				continue
			}
			decrypted, err := crypt.Decrypt(password, value)
			if err != nil || decrypted == "" || !utf8.ValidString(decrypted) {
				return false
			}
			plain[j] = decrypted
		}
		results[i] = result{plain[0], plain[1]}
	}
	for i, r := range results {
		wallet.accounts[i].decryptedSeed = r.seed
		wallet.accounts[i].decryptedPrivateKey = r.privateKey
	}
	if len(results) > 0 {
		wallet.password = password
	}
	wallet.locked = false
	return true
}

func (walletManager *DefaultWalletManager) find(id string) *Wallet {
	for _, wallet := range walletManager.wallets {
		if wallet.ID == id {
			return wallet
		}
	}
	return nil
}
