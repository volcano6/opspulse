package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/volcano6/opspulse/internal/server"
	"golang.org/x/crypto/ssh"
)

// ResolveAndSecureKeyPath checks if rawKeyPath is a valid SSH private key and whether it is in ~/.ssh/.
// If outside ~/.ssh/, it prompts the user (or uses noCopy flag) to copy the key into ~/.ssh/opspulse_<serverName><ext>
// and sets 0600 permissions. It returns the resolved path and whether a new file was copied.
func ResolveAndSecureKeyPath(in io.Reader, out io.Writer, serverName, rawKeyPath string, noCopy bool) (resolvedPath string, copied bool, err error) {
	trimmed := strings.TrimSpace(rawKeyPath)
	if trimmed == "" {
		return "", false, nil
	}

	expandedPath := expandHome(trimmed)
	stat, err := os.Stat(expandedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, fmt.Errorf("private key file %q not found", rawKeyPath)
		}
		return "", false, fmt.Errorf("inspect private key %q: %w", rawKeyPath, err)
	}
	if stat.IsDir() {
		return "", false, fmt.Errorf("private key path %q is a directory", rawKeyPath)
	}

	// Validate that the file is actually a valid SSH private key
	keyData, err := os.ReadFile(filepath.Clean(expandedPath)) // #nosec G304 -- user explicitly provided private key path
	if err != nil {
		return "", false, fmt.Errorf("read private key %q: %w", rawKeyPath, err)
	}
	if err := validatePrivateKeyContent(keyData); err != nil {
		return "", false, fmt.Errorf("file %q is invalid: %w", rawKeyPath, err)
	}

	// Check if already in ~/.ssh/
	if isInsideSSHDir(expandedPath) {
		// Enforce 0600 permissions
		_ = os.Chmod(expandedPath, 0o600)
		return trimmed, false, nil
	}

	// Key is outside ~/.ssh/
	ext := filepath.Ext(expandedPath)
	storedDest, expandedDest, err := copyKeyDestinationPath(serverName, ext)
	if err != nil {
		return "", false, err
	}

	shouldCopy := !noCopy
	if !noCopy && in != nil {
		if out != nil {
			_, _ = fmt.Fprintf(out, "💡 Private key %q is not in ~/.ssh/.\n", trimmed)
			_, _ = fmt.Fprintf(out, "   Copy to %s and secure permissions (0600)? [Y/n]: ", storedDest)
		}
		var response string
		scanner := bufio.NewScanner(in)
		if scanner.Scan() {
			response = strings.TrimSpace(scanner.Text())
		}
		if response != "" && (strings.EqualFold(response, "n") || strings.EqualFold(response, "no")) {
			shouldCopy = false
		}
	}

	if shouldCopy {
		if err := copyKeyFile(expandedPath, expandedDest); err != nil {
			return "", false, fmt.Errorf("copy key to ~/.ssh: %w", err)
		}
		if out != nil {
			_, _ = fmt.Fprintf(out, "✅ Private key copied to %s (mode 0600)\n", storedDest)
		}
		return storedDest, true, nil
	}

	// User declined copying, secure permissions on original file if possible
	_ = os.Chmod(expandedPath, 0o600)
	if out != nil {
		_, _ = fmt.Fprintf(out, "⚠️ Retained original key path %s (mode 0600)\n", trimmed)
	}
	return trimmed, false, nil
}

func validatePrivateKeyContent(data []byte) error {
	_, err := ssh.ParsePrivateKey(data)
	if err == nil {
		return nil
	}
	var passErr *ssh.PassphraseMissingError
	if errors.As(err, &passErr) || strings.Contains(err.Error(), "passphrase") || strings.Contains(err.Error(), "protected") {
		return nil
	}
	return fmt.Errorf("not a valid SSH private key (ensure you selected a private key, not a .pub or plain text file): %w", err)
}

