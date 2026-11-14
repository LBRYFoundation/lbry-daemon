package wallet

import "lbry/daemon/auth"

type DefaultWalletManager struct {
	wallets []Wallet
}

type Wallet struct {
	ID   string
	Name string
}

func NewDefaultWalletManager() WalletManager {
	return &DefaultWalletManager{}
}

func (walletManager *DefaultWalletManager) List(user *auth.User) []Wallet {
	// TODO: Implement
	return []Wallet{}
}

func (walletManager *DefaultWalletManager) Get(user *auth.User, id string) *Wallet {
	for _, wallet := range walletManager.wallets {
		if wallet.ID == id {
			return &wallet
		}
	}
	return nil
}

func (walletManager *DefaultWalletManager) Remove(user *auth.User, id string) {
	// TODO: Implement
}

func (walletManager *DefaultWalletManager) Lock(user *auth.User, id string) bool {
	// TODO: Implement
	return false
}

func (walletManager *DefaultWalletManager) Unlock(user *auth.User, id string, password string) bool {
	// TODO: Implement
	return false
}
