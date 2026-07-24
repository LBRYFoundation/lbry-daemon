package keys

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"golang.org/x/crypto/ripemd160"
)

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

var (
	ErrInvalidBase58Character = errors.New("invalid base58 character")
	ErrInvalidBase58Checksum  = errors.New("invalid base58 checksum")
)

// Hash160 computes RIPEMD160(SHA256(value)), as used for key identifiers and
// LBRY P2PKH addresses.
func Hash160(value []byte) [ripemd160.Size]byte {
	shaHash := sha256.Sum256(value)
	hasher := ripemd160.New()
	_, _ = hasher.Write(shaHash[:])
	var result [ripemd160.Size]byte
	copy(result[:], hasher.Sum(nil))
	return result
}

func doubleSHA256(value []byte) [sha256.Size]byte {
	first := sha256.Sum256(value)
	return sha256.Sum256(first[:])
}

// EncodeBase58Check appends the Bitcoin-style four-byte double-SHA256
// checksum and encodes the result with the legacy Base58 alphabet.
func EncodeBase58Check(payload []byte) string {
	checksum := doubleSHA256(payload)
	checked := make([]byte, 0, len(payload)+4)
	checked = append(checked, payload...)
	checked = append(checked, checksum[:4]...)
	return encodeBase58(checked)
}

// DecodeBase58Check decodes and verifies a Base58Check payload.
func DecodeBase58Check(encoded string) ([]byte, error) {
	decoded, err := decodeBase58(encoded)
	if err != nil {
		return nil, err
	}
	if len(decoded) < 4 {
		return nil, ErrInvalidBase58Checksum
	}
	payload := decoded[:len(decoded)-4]
	want := doubleSHA256(payload)
	if !bytes.Equal(decoded[len(decoded)-4:], want[:4]) {
		return nil, fmt.Errorf("%w for %s", ErrInvalidBase58Checksum, encoded)
	}
	return payload, nil
}

// DecodeBase58 mirrors lbry.crypto.base58.Base58.decode without checking a
// checksum. The pinned helper retains an extra zero byte for an all-'1' input
// because int_to_bytes(0) returns one zero before leading zeroes are prepended.
func DecodeBase58(encoded string) ([]byte, error) {
	return decodeBase58(encoded)
}

// EncodeBase58 exposes the checksum-free encoding used by channel export.
func EncodeBase58(value []byte) string { return encodeBase58(value) }

func encodeBase58(value []byte) string {
	integer := new(big.Int).SetBytes(value)
	base := big.NewInt(58)
	remainder := new(big.Int)
	encoded := make([]byte, 0, len(value)*138/100+1)
	for integer.Sign() > 0 {
		integer.QuoRem(integer, base, remainder)
		encoded = append(encoded, base58Alphabet[remainder.Int64()])
	}
	for _, character := range value {
		if character != 0 {
			break
		}
		encoded = append(encoded, base58Alphabet[0])
	}
	for left, right := 0, len(encoded)-1; left < right; left, right = left+1, right-1 {
		encoded[left], encoded[right] = encoded[right], encoded[left]
	}
	return string(encoded)
}

func decodeBase58(encoded string) ([]byte, error) {
	if encoded == "" {
		return nil, errors.New("base58 string cannot be empty")
	}
	integer := new(big.Int)
	base := big.NewInt(58)
	for offset := 0; offset < len(encoded); offset++ {
		digit := strings.IndexByte(base58Alphabet, encoded[offset])
		if digit < 0 {
			return nil, fmt.Errorf("%w %q", ErrInvalidBase58Character, encoded[offset])
		}
		integer.Mul(integer, base)
		integer.Add(integer, big.NewInt(int64(digit)))
	}
	decoded := integer.Bytes()
	if integer.Sign() == 0 {
		decoded = []byte{0}
	}
	leadingZeroes := 0
	for leadingZeroes < len(encoded) && encoded[leadingZeroes] == base58Alphabet[0] {
		leadingZeroes++
	}
	if leadingZeroes == 0 {
		return decoded, nil
	}
	result := make([]byte, leadingZeroes+len(decoded))
	copy(result[leadingZeroes:], decoded)
	return result, nil
}
