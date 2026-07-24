package wallet

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

var (
	ErrInvalidAccountPassword = errors.New("invalid account password")
	ErrInvalidAccountIV       = errors.New("invalid account initialization vector")
)

// EncryptAccountSecret matches lbry.crypto.crypt.aes_encrypt. A nil IV uses
// crypto/rand, while account serialization passes a cached 16-byte IV.
func EncryptAccountSecret(password, value string, initializationVector []byte) (string, error) {
	if initializationVector == nil {
		initializationVector = make([]byte, aes.BlockSize)
		if _, err := io.ReadFull(rand.Reader, initializationVector); err != nil {
			return "", fmt.Errorf("generate account initialization vector: %w", err)
		}
	}
	if len(initializationVector) != aes.BlockSize {
		return "", fmt.Errorf("%w: got %d bytes, want %d", ErrInvalidAccountIV, len(initializationVector), aes.BlockSize)
	}
	if !utf8.ValidString(password) || !utf8.ValidString(value) {
		return "", errors.New("account password and value must be valid UTF-8")
	}

	block, err := aes.NewCipher(doubleSHA256([]byte(password)))
	if err != nil {
		return "", err
	}
	plaintext := []byte(value)
	paddingLength := aes.BlockSize - len(plaintext)%aes.BlockSize
	padded := make([]byte, len(plaintext)+paddingLength)
	copy(padded, plaintext)
	for index := len(plaintext); index < len(padded); index++ {
		padded[index] = byte(paddingLength)
	}

	encoded := make([]byte, aes.BlockSize+len(padded))
	copy(encoded, initializationVector)
	cipher.NewCBCEncrypter(block, initializationVector).CryptBlocks(encoded[aes.BlockSize:], padded)
	return base64.StdEncoding.EncodeToString(encoded), nil
}

// DecryptAccountSecret returns both the plaintext and stored IV, matching the
// tuple returned by the pinned Python SDK.
func DecryptAccountSecret(password, encoded string) (string, []byte, error) {
	if !utf8.ValidString(password) || !utf8.ValidString(encoded) {
		return "", nil, errors.New("account password and ciphertext must be valid UTF-8")
	}
	data, err := decodePythonBase64(encoded)
	if err != nil {
		return "", nil, fmt.Errorf("decode account ciphertext: %w", err)
	}
	if len(data) < aes.BlockSize {
		return "", nil, fmt.Errorf("%w: got %d bytes, want %d", ErrInvalidAccountIV, len(data), aes.BlockSize)
	}
	initializationVector := append([]byte(nil), data[:aes.BlockSize]...)
	ciphertext := data[aes.BlockSize:]
	// The pinned SDK calls decryptor.update but never decryptor.finalize.
	// OpenSSL therefore buffers and silently discards a trailing partial block.
	ciphertext = ciphertext[:len(ciphertext)/aes.BlockSize*aes.BlockSize]

	block, err := aes.NewCipher(doubleSHA256([]byte(password)))
	if err != nil {
		return "", initializationVector, err
	}
	plaintext := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, initializationVector).CryptBlocks(plaintext, ciphertext)
	plaintext, err = unpadAccountSecret(plaintext)
	if err != nil {
		return "", initializationVector, err
	}
	if !utf8.Valid(plaintext) {
		return "", initializationVector, ErrInvalidAccountPassword
	}
	return string(plaintext), initializationVector, nil
}

func doubleSHA256(value []byte) []byte {
	first := sha256.Sum256(value)
	second := sha256.Sum256(first[:])
	return second[:]
}

func unpadAccountSecret(value []byte) ([]byte, error) {
	if len(value) == 0 {
		return nil, ErrInvalidAccountPassword
	}
	paddingLength := int(value[len(value)-1])
	if paddingLength < 1 || paddingLength > aes.BlockSize || paddingLength > len(value) {
		return nil, ErrInvalidAccountPassword
	}
	want := byte(paddingLength)
	valid := 1
	for _, paddingByte := range value[len(value)-paddingLength:] {
		valid &= subtle.ConstantTimeByteEq(paddingByte, want)
	}
	if valid != 1 {
		return nil, ErrInvalidAccountPassword
	}
	return value[:len(value)-paddingLength], nil
}

func decodePythonBase64(encoded string) ([]byte, error) {
	// This is the non-strict state machine from CPython 3.11's
	// binascii.a2b_base64. The pinned SDK calls base64.b64decode without
	// validate=True, so invalid bytes and misplaced padding are observable.
	decoded := make([]byte, 0, (len(encoded)+3)/4*3)
	quadPosition := 0
	left := byte(0)
	pads := 0
	for index := 0; index < len(encoded); index++ {
		value := encoded[index]
		if value == '=' {
			if quadPosition >= 2 {
				pads++
				if quadPosition+pads >= 4 {
					return decoded, nil
				}
			}
			continue
		}
		digit, valid := pythonBase64Digit(value)
		if !valid {
			continue
		}
		pads = 0
		switch quadPosition {
		case 0:
			quadPosition = 1
			left = digit
		case 1:
			quadPosition = 2
			decoded = append(decoded, left<<2|digit>>4)
			left = digit & 0x0f
		case 2:
			quadPosition = 3
			decoded = append(decoded, left<<4|digit>>2)
			left = digit & 0x03
		case 3:
			quadPosition = 0
			decoded = append(decoded, left<<6|digit)
			left = 0
		}
	}
	if quadPosition == 0 {
		return decoded, nil
	}
	if quadPosition == 1 {
		return nil, errors.New("invalid base64 data: data character count is 1 more than a multiple of 4")
	}
	return nil, errors.New("invalid base64 data: incorrect padding")
}

func pythonBase64Digit(value byte) (byte, bool) {
	switch {
	case value >= 'A' && value <= 'Z':
		return value - 'A', true
	case value >= 'a' && value <= 'z':
		return value - 'a' + 26, true
	case value >= '0' && value <= '9':
		return value - '0' + 52, true
	case value == '+':
		return 62, true
	case value == '/':
		return 63, true
	default:
		return 0, false
	}
}
