package dht

import (
	"crypto/rand"
	"crypto/sha512"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	nodeIDFilename     = "node_id"
	nodeIDEntropyBytes = 64
	base58Alphabet     = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
)

var nodeIDFileMu sync.Mutex

// LoadOrCreateNodeID returns the persistent DHT identity stored in dataDir.
func LoadOrCreateNodeID(dataDir string) ([HashSize]byte, error) {
	return LoadOrCreateNodeIDWithReader(dataDir, rand.Reader)
}

// LoadOrCreateNodeIDWithReader is LoadOrCreateNodeID with injectable entropy.
// The Python SDK generated a uniform 512-bit integer and hashed its decimal
// representation with SHA-384; reading 64 random bytes preserves that contract.
func LoadOrCreateNodeIDWithReader(dataDir string, entropy io.Reader) ([HashSize]byte, error) {
	return loadOrCreateNodeID(dataDir, entropy, os.Link)
}

func loadOrCreateNodeID(
	dataDir string,
	entropy io.Reader,
	linkFile func(string, string) error,
) ([HashSize]byte, error) {
	var zero [HashSize]byte
	if entropy == nil {
		return zero, fmt.Errorf("DHT node ID entropy source is nil")
	}
	if dataDir == "" {
		dataDir = "."
	}

	nodeIDFileMu.Lock()
	defer nodeIDFileMu.Unlock()

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return zero, fmt.Errorf("create DHT data directory: %w", err)
	}
	path := filepath.Join(dataDir, nodeIDFilename)
	if id, exists, err := readNodeID(path); err != nil {
		return zero, err
	} else if exists {
		return id, nil
	}

	randomBytes := make([]byte, nodeIDEntropyBytes)
	if _, err := io.ReadFull(entropy, randomBytes); err != nil {
		return zero, fmt.Errorf("generate DHT node ID: %w", err)
	}
	randomInteger := new(big.Int).SetBytes(randomBytes)
	id := sha512.Sum384([]byte(randomInteger.Text(10)))
	encoded := encodeNodeIDBase58(id[:])

	temporary, err := os.CreateTemp(dataDir, ".node_id-*")
	if err != nil {
		return zero, fmt.Errorf("create DHT node ID temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := io.WriteString(temporary, encoded); err != nil {
		temporary.Close()
		return zero, fmt.Errorf("write DHT node ID: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return zero, fmt.Errorf("sync DHT node ID: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return zero, fmt.Errorf("close DHT node ID: %w", err)
	}

	linkErr := linkFile(temporaryPath, path)
	if linkErr == nil {
		return id, nil
	}
	if !os.IsExist(linkErr) {
		created, err := publishNodeIDExclusive(path, encoded)
		if err != nil {
			return zero, fmt.Errorf("publish DHT node ID after hard-link failure (%v): %w", linkErr, err)
		}
		if created {
			return id, nil
		}
	}
	if existing, exists, err := readNodeID(path); err != nil {
		return zero, err
	} else if exists {
		return existing, nil
	}
	return zero, fmt.Errorf("publish DHT node ID: competing writer did not create %s", path)
}

func publishNodeIDExclusive(path, encoded string) (created bool, err error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(path)
		}
	}()
	if _, err := io.WriteString(file, encoded); err != nil {
		_ = file.Close()
		return false, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return false, err
	}
	if err := file.Close(); err != nil {
		return false, err
	}
	complete = true
	return true, nil
}

func readNodeID(path string) ([HashSize]byte, bool, error) {
	var id [HashSize]byte
	contents, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return id, false, nil
		}
		return id, false, fmt.Errorf("read DHT node ID: %w", err)
	}
	decoded, err := decodeNodeIDBase58(strings.TrimSpace(string(contents)))
	if err != nil {
		return id, false, fmt.Errorf("decode DHT node ID: %w", err)
	}
	if len(decoded) != HashSize {
		return id, false, fmt.Errorf("decode DHT node ID: got %d bytes, want %d", len(decoded), HashSize)
	}
	copy(id[:], decoded)
	return id, true, nil
}

func encodeNodeIDBase58(value []byte) string {
	leadingZeroes := 0
	for leadingZeroes < len(value) && value[leadingZeroes] == 0 {
		leadingZeroes++
	}

	number := new(big.Int).SetBytes(value)
	base := big.NewInt(58)
	quotient := new(big.Int)
	remainder := new(big.Int)
	encoded := make([]byte, 0, len(value)*2)
	for number.Sign() > 0 {
		quotient.QuoRem(number, base, remainder)
		encoded = append(encoded, base58Alphabet[remainder.Int64()])
		number.Set(quotient)
	}
	for range leadingZeroes {
		encoded = append(encoded, base58Alphabet[0])
	}
	for left, right := 0, len(encoded)-1; left < right; left, right = left+1, right-1 {
		encoded[left], encoded[right] = encoded[right], encoded[left]
	}
	return string(encoded)
}

func decodeNodeIDBase58(value string) ([]byte, error) {
	base := big.NewInt(58)
	number := new(big.Int)
	for index := 0; index < len(value); index++ {
		digit := strings.IndexByte(base58Alphabet, value[index])
		if digit < 0 {
			return nil, fmt.Errorf("invalid base58 character %q at byte %d", value[index], index)
		}
		number.Mul(number, base)
		number.Add(number, big.NewInt(int64(digit)))
	}

	leadingZeroes := 0
	for leadingZeroes < len(value) && value[leadingZeroes] == base58Alphabet[0] {
		leadingZeroes++
	}
	decodedNumber := number.Bytes()
	decoded := make([]byte, leadingZeroes+len(decodedNumber))
	copy(decoded[leadingZeroes:], decodedNumber)
	return decoded, nil
}
