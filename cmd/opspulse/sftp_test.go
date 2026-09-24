package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/volcano6/opspulse/internal/config"
	"github.com/volcano6/opspulse/internal/server"
)

func setupTestServerStore(t *testing.T) string {
	t.Helper()
	tempDir := t.TempDir()
	t.Setenv(config.EnvHome, tempDir)

	store := server.NewDefaultStore()
	testServer := server.Server{
		Name: "test-vps",
		Host: "192.168.10.100",
		Port: 22,
		User: "root",
	}
	if err := store.Save(testServer); err != nil {
		t.Fatalf("failed to save test server: %v", err)
	}
	return tempDir
}

func TestAttachAskpass(t *testing.T) {
	t.Run("a key-only server needs no helper", func(t *testing.T) {
		cmd := exec.Command("sftp", "root@10.0.0.1")
		cleanup, err := attachAskpass(cmd, server.Server{Name: "web", Host: "10.0.0.1", KeyPath: "~/.ssh/id_ed25519"}, nil)
		if err != nil {
			t.Fatalf("attachAskpass() error: %v", err)
		}
		defer cleanup()
		if cmd.Env != nil {
			t.Errorf("cmd.Env was set for a key-only server: %v", cmd.Env)
		}
	})

	t.Run("a password server gets the helper environment", func(t *testing.T) {
		cmd := exec.Command("sftp", "root@10.0.0.1")
		srv := server.Server{Name: "web", Host: "10.0.0.1", Password: "s3cret"}

		cleanup, err := attachAskpass(cmd, srv, nil)
		if err != nil {
			t.Fatalf("attachAskpass() error: %v", err)
		}
		defer cleanup()

		env := envMapOf(cmd.Env)
		if env["SSH_ASKPASS"] == "" {
			t.Error("SSH_ASKPASS was not set")
		}
		if env["SSH_ASKPASS_REQUIRE"] != "force" {
			t.Errorf("SSH_ASKPASS_REQUIRE = %q, want force", env["SSH_ASKPASS_REQUIRE"])
		}
		if env[askpassHelperFlag] != "1" {
			t.Errorf("%s = %q, want 1", askpassHelperFlag, env[askpassHelperFlag])
		}

		// The payload must be readable through the same contract the helper
		// uses, otherwise sftp would be handed an unanswerable prompt.
		payload := env[askpassDataFile]
		if payload == "" {
			t.Fatal("the payload path was not exported")
		}
		t.Setenv(askpassDataFile, payload)
		pass, err := readSSHAskpassPassword("root@10.0.0.1's password: ")
		if err != nil {
			t.Fatalf("helper could not read the payload: %v", err)
		}
		if pass != "s3cret" {
			t.Errorf("helper returned %q, want %q", pass, "s3cret")
		}

		cleanup()
		if _, err := os.Stat(filepath.Dir(payload)); !os.IsNotExist(err) {
			t.Errorf("cleanup left %s behind (stat err: %v)", filepath.Dir(payload), err)
		}
	})

	t.Run("a password jump host is answered too", func(t *testing.T) {
		cmd := exec.Command("sftp", "deploy@10.0.0.5")
		// The target authenticates by key; only the hop needs a password, and
		// the ssh(1) the ProxyCommand spawns must be able to answer for it.
		srv := server.Server{Name: "internal", Host: "10.0.0.5", KeyPath: "~/.ssh/id_ed25519"}
		jump := &server.Server{Name: "bastion", Host: "203.0.113.9", Password: "jump-pass"}

		cleanup, err := attachAskpass(cmd, srv, jump)
		if err != nil {
			t.Fatalf("attachAskpass() error: %v", err)
		}
		defer cleanup()

		if cmd.Env == nil {
			t.Fatal("the jump host password was not handed to the child process")
		}
		t.Setenv(askpassDataFile, envMapOf(cmd.Env)[askpassDataFile])
		pass, err := readSSHAskpassPassword("ops@203.0.113.9's password: ")
		if err != nil {
			t.Fatalf("helper could not answer the jump host prompt: %v", err)
		}
		if pass != "jump-pass" {
			t.Errorf("helper returned %q, want the jump host password", pass)
		}
	})

	t.Run("each host is answered with its own password", func(t *testing.T) {
		cmd := exec.Command("sftp", "root@10.0.0.5")
		target := server.Server{Name: "internal", Host: "10.0.0.5", Password: "target-pass"}
		jump := &server.Server{Name: "bastion", Host: "203.0.113.9", Password: "jump-pass"}

		cleanup, err := attachAskpass(cmd, target, jump)
		if err != nil {
			t.Fatalf("attachAskpass() error: %v", err)
		}
		defer cleanup()

		t.Setenv(askpassDataFile, envMapOf(cmd.Env)[askpassDataFile])
		for prompt, want := range map[string]string{
			"root@10.0.0.5's password: ":   "target-pass",
			"ops@203.0.113.9's password: ": "jump-pass",
		} {
			pass, err := readSSHAskpassPassword(prompt)
			if err != nil {
				t.Errorf("helper could not answer %q: %v", prompt, err)
				continue
			}
			if pass != want {
				t.Errorf("helper answered %q with %q, want %q", prompt, pass, want)
			}
		}
	})
}

