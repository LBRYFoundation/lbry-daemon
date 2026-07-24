// Package wallet implements isolated legacy wallet, account, header, and ledger
// persistence compatibility. Network synchronization and wallet RPC integration
// remain separate milestones.
package wallet

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"reflect"
	"strconv"
	"strings"
)

const LatestVersion = 1

type WalletJSONDecodeError struct {
	Err error
}

func (err *WalletJSONDecodeError) Error() string { return err.Err.Error() }
func (err *WalletJSONDecodeError) Unwrap() error { return err.Err }

type WalletJSONObjectError struct {
	Value any
}

func (err *WalletJSONObjectError) Error() string {
	return fmt.Sprintf("wallet JSON value of type %T has no attribute get", err.Value)
}

type WalletStorage struct {
	path     *string
	defaults *Object
}

// NewWalletStorage creates disk-backed storage. An empty path remains distinct
// from memory storage, matching Python's None-versus-empty-string behavior.
func NewWalletStorage(path string, defaults ...*Object) *WalletStorage {
	return newWalletStorage(&path, defaults...)
}

func NewMemoryWalletStorage(defaults ...*Object) *WalletStorage {
	return newWalletStorage(nil, defaults...)
}

func newWalletStorage(path *string, defaults ...*Object) *WalletStorage {
	var selected *Object
	if len(defaults) > 0 {
		selected = defaults[0]
	}
	if selected == nil || selected.Len() == 0 {
		selected = defaultWalletDocument()
	}
	return &WalletStorage{path: path, defaults: selected}
}

func defaultWalletDocument() *Object {
	return NewObject(
		Member{Key: "version", Value: LatestVersion},
		Member{Key: "name", Value: "My Wallet"},
		Member{Key: "preferences", Value: NewObject()},
		Member{Key: "accounts", Value: []any{}},
	)
}

func (storage *WalletStorage) Path() (string, bool) {
	if storage == nil || storage.path == nil {
		return "", false
	}
	return *storage.path, true
}

func (storage *WalletStorage) Read() (*Object, error) {
	if storage == nil {
		return nil, errors.New("wallet storage is nil")
	}
	if storage.path == nil || *storage.path == "" {
		return storage.defaults.ShallowCopy(), nil
	}
	if _, err := os.Stat(*storage.path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return storage.defaults.ShallowCopy(), nil
		}
		return nil, err
	}
	contents, err := os.ReadFile(*storage.path)
	if err != nil {
		return nil, err
	}
	decoded, err := decodeOrderedJSON(contents)
	if err != nil {
		return nil, &WalletJSONDecodeError{Err: err}
	}
	document, ok := decoded.(*Object)
	if !ok {
		return nil, &WalletJSONObjectError{Value: decoded}
	}
	version, _ := document.Get("version")
	if pythonVersionEqualsCurrent(version) && document.hasSameKeys(storage.defaults) {
		return document, nil
	}
	return storage.Upgrade(document), nil
}

// Upgrade preserves the pinned SDK bug: it builds no effective upgraded
// document and returns only a shallow copy with the version member removed.
func (storage *WalletStorage) Upgrade(document *Object) *Object {
	upgraded := document.ShallowCopy()
	upgraded.Delete("version")
	return upgraded
}

func (storage *WalletStorage) Encode(value any) ([]byte, error) {
	return encodeWalletJSON(value)
}

// Write returns encoded bytes for memory storage and nil for disk storage,
// mirroring Python's string-versus-None return behavior.
func (storage *WalletStorage) Write(value any) ([]byte, error) {
	encoded, err := storage.Encode(value)
	if err != nil {
		return nil, err
	}
	if storage.path == nil {
		return encoded, nil
	}

	path := *storage.path
	temporaryPath := path + ".tmp." + strconv.Itoa(os.Getpid())
	temporary, err := os.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return nil, err
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return nil, err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return nil, err
	}
	if err := temporary.Close(); err != nil {
		return nil, err
	}

	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode()
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		if removeErr := removeWalletFile(path); removeErr != nil {
			return nil, removeErr
		}
		if renameErr := os.Rename(temporaryPath, path); renameErr != nil {
			return nil, renameErr
		}
	}
	if err := os.Chmod(path, mode); err != nil {
		return nil, err
	}
	return nil, nil
}

func removeWalletFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("remove wallet path %s: is a directory", path)
	}
	return os.Remove(path)
}

func pythonVersionEqualsCurrent(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case json.Number:
		if strings.ContainsAny(string(typed), ".eE") {
			number, err := strconv.ParseFloat(string(typed), 64)
			return err == nil && number == LatestVersion
		}
		integer, ok := new(big.Int).SetString(string(typed), 10)
		return ok && integer.Cmp(big.NewInt(LatestVersion)) == 0
	case *big.Int:
		return typed != nil && typed.Cmp(big.NewInt(LatestVersion)) == 0
	case big.Int:
		return typed.Cmp(big.NewInt(LatestVersion)) == 0
	case float64:
		return typed == LatestVersion
	case float32:
		return typed == LatestVersion
	}
	if value == nil {
		return false
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflected.Int() == LatestVersion
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return reflected.Uint() == LatestVersion
	}
	return false
}
