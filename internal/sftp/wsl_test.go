package sftp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTranslateWindowsPathToWSL(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{`C:\Users\user`, `/mnt/c/Users/user`},
		{`D:\Projects\app`, `/mnt/d/Projects/app`},
		{`no_drive_letter`, `no_drive_letter`},
	}

	for _, tc := range tests {
		actual := translateWindowsPathToWSL(tc.input)
		if actual != tc.expected {
			t.Errorf("translateWindowsPathToWSL(%q) = %q, want %q", tc.input, actual, tc.expected)
		}
	}
}

func TestIsWSL(t *testing.T) {
	// Simple test to ensure it doesn't panic. Environment specific.
	orig := os.Getenv("WSL_DISTRO_NAME")
	_ = os.Setenv("WSL_DISTRO_NAME", "Ubuntu")
	if !IsWSL() {
		t.Error("Expected true when WSL_DISTRO_NAME is set")
	}
	if orig != "" {
		_ = os.Setenv("WSL_DISTRO_NAME", orig)
	} else {
		_ = os.Unsetenv("WSL_DISTRO_NAME")
	}
}

func TestEnsurePrivateDirTightensExistingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mirror")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("prepare directory: %v", err)
	}
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatalf("prepare directory mode: %v", err)
	}

	if err := ensurePrivateDir(dir); err != nil {
		t.Fatalf("ensurePrivateDir() error: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat directory: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("directory mode = %o, want 700", got)
	}
}
