package wallet

import "lbry/daemon/auth"

type WalletManager interface {
	List(user *auth.User) []Wallet
	Get(user *auth.User, id string) *Wallet
	Remove(user *auth.User, id string)
	Lock(user *auth.User, id string) bool
	Unlock(user *auth.User, id string, password string) bool
}

type Wallet struct {
	ID   string
	Name string

	locked   bool
	accounts []Account
	password string // set while unlocked with a password, enables Lock
}
