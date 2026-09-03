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

func TestTOFUHostKeyCallbackTrustsFirstKeyAndRejectsChanges(t *testing.T) {
	knownHostsPath := filepath.Join(t.TempDir(), ".ssh", "known_hosts")
	callback := tofuHostKeyCallbackFor(knownHostsPath)
	remote := &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 2222}
	hostname := "example.test:2222"

	firstKey := newTestHostKey(t)
	if err := callback(hostname, remote, firstKey); err != nil {
		t.Fatalf("first host key must be trusted: %v", err)
	}
	if err := callback(hostname, remote, firstKey); err != nil {
		t.Fatalf("stored host key must be accepted: %v", err)
	}
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
