// Package mnemonic implements the versioned mnemonic scheme used by the
// pinned Python SDK. Despite using the BIP39 English word list, this is the
// Electrum-style scheme used by LBRY and is not BIP39.
package mnemonic

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

const (
	StandardPrefix  = "01"
	TwoFactorPrefix = "101"
	SegWitPrefix    = "100"
	DefaultBits     = 132
	PBKDF2Rounds    = 2048
)

var ErrLanguageUnavailable = errors.New("mnemonic language is unavailable in the pinned SDK")

type UnknownWordError struct {
	Word string
}

func (err *UnknownWordError) Error() string {
	return fmt.Sprintf("mnemonic word %q is not in the word list", err.Word)
}

type Mnemonic struct {
	words  []string
	index  map[string]int
	random io.Reader
}

// New mirrors Mnemonic(lang). The pinned SDK only successfully loads "en";
// unknown language codes fall back to English, while its four recognized
// non-English codes fail because their import path does not exist.
func New(language string) (*Mnemonic, error) {
	return newWithReader(language, rand.Reader)
}

// NewEnglish is the Go equivalent of Python's Mnemonic() default.
func NewEnglish() *Mnemonic {
	mnemonic, err := New("en")
	if err != nil {
		panic(err)
	}
	return mnemonic
}

func newWithReader(language string, random io.Reader) (*Mnemonic, error) {
	switch language {
	case "es", "ja", "pt", "zh":
		return nil, fmt.Errorf("%w: %s", ErrLanguageUnavailable, language)
	}
	words := loadEnglishWords()
	index := make(map[string]int, len(words))
	for position, word := range words {
		index[word] = position
	}
	return &Mnemonic{words: words, index: index, random: random}, nil
}

// Words returns a copy so callers cannot alter encoding compatibility.
func (mnemonic *Mnemonic) Words() []string {
	return append([]string(nil), mnemonic.words...)
}

// ToSeed matches Mnemonic.mnemonic_to_seed. In particular, the normalized
// passphrase itself is the PBKDF2 salt; BIP39's "mnemonic" salt prefix is not
// used.
func ToSeed(phrase, passphrase string) []byte {
	phrase = NormalizeText(phrase)
	passphrase = NormalizeText(passphrase)
	return pbkdf2.Key([]byte(phrase), []byte(passphrase), PBKDF2Rounds, 64, sha512.New)
}

// Encode returns the little-endian base-N word encoding used by Python.
func (mnemonic *Mnemonic) Encode(value *big.Int) (string, error) {
	if value == nil {
		return "", errors.New("mnemonic entropy is nil")
	}
	if value.Sign() < 0 {
		return "", errors.New("mnemonic entropy must not be negative")
	}
	remaining := new(big.Int).Set(value)
	base := big.NewInt(int64(len(mnemonic.words)))
	var words []string
	for remaining.Sign() != 0 {
		quotient, remainder := new(big.Int), new(big.Int)
		quotient.QuoRem(remaining, base, remainder)
		words = append(words, mnemonic.words[remainder.Int64()])
		remaining = quotient
	}
	return strings.Join(words, " "), nil
}

// Decode reverses Encode. It intentionally does not normalize or lowercase
// words, matching list.index() in the Python implementation.
func (mnemonic *Mnemonic) Decode(phrase string) (*big.Int, error) {
	words := splitPythonWhitespace(phrase)
	value := new(big.Int)
	base := big.NewInt(int64(len(mnemonic.words)))
	for position := len(words) - 1; position >= 0; position-- {
		wordIndex, ok := mnemonic.index[words[position]]
		if !ok {
			return nil, &UnknownWordError{Word: words[position]}
		}
		value.Mul(value, base)
		value.Add(value, big.NewInt(int64(wordIndex)))
	}
	return value, nil
}

// IsNewSeed checks the hexadecimal HMAC prefix used to version Electrum-style
// seeds. Prefix is text because every SDK prefix is an ASCII byte string.
func IsNewSeed(phrase, prefix string) bool {
	hash := hmac.New(sha512.New, []byte("Seed version"))
	_, _ = hash.Write([]byte(NormalizeText(phrase)))
	encoded := make([]byte, hex.EncodedLen(hash.Size()))
	hex.Encode(encoded, hash.Sum(nil))
	return strings.HasPrefix(string(encoded), prefix)
}

func (mnemonic *Mnemonic) MakeDefaultSeed() (string, error) {
	return mnemonic.MakeSeed(StandardPrefix, DefaultBits)
}

// MakeSeed mirrors the SDK's entropy threshold and nonce search. The random
// source is crypto/rand.Reader for values returned by New.
func (mnemonic *Mnemonic) MakeSeed(prefix string, numBits int) (string, error) {
	if mnemonic == nil || len(mnemonic.words) == 0 {
		return "", errors.New("mnemonic has no word list")
	}
	if mnemonic.random == nil {
		return "", errors.New("mnemonic has no random source")
	}

	bitsPerWord := math.Log2(float64(len(mnemonic.words)))
	roundedBits := int(math.Ceil(float64(numBits)/bitsPerWord) * bitsPerWord)
	entropy := big.NewInt(1)
	minimumExponent := roundedBits - int(bitsPerWord)
	if minimumExponent > 0 {
		minimum := new(big.Int).Lsh(big.NewInt(1), uint(minimumExponent))
		maximum := new(big.Int).Lsh(big.NewInt(1), uint(roundedBits))
		for entropy.Sign() > 0 && entropy.Cmp(minimum) < 0 {
			var err error
			entropy, err = rand.Int(mnemonic.random, maximum)
			if err != nil {
				return "", fmt.Errorf("read mnemonic entropy: %w", err)
			}
		}
	}

	candidate := new(big.Int)
	nonce := new(big.Int)
	one := big.NewInt(1)
	for {
		nonce.Add(nonce, one)
		candidate.Set(entropy).Add(candidate, nonce)
		phrase, err := mnemonic.Encode(candidate)
		if err != nil {
			return "", err
		}
		decoded, err := mnemonic.Decode(phrase)
		if err != nil {
			return "", err
		}
		if decoded.Cmp(candidate) != 0 {
			return "", errors.New("cannot extract same entropy from mnemonic")
		}
		if IsNewSeed(phrase, prefix) {
			return phrase, nil
		}
	}
}
