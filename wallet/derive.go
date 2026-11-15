package wallet

import "crypto/hmac"
import "crypto/sha256"
import "crypto/sha512"
import "encoding/binary"
import "encoding/hex"
import "errors"
import "fmt"

import "github.com/decred/dcrd/dcrec/secp256k1/v4"
import "golang.org/x/crypto/ripemd160"

// ExtendedPublicKey is a BIP32 extended public key. Only public derivation of non-hardened children is
// supported: no private keys are handled.
type ExtendedPublicKey struct {
	depth             byte
	parentFingerprint [4]byte
	childNumber       uint32
	chainCode         [32]byte
	key               [33]byte // compressed public key
}

// Chains of an account (lbry-sdk KeyPath).
const (
	ReceivingChain = 0
	ChangeChain    = 1
)

const hardenedKeyStart = 1 << 31

// ParseExtendedPublicKey reads an `xpub...` string with the LBRY version bytes.
func ParseExtendedPublicKey(extended string) (*ExtendedPublicKey, error) {
	payload, err := base58CheckDecode(extended)
	if err != nil {
		return nil, err
	}
	if len(payload) != 78 || string(payload[:4]) != string(extendedPublicKeyPrefix) {
		return nil, errors.New("not a LBRY extended public key")
	}
	key := &ExtendedPublicKey{depth: payload[4], childNumber: binary.BigEndian.Uint32(payload[9:13])}
	copy(key.parentFingerprint[:], payload[5:9])
	copy(key.chainCode[:], payload[13:45])
	copy(key.key[:], payload[45:])
	if _, err := secp256k1.ParsePubKey(key.key[:]); err != nil {
		return nil, fmt.Errorf("invalid public key: %w", err)
	}
	return key, nil
}

// String serializes the key like the `public_key` field of a wallet account.
func (key *ExtendedPublicKey) String() string {
	payload := append([]byte{}, extendedPublicKeyPrefix...)
	payload = append(payload, key.depth)
	payload = append(payload, key.parentFingerprint[:]...)
	payload = binary.BigEndian.AppendUint32(payload, key.childNumber)
	payload = append(payload, key.chainCode[:]...)
	payload = append(payload, key.key[:]...)
	return base58CheckEncode(payload)
}

func hash160(data []byte) []byte {
	sha := sha256.Sum256(data)
	hash := ripemd160.New()
	hash.Write(sha[:])
	return hash.Sum(nil)
}

// Child derives the non-hardened child n (BIP32 CKDpub), like lbry-sdk PublicKey.child.
func (key *ExtendedPublicKey) Child(n uint32) (*ExtendedPublicKey, error) {
	if n >= hardenedKeyStart {
		return nil, errors.New("invalid BIP32 public key child number")
	}
	mac := hmac.New(sha512.New, key.chainCode[:])
	mac.Write(key.key[:])
	mac.Write(binary.BigEndian.AppendUint32(nil, n))
	sum := mac.Sum(nil)

	var tweak secp256k1.ModNScalar
	if overflow := tweak.SetByteSlice(sum[:32]); overflow || tweak.IsZero() {
		return nil, errors.New("invalid child key, try the next index")
	}
	parent, err := secp256k1.ParsePubKey(key.key[:])
	if err != nil {
		return nil, err
	}
	var tweakPoint, parentPoint, childPoint secp256k1.JacobianPoint
	secp256k1.ScalarBaseMultNonConst(&tweak, &tweakPoint)
	parent.AsJacobian(&parentPoint)
	secp256k1.AddNonConst(&tweakPoint, &parentPoint, &childPoint)
	if (childPoint.X.IsZero() && childPoint.Y.IsZero()) || childPoint.Z.IsZero() {
		return nil, errors.New("invalid child key, try the next index")
	}
	childPoint.ToAffine()
	child := &ExtendedPublicKey{depth: key.depth + 1, childNumber: n}
	copy(child.parentFingerprint[:], hash160(key.key[:])[:4])
	copy(child.chainCode[:], sum[32:])
	copy(child.key[:], secp256k1.NewPublicKey(&childPoint.X, &childPoint.Y).SerializeCompressed())
	return child, nil
}

// Address is the pay-to-public-key-hash address of the key.
func (key *ExtendedPublicKey) Address() string {
	return AddressFromPublicKey(key.key[:])
}

// ScriptHash is what the wallet server indexes an address by: the SHA-256 of its pay-to-public-key-hash
// output script (OP_DUP OP_HASH160 <hash160> OP_EQUALVERIFY OP_CHECKSIG), byte-reversed, in hex.
func (key *ExtendedPublicKey) ScriptHash() string {
	script := append(append([]byte{0x76, 0xa9, 0x14}, hash160(key.key[:])...), 0x88, 0xac)
	sum := sha256.Sum256(script)
	for i, j := 0, len(sum)-1; i < j; i, j = i+1, j-1 {
		sum[i], sum[j] = sum[j], sum[i]
	}
	return hex.EncodeToString(sum[:])
}

// DeriveKeys returns count keys of a chain starting at index start, from an account's extended public key.
func DeriveKeys(extended string, chain uint32, start uint32, count int) ([]*ExtendedPublicKey, error) {
	key, err := ParseExtendedPublicKey(extended)
	if err != nil {
		return nil, err
	}
	chainKey, err := key.Child(chain)
	if err != nil {
		return nil, err
	}
	keys := make([]*ExtendedPublicKey, 0, count)
	for index := start; len(keys) < count; index++ {
		child, err := chainKey.Child(index)
		if err != nil {
			return nil, err
		}
		keys = append(keys, child)
	}
	return keys, nil
}

// DeriveAddresses returns count addresses of a chain (ReceivingChain or ChangeChain) starting at index start,
// from an account's extended public key.
func DeriveAddresses(extended string, chain uint32, start uint32, count int) ([]string, error) {
	keys, err := DeriveKeys(extended, chain, start, count)
	if err != nil {
		return nil, err
	}
	addresses := make([]string, len(keys))
	for i, key := range keys {
		addresses[i] = key.Address()
	}
	return addresses, nil
}

// Addresses returns the first count addresses of a chain of the account, or every address up to the gap when
// count is negative. An account with the single-address generator has only its own address.
func (account *Account) Addresses(chain uint32, count int) ([]string, error) {
	if account.UsesSingleAddress() {
		address, err := AddressFromExtendedPublicKey(account.PublicKey)
		if err != nil {
			return nil, err
		}
		return []string{address}, nil
	}
	if count < 0 {
		count = account.Gap(chain)
	}
	return DeriveAddresses(account.PublicKey, chain, 0, count)
}

// UsesSingleAddress tells whether the account has the single-address generator. A wallet that does not name a
// generator has lbry-sdk's default, the deterministic chain.
func (account *Account) UsesSingleAddress() bool {
	return account.AddressGenerator.Name == ADDRESS_GENERATOR_NAME_SINGLE_ADDRESS
}

// Gap is the number of addresses kept ahead of the last used one on a chain. A wallet that does not name a
// generator has lbry-sdk's defaults, 20 receiving and 6 change.
func (account *Account) Gap(chain uint32) int {
	generator := account.AddressGenerator
	if generator.Name == "" {
		if chain == ChangeChain {
			return 6
		}
		return 20
	}
	if chain == ChangeChain {
		return int(generator.Change.Gap)
	}
	return int(generator.Receiving.Gap)
}
