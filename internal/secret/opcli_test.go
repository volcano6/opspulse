package secret

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/volcano6/opspulse/internal/platform"
)

// TestDetectPrefersWingetWindowsCLIInWSL is the regression guard for the real
// failure mode: a Windows CLI installed by winget that never made it onto PATH,
// leaving WSL to silently pick a Linux `op` that can never reach the Desktop App.
func TestDetectPrefersWingetWindowsCLIInWSL(t *testing.T) {
	if !platform.IsWSL() {
		t.Skip("only meaningful inside WSL")
	}
	local, err := platform.WindowsLocalAppData()
	if err != nil {
		t.Skipf("cannot resolve the Windows LOCALAPPDATA: %v", err)
	}
	if _, ok := findWingetCLIIn(local); !ok {
		t.Skip("no winget-installed 1Password CLI on this host")
	}

	cli := Detect()
	if !cli.Available() {
		t.Fatal("Detect() found no CLI even though the Windows build is installed")
	}
	if !cli.IsWindowsBinary {
		t.Fatalf("Detect() chose %q; inside WSL the Windows build must win", cli.Path)
	}
	if !strings.HasSuffix(strings.ToLower(cli.Path), "op.exe") {
		t.Fatalf("Detect().Path = %q, want an op.exe", cli.Path)
	}
}

