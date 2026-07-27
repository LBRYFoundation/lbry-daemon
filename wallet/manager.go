package wallet

import "lbry/daemon/auth"

type WalletManager struct {
	wallets []Wallet
}

type Wallet struct {
	ID   string
	Name string
}

func (walletManager *WalletManager) List(user *auth.User) []Wallet {
	// TODO: Implement
	return []Wallet{}
}

func (walletManager *WalletManager) Get(user *auth.User, id string) *Wallet {
	for _, wallet := range walletManager.wallets {
		if wallet.ID == id {
			return &wallet
		}
	}
	return nil
}

func (walletManager *WalletManager) Remove(user *auth.User, id string) {
	// TODO: Implement
}

func (walletManager *WalletManager) Lock(user *auth.User, id string) bool {
	// TODO: Implement
	return false
}

func (walletManager *WalletManager) Unlock(user *auth.User, id string, password string) bool {
	// TODO: Implement
	return false
}
