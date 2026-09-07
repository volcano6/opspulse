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

func TestExportSSHConfigCmd(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv(config.EnvHome, tempHome)
	setTestHome(t, tempHome)

	// Add sample servers
	store := server.NewDefaultStore()
	_ = store.Save(server.Server{
		Name:        "srv-alpha",
		Host:        "10.0.0.1",
		User:        "root",
		KeyPath:     "~/.ssh/id_ed25519",
		Description: "Alpha server",
		Tags:        []string{"prod"},
	})
	_ = store.Save(server.Server{
		Name:   "srv-beta",
		Host:   "10.0.0.2",
		User:   "ubuntu",
		Port:   2200,
		Labels: map[string]string{"env": "staging"},
	})

	// Test 1: ops export ssh-config (stdout)
	var outBuf bytes.Buffer
	rootCmd.SetOut(&outBuf)
	rootCmd.SetArgs([]string{"export", "ssh-config"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("export ssh-config error: %v", err)
	}

	// Test 2: ops export ssh-config --write --file <path>
	targetSSHConfig := filepath.Join(tempHome, "custom_ssh_config")
	rootCmd.SetArgs([]string{"export", "ssh-config", "--write", "--file", targetSSHConfig})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("export ssh-config --write error: %v", err)
	}

	diskData, err := os.ReadFile(targetSSHConfig)
	if err != nil {
		t.Fatalf("read exported ssh config file error: %v", err)
	}
	content := string(diskData)
	if !strings.Contains(content, "Host srv-alpha") || !strings.Contains(content, "Host srv-beta") {
		t.Errorf("exported file missing hosts:\n%s", content)
	}
	if !strings.Contains(content, "Port 2200") {
		t.Errorf("exported file missing Port 2200 for srv-beta")
	}

	// Test 3: filter
	filteredSSHConfig := filepath.Join(tempHome, "filtered_ssh_config")
	rootCmd.SetArgs([]string{"export", "ssh-config", "--write", "--file", filteredSSHConfig, "--filter", "env=staging"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("export ssh-config --filter error: %v", err)
	}
	filteredData, _ := os.ReadFile(filteredSSHConfig)
	filteredContent := string(filteredData)
	if strings.Contains(filteredContent, "Host srv-alpha") {
		t.Errorf("filtered output should not contain srv-alpha:\n%s", filteredContent)
	}
	if !strings.Contains(filteredContent, "Host srv-beta") {
		t.Errorf("filtered output should contain srv-beta:\n%s", filteredContent)
	}
}
