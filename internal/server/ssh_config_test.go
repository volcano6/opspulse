package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderSSHConfig(t *testing.T) {
	servers := []Server{
		{
			Name:        "web-01",
			Host:        "192.168.1.10",
			Port:        22,
			User:        "root",
			KeyPath:     "~/.ssh/id_ed25519",
			Description: "Production web server",
			Tags:        []string{"prod", "web"},
		},
		{
			Name:     "db-01",
			Host:     "192.168.1.20",
			Port:     2222,
			User:     "postgres",
			Password: "mypassword",
			Labels:   map[string]string{"env": "prod"},
			JumpHost: "web-01",
		},
	}

	rendered := RenderSSHConfig(servers)

	if !strings.Contains(rendered, MarkerBegin) || !strings.Contains(rendered, MarkerEnd) {
		t.Fatalf("rendered output missing markers:\n%s", rendered)
	}

	// Should contain web-01 block
	if !strings.Contains(rendered, "Host web-01\n") {
		t.Errorf("missing Host web-01 in:\n%s", rendered)
	}
	if !strings.Contains(rendered, "    HostName 192.168.1.10\n") {
		t.Errorf("missing HostName for web-01")
	}
	if !strings.Contains(rendered, "    IdentityFile ~/.ssh/id_ed25519\n") {
		t.Errorf("missing IdentityFile for web-01")
	}
	if !strings.Contains(rendered, "    IdentitiesOnly yes\n") {
		t.Errorf("missing IdentitiesOnly for web-01")
	}
	if !strings.Contains(rendered, "# Production web server | tags: prod,web\n") {
		t.Errorf("missing description/tags comment for web-01")
	}

	// Should contain db-01 block
	if !strings.Contains(rendered, "Host db-01\n") {
		t.Errorf("missing Host db-01 in:\n%s", rendered)
	}
	if !strings.Contains(rendered, "    Port 2222\n") {
		t.Errorf("missing custom Port 2222 for db-01")
	}
	if !strings.Contains(rendered, "    User postgres\n") {
		t.Errorf("missing User postgres for db-01")
	}
	if !strings.Contains(rendered, "    PreferredAuthentications password,keyboard-interactive\n") {
		t.Errorf("missing password auth for db-01")
	}
	if !strings.Contains(rendered, "    ProxyJump web-01\n") {
		t.Errorf("missing ProxyJump web-01 for db-01")
	}
}

func TestRenderSSHConfig_Empty(t *testing.T) {
	rendered := RenderSSHConfig(nil)
	if !strings.Contains(rendered, MarkerBegin) || !strings.Contains(rendered, MarkerEnd) {
		t.Fatalf("empty rendered output missing markers:\n%s", rendered)
	}
	if !strings.Contains(rendered, "No managed hosts configured") {
		t.Errorf("expected empty notice, got:\n%s", rendered)
	}
}

func TestMergeSSHConfigContent_AppendAndReplace(t *testing.T) {
	initialCustom := `Host github.com
    HostName github.com
    User git
    IdentityFile ~/.ssh/github_key
`
	block1 := RenderSSHConfig([]Server{
		{Name: "vps-1", Host: "1.1.1.1", User: "root"},
	})

	// 1. Initial merge (append)
	merged1 := MergeSSHConfigContent(initialCustom, block1)
	if !strings.HasPrefix(merged1, "Host github.com") {
		t.Errorf("merged1 should preserve existing prefix:\n%s", merged1)
	}
	if !strings.Contains(merged1, "Host vps-1") {
		t.Errorf("merged1 missing vps-1:\n%s", merged1)
	}

	// 2. Second merge (replace with vps-2)
	block2 := RenderSSHConfig([]Server{
		{Name: "vps-2", Host: "2.2.2.2", User: "ubuntu"},
	})
	merged2 := MergeSSHConfigContent(merged1, block2)

	// Should still have github.com
	if !strings.Contains(merged2, "Host github.com") {
		t.Errorf("merged2 lost custom Host github.com:\n%s", merged2)
	}
	// Should have vps-2
	if !strings.Contains(merged2, "Host vps-2") {
		t.Errorf("merged2 missing vps-2:\n%s", merged2)
	}
	// Should NOT have vps-1 anymore (replaced)
	if strings.Contains(merged2, "Host vps-1") {
		t.Errorf("merged2 still contains old vps-1:\n%s", merged2)
	}

	// Count markers - should have exactly 1 begin and 1 end
	if strings.Count(merged2, MarkerBegin) != 1 {
		t.Errorf("expected 1 MarkerBegin, got %d", strings.Count(merged2, MarkerBegin))
	}
	if strings.Count(merged2, MarkerEnd) != 1 {
		t.Errorf("expected 1 MarkerEnd, got %d", strings.Count(merged2, MarkerEnd))
	}
}

func TestUpdateSSHConfigFile(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config")

	// Pre-populate with a custom host
	existingContent := "Host bastion\n    HostName 10.0.0.1\n"
	if err := os.WriteFile(configPath, []byte(existingContent), 0o600); err != nil {
		t.Fatal(err)
	}

	servers := []Server{
		{Name: "app-01", Host: "10.0.0.2", User: "root", KeyPath: "~/.ssh/id_rsa"},
	}

	written, count, err := UpdateSSHConfigFile(configPath, servers)
	if err != nil {
		t.Fatalf("UpdateSSHConfigFile error: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}

	// Verify file content on disk
	diskData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	diskContent := string(diskData)
	if diskContent != written {
		t.Errorf("disk content does not match returned written content")
	}
	if !strings.Contains(diskContent, "Host bastion") {
		t.Errorf("bastion was wiped out:\n%s", diskContent)
	}
	if !strings.Contains(diskContent, "Host app-01") {
		t.Errorf("app-01 missing from disk:\n%s", diskContent)
	}

	// Test update idempotency
	written2, count2, err := UpdateSSHConfigFile(configPath, servers)
	if err != nil {
		t.Fatalf("second UpdateSSHConfigFile error: %v", err)
	}
	if count2 != 1 || written2 != written {
		t.Errorf("second run altered content unexpectedly")
	}
}
