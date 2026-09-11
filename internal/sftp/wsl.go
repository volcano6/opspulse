package sftp

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// IsWSL returns true if the current environment is running inside Windows Subsystem for Linux.
func IsWSL() bool {
	// First check environment variable (common in newer WSL2)
	if os.Getenv("WSL_DISTRO_NAME") != "" {
		return true
	}
	// Fallback to checking /proc/version for Microsoft/WSL strings
	if b, err := os.ReadFile("/proc/version"); err == nil {
		content := strings.ToLower(string(b))
		if strings.Contains(content, "microsoft") || strings.Contains(content, "wsl") {
			return true
		}
	}
	return false
}

// WindowsUserHome returns the WSL path to the Windows user's home directory (e.g., /mnt/c/Users/username).
func WindowsUserHome() (string, error) {
	cmd := exec.Command("cmd.exe", "/c", "echo %USERPROFILE%")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get Windows USERPROFILE: %w", err)
	}
	winPath := strings.TrimSpace(string(out))
	if winPath == "" {
		return "", fmt.Errorf("Windows USERPROFILE is empty")
	}

	return translateWindowsPathToWSL(winPath), nil
}

// translateWindowsPathToWSL translates a path like "C:\Users\Username" to "/mnt/c/Users/Username"
func translateWindowsPathToWSL(winPath string) string {
	winPath = strings.TrimSpace(winPath)
	if len(winPath) >= 2 && winPath[1] == ':' {
		drive := strings.ToLower(string(winPath[0]))
		rest := strings.ReplaceAll(winPath[2:], "\\", "/")
		return fmt.Sprintf("/mnt/%s%s", drive, rest)
	}
	return winPath
}

// translateWSLPathToWindows translates a path like "/mnt/c/Users/Username" to "C:\Users\Username"
func translateWSLPathToWindows(wslPath string) string {
	if strings.HasPrefix(wslPath, "/mnt/") && len(wslPath) >= 7 && wslPath[6] == '/' {
		drive := strings.ToUpper(string(wslPath[5]))
		rest := strings.ReplaceAll(wslPath[7:], "/", "\\")
		return fmt.Sprintf("%s:\\%s", drive, rest)
	}
	return wslPath
}

// BridgeKeyToWindows copies the WSL SSH private key to the mirrored Windows directory.
// It returns the native Windows path (C:\...) to be passed to GUI clients.
func BridgeKeyToWindows(linuxKeyPath string) (string, error) {
	if _, err := os.Stat(linuxKeyPath); os.IsNotExist(err) {
		return "", fmt.Errorf("local private key does not exist at %s (run 'ops server setup-key %s' first)", linuxKeyPath, "<server>")
	}

	winHomeWSL, err := WindowsUserHome()
	if err != nil {
		return "", err
	}

	// Calculate target paths
	baseName := filepath.Base(linuxKeyPath)
	winMirrorWSLDir := filepath.Join(winHomeWSL, ".ssh", "opspulse")
	winMirrorWSLPath := filepath.Join(winMirrorWSLDir, baseName)

	if err := os.MkdirAll(winMirrorWSLDir, 0o750); err != nil {
		return "", fmt.Errorf("failed to create Windows mirror directory: %w", err)
	}

	srcContent, err := os.ReadFile(linuxKeyPath) // #nosec G304 -- intended local key path
	if err != nil {
		return "", fmt.Errorf("failed to read linux key: %w", err)
	}

	needsCopy := true
	if dstContent, err := os.ReadFile(winMirrorWSLPath); err == nil { // #nosec G304
		if bytes.Equal(srcContent, dstContent) {
			needsCopy = false
		}
	}

	if needsCopy {
		// #nosec G703 -- winMirrorWSLPath is constructed safely using filepath.Base
		if err := os.WriteFile(winMirrorWSLPath, srcContent, 0o600); err != nil {
			return "", fmt.Errorf("failed to copy key to Windows mirror: %w", err)
		}
		// On WSL, chmod on /mnt/c might not fully mimic POSIX permissions, but we set it anyway.
		_ = os.Chmod(winMirrorWSLPath, 0o600) // #nosec G302
	}

	return translateWSLPathToWindows(winMirrorWSLPath), nil
}
