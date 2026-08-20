package config

import (
	"os"
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
	want := "/custom/opspulse/data"
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
