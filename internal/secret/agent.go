// Package secret provides utilities for dynamic credential resolution and secure agent connections.
package secret

import (
	"net"
	"os"
	"path/filepath"
)

// Try1PAgentSocket attempts to find and connect to the 1Password SSH Agent socket.
// It returns a net.Conn if successful, or nil if not found/available.
func Try1PAgentSocket() net.Conn {
	// 1. Try standard SSH_AUTH_SOCK environment variable
	// This respects user overrides or macOS/Linux standard setups.
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		// #nosec G704 -- SSH_AUTH_SOCK is an intended user-provided socket path
		if conn, err := net.Dial("unix", sock); err == nil {
			return conn
		}
	}

	// 2. Try default 1Password agent path for Linux/macOS.
	// For WSL, this will naturally target the Linux filesystem path 
	// (e.g. if the user used npiperelay to map it to ~/.1password/agent.sock).
	home, err := os.UserHomeDir()
	if err == nil {
		defaultPath := filepath.Join(home, ".1password", "agent.sock")
		if conn, err := net.Dial("unix", defaultPath); err == nil {
			return conn
		}
	}

	// On native Windows, connecting to named pipes directly via net.Dial("unix", ...) is not supported
	// without cgo/winio. Since this tool targets WSL as the primary Linux-side runner, we
	// fallback to existing SSH key mechanisms if agent connection fails.
	return nil
}
