package config

import (
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

const (
	installationIDFilename = "install_id"
	installationIDBytes    = 48
	base58Alphabet         = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
)

var installationIDMu sync.Mutex

// LoadOrCreateInstallationID returns the persistent installation identifier in
// dataDir, creating it from cryptographically secure random bytes when needed.
func LoadOrCreateInstallationID(dataDir string) (string, error) {
	return LoadOrCreateInstallationIDWithReader(dataDir, rand.Reader)
}

// LoadOrCreateInstallationIDWithReader is LoadOrCreateInstallationID with an
// injectable randomness source. It is primarily useful for deterministic tests.
func LoadOrCreateInstallationIDWithReader(dataDir string, random io.Reader) (string, error) {
	if random == nil {
		return "", fmt.Errorf("installation ID randomness source is nil")
	}

	installationIDMu.Lock()
	defer installationIDMu.Unlock()

	if dataDir == "" {
		dataDir = "."
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", fmt.Errorf("create data directory: %w", err)
	}

	path := filepath.Join(dataDir, installationIDFilename)
	if id, exists, err := readInstallationID(path); err != nil {
		return "", err
	} else if exists {
		return id, nil
	}

	randomBytes := make([]byte, installationIDBytes)
	if _, err := io.ReadFull(random, randomBytes); err != nil {
		return "", fmt.Errorf("generate installation ID: %w", err)
	}
	id := encodeBase58(randomBytes)

	temporary, err := os.CreateTemp(dataDir, ".install_id-*")
	if err != nil {
		return "", fmt.Errorf("create installation ID temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return "", fmt.Errorf("set installation ID permissions: %w", err)
	}
	if _, err := io.WriteString(temporary, id); err != nil {
		temporary.Close()
		return "", fmt.Errorf("write installation ID: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return "", fmt.Errorf("sync installation ID: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("close installation ID: %w", err)
	}

	return publishInstallationID(path, temporaryPath)
}

func publishInstallationID(path, temporaryPath string) (string, error) {
	lockPath := path + ".lock"
	for attempts := 0; attempts < 100; attempts++ {
		acquired := false
		if err := os.Link(temporaryPath, lockPath); err == nil {
			acquired = true
		} else if !os.IsExist(err) {
			return "", fmt.Errorf("publish installation ID candidate: %w", err)
		}

		if existing, exists, err := readInstallationID(path); err != nil {
			if acquired {
				_ = os.Remove(lockPath)
			}
			return "", err
		} else if exists {
			_ = os.Remove(lockPath)
			return existing, nil
		}

		candidate, exists, err := readInstallationID(lockPath)
		if err != nil {
			return "", err
		}
		if !exists {
			continue
		}
		if runtime.GOOS == "windows" {
			_ = os.Remove(path)
		}
		if err := os.Rename(lockPath, path); err == nil {
			return candidate, nil
		}
		if existing, exists, err := readInstallationID(path); err != nil {
			return "", err
		} else if exists {
			return existing, nil
		}
	}
	return "", fmt.Errorf("publish installation ID: concurrent update did not settle")
}

func readInstallationID(path string) (string, bool, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read installation ID: %w", err)
	}
	id := strings.TrimSpace(string(contents))
	return id, id != "", nil
}

func encodeBase58(value []byte) string {
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
