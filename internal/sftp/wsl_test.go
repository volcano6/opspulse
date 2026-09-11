package sftp

import (
	"os"
	"testing"
)

func TestTranslateWindowsPathToWSL(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{`C:\Users\volca`, `/mnt/c/Users/volca`},
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

func TestTranslateWSLPathToWindows(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{`/mnt/c/Users/volca`, `C:\Users\volca`},
		{`/mnt/d/Projects/app`, `D:\Projects\app`},
		{`/home/volca`, `/home/volca`},
	}

	for _, tc := range tests {
		actual := translateWSLPathToWindows(tc.input)
		if actual != tc.expected {
			t.Errorf("translateWSLPathToWindows(%q) = %q, want %q", tc.input, actual, tc.expected)
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
