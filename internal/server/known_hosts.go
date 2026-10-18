package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/volcano6/opspulse/internal/config"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// KnownHostStatus is the result of comparing one server's advertised host key
// with this machine's known_hosts file.
type KnownHostStatus string

const (
	KnownHostPresent   KnownHostStatus = "present"
	KnownHostMissing   KnownHostStatus = "missing"
	KnownHostMismatch  KnownHostStatus = "mismatch"
	KnownHostUnprobed  KnownHostStatus = "unprobed"
	KnownHostProbeFail KnownHostStatus = "probe-failed"
)

// HostKey is one public host key advertised by a server. Source names where the
// key was learned from, because a fingerprint is only useful when the user knows
// what they are comparing it with.
type HostKey struct {
	Type        string `json:"type"`
	Fingerprint string `json:"fingerprint"`
	Source      string `json:"source"`
	// Key is kept out of JSON: it is the material that would be written to
	// known_hosts, not something a report needs to print.
	Key ssh.PublicKey `json:"-"`
}

// KnownHostCheck is the read-only result for one server.
type KnownHostCheck struct {
	Server      string          `json:"server"`
	Host        string          `json:"host"`
	Port        int             `json:"port"`
	Address     string          `json:"address"`
	Status      KnownHostStatus `json:"status"`
	Keys        []HostKey       `json:"keys,omitempty"`
	KnownHosts  string          `json:"known_hosts"`
	Error       string          `json:"error,omitempty"`
	MatchedKeys []string        `json:"matched_keys,omitempty"`
}

// KnownHostsPath returns the known_hosts file used by OpsPulse's built-in SSH
// client, honouring OPSPULSE_KNOWN_HOSTS just like executor.BuildClientConfig.
func KnownHostsPath() (string, error) {
	if custom := strings.TrimSpace(os.Getenv("OPSPULSE_KNOWN_HOSTS")); custom != "" {
		return config.ExpandPath(custom), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory for SSH host keys: %w", err)
	}
	return filepath.Join(home, ".ssh", "known_hosts"), nil
}

// ProbeHostKeys asks a host for the keys it advertises, without authenticating
// and without writing anything. The built-in probe drives ssh-keyscan(1); tests
// inject a deterministic probe through SetupDeps.
func ProbeHostKeys(ctx context.Context, srv Server) ([]HostKey, error) {
	keyscan, err := exec.LookPath("ssh-keyscan")
	if err != nil {
		return nil, fmt.Errorf("system 'ssh-keyscan' not found in PATH: %w", err)
	}
	port := srv.Port
	if port <= 0 {
		port = 22
	}
	// "--" terminates option parsing so an inventory host beginning with "-"
	// cannot be interpreted as an ssh-keyscan flag.
	args := []string{"-T", "10", "-p", strconv.Itoa(port), "--", srv.Host}
	out, err := exec.CommandContext(ctx, keyscan, args...).CombinedOutput() // #nosec G204 -- fixed binary, inventory host/port
	if err != nil {
		return nil, fmt.Errorf("ssh-keyscan %s: %w: %s", srv.Host, err, strings.TrimSpace(string(out)))
	}
	return ParseScannedHostKeys(string(out), "ssh-keyscan "+srv.Host)
}

// ParseScannedHostKeys parses ssh-keyscan output into de-duplicated keys.
//
// Only the key type, fingerprint and source are retained for display. The raw
// key is still needed to append the exact advertised line after confirmation.
func ParseScannedHostKeys(output, source string) ([]HostKey, error) {
	seen := map[string]bool{}
	var keys []HostKey
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.Join(fields[1:], " ")))
		if err != nil {
			continue
		}
		fingerprint := ssh.FingerprintSHA256(key)
		if seen[fingerprint] {
			continue
		}
		seen[fingerprint] = true
		keys = append(keys, HostKey{
			Type:        key.Type(),
			Fingerprint: fingerprint,
			Source:      source,
			Key:         key,
		})
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no host key was returned by %s", source)
	}
	return keys, nil
}

