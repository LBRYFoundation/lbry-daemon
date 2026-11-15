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
	server := NewRPCServer(settings.InitializeConfiguration(), &auth.AuthManager{}, &blob.BlobManager{}, nil, wallet.NewDefaultWalletManager(t.TempDir()))
	return server.httpServer.Handler
}

// wallet_list needs an authenticated user: an allowed request reaches the handler (it answers with a list),
// a Refused one is answered 401. A method that is implemented, so that the tests keep working as the methods
// that are still answered 501 get implemented.
const (
	Reached = `"items"`
	Refused = "Not permitted"
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
	body := `{"jsonrpc":"2.0","id":1,"method":"wallet_list","params":{}}`
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

func TestEveryClientNeedsCredentials(t *testing.T) {
	handler := newTestHandler(t)
	t.Setenv("ADMIN_USERNAME", "admin")
	t.Setenv("ADMIN_PASSWORD", "s3cret")
	for name, o := range map[string]opts{
		"loopback client":   {},
		"remote address":    {remote: "203.0.113.9:4000"},
		"foreign host":      {host: "evil.example:5279"},
		"foreign origin":    {origin: "https://evil.example"},
		"wrong credentials": {user: "admin", pass: "wrong"},
	} {
		if rec := do(handler, o); !strings.Contains(rec.Body.String(), Refused) {
			t.Errorf("%s should be refused, got %s", name, rec.Body.String())
		}
	}
}

func TestAdminCredentialsWorkFromAnywhere(t *testing.T) {
	handler := newTestHandler(t)
	t.Setenv("ADMIN_USERNAME", "admin")
	t.Setenv("ADMIN_PASSWORD", "s3cret")
	for name, o := range map[string]opts{
		"loopback client": {},
		"remote address":  {remote: "203.0.113.9:4000", host: "daemon.example:5279"},
		"web origin":      {origin: "https://app.example"},
	} {
		o.user, o.pass = "admin", "s3cret"
		if rec := do(handler, o); !strings.Contains(rec.Body.String(), Reached) {
			t.Errorf("%s with the admin credentials should be let in, got %s", name, rec.Body.String())
		}
	}
}

// The packaged desktop runs with webSecurity on. Electron removes Origin from the POST but the browser's
// preflight still carries one, and the response must carry Access-Control-Allow-Origin, or Chromium will not
// let the app read the answer.
func TestDesktopPreflightAndPost(t *testing.T) {
	handler := newTestHandler(t)
	t.Setenv("ADMIN_USERNAME", "admin")
	t.Setenv("ADMIN_PASSWORD", "s3cret")

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

	post := do(handler, opts{user: "admin", pass: "s3cret"}) // no Origin: Electron removed it
	if !strings.Contains(post.Body.String(), Reached) {
		t.Errorf("the desktop's authenticated POST should be let in, got %s", post.Body.String())
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
