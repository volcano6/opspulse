package secret

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// TestAuthFailureClassifiesOnlyGenuineSignInFailures pins the distinction the
// hints depend on. A false positive is the expensive direction: it tells a user
// whose CLI never answered to go change a sign-in setting that is already right.
func TestAuthFailureClassifiesOnlyGenuineSignInFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "no error", err: nil, want: false},
		{
			name: "not signed in",
			err:  errors.New("op vault list: exit status 1 (stderr: [ERROR] You are not currently signed in.)"),
			want: true,
		},
		{
			name: "no accounts configured",
			err:  errors.New("op vault list: exit status 1 (stderr: No accounts configured for use with 1Password CLI.)"),
			want: true,
		},
		{
			name: "expired session",
			err:  errors.New("op vault list: exit status 1 (stderr: your session has expired)"),
			want: true,
		},
		{
			// The shape of a CLI blocked on an unanswered approval prompt: it
			// exits without ever printing a reason.
			name: "silent failure",
			err:  errors.New("op vault list --format json: exit status 1 (no output)"),
			want: false,
		},
		{
			// The same stall with the WSL relay's own message on stderr. It
			// names no sign-in problem, so it must not be classified as one.
			name: "WSL interop relay timeout",
			err:  errors.New("op vault list --format json: exit status 1 (stderr: <3>WSL ERROR: UtilAcceptVsock:273: accept4 failed 110)"),
			want: false,
		},
		{
			name: "deadline exceeded",
			err:  context.DeadlineExceeded,
			want: false,
		},
		{
			name: "missing item",
			err:  errors.New(`op read: exit status 1 (stderr: "opspulse_web_key" isn't an item in vault Personal)`),
			want: false,
		},
		{
			name: "CLI not found",
			err:  ErrCLINotFound,
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AuthFailure(tt.err); got != tt.want {
				t.Errorf("AuthFailure(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestExecDropsOnlyADeadWSLInterop covers the WSL interop hardening: a Windows
// CLI must not be handed a WSL_INTEROP naming a socket that is gone, while a
// live value is passed through untouched.
func TestExecDropsOnlyADeadWSLInterop(t *testing.T) {
	t.Setenv("WSL_DISTRO_NAME", "Ubuntu")
	if !platform.IsWSL() {
		t.Skip("the interop variable only exists inside WSL")
	}

	live := filepath.Join(t.TempDir(), "1_interop")
	if err := os.WriteFile(live, nil, 0o600); err != nil {
		t.Fatalf("write live interop stand-in: %v", err)
	}
	dead := filepath.Join(t.TempDir(), "99999_interop")

	windowsCLI := CLI{Path: "/mnt/c/tools/op.exe", IsWindowsBinary: true}
	linuxCLI := CLI{Path: "/usr/bin/op"}

	tests := []struct {
		name       string
		cli        CLI
		interop    string
		wantEntry  bool
		wantDetail string
	}{
		{
			name: "dead socket is dropped", cli: windowsCLI, interop: dead, wantEntry: false,
			wantDetail: "a Windows binary spawned through a dead socket stalls in the WSL relay",
		},
		{
			name: "live socket is passed through", cli: windowsCLI, interop: live, wantEntry: true,
			wantDetail: "rewriting a working entry would trade a good session for nothing",
		},
		{
			name: "linux CLI never touches it", cli: linuxCLI, interop: dead, wantEntry: true,
			wantDetail: "WSL_INTEROP means nothing to a Linux binary",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("WSL_INTEROP", tt.interop)
			cmd := tt.cli.Exec(context.Background(), "vault", "list")
			if got := hasEnvEntry(effectiveEnv(cmd), "WSL_INTEROP"); got != tt.wantEntry {
				t.Fatalf("the child would see WSL_INTEROP = %v, want %v (%s)", got, tt.wantEntry, tt.wantDetail)
			}
		})
	}
}

// TestDeadWSLInterop pins what counts as stale. An unset or empty variable is
// not stale: there is nothing to drop, and dropping nothing is what keeps the
// parent environment inherited verbatim.
func TestDeadWSLInterop(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		t.Setenv("WSL_INTEROP", "")
		if deadWSLInterop() {
			t.Error("an empty WSL_INTEROP must not be reported as dead")
		}
	})

	t.Run("points at nothing", func(t *testing.T) {
		t.Setenv("WSL_INTEROP", filepath.Join(t.TempDir(), "99999_interop"))
		if !deadWSLInterop() {
			t.Error("a WSL_INTEROP naming a missing socket must be reported as dead")
		}
	})

	t.Run("points at a live endpoint", func(t *testing.T) {
		live := filepath.Join(t.TempDir(), "1_interop")
		if err := os.WriteFile(live, nil, 0o600); err != nil {
			t.Fatalf("write live interop stand-in: %v", err)
		}
		t.Setenv("WSL_INTEROP", live)
		if deadWSLInterop() {
			t.Error("a WSL_INTEROP naming an existing socket must not be reported as dead")
		}
	})

	t.Run("dangling symlink", func(t *testing.T) {
		dir := t.TempDir()
		link := filepath.Join(dir, "1_interop")
		if err := os.Symlink(filepath.Join(dir, "gone"), link); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		t.Setenv("WSL_INTEROP", link)
		if !deadWSLInterop() {
			t.Error("a WSL_INTEROP pointing through a dangling symlink must be reported as dead")
		}
	})
}

