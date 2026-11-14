// Package environment reads the daemon's environment variables in one place.
package environment

import "os"

// AdminUsername and AdminPassword are the credentials of the admin (ADMIN_USERNAME, ADMIN_PASSWORD). Both
// must be set for HTTP basic authentication to be accepted.
func AdminUsername() string { return os.Getenv("ADMIN_USERNAME") }
func AdminPassword() string { return os.Getenv("ADMIN_PASSWORD") }

// AllowedOrigin is the one browser Origin that may use the API without credentials (LBRY_ALLOWED_ORIGIN);
// "*" allows every origin. Empty means none.
func AllowedOrigin() string { return os.Getenv("LBRY_ALLOWED_ORIGIN") }
