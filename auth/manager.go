package auth

import "net/http"
import "os"

type AuthManager struct {
	Users []User
}

type User struct {
}

func (authManager *AuthManager) WantsRPCAuthentication() bool {
	return true
}

func (authManager *AuthManager) IsAdminUser(username string, password string) bool {
	return username == getAdminUsername() && password == getAdminPassword()
}

// func (authManager *AuthManager) ValidateUnauthenticatedUser() {
// 	return true // TODO: Make only true when dangerous endpoints should be freely accessible
// }

func (authManager *AuthManager) ValidateGuest(req *http.Request) (*User, bool) {
	return nil, true
}

func (authManager *AuthManager) ValidateUser(req *http.Request) (*User, bool) {

	return nil, false
}

func (authManager *AuthManager) ValidateAdminUser(req *http.Request) (*User, bool) {
	// mayUse := !PROTECT_MODE_ADMIN
	//
	// 	if PROTECT_MODE_ADMIN {
	// 		username, password, ok := req.BasicAuth()
	// 		if ok {
	// 			if auth.IsAdminUser(username, password) {
	// 				mayUse = true
	// 			}
	// 		}
	// 	}

	return nil, false
}

func getAdminUsername() string {
	return os.Getenv("ADMIN_USERNAME")
}

func getAdminPassword() string {
	return os.Getenv("ADMIN_PASSWORD")
}
