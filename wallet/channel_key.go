package wallet

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

// Branch of an account's key tree that holds the keys of its channels (lbry-sdk KeyPath.CHANNEL).
const channelChain = 2

// How many deterministic channel keys are tried before giving up on finding one.
const maximumChannelKeys = 1000

var extendedPrivateKeyPrefix = []byte{0x04, 0x88, 0xad, 0xe4}

// ChannelPublicKey is the compressed public key of a channel. A channel claim holds it either as the 33 bytes
// or as a DER encoded SubjectPublicKeyInfo.
func ChannelPublicKey(claimKey []byte) ([]byte, error) {
	if len(claimKey) == 33 {
		if _, err := secp256k1.ParsePubKey(claimKey); err != nil {
			return nil, fmt.Errorf("invalid public key: %w", err)
		}
		return claimKey, nil
	}
	var info struct {
		Algorithm pkix.AlgorithmIdentifier
		Key       asn1.BitString
	}
	if rest, err := asn1.Unmarshal(claimKey, &info); err != nil || len(rest) > 0 {
		return nil, errors.New("the channel's public key is not understood")
	}
	key, err := secp256k1.ParsePubKey(info.Key.Bytes)
	if err != nil {
		return nil, fmt.Errorf("invalid public key: %w", err)
	}
	return key.SerializeCompressed(), nil
}

// ChannelPrivateKey finds the signing key of the channel with this public key in the account: the PEM
// "certificates" first, then the keys derived from the account's own key, which needs the account unlocked.
// A nil key means the account does not have it.
func (account *Account) ChannelPrivateKey(publicKey []byte) (*secp256k1.PrivateKey, error) {
	publicKey, err := ChannelPublicKey(publicKey)
	if err != nil {
		return nil, err
	}
	address := AddressFromPublicKey(publicKey)

	if raw, has := account.Certificates[address]; has {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, errors.New("the channel's certificate is not a PEM string")
		}
		key, err := privateKeyFromPEM(text)
		if err != nil {
			return nil, err
		}
		if AddressFromPublicKey(key.PubKey().SerializeCompressed()) != address {
			return nil, errors.New("the channel's certificate holds another key")
		}
		return key, nil
	}

	if account.Locked || account.PrivateKey == "" {
		return nil, nil
	}
	return account.deriveChannelKey(address)
}

// deriveChannelKey walks the channel keys of the account (m/2/n) until one has the address.
func (account *Account) deriveChannelKey(address string) (*secp256k1.PrivateKey, error) {
	root, err := parseExtendedPrivateKey(account.PrivateKey)
	if err != nil {
		return nil, err
	}
	channels, err := root.child(channelChain)
	if err != nil {
		return nil, err
	}
	for n := uint32(0); n < maximumChannelKeys; n++ {
		child, err := channels.child(n)
		if err != nil {
			continue // astronomically rare: that index is invalid, BIP32 skips it
		}
		if AddressFromPublicKey(child.key.PubKey().SerializeCompressed()) == address {
			return child.key, nil
		}
	}
	return nil, nil
}

type extendedPrivateKey struct {
	key       *secp256k1.PrivateKey
	chainCode []byte
}

// parseExtendedPrivateKey reads an `xprv...` string with the LBRY version bytes.
func parseExtendedPrivateKey(extended string) (extendedPrivateKey, error) {
	payload, err := base58CheckDecode(extended)
	if err != nil || len(payload) != 78 || string(payload[:4]) != string(extendedPrivateKeyPrefix) || payload[45] != 0 {
		return extendedPrivateKey{}, errors.New("the account's private key is not understood")
	}
	return extendedPrivateKey{key: secp256k1.PrivKeyFromBytes(payload[46:]), chainCode: payload[13:45]}, nil
}

// child is BIP32 private derivation of a non-hardened child.
func (parent extendedPrivateKey) child(n uint32) (extendedPrivateKey, error) {
	mac := hmac.New(sha512.New, parent.chainCode)
	mac.Write(parent.key.PubKey().SerializeCompressed())
	mac.Write(binary.BigEndian.AppendUint32(nil, n))
	sum := mac.Sum(nil)

	var tweak, derived secp256k1.ModNScalar
	if overflow := tweak.SetByteSlice(sum[:32]); overflow || tweak.IsZero() {
		return extendedPrivateKey{}, errors.New("invalid child key")
	}
	derived.Set(&parent.key.Key).Add(&tweak)
	if derived.IsZero() {
		return extendedPrivateKey{}, errors.New("invalid child key")
	}
	return extendedPrivateKey{key: secp256k1.NewPrivateKey(&derived), chainCode: sum[32:]}, nil
}

// privateKeyFromPEM reads a secp256k1 key from a PEM block, a SEC1 "EC PRIVATE KEY" or a PKCS8 "PRIVATE KEY",
// which are the two forms lbry-sdk reads.
func privateKeyFromPEM(text string) (*secp256k1.PrivateKey, error) {
	block, _ := pem.Decode([]byte(text))
	if block == nil {
		return nil, errors.New("the channel's certificate is not PEM")
	}
	der := block.Bytes
	if block.Type == "PRIVATE KEY" {
		var info struct {
			Version   int
			Algorithm pkix.AlgorithmIdentifier
			Key       []byte
		}
		if _, err := asn1.Unmarshal(der, &info); err != nil {
			return nil, errors.New("the channel's certificate is not understood")
		}
		der = info.Key
	}
	var ec struct {
		Version    int
		PrivateKey []byte
		Curve      asn1.ObjectIdentifier `asn1:"optional,explicit,tag:0"`
		PublicKey  asn1.BitString        `asn1:"optional,explicit,tag:1"`
	}
	if _, err := asn1.Unmarshal(der, &ec); err != nil || len(ec.PrivateKey) == 0 || len(ec.PrivateKey) > 32 {
		return nil, errors.New("the channel's certificate is not understood")
	}
	padded := make([]byte, 32)
	copy(padded[32-len(ec.PrivateKey):], ec.PrivateKey)
	return secp256k1.PrivKeyFromBytes(padded), nil
}

// SignChannelData is lbry-sdk's Output.sign_data: sha256 of the salt, the channel's claim hash and the data,
// signed as 64 bytes, r then s, in hexadecimal.
func SignChannelData(key *secp256k1.PrivateKey, claimHash []byte, data []byte, salt string) string {
	digest := sha256.New()
	digest.Write([]byte(salt))
	digest.Write(claimHash)
	digest.Write(data)

	signature := ecdsa.Sign(key, digest.Sum(nil))
	r, s := signature.R(), signature.S()
	rBytes, sBytes := r.Bytes(), s.Bytes()
	return fmt.Sprintf("%x%x", rBytes[:], sBytes[:])
}