func isManagedKey(keyPath string) bool {
	trimmed := strings.TrimSpace(keyPath)
	if trimmed == "" {
		return false
	}
	expanded := expandHome(trimmed)
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	managedPrefix := filepath.Join(home, ".ssh", "opspulse_")
	return strings.HasPrefix(expanded, managedPrefix)
}

// IsKeyUsedByOtherServers checks if any server in the store other than excludeServerName uses the given keyPath.
func IsKeyUsedByOtherServers(store *server.Store, excludeServerName, keyPath string) (bool, string, error) {
	if keyPath == "" {
		return false, "", nil
	}
	expandedTarget := expandHome(strings.TrimSpace(keyPath))
	servers, err := store.List()
	if err != nil {
		return false, "", err
	}
	for _, s := range servers {
		if s.Name == excludeServerName {
			continue
		}
		if s.KeyPath == "" {
			continue
		}
		if expandHome(strings.TrimSpace(s.KeyPath)) == expandedTarget {
			return true, s.Name, nil
		}
	}
	return false, "", nil
}

// CleanupManagedKeyWithRefCheck deletes the managed key file if no other server references it.
// If keepKey is true or the key is not managed, it does nothing.
func CleanupManagedKeyWithRefCheck(out io.Writer, store *server.Store, serverName, keyPath string, keepKey bool) error {
	if !isManagedKey(keyPath) || keepKey {
		return nil
	}

	used, otherServer, err := IsKeyUsedByOtherServers(store, serverName, keyPath)
	if err != nil {
		return err
	}
	if used {
		if out != nil {
			_, _ = fmt.Fprintf(out, "💡 Key %s is still in use by server %q (retained).\n", keyPath, otherServer)
		}
		return nil
	}

	cleanupManagedKey(keyPath)
	if out != nil {
		_, _ = fmt.Fprintf(out, "🧹 Cleaned up managed key: %s\n", keyPath)
	}
	return nil
}

func cleanupManagedKey(keyPath string) {
	trimmed := strings.TrimSpace(keyPath)
	if trimmed == "" {
		return
	}
	expanded := expandHome(trimmed)
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	managedPrefix := filepath.Join(home, ".ssh", "opspulse_")
	// Only delete if inside ~/.ssh/ and starts with opspulse_
	if strings.HasPrefix(expanded, managedPrefix) {
		_ = os.Remove(expanded)
		_ = os.Remove(expanded + ".pub")
	}
}

func isInsideSSHDir(expandedPath string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	sshDir := filepath.Join(home, ".ssh")
	rel, err := filepath.Rel(sshDir, expandedPath)
	if err != nil {
		return false
	}
	return !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

func copyKeyDestinationPath(serverName, ext string) (storedPath, expandedPath string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("resolve home directory: %w", err)
	}
	var name strings.Builder
	for _, r := range serverName {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			name.WriteRune(r)
		} else {
			name.WriteByte('_')
		}
	}
	if ext == "" {
		ext = ".key"
	}
	filename := "opspulse_" + name.String() + ext
	storedPath = "~/.ssh/" + filename
	expandedPath = filepath.Join(home, ".ssh", filename)
	return storedPath, expandedPath, nil
}

func copyKeyFile(srcPath, dstPath string) error {
	cleanDst := filepath.Clean(dstPath)
	if err := os.MkdirAll(filepath.Dir(cleanDst), 0o700); err != nil {
		return fmt.Errorf("create ~/.ssh directory: %w", err)
	}
	cleanSrc := filepath.Clean(srcPath)
	data, err := os.ReadFile(cleanSrc) // #nosec G304 -- user explicitly provided private key source path
	if err != nil {
		return fmt.Errorf("read source key %q: %w", srcPath, err)
	}
	if err := os.WriteFile(cleanDst, data, 0o600); err != nil { // #nosec G703,G304 -- user explicitly requested copying to ~/.ssh
		return fmt.Errorf("write destination key %q: %w", dstPath, err)
	}
	return nil
}
