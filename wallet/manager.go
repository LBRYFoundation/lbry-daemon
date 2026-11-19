package wallet

import (
	"encoding/json"
	"errors"
	"fmt"
	"lbry/daemon/auth"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

type WalletFile struct {
	Name     string     `json:"name"`
	Accounts []*Account `json:"accounts"`
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
		newWallet := &Wallet{
			ID:       entry.Name(),
			Name:     name,
			Accounts: parsed.Accounts,
			locked:   locked,
			Preferences: map[string]*Preference{
				"local": &Preference{},
			},
		}
		wallets = append(wallets, newWallet)
	}
	sort.Slice(wallets, func(i, j int) bool { return wallets[i].ID < wallets[j].ID })
	walletManager.mutex.Lock()
	walletManager.wallets = wallets
	walletManager.mutex.Unlock()
	return nil
}

func (walletManager *DefaultWalletManager) Create(user *auth.User, wallet *Wallet) *Wallet {
	return nil
}

func (walletManager *DefaultWalletManager) Read(user *auth.User, id string) *Wallet {
	for _, wallet := range walletManager.wallets {
		if wallet.ID == id {
			return wallet
		}
	}
	return nil
}

func (walletManager *DefaultWalletManager) ReadList(user *auth.User) []*Wallet {
	result := make([]*Wallet, 0)
	for _, wallet := range walletManager.wallets {
		result = append(result, wallet)
	}
	return result
}

func (walletManager *DefaultWalletManager) Update(user *auth.User, wallet *Wallet) bool {
	fmt.Println(json.Marshal(wallet))
	// TODO: Implement
	return false
}

func (walletManager *DefaultWalletManager) Delete(user *auth.User, id string) bool {
	// TODO: Implement
	return false
}
