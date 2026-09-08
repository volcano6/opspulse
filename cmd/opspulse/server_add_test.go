package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/volcano6/opspulse/internal/config"
	"github.com/volcano6/opspulse/internal/server"
)

func TestParseTarget(t *testing.T) {
	tests := []struct {
		input    string
		wantUser string
		wantHost string
		wantPort int
		wantErr  bool
	}{
		{
			input:    "192.168.1.1",
			wantUser: "",
			wantHost: "192.168.1.1",
			wantPort: 0,
		},
		{
			input:    "root@192.168.1.1",
			wantUser: "root",
			wantHost: "192.168.1.1",
			wantPort: 0,
		},
		{
			input:    "root@192.168.1.1:2222",
			wantUser: "root",
			wantHost: "192.168.1.1",
			wantPort: 2222,
		},
		{
			input:    "admin@server.local:8022",
			wantUser: "admin",
			wantHost: "server.local",
			wantPort: 8022,
		},
		{
			input:    "server.local:22",
			wantUser: "",
			wantHost: "server.local",
			wantPort: 22,
		},
		{
			input:    "[2001:db8::1]",
			wantUser: "",
			wantHost: "2001:db8::1",
			wantPort: 0,
		},
		{
			input:    "root@[2001:db8::1]:2222",
			wantUser: "root",
			wantHost: "2001:db8::1",
			wantPort: 2222,
		},
		{
			input:    "[2001:db8::1]:22",
			wantUser: "",
			wantHost: "2001:db8::1",
			wantPort: 22,
		},
		{
			input:    "2001:db8::1",
			wantUser: "",
			wantHost: "2001:db8::1",
			wantPort: 0,
		},
		{
			input:   "root@[2001:db8::1",
			wantErr: true,
		},
		{
			input:   "host:invalid",
			wantErr: true,
		},
		{
			input:   "host:70000",
			wantErr: true,
		},
		{
			input:   "root@",
			wantErr: true,
		},
		{
			input:    "",
			wantUser: "",
			wantHost: "",
			wantPort: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			u, h, p, err := parseTarget(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseTarget(%q) expected error, got nil", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseTarget(%q) unexpected error: %v", tc.input, err)
			}
			if u != tc.wantUser || h != tc.wantHost || p != tc.wantPort {
				t.Errorf("parseTarget(%q) = (%q, %q, %d), want (%q, %q, %d)",
					tc.input, u, h, p, tc.wantUser, tc.wantHost, tc.wantPort)
			}
		})
	}
}

func TestPromptPassword_PipedInput(t *testing.T) {
	in := strings.NewReader("mySecret123\n")
	var out bytes.Buffer

	pwd, err := promptPassword(in, &out, "root", "1.2.3.4")
	if err != nil {
		t.Fatalf("promptPassword unexpected error: %v", err)
	}
	if pwd != "mySecret123" {
		t.Errorf("promptPassword = %q, want 'mySecret123'", pwd)
	}
	if !strings.Contains(out.String(), "Enter SSH password") {
		t.Errorf("prompt output missing prompt message: %s", out.String())
	}

	// Empty input
	inEmpty := strings.NewReader("")
	var outEmpty bytes.Buffer
	pwdEmpty, err := promptPassword(inEmpty, &outEmpty, "root", "1.2.3.4")
	if err != nil {
		t.Fatalf("promptPassword empty unexpected error: %v", err)
	}
	if pwdEmpty != "" {
		t.Errorf("promptPassword empty = %q, want ''", pwdEmpty)
	}
}

func TestPromptConfirm(t *testing.T) {
	cases := []struct {
		name       string
		input      string
		defaultYes bool
		want       bool
	}{
		{"empty-enter-default-yes", "\n", true, true},
		{"empty-enter-default-no", "\n", false, false},
		{"yes-lower", "y\n", false, true},
		{"yes-word", "yes\n", false, true},
		{"no-lower", "n\n", true, false},
		{"no-word", "no\n", true, false},
		{"eof", "", true, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := strings.NewReader(tc.input)
			var out bytes.Buffer
			got := promptConfirm(in, &out, "? Confirm [Y/n]: ", tc.defaultYes)
			if got != tc.want {
				t.Errorf("promptConfirm(%q, %v) = %v, want %v", tc.input, tc.defaultYes, got, tc.want)
			}
		})
	}
}

func TestFindDefaultPublicKey(t *testing.T) {
	tempHome := t.TempDir()
	setTestHome(t, tempHome)

	// Case 1: no keys
	pub, priv, exists := findDefaultPublicKey()
	if exists {
		t.Fatalf("findDefaultPublicKey() should not find keys in empty dir, got (%s, %s)", pub, priv)
	}

	// Case 2: create ~/.ssh/id_ed25519 and id_ed25519.pub
	sshDir := filepath.Join(tempHome, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(sshDir, "id_ed25519"), []byte("privkey"), 0o600)
	_ = os.WriteFile(filepath.Join(sshDir, "id_ed25519.pub"), []byte("pubkey"), 0o600)

	pub, priv, exists = findDefaultPublicKey()
	if !exists || pub != "~/.ssh/id_ed25519.pub" || priv != "~/.ssh/id_ed25519" {
		t.Errorf("findDefaultPublicKey() = (%s, %s, %v), want (~/.ssh/id_ed25519.pub, ~/.ssh/id_ed25519, true)", pub, priv, exists)
	}
}

