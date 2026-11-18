package auth

import "net/http"

type AuthManager interface {
	ValidateUser(*http.Request) (*User, bool)
	ValidateAdminUser(*http.Request) (*User, bool)
}
