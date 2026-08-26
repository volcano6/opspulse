package template

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeLineEndings(t *testing.T) {
	crlf := "echo 'hello'\r\necho 'world'\r\n"
	want := "echo 'hello'\necho 'world'\n"

	got := NormalizeLineEndings(crlf)
	if got != want {
		t.Errorf("NormalizeLineEndings(CRLF) = %q, want %q", got, want)
	}

	cr := "line 1\rline 2\r"
	wantCR := "line 1\nline 2\n"
	gotCR := NormalizeLineEndings(cr)
	if gotCR != wantCR {
		t.Errorf("NormalizeLineEndings(CR) = %q, want %q", gotCR, wantCR)
	}
}

func TestParseTemplate_CRLF(t *testing.T) {
	crlfScript := "#!/bin/bash\r\n# ---\r\n# name: crlf-test\r\n# version: 1\r\n# ---\r\necho 'running on linux'\r\n"
	tmpl, err := ParseTemplate(crlfScript, "crlf-test")
	if err != nil {
		t.Fatalf("ParseTemplate() unexpected error: %v", err)
	}

	if tmpl.Metadata.Name != "crlf-test" {
		t.Errorf("expected name 'crlf-test', got %q", tmpl.Metadata.Name)
	}
	if strings.Contains(tmpl.Content, "\r") {
		t.Errorf("expected Content to contain no carriage returns, got %q", tmpl.Content)
	}
}

func TestExtractMetadata(t *testing.T) {
	scriptWithMeta := `#!/bin/bash
# ---
# name: nginx-custom
# version: 2
# os: [ubuntu, debian]
# description: Custom Nginx Web Server
# ---
echo "installing nginx..."
`

	meta, err := ExtractMetadata(scriptWithMeta)
	if err != nil {
		t.Fatalf("ExtractMetadata() unexpected error: %v", err)
	}

	if meta.Name != "nginx-custom" {
		t.Errorf("expected name 'nginx-custom', got %q", meta.Name)
	}
	if meta.Version != 2 {
		t.Errorf("expected version 2, got %d", meta.Version)
	}
	if len(meta.OS) != 2 || meta.OS[0] != "ubuntu" {
		t.Errorf("expected OS [ubuntu, debian], got %v", meta.OS)
	}
	if meta.Description != "Custom Nginx Web Server" {
		t.Errorf("expected description 'Custom Nginx Web Server', got %q", meta.Description)
	}

	// Script without metadata
	scriptWithoutMeta := `#!/bin/bash
echo "no meta"
`
	_, err = ExtractMetadata(scriptWithoutMeta)
	if err == nil {
		t.Error("expected error for script without metadata, got nil")
	}
}

func TestLoader_BuiltinTemplates(t *testing.T) {
	loader := NewLoader("")

	list, err := loader.List()
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}

	if len(list) < 9 {
		t.Fatalf("expected at least 9 built-in templates, got %d", len(list))
	}

	expectedNames := map[string]bool{
		"base":         false,
		"security":     false,
		"timezone":     false,
		"swap":         false,
		"caddy":        false,
		"tmux":         false,
		"zsh-starship": false,
		"clean":        false,
		"upgrade":      false,
		"docker":       false,
		"restic":       false,
	}

	for _, tmpl := range list {
		if _, ok := expectedNames[tmpl.Metadata.Name]; ok {
			expectedNames[tmpl.Metadata.Name] = true
		}
		if !tmpl.IsBuiltin {
			t.Errorf("expected template %q to be marked built-in", tmpl.Metadata.Name)
		}
	}

	for name, found := range expectedNames {
		if !found {
			t.Errorf("expected built-in template %q to be present", name)
		}
	}

	// Test Get
	dockerTmpl, err := loader.Get("docker")
	if err != nil {
		t.Fatalf("Get('docker') error: %v", err)
	}
	if dockerTmpl.Metadata.Name != "docker" {
		t.Errorf("expected name 'docker', got %q", dockerTmpl.Metadata.Name)
	}
	if !dockerTmpl.IsBuiltin {
		t.Error("expected docker template to be built-in")
	}

	// Test Not Found
	_, err = loader.Get("non-existent")
	if err == nil {
		t.Error("expected error for non-existent template, got nil")
	}
}

func TestLoader_CustomOverride(t *testing.T) {
	tmpDir := t.TempDir()
	customDocker := `#!/bin/bash
# ---
# name: docker
# version: 99
# description: Overridden Docker script
# ---
echo "my custom docker"
`
	err := os.WriteFile(filepath.Join(tmpDir, "docker.sh"), []byte(customDocker), 0o600)
	if err != nil {
		t.Fatalf("failed to write custom template: %v", err)
	}

	customNew := `#!/bin/bash
# ---
# name: nodejs
# version: 1
# description: Install Node.js
# ---
echo "installing node"
`
	err = os.WriteFile(filepath.Join(tmpDir, "nodejs.sh"), []byte(customNew), 0o600)
	if err != nil {
		t.Fatalf("failed to write custom nodejs template: %v", err)
	}

	loader := NewLoader(tmpDir)

	// Get overridden docker template
	tmpl, err := loader.Get("docker")
	if err != nil {
		t.Fatalf("Get('docker') error: %v", err)
	}
	if tmpl.IsBuiltin {
		t.Error("expected custom docker template to override built-in")
	}
	if tmpl.Metadata.Version != 99 {
		t.Errorf("expected version 99, got %d", tmpl.Metadata.Version)
	}

	// Get custom new template
	nodeTmpl, err := loader.Get("nodejs")
	if err != nil {
		t.Fatalf("Get('nodejs') error: %v", err)
	}
	if nodeTmpl.Metadata.Name != "nodejs" {
		t.Errorf("expected name 'nodejs', got %q", nodeTmpl.Metadata.Name)
	}

	// List should include nodejs and overridden docker
	list, err := loader.List()
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}

	foundNode := false
	for _, item := range list {
		if item.Metadata.Name == "nodejs" {
			foundNode = true
		}
		if item.Metadata.Name == "docker" && item.Metadata.Version != 99 {
			t.Errorf("expected listed docker template to be custom version 99, got %d", item.Metadata.Version)
		}
	}
	if !foundNode {
		t.Error("expected 'nodejs' template in List()")
	}
}

func TestLoader_CustomNameMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	customScript := `#!/bin/bash
# ---
# name: custom-alias
# version: 3
# description: File named differently
# ---
echo "running alias"
`
	err := os.WriteFile(filepath.Join(tmpDir, "some_file.sh"), []byte(customScript), 0o600)
	if err != nil {
		t.Fatalf("failed to write custom template: %v", err)
	}

	loader := NewLoader(tmpDir)

	// Get by Metadata.Name
	tmpl, err := loader.Get("custom-alias")
	if err != nil {
		t.Fatalf("Get('custom-alias') error: %v", err)
	}
	if tmpl.Metadata.Name != "custom-alias" {
		t.Errorf("expected name 'custom-alias', got %q", tmpl.Metadata.Name)
	}
	if tmpl.Metadata.Version != 3 {
		t.Errorf("expected version 3, got %d", tmpl.Metadata.Version)
	}

	// Also get by filename without extension
	tmplByFile, err := loader.Get("some_file")
	if err != nil {
		t.Fatalf("Get('some_file') error: %v", err)
	}
	if tmplByFile.Metadata.Name != "custom-alias" {
		t.Errorf("expected name 'custom-alias', got %q", tmplByFile.Metadata.Name)
	}
}

