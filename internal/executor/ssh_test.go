package executor

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestWrapHandshakeError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantAuth bool
	}{
		{
			name:     "server auth error type",
			err:      &ssh.ServerAuthError{Errors: []error{errors.New("no auth")}},
			wantAuth: true,
		},
		{
			name:     "unable to authenticate",
			err:      errors.New("ssh: handshake failed: ssh: unable to authenticate, attempted methods [none publickey], no supported methods remain"),
			wantAuth: true,
		},
		{
			name:     "no supported methods",
			err:      errors.New("ssh: handshake failed: no supported methods remain"),
			wantAuth: true,
		},
		{
			name: "knownhosts key mismatch",
			err: errors.New("ssh: handshake failed: verify SSH host key for example.com:22: " +
				"knownhosts key mismatch (existing key recorded at /home/u/.ssh/known_hosts:3). " +
				"Run 'ssh-keygen -R example.com' or remove the old key line to accept the new key"),
			wantAuth: false,
		},
		{
			name:     "knownhosts key is unknown",
			err:      errors.New("ssh: handshake failed: knownhosts: key is unknown"),
			wantAuth: false,
		},
		{
			name:     "host key mismatch",
			err:      errors.New("ssh: handshake failed: ssh: host key mismatch"),
			wantAuth: false,
		},
		{
			name:     "algorithm negotiation failure",
			err:      errors.New("ssh: handshake failed: ssh: no common algorithm for host key"),
			wantAuth: false,
		},
		{
			name:     "version banner timeout",
			err:      errors.New("ssh: handshake failed: ssh: overflow reading version string"),
			wantAuth: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wrapped := wrapHandshakeError("deploy", "example.com", tt.err)

			var authErr *AuthError
			var netErr *NetworkError
			if tt.wantAuth {
				if !errors.As(wrapped, &authErr) {
					t.Fatalf("wrapHandshakeError(%v) = %T, want *AuthError", tt.err, wrapped)
				}
				if errors.As(wrapped, &netErr) {
					t.Fatalf("wrapHandshakeError(%v) matched *NetworkError as well as *AuthError", tt.err)
				}
				if authErr.User != "deploy" || authErr.Host != "example.com" {
					t.Errorf("AuthError identity = %s@%s, want deploy@example.com", authErr.User, authErr.Host)
				}
			} else {
				if !errors.As(wrapped, &netErr) {
					t.Fatalf("wrapHandshakeError(%v) = %T, want *NetworkError", tt.err, wrapped)
				}
				if errors.As(wrapped, &authErr) {
					t.Fatalf("wrapHandshakeError(%v) matched *AuthError as well as *NetworkError", tt.err)
				}
				if netErr.Host != "example.com" {
					t.Errorf("NetworkError host = %q, want example.com", netErr.Host)
				}
			}

			// The original handshake error must stay reachable and visible.
			if !errors.Is(wrapped, tt.err) {
				t.Errorf("errors.Is(wrapped, original) = false; the error chain is broken")
			}
			if !strings.Contains(wrapped.Error(), tt.err.Error()) {
				t.Errorf("wrapped error text %q does not contain original %q", wrapped.Error(), tt.err)
			}
		})
	}
}
