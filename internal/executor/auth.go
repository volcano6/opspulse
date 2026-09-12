package executor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/volcano6/opspulse/internal/secret"
	"github.com/volcano6/opspulse/internal/server"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// secretResolveTimeout bounds how long a single op:// lookup may take. The CLI
// may be waiting for a biometric approval in the Desktop App, so this is
// generous compared to a network timeout.
const secretResolveTimeout = 90 * time.Second

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

	// Priority 1: 1Password SSH Agent
	if agentConn := secret.Try1PAgentSocket(); agentConn != nil {
		authMethods = append(authMethods, ssh.PublicKeysCallback(agent.NewClient(agentConn).Signers))
	}

	// An explicit key is authoritative. A configured password is also
	// authoritative when no key is bound, avoiding unrelated default keys and
	// remote "too many authentication failures" rejections.
	if srv.KeyPath != "" {
		signer, err := loadSigner(srv.KeyPath)
		if err != nil {
			return nil, &AuthError{User: srv.User, Host: srv.Host, Reason: fmt.Errorf("failed to load private key %s: %w", srv.KeyPath, err)}
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	} else if srv.Password != "" {
		password, err := resolvePassword(srv.Password)
		if err != nil {
			return nil, &AuthError{User: srv.User, Host: srv.Host, Reason: err}
		}
		authMethods = append(authMethods, ssh.Password(password))
	} else {
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

	hostKeyCallback, err := tofuHostKeyCallback()
	if err != nil {
		return nil, &AuthError{User: user, Host: srv.Host, Reason: err}
	}
	config := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         timeout,
		HostKeyAlgorithms: []string{
			ssh.KeyAlgoED25519,
			ssh.KeyAlgoSKED25519,
			ssh.KeyAlgoECDSA256,
			ssh.KeyAlgoSKECDSA256,
			ssh.KeyAlgoECDSA384,
			ssh.KeyAlgoECDSA521,
			ssh.KeyAlgoRSASHA512,
			ssh.KeyAlgoRSASHA256,
			ssh.KeyAlgoRSA,
		},
	}

	return config, nil
}

var knownHostsMu sync.Mutex

func tofuHostKeyCallback() (ssh.HostKeyCallback, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory for SSH host keys: %w", err)
	}
	return tofuHostKeyCallbackFor(filepath.Join(home, ".ssh", "known_hosts")), nil
}

func tofuHostKeyCallbackFor(knownHostsPath string) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		knownHostsMu.Lock()
		defer knownHostsMu.Unlock()

		if err := os.MkdirAll(filepath.Dir(knownHostsPath), 0o700); err != nil {
			return fmt.Errorf("create SSH configuration directory: %w", err)
		}
		file, err := os.OpenFile(knownHostsPath, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return fmt.Errorf("open SSH known_hosts file: %w", err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("close SSH known_hosts file: %w", err)
		}

		check, err := knownhosts.New(knownHostsPath)
		if err != nil {
			return fmt.Errorf("load SSH known_hosts file: %w", err)
		}
		checkErr := check(hostname, remote, key)
		if checkErr == nil {
			return nil
		}
		var keyErr *knownhosts.KeyError
		if !errors.As(checkErr, &keyErr) || len(keyErr.Want) != 0 {
			if errors.As(checkErr, &keyErr) && len(keyErr.Want) > 0 {
				var locs []string
				for _, w := range keyErr.Want {
					locs = append(locs, fmt.Sprintf("%s:%d", w.Filename, w.Line))
				}
				hostOnly, _, _ := net.SplitHostPort(hostname)
				if hostOnly == "" {
					hostOnly = hostname
				}
				return fmt.Errorf("verify SSH host key for %s: knownhosts key mismatch (existing key recorded at %s). Run 'ssh-keygen -R %s' or remove the old key line to accept the new key",
					hostname, strings.Join(locs, ", "), hostOnly)
			}
			return fmt.Errorf("verify SSH host key for %s: %w", hostname, checkErr)
		}

		file, err = os.OpenFile(knownHostsPath, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("append SSH host key: %w", err)
		}
		_, writeErr := fmt.Fprintln(file, knownhosts.Line([]string{hostname}, key))
		closeErr := file.Close()
		if writeErr != nil {
			return fmt.Errorf("append SSH host key: %w", writeErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close SSH known_hosts file: %w", closeErr)
		}
		return nil
	}
}

func loadPrivateKey(path string) (ssh.Signer, error) {
	keyBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ssh.ParsePrivateKey(keyBytes)
}

// loadSigner loads an SSH signer from either a local private key path or a
// 1Password op:// secret reference.
//
// A reference is resolved on every connection, so the private key never has to
// exist on disk. The trade-off is a `op read` round trip (and, depending on the
// account settings, a biometric approval) per connection.
func loadSigner(keyPath string) (ssh.Signer, error) {
	if !secret.Is1PRef(keyPath) {
		return loadPrivateKey(ExpandPath(keyPath))
	}

	ctx, cancel := context.WithTimeout(context.Background(), secretResolveTimeout)
	defer cancel()

	pem, err := secret.NewResolver().ResolveSSHKey(ctx, keyPath)
	if err != nil {
		return nil, fmt.Errorf("resolve 1Password key reference: %w", err)
	}
	signer, err := ssh.ParsePrivateKey([]byte(pem))
	if err != nil {
		return nil, fmt.Errorf("parse private key resolved from %s: %w", keyPath, err)
	}
	return signer, nil
}

// resolvePassword returns the password to authenticate with, fetching it from
// 1Password when the stored value is an op:// reference rather than plaintext.
//
// The reference form is what `ops 1p push` leaves behind, so that the password
// no longer has to sit in servers.yaml.
func resolvePassword(value string) (string, error) {
	if !secret.Is1PRef(value) {
		return value, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), secretResolveTimeout)
	defer cancel()

	password, err := secret.NewResolver().ResolvePassword(ctx, value)
	if err != nil {
		return "", fmt.Errorf("resolve 1Password password reference: %w", err)
	}
	return password, nil
}