// CheckKnownHost compares a server's advertised keys with known_hosts.
//
// A missing file means every host is unknown, not that the check failed. A
// malformed file is a hard error: silently treating it as empty could turn a
// corrupted trust store into a reason to trust a new key.
func CheckKnownHost(srv Server, knownHostsPath string, keys []HostKey) KnownHostCheck {
	port := srv.Port
	if port <= 0 {
		port = 22
	}
	address := net.JoinHostPort(srv.Host, strconv.Itoa(port))
	check := KnownHostCheck{
		Server:     srv.Name,
		Host:       srv.Host,
		Port:       port,
		Address:    address,
		KnownHosts: knownHostsPath,
		Status:     KnownHostUnprobed,
		Keys:       keys,
	}
	if len(keys) == 0 {
		return check
	}
	check.Error = ""

	callback, err := loadKnownHostsCallback(knownHostsPath)
	if err != nil {
		check.Status = KnownHostProbeFail
		check.Error = err.Error()
		return check
	}

	matched := map[string]bool{}
	unknown := 0
	for _, key := range keys {
		remote := &net.TCPAddr{IP: net.ParseIP(srv.Host), Port: port}
		err := callback(address, remote, key.Key)
		switch {
		case err == nil:
			matched[key.Fingerprint] = true
			check.MatchedKeys = append(check.MatchedKeys, key.Fingerprint)
		case isUnknownHostKey(err), isMissingHostKeyType(err, key.Key):
			unknown++
		case isConflictingHostKeyType(err, key.Key):
			check.Status = KnownHostMismatch
			check.Error = err.Error()
			return check
		default:
			// A revoked key or a malformed trust store is not something setup
			// may downgrade to "missing": stop instead of inviting a write.
			check.Status = KnownHostProbeFail
			check.Error = err.Error()
			return check
		}
	}

	switch {
	case len(matched) > 0:
		// At least one advertised algorithm is trusted; another algorithm being
		// absent is not a trust gap for this host.
		check.Status = KnownHostPresent
	default:
		check.Status = KnownHostMissing
	}
	return check
}

func loadKnownHostsCallback(path string) (ssh.HostKeyCallback, error) {
	data, err := os.ReadFile(filepath.Clean(path)) // #nosec G304 -- known_hosts path is fixed or explicitly overridden
	if errors.Is(err, os.ErrNotExist) {
		// knownhosts.New needs a real file. An empty one models "no entries"
		// without creating anything on disk.
		dir, mkErr := os.MkdirTemp("", "opspulse-knownhosts-")
		if mkErr != nil {
			return nil, fmt.Errorf("create temporary known_hosts: %w", mkErr)
		}
		tempPath := filepath.Join(dir, "known_hosts")
		writeErr := os.WriteFile(tempPath, nil, 0o600)
		if writeErr != nil {
			_ = os.RemoveAll(dir)
			return nil, fmt.Errorf("create empty known_hosts: %w", writeErr)
		}
		callback, newErr := knownhosts.New(tempPath)
		_ = os.RemoveAll(dir)
		if newErr != nil {
			return nil, fmt.Errorf("load known_hosts: %w", newErr)
		}
		return callback, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read known_hosts %s: %w", path, err)
	}
	// knownhosts.New reports a malformed file rather than skipping bad lines.
	// Check the bytes first so a directory or unreadable file gets a clearer
	// error than "no such host key".
	if len(bytes.TrimSpace(data)) == 0 {
		return func(string, net.Addr, ssh.PublicKey) error {
			return &knownhosts.KeyError{}
		}, nil
	}
	callback, err := knownhosts.New(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("load known_hosts %s: %w", path, err)
	}
	return callback, nil
}

func isUnknownHostKey(err error) bool {
	var keyErr *knownhosts.KeyError
	return errors.As(err, &keyErr) && len(keyErr.Want) == 0
}

