package keys

import (
	"errors"
	"fmt"
)

// Network identifies one of the ledger parameter sets understood by the
// pinned SDK. Its fields are exposed through methods so callers cannot mutate
// key prefixes after a key has been constructed.
type Network uint8

const (
	MainNet Network = iota
	TestNet
	RegTest
)

var ErrUnknownNetwork = errors.New("unknown LBRY key network")

var networkIDs = [...]string{
	"lbc_mainnet",
	"lbc_testnet",
	"lbc_regtest",
}

var extendedPublicKeyPrefixes = [...][4]byte{
	{0x04, 0x88, 0xb2, 0x1e},
	{0x04, 0x35, 0x87, 0xcf},
	{0x04, 0x35, 0x87, 0xcf},
}

var extendedPrivateKeyPrefixes = [...][4]byte{
	{0x04, 0x88, 0xad, 0xe4},
	{0x04, 0x35, 0x83, 0x94},
	{0x04, 0x35, 0x83, 0x94},
}

func ParseNetwork(id string) (Network, error) {
	for network, candidate := range networkIDs {
		if candidate == id {
			return Network(network), nil
		}
	}
	return 0, fmt.Errorf("%w: %q", ErrUnknownNetwork, id)
}

func (network Network) valid() bool {
	return int(network) < len(networkIDs)
}

func (network Network) ID() string {
	if !network.valid() {
		return ""
	}
	return networkIDs[network]
}

func (network Network) PubKeyAddressPrefix() byte {
	if network == MainNet {
		return 0x55
	}
	if network == TestNet || network == RegTest {
		return 0x6f
	}
	return 0
}

func (network Network) ScriptAddressPrefix() byte {
	if network == MainNet {
		return 0x7a
	}
	if network == TestNet || network == RegTest {
		return 0xc4
	}
	return 0
}

// SecretPrefix is shared by all three Python ledger classes. The legacy WIF
// method returns this byte followed by the private scalar and 0x01 without
// Base58Check encoding.
func (network Network) SecretPrefix() byte {
	if network.valid() {
		return 0x1c
	}
	return 0
}

func (network Network) ExtendedPublicKeyPrefix() [4]byte {
	if !network.valid() {
		return [4]byte{}
	}
	return extendedPublicKeyPrefixes[network]
}

func (network Network) ExtendedPrivateKeyPrefix() [4]byte {
	if !network.valid() {
		return [4]byte{}
	}
	return extendedPrivateKeyPrefixes[network]
}
