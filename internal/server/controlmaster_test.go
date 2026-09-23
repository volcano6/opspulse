package server

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// setControlMasterHome points the socket directory at a throwaway home, so the
// tests neither read nor write the developer's real ~/.ssh.
func setControlMasterHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")
	return home
}

func TestControlMasterArgsRequireAPreparedDirectory(t *testing.T) {
	if !ControlMasterEnabled() {
		t.Skip("connection multiplexing is disabled on this platform")
	}
	home := setControlMasterHome(t)

	// Nothing has created the directory yet. Handing ssh a ControlPath it
	// cannot bind is fatal to the connection, so the flags must be withheld.
	if got := ControlMasterArgs(); got != nil {
		t.Fatalf("ControlMasterArgs() = %v before the directory exists, want nil", got)
	}
	if got := ControlMasterConfigLines(); got != nil {
		t.Fatalf("ControlMasterConfigLines() = %v before the directory exists, want nil", got)
	}

	if err := EnsureControlMasterDir(); err != nil {
		t.Fatalf("EnsureControlMasterDir() error: %v", err)
	}

	wantPath := filepath.ToSlash(filepath.Join(home, ".ssh", ControlMasterDirName, "%r@%h:%p"))
	want := []string{
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + wantPath,
		"-o", "ControlPersist=" + ControlPersist,
	}
	got := ControlMasterArgs()
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("ControlMasterArgs() =\n%v\nwant:\n%v", got, want)
	}
}

func TestControlMasterConfigLinesMirrorTheArgs(t *testing.T) {
	if !ControlMasterEnabled() {
		t.Skip("connection multiplexing is disabled on this platform")
	}
	home := setControlMasterHome(t)
	if err := EnsureControlMasterDir(); err != nil {
		t.Fatalf("EnsureControlMasterDir() error: %v", err)
	}

	lines := ControlMasterConfigLines()
	if len(lines) != 3 {
		t.Fatalf("ControlMasterConfigLines() = %v, want 3 lines", lines)
	}

	// The two spellings must agree on the socket, otherwise 'ops ssh' and a
	// plain 'ssh <name>' using the exported config would authenticate twice
	// instead of sharing one session.
	args := ControlMasterArgs()
	argPath := ""
	for _, a := range args {
		if strings.HasPrefix(a, "ControlPath=") {
			argPath = strings.TrimPrefix(a, "ControlPath=")
		}
	}
	if argPath == "" {
		t.Fatalf("ControlMasterArgs() = %v, want a ControlPath", args)
	}
	if want := "    ControlPath " + argPath; lines[1] != want {
		t.Errorf("config line = %q, want %q", lines[1], want)
	}
	if want := filepath.ToSlash(filepath.Join(home, ".ssh", ControlMasterDirName)); !strings.Contains(argPath, want) {
		t.Errorf("ControlPath %q does not sit under %q", argPath, want)
	}
}

func TestEnsureControlMasterDirIsPrivate(t *testing.T) {
	if !ControlMasterEnabled() {
		t.Skip("connection multiplexing is disabled on this platform")
	}
	setControlMasterHome(t)
	if err := EnsureControlMasterDir(); err != nil {
		t.Fatalf("EnsureControlMasterDir() error: %v", err)
	}

	dir, err := ControlMasterDir()
	if err != nil {
		t.Fatalf("ControlMasterDir() error: %v", err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		t.Errorf("socket directory mode = %o, want 700", perm)
	}

	// Idempotent: a second call must not fail on the existing directory.
	if err := EnsureControlMasterDir(); err != nil {
		t.Errorf("second EnsureControlMasterDir() error: %v", err)
	}
}

func TestControlMasterPathUsesPerTargetTokens(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("path separators differ on Windows")
	}
	setControlMasterHome(t)
	path, err := ControlMasterPath()
	if err != nil {
		t.Fatalf("ControlMasterPath() error: %v", err)
	}
	// One socket per user@host:port, so a second port on the same host does not
	// silently reuse the first port's session.
	for _, token := range []string{"%r", "%h", "%p"} {
		if !strings.Contains(path, token) {
			t.Errorf("ControlPath %q is missing the %s token", path, token)
		}
	}
}
