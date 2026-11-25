package auth

import (
	"crypto/subtle"
	"lbry/daemon/environment"
	"net/http"
)

type DefaultAuthManager struct {
	Users []User
}

var _ AuthManager = (*DefaultAuthManager)(nil)

type UserAuthManager struct{}

var _ AuthManager = (*UserAuthManager)(nil)

// NewAuthManager checks every request against the admin credentials in the environment when both are set, and
// accepts every request when they are not (the open manager, for development). The second result tells which.
func NewAuthManager() (AuthManager, bool) {
	if environment.AdminUsername() != "" && environment.AdminPassword() != "" {
		return &UserAuthManager{}, true
	}
	return NewDefaultAuthManager(), false
}

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
