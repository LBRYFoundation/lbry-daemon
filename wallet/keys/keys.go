package keys

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"fmt"

	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"
)

const (
	chainCodeLength   = 32
	extendedKeyLength = 78
	hardenedIndex     = int64(1 << 31)
)

var (
	ErrInvalidChainCode   = errors.New("invalid chain code")
	ErrInvalidChildNumber = errors.New("invalid child number")
	ErrInvalidDepth       = errors.New("invalid depth")
	ErrInvalidPrivateKey  = errors.New("invalid private key")
	ErrInvalidPublicKey   = errors.New("invalid public key")
	ErrInvalidExtendedKey = errors.New("invalid extended key")
	ErrUnknownKeyVersion  = errors.New("extended key version bytes unrecognised")
	ErrInvalidDerivation  = errors.New("invalid BIP32 derivation")
)

// ExtendedKey is the common read-only surface of legacy extended private and
// public keys.
type ExtendedKey interface {
	Network() Network
	ChainCode() [chainCodeLength]byte
	ChildNumber() uint32
	Depth() uint8
	Identifier() [20]byte
	Fingerprint() [4]byte
	ExtendedKeyBytes() []byte
	ExtendedKeyString() string
	IsPrivate() bool
	isExtendedKey()
}

type keyBase struct {
	network     Network
	chainCode   [chainCodeLength]byte
	childNumber uint32
	depth       uint8
}

type PrivateKey struct {
	keyBase
	key    *secp256k1.PrivateKey
	parent *PrivateKey
}

type PublicKey struct {
	keyBase
	key    *secp256k1.PublicKey
	parent *PublicKey
}

func validateMetadata(network Network, chainCode []byte, childNumber int64, depth int) (keyBase, error) {
	if !network.valid() {
		return keyBase{}, fmt.Errorf("%w: %d", ErrUnknownNetwork, network)
	}
	if len(chainCode) != chainCodeLength {
		return keyBase{}, fmt.Errorf("%w: got %d bytes, want %d", ErrInvalidChainCode, len(chainCode), chainCodeLength)
	}
	if childNumber < 0 || childNumber >= 1<<32 {
		return keyBase{}, fmt.Errorf("%w: %d", ErrInvalidChildNumber, childNumber)
	}
	if depth < 0 || depth >= 1<<8 {
		return keyBase{}, fmt.Errorf("%w: %d", ErrInvalidDepth, depth)
	}
	base := keyBase{network: network, childNumber: uint32(childNumber), depth: uint8(depth)}
	copy(base.chainCode[:], chainCode)
	return base, nil
}

// NewPrivateKey validates and constructs a legacy extended private key. A nil
// parent intentionally causes a zero serialized parent fingerprint, even when
// depth is nonzero, matching keys parsed by the Python SDK.
func NewPrivateKey(
	network Network, privateKey, chainCode []byte, childNumber int64, depth int, parent *PrivateKey,
) (*PrivateKey, error) {
	base, err := validateMetadata(network, chainCode, childNumber, depth)
	if err != nil {
		return nil, err
	}
	scalar, err := parsePrivateScalar(privateKey)
	if err != nil {
		return nil, err
	}
	return &PrivateKey{keyBase: base, key: secp256k1.NewPrivateKey(&scalar), parent: parent}, nil
}

// NewPublicKey validates and constructs a legacy extended public key. Only the
// 33-byte compressed form accepted by the Python constructor is supported.
func NewPublicKey(
	network Network, publicKey, chainCode []byte, childNumber int64, depth int, parent *PublicKey,
) (*PublicKey, error) {
	base, err := validateMetadata(network, chainCode, childNumber, depth)
	if err != nil {
		return nil, err
	}
	parsed, err := parseCompressedPublicKey(publicKey)
	if err != nil {
		return nil, err
	}
	return &PublicKey{keyBase: base, key: parsed, parent: parent}, nil
}

// PublicKeyFromCompressed creates the network-bound form of Python's
// PublicKey.from_compressed, with an empty chain code and root metadata.
// Ledgerless verification keys are added with the signing/verification slice.
func PublicKeyFromCompressed(network Network, publicKey []byte) (*PublicKey, error) {
	return NewPublicKey(network, publicKey, make([]byte, chainCodeLength), 0, 0, nil)
}

