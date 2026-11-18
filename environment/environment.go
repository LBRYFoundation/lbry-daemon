package environment

import "os"

func AdminUsername() string {
	return os.Getenv("ADMIN_USERNAME")
}

func AdminPassword() string {
	return os.Getenv("ADMIN_PASSWORD")
}
