package sftp

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/volcano6/opspulse/internal/platform"
)

// IsWSL returns true if the current environment is running inside Windows Subsystem for Linux.
func IsWSL() bool {
	return platform.IsWSL()
}

// WindowsUserHome returns the WSL path to the Windows user's home directory (e.g., /mnt/c/Users/username).
func WindowsUserHome() (string, error) {
	return platform.WindowsUserHome()
}

// translateWindowsPathToWSL translates a path like "C:\Users\Username" to "/mnt/c/Users/Username"
func translateWindowsPathToWSL(winPath string) string {
	return platform.ToWSLPath(winPath)
}

// translateWSLPathToWindows translates a path like "/mnt/c/Users/Username" to "C:\Users\Username"
func translateWSLPathToWindows(wslPath string) string {
	return platform.ToWindowsPath(wslPath)
}

// BridgeKeyToWindows copies the WSL SSH private key to the mirrored Windows directory.
// It returns the native Windows path (C:\...) to be passed to GUI clients.
func BridgeKeyToWindows(linuxKeyPath string) (string, error) {
	if _, err := os.Stat(linuxKeyPath); os.IsNotExist(err) {
		return "", fmt.Errorf("local private key does not exist at %s (run 'ops server setup-key %s' first)", linuxKeyPath, "<server>")
	}

	content, err := os.ReadFile(linuxKeyPath) // #nosec G304 -- intended local key path
	if err != nil {
		return "", fmt.Errorf("failed to read linux key: %w", err)
	}

	baseName := filepath.Base(linuxKeyPath)
	winPath, err := BridgeKeyContentToWindows(baseName, content)
	if err != nil {
		return "", err
	}
	return winPath, nil
}

// BridgeKeyContentToWindows mirrors raw private key bytes into the Windows-side
// OpsPulse key directory so that GUI clients running outside WSL can read them.
// It returns the native Windows path of the mirrored file.
//
// Unlike an ephemeral temp file, this mirror is intentionally kept on disk: GUI
// clients open asynchronously and may read the key long after the launching
// process exits.
func BridgeKeyContentToWindows(baseName string, content []byte) (string, error) {
	winHomeWSL, err := platform.WindowsUserHome()
	if err != nil {
		return "", err
	}

	// Calculate target paths
	winMirrorWSLDir := filepath.Join(winHomeWSL, ".ssh", "opspulse")
	winMirrorWSLPath := filepath.Join(winMirrorWSLDir, filepath.Base(baseName))

	if err := os.MkdirAll(winMirrorWSLDir, 0o750); err != nil {
		return "", fmt.Errorf("failed to create Windows mirror directory: %w", err)
	}

	if dstContent, err := os.ReadFile(winMirrorWSLPath); err != nil || !bytes.Equal(content, dstContent) { // #nosec G304
		if err := os.WriteFile(winMirrorWSLPath, content, 0o600); err != nil { // #nosec G304 G703
			return "", fmt.Errorf("failed to copy key to Windows mirror: %w", err)
		}
		// On WSL, chmod on /mnt/c might not fully mimic POSIX permissions, but we set it anyway.
		_ = os.Chmod(winMirrorWSLPath, 0o600) // #nosec G302
	}

	return platform.ToWindowsPath(winMirrorWSLPath), nil
}
