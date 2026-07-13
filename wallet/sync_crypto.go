package wallet

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/scrypt"
)

const (
	walletSyncScryptN = 1 << 13
	walletSyncScryptR = 16
	walletSyncScryptP = 1
)

var (
	ErrInvalidWalletPassword = errors.New("Password is invalid.")
	ErrInvalidSyncEnvelope   = errors.New("invalid wallet synchronization envelope")
)

// BetterAESEncrypt implements lbry.crypto.crypt.better_aes_encrypt. The
// envelope is intentionally unauthenticated because existing SDK clients must
// be able to exchange their historical payloads.
func BetterAESEncrypt(password string, value []byte) ([]byte, error) {
	return betterAESEncrypt(password, value, rand.Reader)
}

func betterAESEncrypt(password string, value []byte, entropy io.Reader) ([]byte, error) {
	if !utf8.ValidString(password) {
		return nil, errors.New("wallet synchronization password must be valid UTF-8")
	}
	if entropy == nil {
		entropy = rand.Reader
	}
	initializationVector := make([]byte, aes.BlockSize)
	if _, err := io.ReadFull(entropy, initializationVector); err != nil {
		return nil, fmt.Errorf("generate wallet synchronization initialization vector: %w", err)
	}
	key, err := scrypt.Key(
		[]byte(password), initializationVector,
		walletSyncScryptN, walletSyncScryptR, walletSyncScryptP, 32,
	)
	if err != nil {
		return nil, fmt.Errorf("derive wallet synchronization key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	padded := padWalletSyncValue(value)
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, initializationVector).CryptBlocks(ciphertext, padded)

	prefix := []byte("s:8192:16:1:")
	envelope := make([]byte, 0, len(prefix)+len(initializationVector)+len(ciphertext))
	envelope = append(envelope, prefix...)
	envelope = append(envelope, initializationVector...)
	envelope = append(envelope, ciphertext...)
	encoded := make([]byte, base64.StdEncoding.EncodedLen(len(envelope)))
	base64.StdEncoding.Encode(encoded, envelope)
	return encoded, nil
}

// BetterAESDecrypt implements lbry.crypto.crypt.better_aes_decrypt, including
// the legacy behavior where a wrong password can return bytes when its random
// plaintext happens to have valid PKCS#7 padding.
func BetterAESDecrypt(password string, encoded []byte) ([]byte, error) {
	if !utf8.ValidString(password) {
		return nil, errors.New("wallet synchronization password must be valid UTF-8")
	}
	decoded, err := decodePythonBase64(string(encoded))
	if err != nil {
		return nil, fmt.Errorf("%w: decode base64: %v", ErrInvalidSyncEnvelope, err)
	}
	fields := strings.SplitN(string(decoded), ":", 5)
	if len(fields) != 5 {
		return nil, fmt.Errorf("%w: expected five colon-delimited fields", ErrInvalidSyncEnvelope)
	}
	n, err := walletSyncScryptInteger(fields[1], "n")
	if err != nil {
		return nil, err
	}
	r, err := walletSyncScryptInteger(fields[2], "r")
	if err != nil {
		return nil, err
	}
	p, err := walletSyncScryptInteger(fields[3], "p")
	if err != nil {
		return nil, err
	}
	payload := []byte(fields[4])
	if len(payload) < aes.BlockSize {
		return nil, fmt.Errorf("%w: initialization vector has %d bytes, want %d", ErrInvalidSyncEnvelope, len(payload), aes.BlockSize)
	}
	initializationVector := payload[:aes.BlockSize]
	ciphertext := payload[aes.BlockSize:]
	key, err := scrypt.Key([]byte(password), initializationVector, n, r, p, 32)
	if err != nil {
		return nil, fmt.Errorf("%w: derive key: %v", ErrInvalidSyncEnvelope, err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	// The pinned implementation never calls decryptor.finalize(). OpenSSL
	// buffers and discards a trailing partial ciphertext block in that case.
	ciphertext = ciphertext[:len(ciphertext)/aes.BlockSize*aes.BlockSize]
	plaintext := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, initializationVector).CryptBlocks(plaintext, ciphertext)
	return unpadWalletSyncValue(plaintext)
}

func walletSyncScryptInteger(value, name string) (int, error) {
	integer, err := pythonBytesDecimalInteger(value)
	if err != nil {
		return 0, fmt.Errorf("%w: invalid scrypt %s: %v", ErrInvalidSyncEnvelope, name, err)
	}
	if !integer.IsInt64() {
		return 0, fmt.Errorf("%w: scrypt %s does not fit an integer", ErrInvalidSyncEnvelope, name)
	}
	parsed := integer.Int64()
	maximum := int64(^uint(0) >> 1)
	if parsed > maximum || parsed < -maximum-1 {
		return 0, fmt.Errorf("%w: scrypt %s does not fit an integer", ErrInvalidSyncEnvelope, name)
	}
	return int(parsed), nil
}

func pythonBytesDecimalInteger(value string) (*big.Int, error) {
	value = strings.Trim(value, " \t\n\v\f\r")
	if value == "" {
		return nil, errors.New("invalid literal for int()")
	}
	digitStart := 0
	if value[0] == '+' || value[0] == '-' {
		digitStart = 1
	}
	if digitStart == len(value) {
		return nil, errors.New("invalid literal for int()")
	}
	for index := digitStart; index < len(value); index++ {
		character := value[index]
		switch {
		case character >= '0' && character <= '9':
		case character == '_' && index > digitStart && index+1 < len(value) &&
			value[index-1] >= '0' && value[index-1] <= '9' &&
			value[index+1] >= '0' && value[index+1] <= '9':
		default:
			return nil, errors.New("invalid literal for int()")
		}
	}
	integer, ok := new(big.Int).SetString(strings.ReplaceAll(value, "_", ""), 10)
	if !ok {
		return nil, errors.New("invalid literal for int()")
	}
	return integer, nil
}

func padWalletSyncValue(value []byte) []byte {
	paddingLength := aes.BlockSize - len(value)%aes.BlockSize
	padded := make([]byte, len(value)+paddingLength)
	copy(padded, value)
	for index := len(value); index < len(padded); index++ {
		padded[index] = byte(paddingLength)
	}
	return padded
}

func unpadWalletSyncValue(value []byte) ([]byte, error) {
	if len(value) == 0 {
		return nil, ErrInvalidWalletPassword
	}
	paddingLength := int(value[len(value)-1])
	if paddingLength < 1 || paddingLength > aes.BlockSize || paddingLength > len(value) {
		return nil, ErrInvalidWalletPassword
	}
	valid := 1
	for _, paddingByte := range value[len(value)-paddingLength:] {
		valid &= subtle.ConstantTimeByteEq(paddingByte, byte(paddingLength))
	}
	if valid != 1 {
		return nil, ErrInvalidWalletPassword
	}
	return value[:len(value)-paddingLength], nil
}
