package wallet

import (
	"fmt"

	"lbry/daemon/wallet/keys"
	"lbry/daemon/wallet/mnemonic"
)

// GenerateAccount mirrors Account.generate: create a fresh English mnemonic
// and let NewAccount derive all key material and generator defaults from it.
func GenerateAccount(network keys.Network, name, generator string) (*Account, error) {
	if generator != DeterministicChainGenerator && generator != SingleAddressGenerator {
		return nil, fmt.Errorf("%w: %s", ErrUnknownAddressGenerator, generator)
	}
	seed, err := mnemonic.NewEnglish().MakeDefaultSeed()
	if err != nil {
		return nil, err
	}
	return NewAccount(network, NewObject(
		Member{Key: "name", Value: name},
		Member{Key: "seed", Value: seed},
		Member{Key: "address_generator", Value: NewObject(
			Member{Key: "name", Value: generator},
		)},
	))
}
