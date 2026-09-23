package server

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// ControlMasterDirName is the ~/.ssh subdirectory holding the multiplexing
// sockets. They live in their own directory rather than in ~/.ssh itself so a
// stray socket is never mistaken for user-managed key material.
const ControlMasterDirName = "opspulse-cm"

// ControlPersist is how long an authenticated connection stays reusable after
// the last client detaches. Long enough to cover the usual "connect, look
// around, open a second session" burst, short enough that a forgotten socket
// does not outlive the work.
const ControlPersist = "10m"

// ControlMasterEnabled reports whether connection multiplexing is worth
// injecting on this platform.
//
// Windows' bundled OpenSSH does not implement the unix sockets ControlPath
// needs, so the flags are left out there rather than handed to a client that
// cannot honour them.
func ControlMasterEnabled() bool {
	return runtime.GOOS != "windows"
}

// ControlMasterDir returns the directory holding the multiplexing sockets.
func ControlMasterDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine user home directory: %w", err)
	}
	return filepath.Join(home, ".ssh", ControlMasterDirName), nil
}

// ControlMasterPath returns the ControlPath pattern.
//
// The %r/%h/%p tokens keep one socket per user@host:port, so two inventory
// entries that resolve to the same machine reuse a session while a second port
// on that machine does not collide with the first.
func ControlMasterPath() (string, error) {
	dir, err := ControlMasterDir()
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(filepath.Join(dir, "%r@%h:%p")), nil
}

// EnsureControlMasterDir creates the socket directory with 0700.
//
// The directory has to exist before ssh runs. Measured on OpenSSH 9.6: when the
// ControlPath's parent is missing, ssh authenticates and then dies with
//
//	unix_listener: cannot bind to path ...: No such file or directory
//
// and exit status 255 - ControlMaster=auto does NOT fall back to a plain
// connection. So a missing directory must mean "omit the flags", never "pass a
// path ssh will fail to bind".
func EnsureControlMasterDir() error {
	if !ControlMasterEnabled() {
		return nil
	}
	dir, err := ControlMasterDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create SSH multiplexing directory %s: %w", dir, err)
	}
	return nil
}

// controlMasterPathIfReady returns the ControlPath only once its directory
// exists.
//
// This is the guard that keeps the feature from making things worse: ssh treats
// an unbindable ControlPath as fatal (see EnsureControlMasterDir), so an absent
// directory has to mean "no multiplexing" rather than "hand ssh a path it will
// die on". Every caller below is argv- or config-shaped and cannot create
// anything, which is why the check lives here and the creation does not.
func controlMasterPathIfReady() (string, bool) {
	if !ControlMasterEnabled() {
		return "", false
	}
	path, err := ControlMasterPath()
	if err != nil {
		return "", false
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		return "", false
	}
	return path, true
}

// ControlMasterArgs returns the -o flags for the system ssh or sftp client, or
// nil when multiplexing is unavailable.
func ControlMasterArgs() []string {
	path, ok := controlMasterPathIfReady()
	if !ok {
		return nil
	}
	return []string{
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + path,
		"-o", "ControlPersist=" + ControlPersist,
	}
}

// ControlMasterConfigLines returns the ssh_config equivalent for the exported
// managed block, or nil when multiplexing is unavailable.
func ControlMasterConfigLines() []string {
	path, ok := controlMasterPathIfReady()
	if !ok {
		return nil
	}
	return []string{
		"    ControlMaster auto",
		"    ControlPath " + path,
		"    ControlPersist " + ControlPersist,
	}
}
