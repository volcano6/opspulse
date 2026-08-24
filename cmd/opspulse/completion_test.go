package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/backup"
	"github.com/volcano6/opspulse/internal/config"
	"github.com/volcano6/opspulse/internal/server"
)

func setupTestEnv(t *testing.T) string {
	t.Helper()
	tempDir := t.TempDir()
	t.Setenv(config.EnvHome, tempDir)

	// Create sample servers.yaml
	serverStore := server.NewDefaultStore()
	_ = serverStore.Save(server.Server{
		Name:        "web-01",
		Host:        "198.51.100.10",
		Port:        22,
		User:        "root",
		Description: "Production web server",
	})
	_ = serverStore.Save(server.Server{
		Name: "db-01",
		Host: "198.51.100.20",
		Port: 22,
		User: "root",
	})

	// Create sample backups.yaml
	backupStore := backup.NewDefaultStore()
	_ = backupStore.Save(backup.Job{
		Name:    "vps01-etc",
		Server:  "web-01",
		Backend: "local:/tmp/backup",
		Paths:   []string{"/etc"},
	})
	_ = backupStore.Save(backup.Job{
		Name:    "web-data",
		Server:  "web-01",
		Backend: "s3:https://s3.example.com/backup",
		Paths:   []string{"/var/www"},
	})

	// Create a custom template
	customTmplDir := filepath.Join(tempDir, "templates")
	_ = os.MkdirAll(customTmplDir, 0o750)
	customTmplContent := `---
name: custom-app
version: 1
description: Custom app setup
---
echo "installing custom app"
`
	_ = os.WriteFile(filepath.Join(customTmplDir, "custom-app.sh"), []byte(customTmplContent), 0o600)

	return tempDir
}

func TestCompleteServerNames(t *testing.T) {
	setupTestEnv(t)

	// 1. Initial completion
	comps, directive := completeServerNames(serverTestCmd, nil, "")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("expected directive ShellCompDirectiveNoFileComp, got %v", directive)
	}
	if len(comps) != 2 {
		t.Fatalf("expected 2 server completions, got %d: %v", len(comps), comps)
	}

	foundWeb := false
	foundDB := false
	for _, c := range comps {
		if strings.HasPrefix(c, "web-01\t198.51.100.10 (Production web server)") {
			foundWeb = true
		}
		if strings.HasPrefix(c, "db-01\t198.51.100.20") {
			foundDB = true
		}
	}
	if !foundWeb || !foundDB {
		t.Errorf("missing expected server completions: %v", comps)
	}

	// 2. Already has argument
	comps, directive = completeServerNames(serverTestCmd, []string{"web-01"}, "")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("expected directive ShellCompDirectiveNoFileComp, got %v", directive)
	}
	if len(comps) != 0 {
		t.Errorf("expected 0 completions when args already present, got %v", comps)
	}
}

func TestCompleteTemplateNames(t *testing.T) {
	setupTestEnv(t)

	comps, directive := completeTemplateNames(templateShowCmd, nil, "")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("expected directive ShellCompDirectiveNoFileComp, got %v", directive)
	}

	// Should contain built-in (base, docker, restic, security) + custom-app
	foundBase := false
	foundCustom := false
	for _, c := range comps {
		if strings.HasPrefix(c, "base\t") {
			foundBase = true
		}
		if strings.HasPrefix(c, "custom-app\tCustom app setup") {
			foundCustom = true
		}
	}
	if !foundBase {
		t.Errorf("expected built-in 'base' template in completions, got %v", comps)
	}
	if !foundCustom {
		t.Errorf("expected 'custom-app' template in completions, got %v", comps)
	}

	// When args present
	comps, _ = completeTemplateNames(templateShowCmd, []string{"base"}, "")
	if len(comps) != 0 {
		t.Errorf("expected 0 completions when args present, got %v", comps)
	}
}

func TestCompleteBackupJobNames(t *testing.T) {
	setupTestEnv(t)

	comps, directive := completeBackupJobNames(backupHistoryCmd, nil, "")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("expected directive ShellCompDirectiveNoFileComp, got %v", directive)
	}
	if len(comps) != 2 {
		t.Fatalf("expected 2 backup job completions, got %d: %v", len(comps), comps)
	}

	foundJob := false
	for _, c := range comps {
		if strings.HasPrefix(c, "vps01-etc\tweb-01 (local:/tmp/backup)") {
			foundJob = true
		}
	}
	if !foundJob {
		t.Errorf("expected 'vps01-etc' job in completions, got %v", comps)
	}
}

func TestCompleteBackupRunArgs(t *testing.T) {
	setupTestEnv(t)

	// 1. Initial completion: should include 'all' + jobs
	comps, directive := completeBackupRunArgs(backupRunCmd, nil, "")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("expected directive ShellCompDirectiveNoFileComp, got %v", directive)
	}
	foundAll := false
	foundWebData := false
	for _, c := range comps {
		if strings.HasPrefix(c, "all\t") {
			foundAll = true
		}
		if strings.HasPrefix(c, "web-data\t") {
			foundWebData = true
		}
	}
	if !foundAll || !foundWebData {
		t.Errorf("expected 'all' and 'web-data' in completions, got %v", comps)
	}

	// 2. Comma-separated completion
	comps, _ = completeBackupRunArgs(backupRunCmd, nil, "vps01-etc,")
	if len(comps) != 1 {
		t.Fatalf("expected 1 remaining completion for 'vps01-etc,', got %d: %v", len(comps), comps)
	}
	if !strings.HasPrefix(comps[0], "vps01-etc,web-data\t") {
		t.Errorf("expected 'vps01-etc,web-data' prefix completion, got %q", comps[0])
	}
}

func TestCompleteBootstrapArgsAndFlags(t *testing.T) {
	setupTestEnv(t)

	// Server args completion
	comps, directive := completeBootstrapServerArgs(bootstrapCmd, nil, "")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("expected directive ShellCompDirectiveNoFileComp, got %v", directive)
	}
	if len(comps) != 2 {
		t.Fatalf("expected 2 server completions, got %d: %v", len(comps), comps)
	}

	// Comma-separated server completion
	comps, _ = completeBootstrapServerArgs(bootstrapCmd, nil, "web-01,")
	if len(comps) != 1 {
		t.Fatalf("expected 1 remaining server completion for 'web-01,', got %d: %v", len(comps), comps)
	}
	if !strings.HasPrefix(comps[0], "web-01,db-01\t") {
		t.Errorf("expected 'web-01,db-01' completion, got %q", comps[0])
	}

	// Template flag completion
	flagComps, flagDir := completeBootstrapTemplateFlag(bootstrapCmd, nil, "")
	if flagDir != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("expected flag directive ShellCompDirectiveNoFileComp, got %v", flagDir)
	}
	if len(flagComps) == 0 {
		t.Errorf("expected templates in flag completions, got empty")
	}

	// Comma-separated template flag completion
	flagComps, _ = completeBootstrapTemplateFlag(bootstrapCmd, nil, "base,")
	foundCustom := false
	for _, c := range flagComps {
		if strings.HasPrefix(c, "base,custom-app\t") {
			foundCustom = true
		}
	}
	if !foundCustom {
		t.Errorf("expected 'base,custom-app' in comma-separated flag completions, got %v", flagComps)
	}
}
