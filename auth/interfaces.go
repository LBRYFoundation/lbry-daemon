package auth

import "net/http"

type AuthManager interface {
	ValidateGuest(*http.Request) (*User, bool)
	ValidateUser(*http.Request) (*User, bool)
	ValidateAdminUser(*http.Request) (*User, bool)
}
