//go:build windows

package config

import (
	"os"
)

// isSecureDir validates that a directory exists and is a directory on Windows.
func isSecureDir(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	return true
}
