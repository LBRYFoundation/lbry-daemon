package wallet

import (
	"slices"
	"testing"
)

// An extended public key whose key is the secp256k1 generator point and whose chain code is the bytes 0..31.
// The addresses below were derived from it with an independent implementation.
const (
	publicKeyG = "xpub661MyMwAqRbcEYSGagKuFUqExQV8d2eizDP5SamP9TcLeqAk9JsrNexcG7MDch4KFDn8q8MASAtJEQfviZUhf6FVHbir7V5wN9h8zrFNiQg"
	idG        = "bPQYFPE6iQFEfA3MJSRR3WYrczLjWVjiWG"
)

// BIP32 test vector 1: the public derivation of m/0H/1 from the extended public key of m/0H.
func TestChildMatchesBIP32TestVector(t *testing.T) {
	parent, err := ParseExtendedPublicKey("xpub68Gmy5EdvgibQVfPdqkBBCHxA5htiqg55crXYuXoQRKfDBFA1WEjWgP6LHhwBZeNK1VTsfTFUHCdrfp1bgwQ9xv5ski8PX9rL2dZXvgGDnw")
	if err != nil {
		t.Fatal(err)
	}
	child, err := parent.Child(1)
	if err != nil {
		t.Fatal(err)
	}
	const want = "xpub6ASuArnXKPbfEwhqN6e3mwBcDTgzisQN1wXN9BJcM47sSikHjJf3UFHKkNAWbWMiGj7Wf5uMash7SyYq527Hqck2AxYysAA7xmALppuCkwQ"
	if got := child.String(); got != want {
		t.Errorf("m/0H/1 = %s, want %s", got, want)
	}
}

func TestExtendedPublicKeyRoundTrips(t *testing.T) {
	key, err := ParseExtendedPublicKey(publicKeyG)
	if err != nil || key.String() != publicKeyG {
		t.Fatalf("round trip failed: %v", err)
	}
}

func TestParseExtendedPublicKeyRejects(t *testing.T) {
	for name, value := range map[string]string{
		"empty":        "",
		"bad checksum": publicKeyG[:len(publicKeyG)-1] + "a",
		"private key":  "xprv9s21ZrQH143K3QTDL4LXw2F7HEK3wJUD2nW2nRk4stbPy6cq3jPPqjiChkVvvNKmPGJxWUtg6LnF5kejMRNNU3TGtRBeJgk33yuGBxrMPHi",
	} {
		if _, err := ParseExtendedPublicKey(value); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestChildRejectsHardenedIndexes(t *testing.T) {
	key, _ := ParseExtendedPublicKey(publicKeyG)
	if _, err := key.Child(1 << 31); err == nil {
		t.Error("a hardened index needs the private key")
	}
}

// Addresses computed with an independent implementation (pure Python secp256k1, hmac, hashlib ripemd160)
// from publicKeyG: chain 0 is receiving, chain 1 change.
func TestDeriveAddresses(t *testing.T) {
	receiving, err := DeriveAddresses(publicKeyG, ReceivingChain, 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"bPG2gs7buobagNmQi2kmMNy3D7TjrwqbAG", "bJEiScD6S7aBEMpprUUNdfy7pPxQYCMTHW", "bbA6E4iHaLEw1PsVTiJnPvZFvKqVmkSZ5V"}; !slices.Equal(receiving, want) {
		t.Errorf("receiving = %v", receiving)
	}
	change, err := DeriveAddresses(publicKeyG, ChangeChain, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"bVGGnLSzDQ5nDtwAg4fuFkAcom6EPCeVSt", "bMWyS46baDTRgH3LkcLB7kJfNJv13YuyEY"}; !slices.Equal(change, want) {
		t.Errorf("change = %v", change)
	}
	// Starting later gives the same addresses as skipping ahead.
	later, _ := DeriveAddresses(publicKeyG, ReceivingChain, 1, 2)
	if !slices.Equal(later, receiving[1:]) {
		t.Errorf("start index: %v", later)
	}
}

func TestAccountAddressesUseTheGaps(t *testing.T) {
	account := Account{PublicKey: publicKeyG}
	for chain, want := range map[uint32]int{ReceivingChain: 20, ChangeChain: 6} {
		addresses, err := account.Addresses(chain, -1)
		if err != nil || len(addresses) != want {
			t.Errorf("chain %d: %d addresses, %v, want %d", chain, len(addresses), err, want)
		}
	}
	custom := Account{PublicKey: publicKeyG, AddressGenerator: AddressGenerator{Name: ADDRESS_GENERATOR_NAME_DETERMINISTIC_CHAIN, Receiving: AddressGeneratorChain{Gap: 4, MaximumUsesPerAddress: 1}}}
	if addresses, _ := custom.Addresses(ReceivingChain, -1); len(addresses) != 4 {
		t.Errorf("custom gap: %d addresses", len(addresses))
	}
	if addresses, _ := custom.Addresses(ReceivingChain, 2); len(addresses) != 2 {
		t.Errorf("explicit count: %d addresses", len(addresses))
	}
}

// With the single-address generator both chains use the account's own address.
func TestAccountAddressesSingleAddress(t *testing.T) {
	account := Account{PublicKey: publicKeyG, AddressGenerator: AddressGenerator{Name: ADDRESS_GENERATOR_NAME_SINGLE_ADDRESS}}
	for _, chain := range []uint32{ReceivingChain, ChangeChain} {
		if addresses, err := account.Addresses(chain, -1); err != nil || !slices.Equal(addresses, []string{idG}) {
			t.Errorf("chain %d: %v %v", chain, addresses, err)
		}
	}
}
