package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/volcano6/opspulse/internal/server"
)

func setTestHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")
	t.Setenv("OPSPULSE_HOME", home)
}

func TestSetupKeyPath(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

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

func TestSetupKeyRemovePasswordFlagRegistered(t *testing.T) {
	f := serverSetupKeyCmd.Flags().Lookup("remove-password")
	if f == nil {
		t.Fatal("ops server setup-key should expose --remove-password")
	}
	if f.DefValue != "false" {
		t.Errorf("--remove-password should default to false, got %q", f.DefValue)
	}
}

// TestRemovePasswordKeepsPasswordWhenKeyProbeFails pins the safety property of
// --remove-password: the plaintext password is dropped only after the key has
// authenticated on its own, so a probe that cannot prove that must leave it.
func TestRemovePasswordKeepsPasswordWhenKeyProbeFails(t *testing.T) {
	setTestHome(t, t.TempDir())
	store := server.NewStore(filepath.Join(t.TempDir(), "servers.yaml"))
	srv := &server.Server{
		Name:     "unreachable",
		Host:     "127.0.0.1",
		Port:     1, // nothing listens here, so the probe fails at once
		User:     "root",
		Password: "hunter2",
	}
	if err := store.Save(*srv); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	err := removePasswordAfterKeyVerification(store, srv, "~/.ssh/opspulse_unreachable")
	if err == nil {
		t.Fatal("expected the key probe to fail against an unreachable host")
	}
	if !strings.Contains(err.Error(), "password was kept") {
		t.Errorf("the error should say the password was kept, got %v", err)
	}

	stored, getErr := store.Get("unreachable")
	if getErr != nil {
		t.Fatalf("reload server: %v", getErr)
	}
	if stored.Password != "hunter2" {
		t.Errorf("the password must survive a failed verification, got %q", stored.Password)
	}
}