// PrivateKeyFromSeed implements the BIP32 master-key HMAC used by the pinned
// SDK. It expects the already-derived binary mnemonic seed.
func PrivateKeyFromSeed(network Network, seed []byte) (*PrivateKey, error) {
	hasher := hmac.New(sha512.New, []byte("Bitcoin seed"))
	_, _ = hasher.Write(seed)
	material := hasher.Sum(nil)
	return NewPrivateKey(network, material[:32], material[32:], 0, 0, nil)
}

func parsePrivateScalar(serialized []byte) (secp256k1.ModNScalar, error) {
	var scalar secp256k1.ModNScalar
	if len(serialized) != secp256k1.PrivKeyBytesLen {
		return scalar, fmt.Errorf("%w: got %d bytes, want %d", ErrInvalidPrivateKey, len(serialized), secp256k1.PrivKeyBytesLen)
	}
	var bytes32 [32]byte
	copy(bytes32[:], serialized)
	if overflow := scalar.SetBytes(&bytes32); overflow != 0 || scalar.IsZero() {
		return secp256k1.ModNScalar{}, ErrInvalidPrivateKey
	}
	return scalar, nil
}

func parseCompressedPublicKey(serialized []byte) (*secp256k1.PublicKey, error) {
	if len(serialized) != secp256k1.PubKeyBytesLenCompressed {
		return nil, fmt.Errorf("%w: got %d bytes, want %d", ErrInvalidPublicKey, len(serialized), secp256k1.PubKeyBytesLenCompressed)
	}
	if serialized[0] != secp256k1.PubKeyFormatCompressedEven && serialized[0] != secp256k1.PubKeyFormatCompressedOdd {
		return nil, fmt.Errorf("%w: invalid compressed prefix 0x%02x", ErrInvalidPublicKey, serialized[0])
	}
	publicKey, err := secp256k1.ParsePubKey(serialized)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPublicKey, err)
	}
	return publicKey, nil
}

func (base keyBase) Network() Network                 { return base.network }
func (base keyBase) ChainCode() [chainCodeLength]byte { return base.chainCode }
func (base keyBase) ChildNumber() uint32              { return base.childNumber }
func (base keyBase) Depth() uint8                     { return base.depth }

func (key *PrivateKey) Network() Network                 { return key.keyBase.Network() }
func (key *PrivateKey) ChainCode() [chainCodeLength]byte { return key.keyBase.ChainCode() }
func (key *PrivateKey) ChildNumber() uint32              { return key.keyBase.ChildNumber() }
func (key *PrivateKey) Depth() uint8                     { return key.keyBase.Depth() }
func (key *PrivateKey) IsPrivate() bool                  { return true }
func (key *PrivateKey) isExtendedKey()                   {}
func (key *PrivateKey) Parent() *PrivateKey              { return key.parent }

func (key *PublicKey) Network() Network                 { return key.keyBase.Network() }
func (key *PublicKey) ChainCode() [chainCodeLength]byte { return key.keyBase.ChainCode() }
func (key *PublicKey) ChildNumber() uint32              { return key.keyBase.ChildNumber() }
func (key *PublicKey) Depth() uint8                     { return key.keyBase.Depth() }
func (key *PublicKey) IsPrivate() bool                  { return false }
func (key *PublicKey) isExtendedKey()                   {}
func (key *PublicKey) Parent() *PublicKey               { return key.parent }

func (key *PrivateKey) PrivateKeyBytes() []byte {
	return key.key.Serialize()
}

func (key *PrivateKey) PublicKey() *PublicKey {
	var parent *PublicKey
	if key.parent != nil {
		parent = key.parent.PublicKey()
	}
	return &PublicKey{
		keyBase: key.keyBase,
		key:     key.key.PubKey(),
		parent:  parent,
	}
}

func (key *PublicKey) CompressedBytes() []byte {
	return key.key.SerializeCompressed()
}

func (key *PrivateKey) Identifier() [20]byte { return key.PublicKey().Identifier() }
func (key *PublicKey) Identifier() [20]byte  { return Hash160(key.CompressedBytes()) }

func fingerprint(identifier [20]byte) [4]byte {
	var result [4]byte
	copy(result[:], identifier[:4])
	return result
}

func (key *PrivateKey) Fingerprint() [4]byte { return fingerprint(key.Identifier()) }
func (key *PublicKey) Fingerprint() [4]byte  { return fingerprint(key.Identifier()) }

func (key *PrivateKey) parentFingerprint() [4]byte {
	if key.parent == nil {
		return [4]byte{}
	}
	return key.parent.Fingerprint()
}

func (key *PublicKey) parentFingerprint() [4]byte {
	if key.parent == nil {
		return [4]byte{}
	}
	return key.parent.Fingerprint()
}