// isMissingHostKeyType reports whether the host is known but this algorithm is
// not. knownhosts returns every key recorded for the host in KeyError.Want, so
// a different algorithm must not be mistaken for a changed key.
func isMissingHostKeyType(err error, key ssh.PublicKey) bool {
	var keyErr *knownhosts.KeyError
	if !errors.As(err, &keyErr) || len(keyErr.Want) == 0 {
		return false
	}
	for _, known := range keyErr.Want {
		if known.Key.Type() == key.Type() {
			return false
		}
	}
	return true
}

func isConflictingHostKeyType(err error, key ssh.PublicKey) bool {
	var keyErr *knownhosts.KeyError
	if !errors.As(err, &keyErr) || len(keyErr.Want) == 0 {
		return false
	}
	for _, known := range keyErr.Want {
		if known.Key.Type() == key.Type() {
			return true
		}
	}
	return false
}

// AppendKnownHosts records explicitly selected keys for one server.
//
// It never appends all advertised keys by default: the caller passes the exact
// fingerprints the user confirmed. Existing entries are left untouched, and a
// conflicting entry aborts instead of being silently replaced.
func AppendKnownHosts(path string, srv Server, keys []HostKey, selected []string) (int, error) {
	if len(selected) == 0 {
		return 0, fmt.Errorf("no host key selected for %s", srv.Name)
	}
	wanted := make(map[string]HostKey, len(keys))
	for _, key := range keys {
		wanted[key.Fingerprint] = key
	}

	port := srv.Port
	if port <= 0 {
		port = 22
	}
	address := net.JoinHostPort(srv.Host, strconv.Itoa(port))

	existing, err := os.ReadFile(filepath.Clean(path)) // #nosec G304 -- known_hosts path is fixed or explicitly overridden
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, fmt.Errorf("read known_hosts %s: %w", path, err)
	}
	if len(bytes.TrimSpace(existing)) > 0 {
		callback, err := knownhosts.New(filepath.Clean(path))
		if err != nil {
			return 0, fmt.Errorf("refusing to edit known_hosts %s: %w", path, err)
		}
		for _, fingerprint := range selected {
			key, ok := wanted[fingerprint]
			if !ok {
				return 0, fmt.Errorf("fingerprint %s was not advertised by %s", fingerprint, srv.Name)
			}
			if err := callback(address, &net.TCPAddr{Port: port}, key.Key); err != nil {
				if !isUnknownHostKey(err) && !isMissingHostKeyType(err, key.Key) {
					return 0, fmt.Errorf("refusing to add %s for %s: known_hosts already records a different key (%v); remove the old entry explicitly with 'ssh-keygen -R %s -f %s'", fingerprint, address, err, srv.Host, path)
				}
			} else {
				delete(wanted, fingerprint)
			}
		}
	}

	lines := make([]string, 0, len(selected))
	for _, fingerprint := range selected {
		key, ok := wanted[fingerprint]
		if !ok {
			continue
		}
		lines = append(lines, knownhosts.Line([]string{address}, key.Key))
		delete(wanted, fingerprint)
	}
	if len(lines) == 0 {
		return 0, nil
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, fmt.Errorf("create SSH directory %s: %w", dir, err)
	}
	f, err := os.OpenFile(filepath.Clean(path), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 G302
	if err != nil {
		return 0, fmt.Errorf("open known_hosts %s: %w", path, err)
	}
	for _, line := range lines {
		if _, err := fmt.Fprintln(f, line); err != nil {
			_ = f.Close()
			return 0, fmt.Errorf("append known_hosts %s: %w", path, err)
		}
	}
	if err := f.Close(); err != nil {
		return 0, fmt.Errorf("close known_hosts %s: %w", path, err)
	}
	_ = os.Chmod(path, 0o600)
	return len(lines), nil
}

// DefaultProbeHostKeys is the production probe used by the setup command. It is
// a variable so tests can replace it without opening a network connection.
var DefaultProbeHostKeys = func(ctx context.Context, srv Server) ([]HostKey, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return ProbeHostKeys(probeCtx, srv)
}