// TestExecKeepsExtraEnvWhileCleaningInterop guards the combination: dropping a
// dead WSL_INTEROP must not also drop the OP_ACCOUNT the CLI carries.
func TestExecKeepsExtraEnvWhileCleaningInterop(t *testing.T) {
	t.Setenv("WSL_DISTRO_NAME", "Ubuntu")
	if !platform.IsWSL() {
		t.Skip("the interop variable only exists inside WSL")
	}
	t.Setenv("WSL_INTEROP", filepath.Join(t.TempDir(), "99999_interop"))

	cli := CLI{Path: "/mnt/c/tools/op.exe", IsWindowsBinary: true}.WithAccount("acme.1password.com")
	cmd := cli.Exec(context.Background(), "vault", "list")
	env := effectiveEnv(cmd)
	if hasEnvEntry(env, "WSL_INTEROP") {
		t.Error("the dead WSL_INTEROP survived")
	}
	if !hasEnvEntry(env, "OP_ACCOUNT") {
		t.Error("OP_ACCOUNT was dropped along with the dead WSL_INTEROP")
	}
}

// TestWSLInteropFailure pins the classification the retry depends on. The
// markers come from WSL's interop init, so a match means the Windows process
// never started and the invocation had no effect. Widening this would re-run
// calls that already took effect, so every non-marker shape must stay false.
func TestWSLInteropFailure(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{
			name: "relay gave up",
			err:  errors.New("op vault list --format json: exit status 1 (stderr: <3>WSL (108666 - ) ERROR: UtilAcceptVsock:273: accept4 failed 110)"),
			want: true,
		},
		{
			name: "bare errno 110",
			err:  errors.New("accept4 failed 110"),
			want: true,
		},
		{
			name: "op ran and refused",
			err:  errors.New(`op read: exit status 1 (stderr: "opspulse_web_key" isn't an item in vault Personal)`),
			want: false,
		},
		{
			name: "not signed in",
			err:  errors.New("op vault list: exit status 1 (stderr: [ERROR] You are not currently signed in.)"),
			want: false,
		},
		{name: "deadline exceeded", err: context.DeadlineExceeded, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WSLInteropFailure(tt.err); got != tt.want {
				t.Errorf("WSLInteropFailure(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestRunWithStdinRetriesARefusedWSLSpawn covers the recovery the four-wide
// batch needed: the interop relay refuses a spawn, and the same invocation is
// repeated instead of losing that server from the backup.
func TestRunWithStdinRetriesARefusedWSLSpawn(t *testing.T) {
	script, _ := writeStubCLI(t, `
if [ -f "$FLAG" ]; then
  echo ok
  exit 0
fi
: >"$FLAG"
echo '<3>WSL (1 - ) ERROR: UtilAcceptVsock:273: accept4 failed 110' >&2
exit 1
`)

	cli := CLI{Path: script, IsWindowsBinary: true}
	out, err := cli.Run(context.Background(), "vault", "list")
	if err != nil {
		t.Fatalf("Run() = %v, want the refused spawn to be retried to success", err)
	}
	if got := strings.TrimSpace(string(out)); got != "ok" {
		t.Fatalf("Run() stdout = %q, want %q", got, "ok")
	}
}

// TestRunWithStdinGivesUpAfterTheRetryBudget keeps the retry bounded. A relay
// that keeps refusing must surface the failure rather than loop forever.
func TestRunWithStdinGivesUpAfterTheRetryBudget(t *testing.T) {
	script, stateDir := writeStubCLI(t, `
echo x >>"$COUNTS"
echo '<3>WSL (1 - ) ERROR: UtilAcceptVsock:273: accept4 failed 110' >&2
exit 1
`)
	counts := filepath.Join(stateDir, "counts")

	cli := CLI{Path: script, IsWindowsBinary: true}
	_, err := cli.Run(context.Background(), "vault", "list")
	if err == nil {
		t.Fatal("Run() = nil, want the relay failure to surface once the retries are spent")
	}
	if !WSLInteropFailure(err) {
		t.Fatalf("Run() error = %v, want the relay message preserved", err)
	}

	raw, readErr := os.ReadFile(counts)
	if readErr != nil {
		t.Fatalf("read call log: %v", readErr)
	}
	got := strings.Count(string(raw), "x")
	if want := 1 + len(wslInteropRetryDelays); got != want {
		t.Fatalf("the CLI ran %d time(s), want %d (one attempt plus the retry budget)", got, want)
	}
}

// TestRunWithStdinDoesNotRetryAnOrdinaryFailure is the guard against the retry
// turning into a general-purpose re-run: an op that ran and failed has already
// had its say, and repeating it could double-apply a write.
func TestRunWithStdinDoesNotRetryAnOrdinaryFailure(t *testing.T) {
	script, stateDir := writeStubCLI(t, `
echo x >>"$COUNTS"
echo "isn't an item in vault Personal" >&2
exit 1
`)
	counts := filepath.Join(stateDir, "counts")

	tests := []struct {
		name string
		cli  CLI
	}{
		{name: "windows build", cli: CLI{Path: script, IsWindowsBinary: true}},
		{name: "linux build", cli: CLI{Path: script}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.Remove(counts); err != nil && !os.IsNotExist(err) {
				t.Fatalf("reset call log: %v", err)
			}
			if _, err := tt.cli.Run(context.Background(), "vault", "list"); err == nil {
				t.Fatal("Run() = nil, want the CLI's own failure")
			}
			raw, readErr := os.ReadFile(counts)
			if readErr != nil {
				t.Fatalf("read call log: %v", readErr)
			}
			if got := strings.Count(string(raw), "x"); got != 1 {
				t.Fatalf("the CLI ran %d time(s), want exactly 1", got)
			}
		})
	}
}

// writeStubCLI writes an executable shell stand-in for op and returns its path
// plus the directory holding its state files. The body reads that state through
// $FLAG and $COUNTS, so it stays a literal the test can read at a glance while
// still behaving differently on the first and later attempts.
func writeStubCLI(t *testing.T, body string) (scriptPath, stateDir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in CLI is a shell script")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "op")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil { // #nosec G306 -- the stub must be executable
		t.Fatalf("write stub CLI: %v", err)
	}
	// The stub reads its state files from the environment so the script body
	// stays a literal, and t.Setenv keeps the cleanup automatic.
	t.Setenv("FLAG", filepath.Join(dir, "touched"))
	t.Setenv("COUNTS", filepath.Join(dir, "counts"))
	return path, dir
}

// effectiveEnv returns the environment the spawned child will actually see.
// A nil cmd.Env means the parent's environment is inherited verbatim, so the
// two are equivalent from the child's point of view.
func effectiveEnv(cmd *exec.Cmd) []string {
	if cmd.Env != nil {
		return cmd.Env
	}
	return os.Environ()
}

// TestDropEnvEntryOnlyMatchesWholeKeys pins the prefix boundary: dropping
// WSL_INTEROP must not take WSL_INTEROP_SOMETHING with it.
func TestDropEnvEntryOnlyMatchesWholeKeys(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"WSL_INTEROP=/run/WSL/1_interop",
		"WSL_INTEROP_EXTRA=keep",
		"WSLENV=WSL_INTEROP/u",
	}
	got := dropEnvEntry(env, "WSL_INTEROP")
	want := []string{"PATH=/usr/bin", "WSL_INTEROP_EXTRA=keep", "WSLENV=WSL_INTEROP/u"}
	if len(got) != len(want) {
		t.Fatalf("dropEnvEntry() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dropEnvEntry() = %v, want %v", got, want)
		}
	}
}

// hasEnvEntry reports whether env contains an assignment to key.
func hasEnvEntry(env []string, key string) bool {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return true
		}
	}
	return false
}
