package wallet

import "encoding/json"
import "errors"
import "os"
import "path/filepath"
import "sort"
import "sync"

import "lbry/daemon/auth"

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

func NewDefaultWalletManager(directory string) WalletManager {
	return &DefaultWalletManager{
		directory: directory,
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
			if account.Locked {
				locked = true
			}
		}
		wallets = append(wallets, &Wallet{ID: entry.Name(), Name: name, Accounts: parsed.Accounts, locked: locked})
	}
	sort.Slice(wallets, func(i, j int) bool { return wallets[i].ID < wallets[j].ID })
	walletManager.mutex.Lock()
	walletManager.wallets = wallets
	walletManager.mutex.Unlock()
	return nil
}

func (wallet *Wallet) snapshot() Wallet {
	return Wallet{ID: wallet.ID, Name: wallet.Name, locked: wallet.locked, encrypted: wallet.locked || wallet.password != ""}
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

	type pendingEncryption struct {
		index   int
		account Account
	}
	pending := make([]pendingEncryption, 0, len(wallet.Accounts))
	for i := range wallet.Accounts {
		if wallet.Accounts[i].Locked {
			continue
		}
		account := wallet.Accounts[i]
		if err := account.Lock(wallet.password); err != nil {
			return false
		}
		pending = append(pending, pendingEncryption{index: i, account: account})
	}
	for _, item := range pending {
		wallet.Accounts[item.index] = item.account
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

	// Decrypt copies first so a wrong password leaves every account untouched.
	type pendingDecryption struct {
		index   int
		account Account
	}
	pending := make([]pendingDecryption, 0, len(wallet.Accounts))
	for i := range wallet.Accounts {
		if !wallet.Accounts[i].Locked {
			continue
		}
		account := wallet.Accounts[i]
		if err := account.Unlock(password); err != nil {
			return false
		}
		pending = append(pending, pendingDecryption{index: i, account: account})
	}
	for _, item := range pending {
		wallet.Accounts[item.index] = item.account
	}

	wallet.password = password
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