func TestServerAddCommand_Integration(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv(config.EnvHome, tempHome)
	setTestHome(t, tempHome)

	// Test 1: ops add node-1 10.0.0.1 --skip-test
	rootCmd.SetArgs([]string{"add", "node-1", "10.0.0.1", "--skip-test"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("rootCmd.Execute(add node-1 10.0.0.1) error: %v", err)
	}

	store := server.NewDefaultStore()
	s1, err := store.Get("node-1")
	if err != nil {
		t.Fatalf("failed to retrieve node-1: %v", err)
	}
	if s1.Host != "10.0.0.1" || s1.User != "root" || s1.Port != 22 {
		t.Errorf("node-1 attributes mismatch: host=%s user=%s port=%d", s1.Host, s1.User, s1.Port)
	}

	// Test 2: ops add node-2 ubuntu@10.0.0.2:2222 -i ~/.ssh/test_key --skip-test -l env=prod,dc=us -t web,app -d "Production Web"
	// Create dummy key
	sshDir := filepath.Join(tempHome, ".ssh")
	_ = os.MkdirAll(sshDir, 0o700)
	dummyKeyPath := filepath.Join(sshDir, "test_key")
	_ = os.WriteFile(dummyKeyPath, generateTestKey(t), 0o600)

	rootCmd.SetArgs([]string{
		"add", "node-2", "ubuntu@10.0.0.2:2222",
		"-i", dummyKeyPath,
		"--skip-test",
		"-l", "env=prod,dc=us",
		"-t", "web,app",
		"-d", "Production Web",
	})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("rootCmd.Execute(add node-2) error: %v", err)
	}

	s2, err := store.Get("node-2")
	if err != nil {
		t.Fatalf("failed to retrieve node-2: %v", err)
	}
	if s2.Host != "10.0.0.2" || s2.User != "ubuntu" || s2.Port != 2222 {
		t.Errorf("node-2 attributes mismatch: host=%s user=%s port=%d", s2.Host, s2.User, s2.Port)
	}
	if s2.Labels["env"] != "prod" || s2.Labels["dc"] != "us" {
		t.Errorf("node-2 labels mismatch: %v", s2.Labels)
	}
	if len(s2.Tags) != 2 || s2.Tags[0] != "web" || s2.Tags[1] != "app" {
		t.Errorf("node-2 tags mismatch: %v", s2.Tags)
	}
	if s2.Description != "Production Web" {
		t.Errorf("node-2 description mismatch: %s", s2.Description)
	}

	// Test 3: serverAddCmd also works (ops server add ...)
	rootCmd.SetArgs([]string{"server", "add", "node-3", "10.0.0.3", "--port", "2200", "--skip-test"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("rootCmd.Execute(server add node-3) error: %v", err)
	}
	s3, err := store.Get("node-3")
	if err != nil {
		t.Fatalf("failed to retrieve node-3: %v", err)
	}
	if s3.Host != "10.0.0.3" || s3.Port != 2200 {
		t.Errorf("node-3 attributes mismatch: host=%s port=%d", s3.Host, s3.Port)
	}

	// Test 4: Missing host error
	rootCmd.SetArgs([]string{"add", "node-missing", "--skip-test"})
	if err := rootCmd.Execute(); err == nil {
		t.Fatalf("expected error when host is missing, got nil")
	}

	// Test 5: Add with Jump Host (-J)
	rootCmd.SetArgs([]string{"add", "node-internal", "ubuntu@vps2", "-J", "node-1", "--skip-test"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("rootCmd.Execute(add with -J) error: %v", err)
	}
	sJump, err := store.Get("node-internal")
	if err != nil {
		t.Fatalf("failed to retrieve node-internal: %v", err)
	}
	if sJump.JumpHost != "node-1" || sJump.Host != "vps2" || sJump.User != "ubuntu" {
		t.Errorf("node-internal attributes mismatch: JumpHost=%s, Host=%s, User=%s", sJump.JumpHost, sJump.Host, sJump.User)
	}

	// Test 6: Add with non-existent jump host error
	rootCmd.SetArgs([]string{"add", "node-bad-jump", "10.0.0.5", "-J", "ghost-server", "--skip-test"})
	if err := rootCmd.Execute(); err == nil {
		t.Fatalf("expected error when jump host doesn't exist, got nil")
	}

	// Test 7: Add with self-referencing jump host error
	rootCmd.SetArgs([]string{"add", "self-jump", "10.0.0.6", "-J", "self-jump", "--skip-test"})
	if err := rootCmd.Execute(); err == nil {
		t.Fatalf("expected error when jump host is self-referencing, got nil")
	}

	// Test 8: Remove jump host that has dependent servers is blocked
	rootCmd.SetArgs([]string{"server", "remove", "node-1"})
	if err := rootCmd.Execute(); err == nil {
		t.Fatalf("expected error when removing server with dependents, got nil")
	}

	// Remove dependent first, then removing jump host succeeds
	rootCmd.SetArgs([]string{"server", "remove", "node-internal"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error removing dependent server: %v", err)
	}
	rootCmd.SetArgs([]string{"server", "remove", "node-1"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("unexpected error removing jump host after dependent was removed: %v", err)
	}

	// Test 9: Add with --skip-batch
	rootCmd.SetArgs([]string{"add", "node-skip", "10.0.0.9", "--skip-test", "--skip-batch", "-J", ""})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("rootCmd.Execute(add with --skip-batch) error: %v", err)
	}
	sSkip, err := store.Get("node-skip")
	if err != nil {
		t.Fatalf("failed to retrieve node-skip: %v", err)
	}
	if !sSkip.SkipBatch {
		t.Errorf("expected node-skip to have SkipBatch=true, got false")
	}
}

