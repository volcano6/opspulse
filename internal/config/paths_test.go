package config

import (
	"os"
	"testing"
)

func TestConfigDir_EnvOverride(t *testing.T) {
	t.Setenv(EnvHome, "/custom/opspulse")
	got := ConfigDir()
	if got != "/custom/opspulse" {
		t.Errorf("ConfigDir() = %q, want /custom/opspulse", got)
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

func TestConfigDir_XDGFallback(t *testing.T) {
	os.Unsetenv(EnvHome)
	got := ConfigDir()
	if got == "" {
		t.Error("ConfigDir() returned empty string")
	}
}
