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
