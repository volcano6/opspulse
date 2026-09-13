package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDir_EnvOverride(t *testing.T) {
	t.Setenv(EnvHome, "/custom/opspulse")
	got := Dir()
	if got != "/custom/opspulse" {
		t.Errorf("Dir() = %q, want /custom/opspulse", got)
	}
}

func TestDataDir_EnvOverride(t *testing.T) {
	t.Setenv(EnvHome, "/custom/opspulse")
	got := DataDir()
	want := filepath.Join("/custom/opspulse", "data")
	if got != want {
		t.Errorf("DataDir() = %q, want %q", got, want)
	}
}

func TestDir_XDGFallback(t *testing.T) {
	if err := os.Unsetenv(EnvHome); err != nil {
		t.Fatalf("failed to unset env: %v", err)
	}
	got := Dir()
	if got == "" {
		t.Error("Dir() returned empty string")
	}
}

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("unable to get user home directory")
	}

	tests := []struct {
		input string
		want  string
	}{
		{"~", home},
		{"~/test.key", filepath.Join(home, "test.key")},
		{"~\\test.key", filepath.Join(home, "test.key")},
		{"/var/log/test", "/var/log/test"},
		{"relative/path", "relative/path"},
	}

	for _, tt := range tests {
		got := ExpandPath(tt.input)
		if got != tt.want {
			t.Errorf("ExpandPath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestIsSecureDir(t *testing.T) {
	tempDir := t.TempDir()
	if !isSecureDir(tempDir) {
		t.Errorf("expected t.TempDir() to be secure")
	}

	nonExistent := filepath.Join(tempDir, "non_existent")
	if isSecureDir(nonExistent) {
		t.Errorf("expected non-existent dir to not be secure")
	}
}