func extendedKeyBytes(base keyBase, parentFingerprint [4]byte, version [4]byte, serializedKey []byte) []byte {
	result := make([]byte, extendedKeyLength)
	copy(result[:4], version[:])
	result[4] = base.depth
	copy(result[5:9], parentFingerprint[:])
	binary.BigEndian.PutUint32(result[9:13], base.childNumber)
	copy(result[13:45], base.chainCode[:])
	copy(result[45:], serializedKey)
	return result
}

func (key *PrivateKey) ExtendedKeyBytes() []byte {
	serialized := make([]byte, 33)
	copy(serialized[1:], key.PrivateKeyBytes())
	return extendedKeyBytes(
		key.keyBase, key.parentFingerprint(), key.network.ExtendedPrivateKeyPrefix(), serialized,
	)
}

func (key *PublicKey) ExtendedKeyBytes() []byte {
	return extendedKeyBytes(
		key.keyBase, key.parentFingerprint(), key.network.ExtendedPublicKeyPrefix(), key.CompressedBytes(),
	)
}

func (key *PrivateKey) ExtendedKeyString() string { return EncodeBase58Check(key.ExtendedKeyBytes()) }
func (key *PublicKey) ExtendedKeyString() string  { return EncodeBase58Check(key.ExtendedKeyBytes()) }

func address(network Network, identifier [20]byte) string {
	payload := make([]byte, 21)
	payload[0] = network.PubKeyAddressPrefix()
	copy(payload[1:], identifier[:])
	return EncodeBase58Check(payload)
}

func (key *PrivateKey) Address() string { return address(key.network, key.Identifier()) }
func (key *PublicKey) Address() string  { return address(key.network, key.Identifier()) }

// AddressFromPublicKeyBytes mirrors Ledger.public_key_to_address. The pinned
// helper hashes arbitrary bytes and does not validate a secp256k1 point.
func AddressFromPublicKeyBytes(network Network, publicKey []byte) (string, error) {
	if !network.valid() {
		return "", fmt.Errorf("%w: %d", ErrUnknownNetwork, network)
	}
	return address(network, Hash160(publicKey)), nil
}

// LegacyWIFBytes preserves the pinned SDK's private_key_to_wif behavior. It is
// raw prefix/key/compression-marker bytes, despite the Python method's name.
func (key *PrivateKey) LegacyWIFBytes() []byte {
	result := make([]byte, 0, 34)
	result = append(result, key.network.SecretPrefix())
	result = append(result, key.PrivateKeyBytes()...)
	return append(result, 0x01)
}

func (key *PrivateKey) Child(index int64) (*PrivateKey, error) {
	if index < 0 || index >= 1<<32 {
		return nil, fmt.Errorf("%w: invalid BIP32 private key child number %d", ErrInvalidChildNumber, index)
	}
	if key.depth == 255 {
		return nil, fmt.Errorf("%w: child depth exceeds 255", ErrInvalidDepth)
	}
	message := make([]byte, 0, 37)
	if index >= hardenedIndex {
		message = append(message, 0)
		message = append(message, key.PrivateKeyBytes()...)
	} else {
		message = append(message, key.PublicKey().CompressedBytes()...)
	}
	var childNumber [4]byte
	binary.BigEndian.PutUint32(childNumber[:], uint32(index))
	message = append(message, childNumber[:]...)
	left, right := key.deriveMaterial(message)
	derived, err := addPrivateTweak(key.key, left[:])
	if err != nil {
		return nil, err
	}
	return &PrivateKey{
		keyBase: keyBase{
			network:     key.network,
			chainCode:   right,
			childNumber: uint32(index),
			depth:       key.depth + 1,
		},
		key:    derived,
		parent: key,
	}, nil
}

func (key *PublicKey) Child(index int64) (*PublicKey, error) {
	if index < 0 || index >= hardenedIndex {
		return nil, fmt.Errorf("%w: invalid BIP32 public key child number %d", ErrInvalidChildNumber, index)
	}
	if key.depth == 255 {
		return nil, fmt.Errorf("%w: child depth exceeds 255", ErrInvalidDepth)
	}
	message := append([]byte(nil), key.CompressedBytes()...)
	var childNumber [4]byte
	binary.BigEndian.PutUint32(childNumber[:], uint32(index))
	message = append(message, childNumber[:]...)
	left, right := key.deriveMaterial(message)
	derived, err := addPublicTweak(key.key, left[:])
	if err != nil {
		return nil, err
	}
	return &PublicKey{
		keyBase: keyBase{
			network:     key.network,
			chainCode:   right,
			childNumber: uint32(index),
			depth:       key.depth + 1,
		},
		key:    derived,
		parent: key,
	}, nil
}

