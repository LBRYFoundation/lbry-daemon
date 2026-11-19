package rpc

import (
	"encoding/json"
	"lbry/daemon/auth"
	"lbry/daemon/blob"
	"lbry/daemon/crypto"
	"lbry/daemon/settings"
	"lbry/daemon/wallet"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func walletStatus(t *testing.T, body string, userAgent string) (int, map[string]any) {
	t.Helper()
	dir := t.TempDir()
	plain, _ := json.Marshal(wallet.WalletFile{Name: "My Wallet", Accounts: []*wallet.Account{{Name: "plain", Seed: "seed words"}}})
	secret, _ := json.Marshal(wallet.WalletFile{Name: "Secret", Accounts: []*wallet.Account{{
		Name: "enc", Locked: true, Seed: crypto.Encrypt("hunter2", "seed words", []byte("0123456789abcdef")),
	}}})
	os.WriteFile(filepath.Join(dir, wallet.DEFAULT_WALLET), plain, 0o600)
	os.WriteFile(filepath.Join(dir, "secret"), secret, 0o600)
	manager := wallet.NewDefaultWalletManager(dir)
	if err := manager.(*wallet.DefaultWalletManager).Load(); err != nil {
		t.Fatal(err)
	}
	server := NewRPCServer(settings.InitializeConfiguration(), &auth.DefaultAuthManager{}, &blob.BlobManager{}, nil, manager)

	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	rec := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(rec, req)
	var reply map[string]any
	json.Unmarshal(rec.Body.Bytes(), &reply)
	return rec.Code, reply
}

func TestWalletStatus(t *testing.T) {
	for name, tc := range map[string]struct {
		params string
		want   map[string]any
	}{
		"default wallet without parameters": {`{}`, map[string]any{"is_encrypted": false, "is_syncing": false, "is_locked": false}},
		"locked wallet":                     {`{"wallet_id":"secret"}`, map[string]any{"is_encrypted": true, "is_syncing": false, "is_locked": true}},
	} {
		code, reply := walletStatus(t, `{"jsonrpc":"2.0","id":1,"method":"wallet_status","params":`+tc.params+`}`, "")
		result, _ := reply["result"].(map[string]any)
		if code != 200 || len(result) != len(tc.want) {
			t.Errorf("%s: status %d, reply %v", name, code, reply)
			continue
		}
		for key, want := range tc.want {
			if result[key] != want {
				t.Errorf("%s: %s = %v, want %v", name, key, result[key], want)
			}
		}
	}
}

func TestWalletStatusWithoutParamsKey(t *testing.T) {
	_, reply := walletStatus(t, `{"jsonrpc":"2.0","id":1,"method":"wallet_status"}`, "")
	if _, ok := reply["result"].(map[string]any); !ok {
		t.Errorf("expected the default wallet's status, got %v", reply)
	}
}

func TestWalletStatusUnknownWallet(t *testing.T) {
	code, reply := walletStatus(t, `{"jsonrpc":"2.0","id":1,"method":"wallet_status","params":{"wallet_id":"missing"}}`, "")
	if _, ok := reply["result"]; ok {
		t.Errorf("an unknown wallet must not return a result (status %d): %v", code, reply)
	}
}
