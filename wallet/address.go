package wallet

import "bytes"
import "crypto/sha256"
import "errors"
import "math/big"

import "golang.org/x/crypto/ripemd160"

// LBRY mainnet (lbry-sdk lbry/wallet/ledger.py): P2PKH address prefix and BIP32 extended public key prefix.
const pubkeyAddressPrefix = 0x55

var extendedPublicKeyPrefix = []byte{0x04, 0x88, 0xb2, 0x1e}

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func doubleSHA256(data []byte) [32]byte {
	first := sha256.Sum256(data)
	return sha256.Sum256(first[:])
}

func base58CheckEncode(payload []byte) string {
	checksum := doubleSHA256(payload)
	data := append(append([]byte{}, payload...), checksum[:4]...)
	number := new(big.Int).SetBytes(data)
	base, mod := big.NewInt(58), new(big.Int)
	var out []byte
	for number.Sign() > 0 {
		number.DivMod(number, base, mod)
		out = append(out, base58Alphabet[mod.Int64()])
	}
	for _, b := range data {
		if b != 0 {
			break
		}
		out = append(out, base58Alphabet[0])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

func base58CheckDecode(text string) ([]byte, error) {
	number := new(big.Int)
	base := big.NewInt(58)
	for _, char := range []byte(text) {
		index := bytes.IndexByte([]byte(base58Alphabet), char)
		if index < 0 {
			return nil, errors.New("invalid base58 character")
		}
		number.Mul(number, base)
		number.Add(number, big.NewInt(int64(index)))
	}
	data := number.Bytes()
	for _, char := range []byte(text) {
		if char != base58Alphabet[0] {
			break
		}
		data = append([]byte{0}, data...)
	}
	if len(data) < 5 {
		return nil, errors.New("base58 value too short")
	}
	payload, checksum := data[:len(data)-4], data[len(data)-4:]
	expected := doubleSHA256(payload)
	if !bytes.Equal(checksum, expected[:4]) {
		return nil, errors.New("invalid base58 checksum")
	}
	return payload, nil
}

// AddressFromPublicKey returns the LBRY mainnet pay-to-public-key-hash address of a compressed public key.
func AddressFromPublicKey(publicKey []byte) string {
	sha := sha256.Sum256(publicKey)
	hash := ripemd160.New()
	hash.Write(sha[:])
	return base58CheckEncode(append([]byte{pubkeyAddressPrefix}, hash.Sum(nil)...))
}

// AddressFromExtendedPublicKey returns the address of an account's BIP32 extended public key (the `public_key`
// field of a wallet account). lbry-sdk uses it as the account id.
func AddressFromExtendedPublicKey(extended string) (string, error) {
	payload, err := base58CheckDecode(extended)
	if err != nil {
		return "", err
	}
	// version(4) depth(1) parent fingerprint(4) child number(4) chain code(32) key(33)
	if len(payload) != 78 || !bytes.Equal(payload[:4], extendedPublicKeyPrefix) {
		return "", errors.New("not a LBRY extended public key")
	}
	key := payload[45:]
	if key[0] != 0x02 && key[0] != 0x03 {
		return "", errors.New("not a compressed public key")
	}
	return AddressFromPublicKey(key), nil
}
