package wallet

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"unicode/utf8"

	"lbry/daemon/auth"
)

type account struct {
	Name       string `json:"name"`
	Encrypted  bool   `json:"encrypted"`
	Seed       string `json:"seed"`
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
}

type walletFile struct {
	Name     string    `json:"name"`
	Accounts []account `json:"accounts"`
}

type Wallet struct {
	ID   string
	Name string

	accounts []account
	password string // set while unlocked with a password, enables Lock
	unlocked bool
}

// IsLocked reports whether any account still has encrypted keys.
func (wallet *Wallet) IsLocked() bool {
	if wallet.unlocked {
		return false
	}
	for _, account := range wallet.accounts {
		if account.Encrypted {
			return true
		}
	}
	return false
}

type WalletManager struct {
	mutex   sync.Mutex
	wallets []*Wallet
}

// DefaultDirectory returns the directory lbrynet keeps its wallet files in.
// It can be overridden with the LBRY_WALLET_DIR environment variable.
func DefaultDirectory() string {
	if dir := os.Getenv("LBRY_WALLET_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch runtime.GOOS {
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "LBRY", "lbryum", "wallets")
		}
		return filepath.Join(home, "AppData", "Roaming", "LBRY", "lbryum", "wallets")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "LBRY", "lbryum", "wallets")
	default:
		return filepath.Join(home, ".local", "share", "lbry", "lbryum", "wallets")
	}
}

// NewManager loads every wallet file found in directory. A missing directory
// yields an empty manager; unreadable or malformed files are skipped.
func NewManager(directory string) (*WalletManager, error) {
	manager := &WalletManager{}
	entries, err := os.ReadDir(directory)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return manager, nil
		}
		return manager, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			continue
		}
		var parsed walletFile
		if json.Unmarshal(data, &parsed) != nil {
			continue
		}
		name := parsed.Name
		if name == "" {
			name = entry.Name()
		}
		manager.wallets = append(manager.wallets, &Wallet{ID: entry.Name(), Name: name, accounts: parsed.Accounts})
	}
	sort.Slice(manager.wallets, func(i, j int) bool { return manager.wallets[i].ID < manager.wallets[j].ID })
	return manager, nil
}

func (walletManager *WalletManager) List(user *auth.User) []Wallet {
	walletManager.mutex.Lock()
	defer walletManager.mutex.Unlock()
	result := make([]Wallet, 0, len(walletManager.wallets))
	for _, wallet := range walletManager.wallets {
		result = append(result, Wallet{ID: wallet.ID, Name: wallet.Name})
	}
	return result
}

func (walletManager *WalletManager) Get(user *auth.User, id string) *Wallet {
	walletManager.mutex.Lock()
	defer walletManager.mutex.Unlock()
	for _, wallet := range walletManager.wallets {
		if wallet.ID == id {
			return &Wallet{ID: wallet.ID, Name: wallet.Name}
		}
	}
	return nil
}

func (walletManager *WalletManager) Remove(user *auth.User, id string) {
	// TODO: Implement
}

// Lock re-locks a wallet that was unlocked with a password in this session.
// Like lbrynet it refuses wallets that were never encrypted.
func (walletManager *WalletManager) Lock(user *auth.User, id string) bool {
	walletManager.mutex.Lock()
	defer walletManager.mutex.Unlock()
	wallet := walletManager.find(id)
	if wallet == nil || wallet.password == "" {
		return false
	}
	wallet.unlocked = false
	wallet.password = ""
	return true
}

// Unlock checks the password against every encrypted account (seed and private
// key) and reports whether the wallet is now unlocked. A wallet without
// encrypted accounts is already unlocked and reports true.
func (walletManager *WalletManager) Unlock(user *auth.User, id string, password string) bool {
	walletManager.mutex.Lock()
	defer walletManager.mutex.Unlock()
	wallet := walletManager.find(id)
	if wallet == nil {
		return false
	}
	encrypted := false
	for _, account := range wallet.accounts {
		if !account.Encrypted {
			continue
		}
		encrypted = true
		for _, value := range []string{account.Seed, account.PrivateKey} {
			if value == "" {
				continue
			}
			plain, err := aesDecrypt(password, value)
			if err != nil || plain == "" || !utf8.ValidString(plain) {
				return false
			}
		}
	}
	if encrypted {
		wallet.unlocked = true
		wallet.password = password
	}
	return true
}

func (walletManager *WalletManager) find(id string) *Wallet {
	for _, wallet := range walletManager.wallets {
		if wallet.ID == id {
			return wallet
		}
	}
	return nil
}
