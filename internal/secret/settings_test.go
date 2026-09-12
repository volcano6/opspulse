package secret

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), SettingsFileName)

	want := Settings{Vault: "Personal", Account: "example.1password.com"}
	if err := want.SaveTo(path); err != nil {
		t.Fatalf("SaveTo() error: %v", err)
	}

	got, err := LoadSettingsFrom(path)
	if err != nil {
		t.Fatalf("LoadSettingsFrom() error: %v", err)
	}
	if got != want {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}

	// Windows has no POSIX permission bits: os.Chmod only toggles the read-only
	// flag, so the mode reads back as 0666 there.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("settings file mode = %o, want 600", perm)
		}
	}
}

func TestLoadSettingsFromMissingFileIsNotAnError(t *testing.T) {
	got, err := LoadSettingsFrom(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("LoadSettingsFrom() error: %v", err)
	}
	if !got.IsZero() {
		t.Fatalf("got %+v, want zero settings", got)
	}
}

func TestLoadSettingsFromTrimsWhitespace(t *testing.T) {
	path := filepath.Join(t.TempDir(), SettingsFileName)
	body := "vault: \"  Personal  \"\naccount: \" acme.1password.com \"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	got, err := LoadSettingsFrom(path)
	if err != nil {
		t.Fatalf("LoadSettingsFrom() error: %v", err)
	}
	if got.Vault != "Personal" || got.Account != "acme.1password.com" {
		t.Fatalf("got %+v, want trimmed values", got)
	}
}

func TestLoadSettingsFromMalformedFileReportsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), SettingsFileName)
	if err := os.WriteFile(path, []byte("vault: [oops\n"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// A corrupt file must be surfaced rather than silently discarded, otherwise
	// the user's configuration would appear to vanish.
	if _, err := LoadSettingsFrom(path); err == nil {
		t.Fatal("expected an error for malformed YAML")
	}
}

func TestSaveEmptySettingsRemovesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), SettingsFileName)
	if err := (Settings{Vault: "Personal"}).SaveTo(path); err != nil {
		t.Fatalf("SaveTo() error: %v", err)
	}
	if err := (Settings{}).SaveTo(path); err != nil {
		t.Fatalf("SaveTo() on empty settings error: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("clearing the settings should remove %s, stat err = %v", path, err)
	}
}

func TestWithAccountFallback(t *testing.T) {
	base := CLI{Path: "/usr/bin/op"}

	t.Run("applies when OP_ACCOUNT is unset", func(t *testing.T) {
		t.Setenv("OP_ACCOUNT", "")
		got := base.WithAccountFallback("example.1password.com")
		if len(got.Env) != 1 || got.Env[0] != "OP_ACCOUNT=example.1password.com" {
			t.Fatalf("Env = %v, want [OP_ACCOUNT=example.1password.com]", got.Env)
		}
	})

	t.Run("an exported OP_ACCOUNT wins", func(t *testing.T) {
		t.Setenv("OP_ACCOUNT", "acme.1password.com")
		if got := base.WithAccountFallback("example.1password.com"); len(got.Env) != 0 {
			t.Fatalf("Env = %v, want the environment to take precedence", got.Env)
		}
	})

	t.Run("empty account is a no-op", func(t *testing.T) {
		t.Setenv("OP_ACCOUNT", "")
		if got := base.WithAccountFallback(""); len(got.Env) != 0 {
			t.Fatalf("Env = %v, want no change", got.Env)
		}
	})
}

func TestSettingsPathSitsInTheConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OPSPULSE_HOME", dir)

	got := SettingsPath()
	if filepath.Dir(got) != dir {
		t.Fatalf("SettingsPath() = %q, want it inside %q", got, dir)
	}
	if !strings.HasSuffix(got, SettingsFileName) {
		t.Fatalf("SettingsPath() = %q, want it to end in %q", got, SettingsFileName)
	}
}
