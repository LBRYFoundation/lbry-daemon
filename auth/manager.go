package auth

import "crypto/subtle"
import "net/http"

import "lbry/daemon/environment"

type AuthManager struct {
	Users []User
}

type User struct {
}

func (authManager *AuthManager) WantsRPCAuthentication() bool {
	return true
}

func (authManager *AuthManager) ValidateGuest(req *http.Request) (*User, bool) {
	return nil, true
}

func (authManager *AuthManager) ValidateUser(req *http.Request) (*User, bool) {
    // TODO Validate normal user
	return authManager.ValidateAdminUser(req)
}

func (authManager *AuthManager) ValidateAdminUser(req *http.Request) (*User, bool) {
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
