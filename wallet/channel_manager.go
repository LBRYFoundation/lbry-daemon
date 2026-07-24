package wallet

import (
	"errors"
	"fmt"
	"sync"

	"lbry/daemon/wallet/keys"
)

var ErrChannelKeyUsageUnavailable = errors.New("channel-key usage lookup is unavailable")

type ChannelKeyUsage func(account *Account, publicKey *keys.PublicKey) (bool, error)

// DeterministicChannelKeyManager is the database-independent state machine for
// Account's m/2/index channel keys. The caller supplies the database usage
// probe, keeping SQLite and ledger startup outside this package slice.
type DeterministicChannelKeyManager struct {
	Account   *Account
	LastKnown int64
	Cache     map[string]*keys.PrivateKey

	mu         sync.RWMutex
	privateKey *keys.PrivateKey
}

func NewDeterministicChannelKeyManager(account *Account) *DeterministicChannelKeyManager {
	return &DeterministicChannelKeyManager{
		Account: account,
		Cache:   make(map[string]*keys.PrivateKey),
	}
}

// PrivateKey lazily caches the m/2 root. Once derived it remains available if
// the account is subsequently locked, matching the Python manager.
func (manager *DeterministicChannelKeyManager) PrivateKey() (*keys.PrivateKey, error) {
	if manager == nil {
		return nil, fmt.Errorf("%w: channel-key manager has no account", ErrInvalidAccountData)
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.privateKeyLocked()
}

func (manager *DeterministicChannelKeyManager) privateKeyLocked() (*keys.PrivateKey, error) {
	if manager == nil || manager.Account == nil {
		return nil, fmt.Errorf("%w: channel-key manager has no account", ErrInvalidAccountData)
	}
	if manager.privateKey == nil && manager.Account.PrivateKey != nil {
		privateKey, err := manager.Account.PrivateKey.Child(ChannelChain)
		if err != nil {
			return nil, err
		}
		manager.privateKey = privateKey
	}
	return manager.privateKey, nil
}

func (manager *DeterministicChannelKeyManager) GenerateNextKey(
	isUsed ChannelKeyUsage,
) (*keys.PrivateKey, error) {
	if manager == nil {
		return nil, fmt.Errorf("%w: channel-key manager has no account", ErrInvalidAccountData)
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.generateNextKeyLocked(isUsed)
}

func (manager *DeterministicChannelKeyManager) generateNextKeyLocked(
	isUsed ChannelKeyUsage,
) (*keys.PrivateKey, error) {
	privateKey, err := manager.privateKeyLocked()
	if err != nil {
		return nil, err
	}
	if privateKey == nil {
		return nil, ErrAccountPrivateKeyMissing
	}
	if isUsed == nil {
		return nil, ErrChannelKeyUsageUnavailable
	}
	for {
		nextPrivateKey, err := privateKey.Child(manager.LastKnown)
		if err != nil {
			return nil, err
		}
		publicKey := nextPrivateKey.PublicKey()
		manager.Cache[publicKey.Address()] = nextPrivateKey
		used, err := isUsed(manager.Account, publicKey)
		if err != nil {
			return nil, err
		}
		if !used {
			return nextPrivateKey, nil
		}
		manager.LastKnown++
	}
}

func (manager *DeterministicChannelKeyManager) EnsureCachePrimed(
	isUsed ChannelKeyUsage,
) error {
	if manager == nil {
		return fmt.Errorf("%w: channel-key manager has no account", ErrInvalidAccountData)
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	privateKey, err := manager.privateKeyLocked()
	if err != nil {
		return err
	}
	if privateKey == nil {
		return nil
	}
	_, err = manager.generateNextKeyLocked(isUsed)
	return err
}

// MaybeGenerateForChannel observes only the current candidate. It never scans
// ahead, and increments LastKnown only on an exact compressed-key match.
func (manager *DeterministicChannelKeyManager) MaybeGenerateForChannel(
	publicKeyBytes []byte,
) (bool, error) {
	if manager == nil {
		return false, fmt.Errorf("%w: channel-key manager has no account", ErrInvalidAccountData)
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	privateKey, err := manager.privateKeyLocked()
	if err != nil {
		return false, err
	}
	if privateKey == nil {
		return false, nil
	}
	nextPrivateKey, err := privateKey.Child(manager.LastKnown)
	if err != nil {
		return false, err
	}
	publicKey := nextPrivateKey.PublicKey()
	if !equalBytes(publicKey.CompressedBytes(), publicKeyBytes) {
		return false, nil
	}
	manager.Cache[publicKey.Address()] = nextPrivateKey
	manager.LastKnown++
	return true, nil
}

func (manager *DeterministicChannelKeyManager) GetPrivateKey(address string) *keys.PrivateKey {
	if manager == nil {
		return nil
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.Cache[address]
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
