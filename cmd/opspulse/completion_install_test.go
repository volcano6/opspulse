package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateProfileFile_NewFile(t *testing.T) {
	tempDir := t.TempDir()
	targetFile := filepath.Join(tempDir, ".bashrc")

	begin := "# >>> Test >>>"
	end := "# <<< Test <<<"
	content := begin + "\neval something\n" + end + "\n"

	err := updateProfileFile(targetFile, begin, end, content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	if string(data) != content {
		t.Errorf("got %q, want %q", string(data), content)
	}
}

func TestUpdateProfileFile_AppendAndIdempotent(t *testing.T) {
	tempDir := t.TempDir()
	targetFile := filepath.Join(tempDir, ".zshrc")

	initial := "export FOO=bar\n"
	if err := os.WriteFile(targetFile, []byte(initial), 0o600); err != nil {
		t.Fatalf("failed to write initial: %v", err)
	}

	begin := "# >>> Test >>>"
	end := "# <<< Test <<<"
	blockV1 := begin + "\nv1\n" + end + "\n"

	// 1. First append
	if err := updateProfileFile(targetFile, begin, end, blockV1); err != nil {
		t.Fatalf("failed update: %v", err)
	}
	data, _ := os.ReadFile(targetFile)
	if !strings.Contains(string(data), "export FOO=bar\n\n"+begin) {
		t.Errorf("unexpected content after first update: %q", string(data))
	}

	// 2. Second update with new version of block (must replace in-place)
	blockV2 := begin + "\nv2\n" + end + "\n"
	if err := updateProfileFile(targetFile, begin, end, blockV2); err != nil {
		t.Fatalf("failed second update: %v", err)
	}
	data, _ = os.ReadFile(targetFile)
	if strings.Contains(string(data), "v1") {
		t.Errorf("expected v1 to be replaced: %q", string(data))
	}
	if !strings.Contains(string(data), "v2") {
		t.Errorf("expected v2 to be present: %q", string(data))
	}
	// Check count of begin marker (should only appear once)
	if strings.Count(string(data), begin) != 1 {
		t.Errorf("expected begin marker exactly once, got %d", strings.Count(string(data), begin))
	}
}

func TestDetectCurrentShell(t *testing.T) {
	origShell := os.Getenv("SHELL")
	defer func() {
		_ = os.Setenv("SHELL", origShell)
	}()

	_ = os.Setenv("SHELL", "/bin/zsh")
	if got := detectCurrentShell(); got != "zsh" {
		t.Errorf("got %q, want zsh", got)
	}

	_ = os.Setenv("SHELL", "/usr/local/bin/fish")
	if got := detectCurrentShell(); got != "fish" {
		t.Errorf("got %q, want fish", got)
	}

	_ = os.Setenv("SHELL", "/bin/bash")
	if got := detectCurrentShell(); got != "bash" {
		t.Errorf("got %q, want bash", got)
	}
}
