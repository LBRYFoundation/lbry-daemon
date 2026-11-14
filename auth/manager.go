package auth

import "context"
import "crypto/subtle"
import "net/http"

import "lbry/daemon/environment"

type AuthManager struct {
	Users []User
}

type User struct {
}

type trustedLocalKey struct{}

// TrustLocal marks a request as coming from a trusted local client. The RPC layer calls it after checking
// where the request came from; the auth manager only reads the mark.
func TrustLocal(req *http.Request) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), trustedLocalKey{}, true))
}

func isTrustedLocal(req *http.Request) bool {
	trusted, _ := req.Context().Value(trustedLocalKey{}).(bool)
	return trusted
}

func (authManager *AuthManager) WantsRPCAuthentication() bool {
	return true
}

// IsAdminUser checks a username and password against ADMIN_USERNAME and ADMIN_PASSWORD, in constant time.
// Empty credentials are never accepted.
func (authManager *AuthManager) IsAdminUser(username string, password string) bool {
	adminUsername, adminPassword := environment.AdminUsername(), environment.AdminPassword()
	if adminUsername == "" || adminPassword == "" {
		return false
	}
	userOK := subtle.ConstantTimeCompare([]byte(username), []byte(adminUsername))
	passOK := subtle.ConstantTimeCompare([]byte(password), []byte(adminPassword))
	return userOK&passOK == 1
}

func (authManager *AuthManager) ValidateGuest(req *http.Request) (*User, bool) {
	return nil, true
}

// ValidateUser accepts a valid admin, since an admin is always a valid user. There are no other users yet.
func (authManager *AuthManager) ValidateUser(req *http.Request) (*User, bool) {
	return authManager.ValidateAdminUser(req)
}

// ValidateAdminUser accepts HTTP basic authentication with the admin credentials, from any address, and a
// request the RPC layer has marked as a trusted local client (see TrustLocal).
func (authManager *AuthManager) ValidateAdminUser(req *http.Request) (*User, bool) {
	username, password, hasBasic := req.BasicAuth()
	if hasBasic && authManager.IsAdminUser(username, password) {
		return &User{}, true
	}
	if isTrustedLocal(req) {
		return &User{}, true
	}
	return nil, false
}
