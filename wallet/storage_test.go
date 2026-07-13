package wallet

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWalletStorageMissingAndFalsyDefaultsMatchPython(t *testing.T) {
	for _, storage := range []*WalletStorage{
		NewMemoryWalletStorage(),
		NewMemoryWalletStorage(NewObject()),
		NewWalletStorage(filepath.Join(t.TempDir(), "missing")),
	} {
		document, err := storage.Read()
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"version", "name", "preferences", "accounts"}; !reflect.DeepEqual(document.Keys(), want) {
			t.Fatalf("default keys = %v, want %v", document.Keys(), want)
		}
		name, _ := document.Get("name")
		if name != "My Wallet" {
			t.Fatalf("default name = %v", name)
		}
	}
}

func TestWalletStorageMissingCustomDefaultIsShallowCopied(t *testing.T) {
	nested := NewObject(Member{Key: "value", Value: 1})
	defaults := NewObject(
		Member{Key: "version", Value: LatestVersion},
		Member{Key: "nested", Value: nested},
	)
	storage := NewMemoryWalletStorage(defaults)
	read, err := storage.Read()
	if err != nil {
		t.Fatal(err)
	}
	read.Set("version", 2)
	if version, _ := defaults.Get("version"); version != LatestVersion {
		t.Fatalf("top-level default was aliased: %v", version)
	}
	readNested, _ := read.Get("nested")
	readNested.(*Object).Set("value", 2)
	if value, _ := nested.Get("value"); value != 2 {
		t.Fatalf("nested default was deep-copied: %v", value)
	}
}

func TestWalletStorageReadCurrentVersionUsesPythonEquality(t *testing.T) {
	for _, version := range []string{"1", "1.0", "true"} {
		t.Run(version, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wallet")
			contents := `{"accounts":[],"preferences":{},"name":"Current","version":` + version + `}`
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			document, err := NewWalletStorage(path).Read()
			if err != nil {
				t.Fatal(err)
			}
			if _, exists := document.Get("version"); !exists {
				t.Fatalf("current version %s was sent through upgrade", version)
			}
			name, _ := document.Get("name")
			if name != "Current" {
				t.Fatalf("name = %v", name)
			}
		})
	}
}

func TestWalletStorageUpgradePreservesPinnedVersionRemovalBug(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wallet")
	contents := `{"version":2,"name":"Old","extra":true}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	document, err := NewWalletStorage(path).Read()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"name", "extra"}; !reflect.DeepEqual(document.Keys(), want) {
		t.Fatalf("upgraded keys = %v, want %v", document.Keys(), want)
	}
	if _, exists := document.Get("accounts"); exists {
		t.Fatal("upgrade unexpectedly filled a default account list")
	}
}

func TestWalletStorageReadRejectsMalformedAndNonObjectJSON(t *testing.T) {
	for _, contents := range []string{"{", "null", "[]", `"wallet"`} {
		t.Run(contents, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wallet")
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := NewWalletStorage(path).Read()
			if err == nil {
				t.Fatalf("Read accepted %q", contents)
			}
			if contents == "{" {
				var decodeError *WalletJSONDecodeError
				if !errors.As(err, &decodeError) {
					t.Fatalf("malformed error = %T, want WalletJSONDecodeError", err)
				}
			} else {
				var objectError *WalletJSONObjectError
				if !errors.As(err, &objectError) {
					t.Fatalf("non-object error = %T, want WalletJSONObjectError", err)
				}
			}
		})
	}
}

func TestWalletStorageMemoryWriteReturnsExactJSON(t *testing.T) {
	storage := NewMemoryWalletStorage()
	encoded, err := storage.Write(NewObject(
		Member{Key: "z", Value: "é<>&"},
		Member{Key: "a", Value: []any{1, 2.0}},
	))
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n" +
		"    \"a\": [\n" +
		"        1,\n" +
		"        2.0\n" +
		"    ],\n" +
		"    \"z\": \"\\u00e9<>&\"\n" +
		"}"
	if string(encoded) != want || strings.HasSuffix(string(encoded), "\n") {
		t.Fatalf("memory write = %q, want %q", encoded, want)
	}
}

func TestWalletStorageDiskWriteModesAndTemporaryCleanup(t *testing.T) {
	directory := t.TempDir()
	value := defaultWalletDocument()

	newPath := filepath.Join(directory, "new_wallet")
	if returned, err := NewWalletStorage(newPath).Write(value); err != nil || returned != nil {
		t.Fatalf("new Write returned (%q, %v), want (nil, nil)", returned, err)
	}
	assertWalletFileMode(t, newPath, 0o600)

	existingPath := filepath.Join(directory, "existing_wallet")
	if err := os.WriteFile(existingPath, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWalletStorage(existingPath).Write(value); err != nil {
		t.Fatal(err)
	}
	assertWalletFileMode(t, existingPath, 0o640)

	temporary, err := filepath.Glob(filepath.Join(directory, "*.tmp.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temporary) != 0 {
		t.Fatalf("temporary wallet files remain: %v", temporary)
	}
	contents, err := os.ReadFile(existingPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(string(contents), "\n") || !strings.Contains(string(contents), `"version": 1`) {
		t.Fatalf("wallet contents = %q", contents)
	}
}

func TestWalletStorageDoesNotCreateParentDirectory(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "missing", "wallets")
	path := filepath.Join(directory, "default_wallet")
	_, err := NewWalletStorage(path).Write(defaultWalletDocument())
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Write error = %v, want os.ErrNotExist", err)
	}
	if _, statErr := os.Stat(directory); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("parent directory was created: %v", statErr)
	}
}

func TestWalletStorageRenameFallbackDoesNotDeleteDirectoryTarget(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "wallet")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := NewWalletStorage(path).Write(defaultWalletDocument())
	if err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("Write error = %v, want directory refusal", err)
	}
	info, statErr := os.Stat(path)
	if statErr != nil || !info.IsDir() {
		t.Fatalf("wallet directory was removed or replaced: info=%v err=%v", info, statErr)
	}
	temporary, globErr := filepath.Glob(path + ".tmp.*")
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(temporary) != 1 {
		t.Fatalf("fallback temporary files = %v, want Python-compatible retained temp", temporary)
	}
}

func assertWalletFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode for %s = %04o, want %04o", path, got, want)
	}
}
