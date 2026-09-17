// Package platform provides host-environment detection and cross-boundary path
// translation helpers that are shared by several OpsPulse packages.
//
// It deliberately depends on nothing but the standard library so that both
// internal/secret and internal/sftp can use it without creating an import cycle
// (sftp -> executor -> secret).
package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// IsWSL reports whether the current process is running inside Windows Subsystem for Linux.
func IsWSL() bool {
	// First check environment variable (common in newer WSL2).
	if os.Getenv("WSL_DISTRO_NAME") != "" {
		return true
	}
	// Fallback to checking /proc/version for Microsoft/WSL strings.
	if b, err := os.ReadFile("/proc/version"); err == nil {
		content := strings.ToLower(string(b))
		if strings.Contains(content, "microsoft") || strings.Contains(content, "wsl") {
			return true
		}
	}
	return false
}

// ToWSLPath translates a native Windows path such as `C:\Users\name` into its
// WSL mount form `/mnt/c/Users/name`. Non Windows-looking input is returned unchanged.
func ToWSLPath(winPath string) string {
	trimmed := strings.TrimSpace(winPath)
	if len(trimmed) >= 2 && trimmed[1] == ':' {
		drive := strings.ToLower(string(trimmed[0]))
		rest := strings.ReplaceAll(trimmed[2:], `\`, "/")
		return fmt.Sprintf("/mnt/%s%s", drive, rest)
	}
	return trimmed
}

// ToWindowsPath translates a WSL mount path such as `/mnt/c/Users/name` into its
// native Windows form `C:\Users\name`. Paths outside /mnt are returned unchanged.
func ToWindowsPath(wslPath string) string {
	if strings.HasPrefix(wslPath, "/mnt/") && len(wslPath) >= 7 && wslPath[6] == '/' {
		drive := strings.ToUpper(string(wslPath[5]))
		rest := strings.ReplaceAll(wslPath[7:], "/", `\`)
		return fmt.Sprintf(`%s:\%s`, drive, rest)
	}
	return wslPath
}

func isSystemWindowsUser(name string) bool {
	switch strings.ToLower(name) {
	case "all users", "default", "default user", "public", "desktop.ini":
		return true
	default:
		return false
	}
}

// WindowsUserHome returns the WSL path to the Windows user's home directory
// (e.g. /mnt/c/Users/username). It only makes sense inside WSL.
func WindowsUserHome() (string, error) {
	cmd := exec.Command("cmd.exe", "/c", "echo %USERPROFILE%")
	out, err := cmd.Output()
	if err == nil {
		winPath := strings.TrimSpace(string(out))
		if winPath != "" && !strings.Contains(winPath, "%USERPROFILE%") {
			return ToWSLPath(winPath), nil
		}
	}

	// Fallback 1: check USERPROFILE env var if passed via WSLENV
	if winPath := strings.TrimSpace(os.Getenv("USERPROFILE")); winPath != "" {
		return ToWSLPath(winPath), nil
	}

	// Fallback 2: Direct filesystem scan of /mnt/c/Users (works even if cmd.exe has no execute permission)
	if entries, readErr := os.ReadDir("/mnt/c/Users"); readErr == nil {
		currUser := strings.ToLower(os.Getenv("USER"))
		if currUser != "" {
			for _, entry := range entries {
				if entry.IsDir() && !isSystemWindowsUser(entry.Name()) {
					if strings.HasPrefix(strings.ToLower(entry.Name()), currUser) {
						return filepath.Join("/mnt/c/Users", entry.Name()), nil
					}
				}
			}
		}
		for _, entry := range entries {
			if entry.IsDir() && !isSystemWindowsUser(entry.Name()) {
				candidate := filepath.Join("/mnt/c/Users", entry.Name())
				if info, statErr := os.Stat(filepath.Join(candidate, "AppData", "Local")); statErr == nil && info.IsDir() {
					return candidate, nil
				}
			}
		}
	}

	if err != nil {
		return "", fmt.Errorf("failed to get Windows USERPROFILE: %w", err)
	}
	return "", fmt.Errorf("failed to get Windows USERPROFILE: no user profile found under /mnt/c/Users")
}

// WindowsLocalAppData returns the Windows %LOCALAPPDATA% directory in the form
// the *current* process can use: the WSL mount path (/mnt/c/Users/.../AppData/Local)
// inside WSL, or the native C:\...\AppData\Local path on Windows.
//
// winget installs packages under this directory, which is why callers use it to
// find tools that were installed but never made visible on PATH.
func WindowsLocalAppData() (string, error) {
	out, cmdErr := exec.Command("cmd.exe", "/c", "echo %LOCALAPPDATA%").Output() // #nosec G204 -- fixed command, no user input
	winLocal := strings.TrimSpace(string(out))
	if cmdErr == nil && winLocal != "" && !strings.Contains(winLocal, "%LOCALAPPDATA%") {
		if !IsWSL() {
			return winLocal, nil
		}
		return ToWSLPath(winLocal), nil
	}

	// Fallback 1: check LOCALAPPDATA env var if passed via WSLENV
	if winLocal := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); winLocal != "" {
		if !IsWSL() {
			return winLocal, nil
		}
		return ToWSLPath(winLocal), nil
	}

	// Fallback 2: derive it from the user profile when cmd.exe interop is not
	// available or did not expand the variable.
	home, homeErr := WindowsUserHome()
	if homeErr != nil {
		return "", fmt.Errorf("resolve Windows LOCALAPPDATA: %w", homeErr)
	}
	return filepath.Join(home, "AppData", "Local"), nil
}

// FileExists reports whether the given path exists and is a regular file.
func FileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// wslFMaskPattern matches the automount fmask values that strip the execute bit
// from Windows binaries (011 / 11, with or without surrounding whitespace).
var wslFMaskPattern = regexp.MustCompile(`(?i)fmask\s*=\s*0*11\b`)

// WSLFMaskHint returns a warning when /etc/wsl.conf configures an automount
// fmask that removes the execute permission from Windows binaries, which makes
// WSL unable to run op.exe. It returns "" when no such setting is present.
func WSLFMaskHint() string {
	data, err := os.ReadFile("/etc/wsl.conf")
	if err != nil || !wslFMaskPattern.Match(data) {
		return ""
	}
	return "⚠️  /etc/wsl.conf sets an automount 'fmask' that strips the execute permission from Windows binaries.\n" +
		"💡 If 1Password is already installed on Windows, change it to 'fmask=000' (or remove it) in /etc/wsl.conf and run 'wsl --shutdown'."
}
