package executor

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/volcano6/opspulse/internal/config"
	"github.com/volcano6/opspulse/internal/secret"
	"github.com/volcano6/opspulse/internal/server"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// ExpandPath expands the tilde (~) prefix in a file path to the current user's home directory.
func ExpandPath(path string) string {
	return config.ExpandPath(path)
}

// BuildClientConfig constructs an ssh.ClientConfig from server configuration.
func BuildClientConfig(srv server.Server, timeout time.Duration) (*ssh.ClientConfig, error) {
	return BuildClientConfigWithWriter(srv, timeout, nil)
}

// BuildClientConfigWithWriter constructs an ssh.ClientConfig from server configuration,
// optionally routing host key trust notices through the provided warnWriter.
func BuildClientConfigWithWriter(srv server.Server, timeout time.Duration, warnWriter io.Writer) (*ssh.ClientConfig, error) {
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
		signer, err := loadSigner(srv.KeyPath, srv.Name)
		if err != nil {
			return nil, &AuthError{User: srv.User, Host: srv.Host, Reason: fmt.Errorf("failed to load private key %s: %w", srv.KeyPath, err)}
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	} else if srv.Password != "" {
		password, err := resolvePassword(srv.Password, srv.Name)
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

	hostKeyCallback, err := tofuHostKeyCallbackWithWriter(warnWriter)
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

func tofuHostKeyCallbackWithWriter(warnWriter io.Writer) (ssh.HostKeyCallback, error) {
	if custom := os.Getenv("OPSPULSE_KNOWN_HOSTS"); custom != "" {
		return tofuHostKeyCallbackForWithWriter(custom, warnWriter), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory for SSH host keys: %w", err)
	}
	return tofuHostKeyCallbackForWithWriter(filepath.Join(home, ".ssh", "known_hosts"), warnWriter), nil
}

func tofuHostKeyCallbackForWithWriter(knownHostsPath string, warnWriter io.Writer) ssh.HostKeyCallback {
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

		// Host key is not found in known_hosts (unknown host / first connection).
		// By default, strict host key checking is ENFORCED: unknown hosts are rejected to prevent MITM.
		// Users can explicitly opt-in via OPSPULSE_TRUST_NEW_HOST_KEY=1 to accept and trust new hosts on first use.
		trustNew := os.Getenv("OPSPULSE_TRUST_NEW_HOST_KEY")
		if trustNew != "true" && trustNew != "1" {
			fingerprint := ssh.FingerprintSHA256(key)
			hostOnly, _, _ := net.SplitHostPort(hostname)
			if hostOnly == "" {
				hostOnly = hostname
			}
			return fmt.Errorf("verify SSH host key for %s: host key not recorded in %s (key type: %s, fingerprint: %s). Connect once with 'ssh %s' to accept the key or set OPSPULSE_TRUST_NEW_HOST_KEY=1 to trust new hosts on first use",
				hostname, knownHostsPath, key.Type(), fingerprint, hostOnly)
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

		// Security audit log: route notice through the injected writer (thread-safe and logfile-aware)
		fingerprint := ssh.FingerprintSHA256(key)
		msg := fmt.Sprintf("Warning: Permanently added '%s' (%s, %s) to the list of known hosts (%s).\nHint: Remember to unset OPSPULSE_TRUST_NEW_HOST_KEY once initial host enrollment is complete to enforce strict verification.\n",
			hostname, key.Type(), fingerprint, knownHostsPath)
		if warnWriter != nil {
			_, _ = fmt.Fprint(warnWriter, msg)
		} else {
			_, _ = fmt.Fprint(os.Stderr, msg)
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

// loadSigner loads an SSH signer from a local private key path.
//
// This used to accept an op:// reference and resolve it on every connection.
// Credentials are local now (see internal/secret/guard.go), so a reference here
// means servers.yaml has not been migrated yet and there is nothing useful to
// do but say so.
func loadSigner(keyPath, serverName string) (ssh.Signer, error) {
	if err := secret.RejectLegacy1PRef("key_path", keyPath, serverName); err != nil {
		return nil, err
	}
	return loadPrivateKey(ExpandPath(keyPath))
}

// resolvePassword returns the plaintext password to authenticate with.
//
// An op:// value is a leftover from the era when credentials were resolved at
// connect time; it is rejected now with a pointer at the command that migrates
// it to a local credential.
func resolvePassword(value, serverName string) (string, error) {
	if err := secret.RejectLegacy1PRef("password", value, serverName); err != nil {
		return "", err
	}
	return value, nil
}
