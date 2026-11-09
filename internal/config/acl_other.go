//go:build !windows

package config

// checkCredentialFileACL reports nothing on Unix because the owner-only mode
// check covers the file permissions.
func checkCredentialFileACL(string) error { return nil }
