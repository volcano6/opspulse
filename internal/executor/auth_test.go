package executor

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/volcano6/opspulse/internal/config"
	"github.com/volcano6/opspulse/internal/server"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("unable to get user home directory")
	}

	tests := []struct {
		input string
		want  string
	}{
		{"~", home},
		{"~/test.key", filepath.Join(home, "test.key")},
		{"/var/log/test", "/var/log/test"},
		{"relative/path", "relative/path"},
	}

	for _, tt := range tests {
		got := ExpandPath(tt.input)
		if got != tt.want {
			t.Errorf("ExpandPath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestBuildClientConfig_Password(t *testing.T) {
	srv := server.Server{
		Name:     "vps-01",
		Host:     "192.168.1.1",
		User:     "root",
		Password: "secret-password",
	}

	cfg, err := BuildClientConfig(srv, 5*time.Second)
	if err != nil {
		t.Fatalf("BuildClientConfig() unexpected error: %v", err)
	}

	if cfg.User != "root" {
		t.Errorf("expected user 'root', got %q", cfg.User)
	}
	if len(cfg.Auth) != 1 {
		t.Errorf("expected 1 auth method, got %d", len(cfg.Auth))
	}
}

func TestBuildClientConfig_NoAuth(t *testing.T) {
	srv := server.Server{
		Name:    "vps-02",
		Host:    "192.168.1.2",
		User:    "admin",
		KeyPath: "/path/to/non-existent-key-file-12345",
	}

	_, err := BuildClientConfig(srv, 5*time.Second)
	if err == nil {
		t.Error("expected error for non-existent key, got nil")
	}
}

func TestBuildClientConfig_RejectsLegacy1PRefs(t *testing.T) {
	tests := []struct {
		name string
		srv  server.Server
	}{
		{
			name: "key_path",
			srv: server.Server{
				Name:    "legacy-key",
				Host:    "192.168.1.3",
				User:    "root",
				KeyPath: "op://Vault/Item/private_key",
			},
		},
		{
			name: "password",
			srv: server.Server{
				Name:     "legacy-pass",
				Host:     "192.168.1.4",
				User:     "root",
				Password: "op://Vault/Item/password",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := BuildClientConfig(tt.srv, 5*time.Second)
			if err == nil {
				t.Fatalf("expected error for op:// %s, got nil", tt.name)
			}
			if !strings.Contains(err.Error(), "ops 1p restore") {
				t.Errorf("expected error to point at 'ops 1p restore', got: %v", err)
			}
		})
	}
}

func TestPrefixedWriter(t *testing.T) {
	var buf bytes.Buffer
	pw := NewPrefixedWriter("[vps-01] ", &buf)

	input := "line 1\nline 2\n"
	n, err := pw.Write([]byte(input))
	if err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	if n != len(input) {
		t.Errorf("Write() wrote %d bytes, want %d", n, len(input))
	}

	got := buf.String()
	want := "[vps-01] line 1\n[vps-01] line 2\n"
	if got != want {
		t.Errorf("PrefixedWriter output = %q, want %q", got, want)
	}

	// Partial line with Flush
	buf.Reset()
	_, _ = pw.Write([]byte("partial line"))
	if err := pw.Flush(); err != nil {
		t.Fatalf("Flush() error: %v", err)
	}
	if buf.String() != "[vps-01] partial line" {
		t.Errorf("Flush output = %q, want %q", buf.String(), "[vps-01] partial line")
	}
}

func TestHostKeyCallback_DefaultStrict_RejectsUnknownHost(t *testing.T) {
	knownHostsPath := filepath.Join(t.TempDir(), ".ssh", "known_hosts")
	var warnBuf bytes.Buffer
	callback := tofuHostKeyCallbackForWithWriter(knownHostsPath, &warnBuf)
	remote := &net.TCPAddr{IP: net.ParseIP("192.0.2.11"), Port: 22}
	hostname := "strict.test:22"

	t.Setenv("OPSPULSE_TRUST_NEW_HOST_KEY", "")
	key := newTestHostKey(t)
	err := callback(hostname, remote, key)
	if err == nil {
		t.Fatal("expected error in default strict host key checking mode for unknown host, got nil")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "host key not recorded in") {
		t.Fatalf("expected host key not recorded error, got: %v", errMsg)
	}
	if !strings.Contains(errMsg, "OPSPULSE_TRUST_NEW_HOST_KEY=1") {
		t.Fatalf("expected instruction to set OPSPULSE_TRUST_NEW_HOST_KEY=1, got: %v", errMsg)
	}
	if !strings.Contains(errMsg, ssh.FingerprintSHA256(key)) {
		t.Fatalf("expected fingerprint %s in error message, got: %v", ssh.FingerprintSHA256(key), errMsg)
	}
	if warnBuf.Len() != 0 {
		t.Fatalf("expected no warnings on rejection, got: %s", warnBuf.String())
	}
}

func TestHostKeyCallback_OptInTrustNew_AcceptsAndLogs(t *testing.T) {
	knownHostsPath := filepath.Join(t.TempDir(), ".ssh", "known_hosts")
	var warnBuf bytes.Buffer
	callback := tofuHostKeyCallbackForWithWriter(knownHostsPath, &warnBuf)
	remote := &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 2222}
	hostname := "example.test:2222"

	t.Setenv("OPSPULSE_TRUST_NEW_HOST_KEY", "1")
	firstKey := newTestHostKey(t)
	if err := callback(hostname, remote, firstKey); err != nil {
		t.Fatalf("first host key must be trusted when OPSPULSE_TRUST_NEW_HOST_KEY=1: %v", err)
	}
	if !strings.Contains(warnBuf.String(), "Warning: Permanently added 'example.test:2222'") {
		t.Fatalf("expected warning in warnWriter, got: %s", warnBuf.String())
	}
	if !strings.Contains(warnBuf.String(), ssh.FingerprintSHA256(firstKey)) {
		t.Fatalf("expected fingerprint in warning, got: %s", warnBuf.String())
	}

	// Second connection with same key succeeds without new warnings
	warnBuf.Reset()
	if err := callback(hostname, remote, firstKey); err != nil {
		t.Fatalf("stored host key must be accepted: %v", err)
	}
	if warnBuf.Len() != 0 {
		t.Fatalf("expected no warning for known host, got: %s", warnBuf.String())
	}

	// Changed host key must be rejected
	if err := callback(hostname, remote, newTestHostKey(t)); err == nil {
		t.Fatal("changed host key must be rejected")
	}

	info, err := os.Stat(knownHostsPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("known_hosts mode = %o, want 600", info.Mode().Perm())
	}
}

func TestHostKeyCallback_UnknownHostHandlerAcceptsAndPersists(t *testing.T) {
	knownHostsPath := filepath.Join(t.TempDir(), ".ssh", "known_hosts")
	var warnBuf bytes.Buffer
	var got UnknownHostKey
	callback := tofuHostKeyCallbackForWithWriterAndHandler(knownHostsPath, &warnBuf, func(info UnknownHostKey) (bool, error) {
		got = info
		return true, nil
	})
	remote := &net.TCPAddr{IP: net.ParseIP("192.0.2.12"), Port: 2222}
	hostname := "prompt.test:2222"
	key := newTestHostKey(t)

	t.Setenv("OPSPULSE_TRUST_NEW_HOST_KEY", "")
	if err := callback(hostname, remote, key); err != nil {
		t.Fatalf("accepted unknown host key must be trusted: %v", err)
	}
	if got.Address != hostname || got.Host != "prompt.test" || got.Port != "2222" {
		t.Errorf("handler got address %+v, want prompt.test:2222", got)
	}
	if got.KeyType != key.Type() || got.Fingerprint != ssh.FingerprintSHA256(key) {
		t.Errorf("handler got key %s %s, want %s %s", got.KeyType, got.Fingerprint, key.Type(), ssh.FingerprintSHA256(key))
	}
	if !strings.Contains(warnBuf.String(), "Permanently added 'prompt.test:2222'") {
		t.Errorf("expected trust notice, got: %s", warnBuf.String())
	}

	// A recorded key is accepted without asking again.
	calls := 0
	callback = tofuHostKeyCallbackForWithWriterAndHandler(knownHostsPath, &warnBuf, func(UnknownHostKey) (bool, error) {
		calls++
		return true, nil
	})
	if err := callback(hostname, remote, key); err != nil {
		t.Fatalf("recorded host key must be accepted: %v", err)
	}
	if calls != 0 {
		t.Fatalf("known host key invoked the prompt handler %d time(s)", calls)
	}
}

func TestHostKeyCallback_UnknownHostHandlerDeclineKeepsFailClosed(t *testing.T) {
	knownHostsPath := filepath.Join(t.TempDir(), ".ssh", "known_hosts")
	callback := tofuHostKeyCallbackForWithWriterAndHandler(knownHostsPath, nil, func(UnknownHostKey) (bool, error) {
		return false, nil
	})
	hostname := "decline.test:22"
	err := callback(hostname, &net.TCPAddr{IP: net.ParseIP("192.0.2.13"), Port: 22}, newTestHostKey(t))
	if err == nil || !strings.Contains(err.Error(), "host key not recorded") {
		t.Fatalf("declined host key must be rejected, got %v", err)
	}
	data, readErr := os.ReadFile(knownHostsPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.TrimSpace(string(data)) != "" {
		t.Fatalf("declined host key was written:\n%s", data)
	}
}

func TestHostKeyCallback_UnknownHostHandlerDoesNotOverrideMismatch(t *testing.T) {
	knownHostsPath := filepath.Join(t.TempDir(), ".ssh", "known_hosts")
	existing := newTestHostKey(t)
	if err := os.MkdirAll(filepath.Dir(knownHostsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(knownHostsPath, []byte(knownhosts.Line([]string{"mismatch.test:22"}, existing)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	callback := tofuHostKeyCallbackForWithWriterAndHandler(knownHostsPath, nil, func(UnknownHostKey) (bool, error) {
		called = true
		return true, nil
	})
	err := callback("mismatch.test:22", &net.TCPAddr{IP: net.ParseIP("192.0.2.14"), Port: 22}, newTestHostKey(t))
	if err == nil || !strings.Contains(err.Error(), "knownhosts key mismatch") {
		t.Fatalf("mismatched host key must be rejected, got %v", err)
	}
	if called {
		t.Fatal("prompt handler must not be called for a mismatched host key")
	}
}

func TestHostKeyCallback_EnvTrustSkipsPrompt(t *testing.T) {
	knownHostsPath := filepath.Join(t.TempDir(), ".ssh", "known_hosts")
	called := false
	callback := tofuHostKeyCallbackForWithWriterAndHandler(knownHostsPath, nil, func(UnknownHostKey) (bool, error) {
		called = true
		return false, nil
	})
	t.Setenv("OPSPULSE_TRUST_NEW_HOST_KEY", "1")
	if err := callback("env.test:22", &net.TCPAddr{IP: net.ParseIP("192.0.2.15"), Port: 22}, newTestHostKey(t)); err != nil {
		t.Fatalf("env opt-in must trust the host key: %v", err)
	}
	if called {
		t.Fatal("env opt-in must not prompt for an unknown host key")
	}
}

func TestHostKeyCallback_UnknownHostErrorIncludesNonDefaultPort(t *testing.T) {
	knownHostsPath := filepath.Join(t.TempDir(), ".ssh", "known_hosts")
	callback := tofuHostKeyCallbackForWithWriter(knownHostsPath, nil)
	t.Setenv("OPSPULSE_TRUST_NEW_HOST_KEY", "")
	err := callback("port.test:2222", &net.TCPAddr{IP: net.ParseIP("192.0.2.16"), Port: 2222}, newTestHostKey(t))
	if err == nil {
		t.Fatal("unknown host key must be rejected")
	}
	if !strings.Contains(err.Error(), "'ssh -p 2222 port.test'") {
		t.Fatalf("error must include the non-default SSH port, got: %v", err)
	}
}

func newTestHostKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestLogPathFor(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv(config.EnvHome, tmpDir)

	now := time.Date(2026, 8, 20, 15, 30, 0, 0, time.UTC)
	logPath, err := LogPathFor("vps-prod", now)
	if err != nil {
		t.Fatalf("LogPathFor() error: %v", err)
	}

	if !strings.Contains(logPath, "bootstrap-vps-prod-20260820T153000.log") {
		t.Errorf("unexpected log path: %q", logPath)
	}

	dir := filepath.Dir(logPath)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Errorf("expected log directory to exist at %q", dir)
	}
}