func envMapOf(environ []string) map[string]string {
	out := make(map[string]string, len(environ))
	for _, entry := range environ {
		if key, value, ok := strings.Cut(entry, "="); ok {
			out[key] = value
		}
	}
	return out
}

func TestSFTPCmd_ListApps(t *testing.T) {
	sftpListApps = true
	defer func() { sftpListApps = false }()

	err := listAvailableSFTPApps()
	if err != nil {
		t.Fatalf("listAvailableSFTPApps failed: %v", err)
	}
}

func TestSFTPCmd_ServerNotFound(t *testing.T) {
	setupTestServerStore(t)

	sftpApp = ""
	sftpRemotePath = "/"
	sftpCLI = false
	sftpListApps = false

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs([]string{"sftp", "nonexistent-server-404"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for non-existent server, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

func TestSFTPCmd_CustomAppNotFound(t *testing.T) {
	setupTestServerStore(t)

	sftpApp = "definitely-fake-sftp-client-999"
	sftpRemotePath = "/var/log"
	sftpCLI = false
	sftpListApps = false
	defer func() { sftpApp = "" }()

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs([]string{"sftp", "test-vps", "--app", "definitely-fake-sftp-client-999"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for non-existent client app, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error message, got: %v", err)
	}
}

func TestSFTPCmd_CustomExecutableSuccess(t *testing.T) {
	setupTestServerStore(t)

	tmpDir := t.TempDir()
	mockApp := filepath.Join(tmpDir, "mock-sftp")
	// Write a dummy script that exits 0
	if err := os.WriteFile(mockApp, []byte("#!/bin/sh\nexit 0\n"), 0o600); err != nil {
		t.Fatalf("failed to create mock app: %v", err)
	}
	if err := os.Chmod(mockApp, 0o700); err != nil { // #nosec G302
		t.Fatalf("failed to chmod mock app: %v", err)
	}

	sftpApp = ""
	sftpRemotePath = "/etc"
	sftpCLI = false
	sftpListApps = false

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs([]string{"sftp", "test-vps", "--app", mockApp})

	err := rootCmd.Execute()
	if err != nil {
		t.Fatalf("expected successful launch of mock executable, got: %v", err)
	}
}

func TestSFTPCmd_GUIRejectsJumpHost(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	store := server.NewDefaultStore()
	for _, srv := range []server.Server{
		{Name: "bastion", Host: "203.0.113.9", User: "ops"},
		{Name: "internal", Host: "10.0.0.5", User: "deploy", JumpHost: "bastion"},
	} {
		if err := store.Save(srv); err != nil {
			t.Fatalf("failed to save server %q: %v", srv.Name, err)
		}
	}

	mockApp := filepath.Join(t.TempDir(), "mock-gui-sftp")
	if err := os.WriteFile(mockApp, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("failed to create mock app: %v", err)
	}

	sftpApp = ""
	sftpRemotePath = "/"
	sftpCLI = false
	sftpListApps = false

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs([]string{"sftp", "internal", "--app", mockApp})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("a GUI client cannot tunnel through the jump host; expected a refusal")
	}
	// The user needs to learn why the launch was refused and what to run
	// instead, otherwise the only signal is a client that fails to connect.
	for _, want := range []string{"jump host", "bastion", "ops cp", "--cli"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestSFTPCmd_UnknownJumpHostFails(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	store := server.NewDefaultStore()
	srv := server.Server{Name: "internal", Host: "10.0.0.5", User: "deploy", JumpHost: "ghost"}
	if err := store.Save(srv); err != nil {
		t.Fatalf("failed to save server: %v", err)
	}

	mockApp := filepath.Join(t.TempDir(), "mock-sftp")
	if err := os.WriteFile(mockApp, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("failed to create mock app: %v", err)
	}

	sftpApp = ""
	sftpRemotePath = "/"
	sftpCLI = false
	sftpListApps = false

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs([]string{"sftp", "internal", "--app", mockApp})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected an error for a jump host that is not in the inventory")
	}
	if !strings.Contains(err.Error(), `"ghost"`) {
		t.Errorf("error %q does not name the missing jump host", err)
	}
}