func (base keyBase) deriveMaterial(message []byte) ([32]byte, [32]byte) {
	hasher := hmac.New(sha512.New, base.chainCode[:])
	_, _ = hasher.Write(message)
	material := hasher.Sum(nil)
	var left, right [32]byte
	copy(left[:], material[:32])
	copy(right[:], material[32:])
	return left, right
}

func derivationTweak(serialized []byte) (secp256k1.ModNScalar, error) {
	var tweak secp256k1.ModNScalar
	if len(serialized) != 32 {
		return tweak, fmt.Errorf("%w: tweak has %d bytes", ErrInvalidDerivation, len(serialized))
	}
	var bytes32 [32]byte
	copy(bytes32[:], serialized)
	// Coincurve's add methods pass zero through to libsecp256k1, where it is
	// valid and leaves the parent key unchanged.
	if overflow := tweak.SetBytes(&bytes32); overflow != 0 {
		return secp256k1.ModNScalar{}, fmt.Errorf("%w: invalid child tweak", ErrInvalidDerivation)
	}
	return tweak, nil
}

func addPrivateTweak(parent *secp256k1.PrivateKey, serialized []byte) (*secp256k1.PrivateKey, error) {
	tweak, err := derivationTweak(serialized)
	if err != nil {
		return nil, err
	}
	var child secp256k1.ModNScalar
	child.Add2(&parent.Key, &tweak)
	if child.IsZero() {
		return nil, fmt.Errorf("%w: derived private key is zero", ErrInvalidDerivation)
	}
	return secp256k1.NewPrivateKey(&child), nil
}

func addPublicTweak(parent *secp256k1.PublicKey, serialized []byte) (*secp256k1.PublicKey, error) {
	tweak, err := derivationTweak(serialized)
	if err != nil {
		return nil, err
	}
	var parentPoint, tweakPoint, child secp256k1.JacobianPoint
	parent.AsJacobian(&parentPoint)
	secp256k1.ScalarBaseMultNonConst(&tweak, &tweakPoint)
	secp256k1.AddNonConst(&parentPoint, &tweakPoint, &child)
	if child.Z.IsZero() {
		return nil, fmt.Errorf("%w: derived public key is the point at infinity", ErrInvalidDerivation)
	}
	child.ToAffine()
	return secp256k1.NewPublicKey(&child.X, &child.Y), nil
}

// ParseExtendedKey decodes a Base58Check xprv/xpub (or tprv/tpub) using the
// supplied ledger network.
func ParseExtendedKey(network Network, encoded string) (ExtendedKey, error) {
	payload, err := DecodeBase58Check(encoded)
	if err != nil {
		return nil, err
	}
	return ParseExtendedKeyBytes(network, payload)
}

// ParseExtendedKeyBytes intentionally ignores the serialized parent
// fingerprint. The pinned SDK does not retain it when parsing and therefore
// writes zeroes if the parsed key is serialized again.
func ParseExtendedKeyBytes(network Network, extended []byte) (ExtendedKey, error) {
	if !network.valid() {
		return nil, fmt.Errorf("%w: %d", ErrUnknownNetwork, network)
	}
	if len(extended) != extendedKeyLength {
		return nil, fmt.Errorf("%w: got %d bytes, want %d", ErrInvalidExtendedKey, len(extended), extendedKeyLength)
	}
	version := extended[:4]
	depth := int(extended[4])
	childNumber := int64(binary.BigEndian.Uint32(extended[9:13]))
	chainCode := extended[13:45]
	serializedKey := extended[45:]
	publicPrefix := network.ExtendedPublicKeyPrefix()
	privatePrefix := network.ExtendedPrivateKeyPrefix()
	switch {
	case bytes.Equal(version, publicPrefix[:]):
		return NewPublicKey(network, serializedKey, chainCode, childNumber, depth, nil)
	case bytes.Equal(version, privatePrefix[:]):
		if serializedKey[0] != 0 {
			return nil, fmt.Errorf("%w: invalid extended private key prefix byte", ErrInvalidExtendedKey)
		}
		return NewPrivateKey(network, serializedKey[1:], chainCode, childNumber, depth, nil)
	default:
		return nil, ErrUnknownKeyVersion
	}
}
