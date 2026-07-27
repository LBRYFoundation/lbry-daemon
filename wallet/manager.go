package wallet

type WalletManager struct {
	wallets []Wallet
}

type Wallet struct {
	ID   string
	Name string
}

func (walletManager *WalletManager) List() []Wallet {
	// TODO: Implement
	return []Wallet{}
}
