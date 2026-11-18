package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func request() *http.Request {
	return httptest.NewRequest("POST", "http://localhost:5279/", nil)
}

func withAdmin(t *testing.T) {
	t.Setenv("ADMIN_USERNAME", "admin")
	t.Setenv("ADMIN_PASSWORD", "s3cret")
}

func TestNobodyIsAdminByDefault(t *testing.T) {
	t.Setenv("ADMIN_USERNAME", "")
	t.Setenv("ADMIN_PASSWORD", "")
	manager := &AuthManager{}
	if _, ok := manager.ValidateAdminUser(request()); ok {
		t.Fatal("a plain request must not be admin")
	}
	if _, ok := manager.ValidateUser(request()); ok {
		t.Fatal("a plain request must not be a user")
	}
	if _, ok := manager.ValidateGuest(request()); !ok {
		t.Fatal("guests are always allowed")
	}
}

func TestBasicAuth(t *testing.T) {
	withAdmin(t)
	manager := &AuthManager{}
	for _, tc := range []struct {
		name, user, pass string
		want             bool
	}{
		{"right credentials", "admin", "s3cret", true},
		{"wrong password", "admin", "nope", false},
		{"wrong user", "root", "s3cret", false},
		{"empty password", "admin", "", false},
		{"empty both", "", "", false},
	} {
		req := request()
		req.SetBasicAuth(tc.user, tc.pass)
		if _, ok := manager.ValidateAdminUser(req); ok != tc.want {
			t.Errorf("%s: admin=%v, want %v", tc.name, ok, tc.want)
		}
	}
}

func TestEmptyConfiguredCredentialsAreNeverAccepted(t *testing.T) {
	t.Setenv("ADMIN_USERNAME", "")
	t.Setenv("ADMIN_PASSWORD", "")
	req := request()
	req.SetBasicAuth("", "")
	if _, ok := (&AuthManager{}).ValidateAdminUser(req); ok {
		t.Fatal("empty credentials matched empty configuration")
	}
}

func TestAdminIsAlsoAUser(t *testing.T) {
	withAdmin(t)
	manager := &AuthManager{}
	req := request()
	req.SetBasicAuth("admin", "s3cret")
	if _, ok := manager.ValidateUser(req); !ok {
		t.Fatal("a valid admin must be a valid user")
	}
}
