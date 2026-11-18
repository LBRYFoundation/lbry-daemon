package auth

import "crypto/subtle"
import "net/http"

import "lbry/daemon/environment"

type DefaultAuthManager struct {
	Users []User
}

var _ AuthManager = (*DefaultAuthManager)(nil)

type UserAuthManager struct{}

var _ AuthManager = (*UserAuthManager)(nil)

func NewDefaultAuthManager() AuthManager {
	return &DefaultAuthManager{}
}

type User struct {
}

func (authManager *DefaultAuthManager) ValidateUser(req *http.Request) (*User, bool) {
	return nil, true
}

func (authManager *UserAuthManager) ValidateUser(req *http.Request) (*User, bool) {
	// TODO Validate normal user
	return authManager.ValidateAdminUser(req)
}

func (authManager *DefaultAuthManager) ValidateAdminUser(req *http.Request) (*User, bool) {
	return nil, true
}

func (authManager *UserAuthManager) ValidateAdminUser(req *http.Request) (*User, bool) {
	username, password, hasBasic := req.BasicAuth()
	if hasBasic && username != "" && password != "" {
		userOK := subtle.ConstantTimeCompare([]byte(username), []byte(environment.AdminUsername()))
		passOK := subtle.ConstantTimeCompare([]byte(password), []byte(environment.AdminPassword()))

		if userOK&passOK == 1 {
			return &User{}, true
		}
	}
	return nil, false
}
