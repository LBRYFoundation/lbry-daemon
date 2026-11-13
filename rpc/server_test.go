package rpc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lbry/daemon/auth"
	"lbry/daemon/blob"
	"lbry/daemon/settings"
	"lbry/daemon/wallet"
)

func newTestHandler(t *testing.T) http.Handler {
	t.Setenv("ADMIN_USERNAME", "")
	t.Setenv("ADMIN_PASSWORD", "")
	t.Setenv("LBRY_ALLOWED_ORIGIN", "")
	server := CreateServer(settings.InitializeConfiguration(), &auth.AuthManager{}, &blob.BlobManager{}, nil, &wallet.WalletManager{})
	return server.httpServer.Handler
}

// wallet_balance needs an authenticated user: an allowed request reaches the handler (501, not implemented),
// a refused one is answered 401.
const (
	reached = "not implemented"
	refused = "Not permitted"
)

type opts struct {
	method, remote, host, origin, user, pass string
}

func do(handler http.Handler, o opts) *httptest.ResponseRecorder {
	if o.method == "" {
		o.method = "POST"
	}
	if o.remote == "" {
		o.remote = "127.0.0.1:4000"
	}
	if o.host == "" {
		o.host = "localhost:5279"
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"wallet_balance","params":{}}`
	if o.method == "OPTIONS" {
		body = ""
	}
	req := httptest.NewRequest(o.method, "http://"+o.host+"/", strings.NewReader(body))
	req.RemoteAddr, req.Host = o.remote, o.host
	if o.origin != "" {
		req.Header.Set("Origin", o.origin)
	}
	if o.user != "" || o.pass != "" {
		req.SetBasicAuth(o.user, o.pass)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestLocalClientNeedsNoCredentials(t *testing.T) {
	handler := newTestHandler(t)
	if rec := do(handler, opts{}); !strings.Contains(rec.Body.String(), reached) {
		t.Errorf("a loopback client should be let in, got %s", rec.Body.String())
	}
}

func TestRequestsThatAreNotTrustedLocalAreRefused(t *testing.T) {
	handler := newTestHandler(t)
	for name, o := range map[string]opts{
		"remote address": {remote: "203.0.113.9:4000"},
		"foreign host":   {host: "evil.example:5279"},
		"foreign origin": {origin: "https://evil.example"},
	} {
		if rec := do(handler, o); !strings.Contains(rec.Body.String(), refused) {
			t.Errorf("%s should be refused, got %s", name, rec.Body.String())
		}
	}
}

// The packaged desktop runs with webSecurity on. Electron removes Origin from the POST but the browser's
// preflight still carries one, and the response must carry Access-Control-Allow-Origin, or Chromium will not
// let the app read the answer.
func TestDesktopPreflightAndPost(t *testing.T) {
	handler := newTestHandler(t)

	pre := do(handler, opts{method: "OPTIONS", origin: "file://"})
	if pre.Code != http.StatusNoContent {
		t.Fatalf("preflight status %d", pre.Code)
	}
	if got := pre.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("preflight Access-Control-Allow-Origin = %q", got)
	}
	for _, header := range []string{"Content-Type", "Authorization"} {
		if !strings.Contains(pre.Header().Get("Access-Control-Allow-Headers"), header) {
			t.Errorf("preflight does not allow the %s header: %q", header, pre.Header().Get("Access-Control-Allow-Headers"))
		}
	}

	post := do(handler, opts{}) // no Origin: Electron removed it
	if !strings.Contains(post.Body.String(), reached) {
		t.Errorf("the desktop's POST should be let in, got %s", post.Body.String())
	}
	if got := post.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("the POST response needs Access-Control-Allow-Origin, got %q", got)
	}
}

func TestPreflightIsAnsweredWhateverTheOrigin(t *testing.T) {
	handler := newTestHandler(t)
	rec := do(handler, opts{method: "OPTIONS", origin: "https://evil.example", remote: "203.0.113.9:4000"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestAllowedOriginWithoutCredentials(t *testing.T) {
	handler := newTestHandler(t)
	t.Setenv("LBRY_ALLOWED_ORIGIN", "https://app.example")
	if rec := do(handler, opts{origin: "https://app.example"}); !strings.Contains(rec.Body.String(), reached) {
		t.Errorf("the allowed origin should be let in, got %s", rec.Body.String())
	}
	if rec := do(handler, opts{origin: "https://other.example"}); !strings.Contains(rec.Body.String(), refused) {
		t.Errorf("another origin should be refused, got %s", rec.Body.String())
	}
}

func TestBasicAuthFromAnAllowedOrigin(t *testing.T) {
	handler := newTestHandler(t)
	t.Setenv("LBRY_ALLOWED_ORIGIN", "https://app.example")
	t.Setenv("ADMIN_USERNAME", "admin")
	t.Setenv("ADMIN_PASSWORD", "s3cret")

	remote := opts{origin: "https://app.example", remote: "203.0.113.9:4000", host: "daemon.example:5279"}
	remote.user, remote.pass = "admin", "s3cret"
	if rec := do(handler, remote); !strings.Contains(rec.Body.String(), reached) {
		t.Errorf("the admin credentials should work from anywhere, got %s", rec.Body.String())
	}
	remote.pass = "wrong"
	if rec := do(handler, remote); !strings.Contains(rec.Body.String(), refused) {
		t.Errorf("wrong credentials should be refused, got %s", rec.Body.String())
	}
}

func TestWildcardAllowedOrigin(t *testing.T) {
	handler := newTestHandler(t)
	t.Setenv("LBRY_ALLOWED_ORIGIN", "*")
	if rec := do(handler, opts{origin: "https://anything.example"}); !strings.Contains(rec.Body.String(), reached) {
		t.Errorf("a wildcard allowed origin should let any origin in, got %s", rec.Body.String())
	}
}
