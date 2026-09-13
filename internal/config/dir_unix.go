//go:build !windows

package config

import (
	"os"
	"syscall"
)

// isSecureDir validates that a directory is owned by the current user and not writable by others.
func isSecureDir(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	// Must not be group-writable or other-writable (bits 0o022)
	if info.Mode().Perm()&0o022 != 0 {
		return false
	}
	// Must be owned by current running user
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	uid := os.Getuid()
	if uid < 0 {
		return false
	}
	return stat.Uid == uint32(uid) // #nosec G115 -- guarded by uid >= 0
}
