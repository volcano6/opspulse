package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/volcano6/opspulse/internal/server"
	"golang.org/x/crypto/ssh"
)

// ExpandPath expands the tilde (~) prefix in a file path to the current user's home directory.
func ExpandPath(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
		return filepath.Join(home, path[2:])
	}
	return path
}

// BuildClientConfig constructs an ssh.ClientConfig from server configuration.
func BuildClientConfig(srv server.Server, timeout time.Duration) (*ssh.ClientConfig, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	var authMethods []ssh.AuthMethod

	// 1. Try explicit KeyPath
	if srv.KeyPath != "" {
		signer, err := loadPrivateKey(ExpandPath(srv.KeyPath))
		if err != nil {
			return nil, &AuthError{User: srv.User, Host: srv.Host, Reason: fmt.Errorf("failed to load private key %s: %w", srv.KeyPath, err)}
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	} else {
		// 2. Try standard default key locations
		defaultKeys := []string{
			"~/.ssh/id_ed25519",
			"~/.ssh/id_rsa",
			"~/.ssh/id_ecdsa",
		}
		for _, keyPath := range defaultKeys {
			expanded := ExpandPath(keyPath)
			if _, err := os.Stat(expanded); err == nil {
				signer, err := loadPrivateKey(expanded)
				if err == nil {
					authMethods = append(authMethods, ssh.PublicKeys(signer))
					break
				}
			}
		}
	}

	// 3. Try password if provided
	if srv.Password != "" {
		authMethods = append(authMethods, ssh.Password(srv.Password))
	}

	if len(authMethods) == 0 {
		return nil, &AuthError{
			User:   srv.User,
			Host:   srv.Host,
			Reason: fmt.Errorf("no authentication method available (no private key found and no password provided)"),
		}
	}

	user := srv.User
	if user == "" {
		user = "root"
	}

	config := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // Default to accepting host keys for automated VPS provisioning
		Timeout:         timeout,
	}

	return config, nil
}

func loadPrivateKey(path string) (ssh.Signer, error) {
	keyBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ssh.ParsePrivateKey(keyBytes)
}
