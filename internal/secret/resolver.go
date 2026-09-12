package secret

import (
	"context"
	"fmt"
	"strings"
)

// Prefix1P identifies a 1Password secret reference, for example
// op://Vault/Item/field.
const Prefix1P = "op://"

// Is1PRef reports whether value is a 1Password secret reference.
func Is1PRef(value string) bool {
	return strings.HasPrefix(strings.TrimSpace(value), Prefix1P)
}

// Resolver handles dynamic secret resolution (e.g., 1Password op:// URIs).
type Resolver struct {
	cli CLI
}

// NewResolver creates a new secret resolver bound to the detected 1Password CLI.
//
// The account remembered by `ops 1p config --account` is applied here too, so
// that op:// references keep resolving after the user switches accounts without
// having to re-export OP_ACCOUNT everywhere.
func NewResolver() *Resolver {
	cli := Detect()
	if settings, err := LoadSettings(); err == nil {
		cli = cli.WithAccountFallback(settings.Account)
	}
	return &Resolver{cli: cli}
}

// CLI exposes the resolved 1Password CLI handle.
func (r *Resolver) CLI() CLI {
	return r.cli
}

// WithAccount returns a resolver bound to a specific 1Password account,
// overriding both the remembered preference and $OP_ACCOUNT.
func (r *Resolver) WithAccount(account string) *Resolver {
	return &Resolver{cli: r.cli.WithAccount(account)}
}

// IsAvailable checks if the 1Password CLI (op) is available in PATH.
func (r *Resolver) IsAvailable() bool {
	return r.cli.Available()
}

// Resolve checks if the value is an op:// URI and resolves it.
// If it is not an op:// URI, it returns the value as-is.
func (r *Resolver) Resolve(ctx context.Context, value string) (string, error) {
	ref := strings.TrimSpace(value)
	if !Is1PRef(ref) {
		return value, nil
	}
	if !r.cli.Available() {
		return "", fmt.Errorf("secret %q starts with op:// but the 1Password CLI is unavailable: %w", ref, ErrCLINotFound)
	}

	out, err := r.cli.Run(ctx, "read", ref, "--no-newline")
	if err != nil {
		return "", fmt.Errorf("failed to read 1Password secret %q: %w", ref, err)
	}
	return string(out), nil
}

// ResolvePassword resolves an op:// reference to the password field it points
// at, with any line ending a Windows CLI appended stripped off.
//
// Password authentication sends the value verbatim, so a stray \r (easy to pick
// up when op.exe runs under WSL) would be an authentication failure that looks
// like a wrong password.
func (r *Resolver) ResolvePassword(ctx context.Context, ref string) (string, error) {
	trimmed := strings.TrimSpace(ref)
	if !Is1PRef(trimmed) {
		return ref, nil
	}
	if !r.cli.Available() {
		return "", fmt.Errorf("secret %q starts with op:// but the 1Password CLI is unavailable: %w", trimmed, ErrCLINotFound)
	}

	out, err := r.cli.Run(ctx, "read", trimmed, "--no-newline")
	if err != nil {
		return "", fmt.Errorf("failed to read 1Password secret %q: %w", trimmed, err)
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}

// ResolveSSHKey resolves an op:// reference into an OpenSSH formatted private key.
//
// The ssh-format=openssh query parameter is mandatory: without it 1Password
// returns the key in its own internal storage format, which neither
// crypto/ssh nor ssh(1) can parse. Line endings are normalised to LF because
// crypto/ssh's PEM decoder rejects CRLF, and op.exe on Windows can produce them.
func (r *Resolver) ResolveSSHKey(ctx context.Context, ref string) (string, error) {
	withFormat, err := SSHKeyRefWithFormat(ref)
	if err != nil {
		return "", err
	}
	if !r.cli.Available() {
		return "", fmt.Errorf("cannot resolve %q: %w", ref, ErrCLINotFound)
	}

	// Deliberately no --no-newline: the raw field content, trailing newline
	// included, is what SSH tooling expects.
	out, err := r.cli.Run(ctx, "read", withFormat)
	if err != nil {
		return "", fmt.Errorf("failed to read SSH key from 1Password: %w", err)
	}
	return strings.ReplaceAll(string(out), "\r\n", "\n"), nil
}

// MaterializeSSHKey resolves an op:// reference and writes the private key to a
// 0600 file, returning its path and a cleanup function.
//
// This exists because some consumers cannot speak the op:// protocol at all:
// the system ssh(1) binary, for instance, only accepts -i <file>. The file is
// always placed in the local (POSIX) temp filesystem, since the consumers are
// local processes rather than the Windows 1Password binary.
func (r *Resolver) MaterializeSSHKey(ctx context.Context, ref string) (string, func(), error) {
	key, err := r.ResolveSSHKey(ctx, ref)
	if err != nil {
		return "", func() {}, err
	}
	if !strings.HasSuffix(key, "\n") {
		key += "\n"
	}
	path, cleanup, err := writeSecretTemp("", "opspulse-key-*", []byte(key))
	if err != nil {
		return "", func() {}, err
	}
	return path, cleanup, nil
}

// ResolveMap iterates over a map and resolves any op:// values.
// Returns a new map with resolved values, leaving non-op:// values untouched.
func (r *Resolver) ResolveMap(ctx context.Context, env map[string]string) (map[string]string, error) {
	if len(env) == 0 {
		return env, nil
	}

	resolved := make(map[string]string, len(env))
	for k, v := range env {
		resValue, err := r.Resolve(ctx, v)
		if err != nil {
			return nil, fmt.Errorf("resolving env %s: %w", k, err)
		}
		resolved[k] = resValue
	}
	return resolved, nil
}

// SSHKeyRefWithFormat appends the ssh-format=openssh query parameter to an op://
// reference when it is not already present.
func SSHKeyRefWithFormat(ref string) (string, error) {
	trimmed := strings.TrimSpace(ref)
	if !Is1PRef(trimmed) {
		return "", fmt.Errorf("%q is not a 1Password secret reference (expected an op:// URI)", ref)
	}
	if strings.Contains(trimmed, "ssh-format=") {
		return trimmed, nil
	}
	separator := "?"
	if strings.Contains(trimmed, "?") {
		separator = "&"
	}
	return trimmed + separator + "ssh-format=openssh", nil
}