// TestFindWingetCLIIn covers the fallback that keeps WSL users from silently
// falling back to a Linux `op`: winget often leaves the Links shim out, so the
// binary only exists inside the package directory.
func TestFindWingetCLIIn(t *testing.T) {
	t.Run("package directory", func(t *testing.T) {
		local := t.TempDir()
		pkg := filepath.Join(local, "Microsoft", "WinGet", "Packages", "AgileBits.1Password.CLI_Microsoft.Winget.Source_8wekyb3d8bbwe")
		if err := os.MkdirAll(pkg, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		want := filepath.Join(pkg, "op.exe")
		if err := os.WriteFile(want, []byte("stub"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}

		got, ok := findWingetCLIIn(local)
		if !ok {
			t.Fatalf("findWingetCLIIn(%q) = not found, want %q", local, want)
		}
		if got != want {
			t.Fatalf("findWingetCLIIn(%q) = %q, want %q", local, got, want)
		}
	})

	t.Run("links shim", func(t *testing.T) {
		local := t.TempDir()
		links := filepath.Join(local, "Microsoft", "WinGet", "Links")
		if err := os.MkdirAll(links, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		want := filepath.Join(links, "op.exe")
		if err := os.WriteFile(want, []byte("stub"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}

		got, ok := findWingetCLIIn(local)
		if !ok || got != want {
			t.Fatalf("findWingetCLIIn(%q) = (%q, %v), want (%q, true)", local, got, ok, want)
		}
	})

	t.Run("missing", func(t *testing.T) {
		if got, ok := findWingetCLIIn(t.TempDir()); ok {
			t.Fatalf("findWingetCLIIn(empty dir) = %q, want not found", got)
		}
		if _, ok := findWingetCLIIn(""); ok {
			t.Fatal("findWingetCLIIn(\"\") reported a hit")
		}
	})
}

// TestDetectInWSLDoesNotFallBackToLinuxOpBeforeWindowsPaths is the regression
// guard for the ordering fix. With the Windows CLI hidden from the WSL PATH but
// still reachable through the Windows-side probes, a Linux `op` sitting on PATH
// must not be chosen: it has no Desktop App session, so every real operation
// stops at an interactive "Do you want to add an account manually now? [Y/n]"
// prompt, which reads as an unexplained password prompt.
func TestDetectInWSLDoesNotFallBackToLinuxOpBeforeWindowsPaths(t *testing.T) {
	if !platform.IsWSL() {
		t.Skip("only meaningful inside WSL")
	}
	if _, ok := locateWindowsExecutable("op.exe"); !ok {
		t.Skip("no Windows op.exe reachable from this host")
	}

	dir := t.TempDir()
	fake := filepath.Join(dir, "op")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake Linux op: %v", err)
	}

	// Drop the WinGet directories so the WSL PATH alone cannot yield an
	// op.exe; the Windows-side probes must still find one.
	kept := []string{dir}
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		if strings.Contains(strings.ToLower(entry), "winget") {
			continue
		}
		kept = append(kept, entry)
	}
	t.Setenv("PATH", strings.Join(kept, string(os.PathListSeparator)))

	cli := Detect()
	if cli.Path == fake {
		t.Fatalf("Detect() chose the Linux op at %q; inside WSL the Windows build must win", fake)
	}
	if !cli.IsWindowsBinary {
		t.Fatalf("Detect() = %q (IsWindowsBinary=false), want a Windows op.exe", cli.Path)
	}
}

// TestWSLWindowsCLIProbesAreWindows pins the invariant that every probe WSL
// consults before falling back to a Linux build yields a Windows op.exe. A
// probe that resolves the bare `op` name would let a sessionless Linux build
// win, which is the bug this ordering exists to prevent.
func TestWSLWindowsCLIProbesAreWindows(t *testing.T) {
	if len(wslWindowsCLIProbes) == 0 {
		t.Fatal("wslWindowsCLIProbes is empty; WSL would fall straight through to a Linux op")
	}
	for i, probe := range wslWindowsCLIProbes {
		p, ok := probe()
		if !ok {
			continue
		}
		if !looksLikeWindowsBinary(p) {
			t.Fatalf("wslWindowsCLIProbes[%d] returned %q, which is not a Windows binary", i, p)
		}
	}
}

// TestWSLBinDirs covers the directories probed for a manually installed CLI.
func TestWSLBinDirs(t *testing.T) {
	dirs := wslBinDirs()
	if len(dirs) == 0 || dirs[0] != "/usr/local/bin" {
		t.Fatalf("wslBinDirs() = %v, want it to start with /usr/local/bin", dirs)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	want := filepath.Join(home, ".local", "bin")
	for _, dir := range dirs {
		if dir == want {
			return
		}
	}
	t.Fatalf("wslBinDirs() = %v, want it to contain %q", dirs, want)
}

// TestWithAccount makes sure the `ops 1p --account` value reaches the CLI
// process as OP_ACCOUNT, and that an empty value is a no-op.
func TestWithAccount(t *testing.T) {
	base := CLI{Path: "/usr/bin/op"}

	if got := base.WithAccount(""); len(got.Env) != 0 {
		t.Fatalf("WithAccount(\"\") added %v, want no change", got.Env)
	}
	if len(base.Env) != 0 {
		t.Fatalf("WithAccount mutated the receiver: %v", base.Env)
	}

	got := base.WithAccount("  acme.1password.com  ")
	if len(got.Env) != 1 || got.Env[0] != "OP_ACCOUNT=acme.1password.com" {
		t.Fatalf("WithAccount env = %v, want [OP_ACCOUNT=acme.1password.com]", got.Env)
	}

	cmd := got.Exec(context.Background(), "vault", "list")
	if cmd.Env == nil {
		t.Fatal("Exec did not populate Env although the CLI carries extra environment entries")
	}
	found := false
	for _, e := range cmd.Env {
		if e == "OP_ACCOUNT=acme.1password.com" {
			found = true
		}
	}
	if !found {
		t.Fatalf("OP_ACCOUNT missing from the spawned command environment")
	}
	if !strings.HasSuffix(cmd.Path, "op") {
		t.Fatalf("spawned path = %q, want it to end in op", cmd.Path)
	}
}

// TestDetectHonoursOverride documents the OPSPULSE_OP_PATH escape hatch, which
// is what a user reaches for when auto-detection picks the wrong binary.
func TestDetectHonoursOverride(t *testing.T) {
	t.Setenv("OPSPULSE_OP_PATH", "/opt/custom/op.exe")
	cli := Detect()
	if cli.Path != "/opt/custom/op.exe" {
		t.Fatalf("Detect().Path = %q, want the override path", cli.Path)
	}
	if !cli.IsWindowsBinary {
		t.Fatal("Detect() should treat a forced .exe path as a Windows binary")
	}

	t.Setenv("OPSPULSE_OP_PATH", "/opt/custom/op")
	if cli := Detect(); cli.IsWindowsBinary {
		t.Fatalf("Detect().IsWindowsBinary = true for %q, want false", cli.Path)
	}
}
