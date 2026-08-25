package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupKeyPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	stored, expanded, err := setupKeyPath("prod/web 01")
	if err != nil {
		t.Fatalf("setupKeyPath() error: %v", err)
	}
	if stored != "~/.ssh/opspulse_prod_web_01" {
		t.Fatalf("stored path = %q", stored)
	}
	wantExpanded := filepath.Join(home, ".ssh", "opspulse_prod_web_01")
	if expanded != wantExpanded {
		t.Fatalf("expanded path = %q, want %q", expanded, wantExpanded)
	}
}

func TestInstallPublicKeyScript(t *testing.T) {
	publicKey := []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest opspulse:test\n")
	script := installPublicKeyScript(publicKey)

	if strings.Contains(script, string(publicKey)) {
		t.Fatal("script embeds the public key without shell-safe encoding")
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(strings.TrimSpace(string(publicKey))))
	if !strings.Contains(script, encoded) {
		t.Fatal("script does not contain the encoded public key")
	}
	if strings.Contains(script, "passwd") || strings.Contains(script, "PasswordAuthentication") {
		t.Fatalf("script must not modify password authentication:\n%s", script)
	}
	for _, command := range []string{"mkdir -p", "authorized_keys", "grep -qxF", "chmod 600"} {
		if !strings.Contains(script, command) {
			t.Fatalf("script missing %q:\n%s", command, script)
		}
	}
}

func TestEnsureSSHKeyPair(t *testing.T) {
	if _, err := os.Stat("/usr/bin/ssh-keygen"); err != nil {
		t.Skip("ssh-keygen is unavailable")
	}
	privateKey := filepath.Join(t.TempDir(), ".ssh", "opspulse_test")
	if err := ensureSSHKeyPair(privateKey, "test"); err != nil {
		t.Fatalf("ensureSSHKeyPair() error: %v", err)
	}
	privateInfo, err := os.Stat(privateKey)
	if err != nil {
		t.Fatalf("private key missing: %v", err)
	}
	if privateInfo.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode = %o, want 600", privateInfo.Mode().Perm())
	}
	if _, err := os.Stat(privateKey + ".pub"); err != nil {
		t.Fatalf("public key missing: %v", err)
	}
}
