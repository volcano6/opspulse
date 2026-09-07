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
	if err := os.WriteFile(mockApp, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("failed to create mock app: %v", err)
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
