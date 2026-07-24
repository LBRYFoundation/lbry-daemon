package wallet

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"lbry/daemon/wallet/keys"
)

var ErrAccountWalletMissing = errors.New("account has no owning wallet")

type ChannelKeyFallback func(address string) (*keys.PrivateKey, error)

func (account *Account) AddChannelPrivateKey(privateKey *keys.PrivateKey) error {
	if account == nil || account.ChannelKeys == nil {
		return fmt.Errorf("%w: channel-key dictionary is unavailable", ErrInvalidAccountData)
	}
	if privateKey == nil {
		return keys.ErrInvalidPrivateKey
	}
	encoded, err := privateKey.ToPEM()
	if err != nil {
		return err
	}
	account.ChannelKeys.Set(privateKey.Address(), encoded)
	return nil
}

// GetChannelPrivateKey checks imported PEM keys before invoking the injected
// deterministic-key resolver. A truthy malformed imported value blocks the
// fallback, matching Account.get_channel_private_key.
func (account *Account) GetChannelPrivateKey(
	publicKeyBytes []byte, fallback ChannelKeyFallback,
) (*keys.PrivateKey, error) {
	if account == nil || account.ChannelKeys == nil {
		return nil, fmt.Errorf("%w: channel-key dictionary is unavailable", ErrInvalidAccountData)
	}
	address, err := keys.AddressFromPublicKeyBytes(account.Network, publicKeyBytes)
	if err != nil {
		return nil, err
	}
	if value, exists := account.ChannelKeys.Get(address); exists && pythonJSONTruthy(value) {
		encoded, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("channel key %q has type %T, want PEM string", address, value)
		}
		return keys.PrivateKeyFromPEM(account.Network, encoded)
	}
	if fallback == nil {
		if account.DeterministicChannelKeys == nil {
			return nil, nil
		}
		return account.DeterministicChannelKeys.GetPrivateKey(address), nil
	}
	return fallback(address)
}

// MigrateChannelKeys filters legacy certificate values, reparses qualifying
// PEM blocks, rekeys them by derived address, and saves once only after a full
// successful rebuild. A save failure leaves the rebuilt in-memory dictionary.
func (account *Account) MigrateChannelKeys() (bool, error) {
	if account == nil || account.ChannelKeys == nil || account.ChannelKeys.Len() == 0 {
		return false, nil
	}
	migrated := NewObject()
	for _, member := range account.ChannelKeys.Members() {
		encoded, ok := member.Value.(string)
		if !ok || !strings.HasPrefix(encoded, "-----BEGIN") {
			continue
		}
		privateKey, err := keys.PrivateKeyFromPEM(account.Network, encoded)
		if err != nil {
			return false, err
		}
		migrated.Set(privateKey.Address(), encoded)
	}
	if channelKeyObjectsEqual(account.ChannelKeys, migrated) {
		return false, nil
	}
	account.ChannelKeys = migrated
	if account.wallet == nil {
		return true, ErrAccountWalletMissing
	}
	_, err := account.wallet.Save()
	return true, err
}

func channelKeyObjectsEqual(left, right *Object) bool {
	if left == nil || right == nil || left.Len() != right.Len() {
		return left == right
	}
	for _, member := range left.Members() {
		value, exists := right.Get(member.Key)
		if !exists || !reflect.DeepEqual(member.Value, value) {
			return false
		}
	}
	return true
}
