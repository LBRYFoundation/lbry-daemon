package wallet

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"errors"
)

// ErrInvalidPassword is returned when a value cannot be decrypted with the given password.
var ErrInvalidPassword = errors.New("invalid password")

// aesDecrypt mirrors lbry-sdk's lbry.crypto.crypt.aes_decrypt: the key is the
// double SHA-256 of the password and the value is base64(iv || AES-256-CBC(PKCS7(data))).
func aesDecrypt(password string, value string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(data) < 2*aes.BlockSize || len(data)%aes.BlockSize != 0 {
		return "", ErrInvalidPassword
	}
	first := sha256.Sum256([]byte(password))
	key := sha256.Sum256(first[:])
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	iv, ciphertext := data[:aes.BlockSize], data[aes.BlockSize:]
	plain := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, ciphertext)

	pad := int(plain[len(plain)-1])
	if pad == 0 || pad > aes.BlockSize || pad > len(plain) {
		return "", ErrInvalidPassword
	}
	for _, b := range plain[len(plain)-pad:] {
		if int(b) != pad {
			return "", ErrInvalidPassword
		}
	}
	return string(plain[:len(plain)-pad]), nil
}

// aesEncrypt is the inverse of aesDecrypt. It is only used to build test fixtures.
func aesEncrypt(password string, value string, iv []byte) string {
	first := sha256.Sum256([]byte(password))
	key := sha256.Sum256(first[:])
	block, _ := aes.NewCipher(key[:])
	pad := aes.BlockSize - len(value)%aes.BlockSize
	padded := append([]byte(value), make([]byte, pad)...)
	for i := len(value); i < len(padded); i++ {
		padded[i] = byte(pad)
	}
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	return base64.StdEncoding.EncodeToString(append(append([]byte{}, iv...), out...))
}
