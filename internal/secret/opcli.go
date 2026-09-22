package secret

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/volcano6/opspulse/internal/platform"
)

// ErrCLINotFound is returned when the 1Password CLI cannot be located on this host.
var ErrCLINotFound = errors.New("1Password CLI ('op') was not found in PATH")

// InstallHint returns a short, OS specific instruction for installing the
// 1Password CLI.
func InstallHint() string {
	switch {
	case platform.IsWSL():
		return "Install the Windows 1Password CLI (winget install AgileBits.1Password.CLI) and make sure it is reachable from WSL, or install the Linux build and sign in with 'op account add'."
	case runtime.GOOS == "windows":
		return "Install it with: winget install AgileBits.1Password.CLI"
	default:
		return "See https://developer.1password.com/docs/cli/get-started for installation instructions."
	}
}

// CLI describes how the 1Password CLI is reached from this host.
type CLI struct {
	// Path is the executable that should be spawned.
	Path string
	// IsWindowsBinary is true when the executable is a Windows build. This
	// happens on native Windows, and inside WSL when the Windows build is used
	// to reuse the Desktop App's unlock state.
	IsWindowsBinary bool
	// Env holds extra environment entries (for example OP_ACCOUNT) that are
	// applied to every invocation.
	Env []string
}

// WithAccount returns a copy of the CLI that runs against a specific 1Password
// account. The account may be a sign-in address (example.1password.com) or an
// account ID, matching the semantics of the OP_ACCOUNT environment variable.
func (c CLI) WithAccount(account string) CLI {
	account = strings.TrimSpace(account)
	if account == "" {
		return c
	}
	c.Env = append(append([]string{}, c.Env...), "OP_ACCOUNT="+account)
	return c
}

// WithAccountFallback applies a stored account preference, but never overrides
// OP_ACCOUNT: an environment variable set for the current shell is a more
// immediate instruction than a value saved in a config file.
func (c CLI) WithAccountFallback(account string) CLI {
	if strings.TrimSpace(os.Getenv("OP_ACCOUNT")) != "" {
		return c
	}
	return c.WithAccount(account)
}

// Available reports whether a 1Password CLI executable was located.
func (c CLI) Available() bool { return c.Path != "" }

// Exec builds a command that runs the CLI with the given arguments.
func (c CLI) Exec(ctx context.Context, args ...string) *exec.Cmd {
	if ctx == nil {
		ctx = context.Background()
	}
	// #nosec G204 -- c.Path is a resolved executable path; args are built by callers, never interpolated from raw input
	cmd := exec.CommandContext(ctx, c.Path, args...)
	if env := c.childEnv(); env != nil {
		cmd.Env = env
	}
	return cmd
}

// wslInteropEnv is the variable WSL exports so that a Windows binary spawned
// from a Linux process can find the Windows side.
const wslInteropEnv = "WSL_INTEROP"

// childEnv returns the environment for the spawned CLI, or nil to inherit the
// parent's environment untouched.
//
// The only entry it ever removes is a WSL_INTEROP that names a socket no longer
// on disk. WSL exports that variable pointing at the interop socket of the
// session that started the process, so a process that outlives its session
// (tmux, screen, a long-running shell, a `sudo` subshell) can keep a value
// naming a socket nobody is listening on, and a Windows binary spawned through
// a dead socket is the documented cause of the relay giving up with
// "UtilAcceptVsock: accept4 failed 110".
//
// A live socket is left alone. On the WSL builds tested the value is only a
// hint, and a wrong-but-present value is tolerated, so rewriting a working entry
// would trade a good session for nothing.
func (c CLI) childEnv() []string {
	dropInterop := c.IsWindowsBinary && platform.IsWSL() && deadWSLInterop()
	if len(c.Env) == 0 && !dropInterop {
		return nil
	}
	base := os.Environ()
	if dropInterop {
		base = dropEnvEntry(base, wslInteropEnv)
	}
	return append(base, c.Env...)
}

// deadWSLInterop reports whether WSL_INTEROP names an interop socket that is
// gone. A missing variable is not dead: there is nothing stale to drop.
func deadWSLInterop() bool {
	path := strings.TrimSpace(os.Getenv(wslInteropEnv))
	if path == "" {
		return false
	}
	// os.Stat, not platform.FileExists: the interop endpoint is a socket, and
	// FileExists only accepts regular files. Stat also follows the symlink WSL
	// uses for its first session, so a dangling link reads as dead.
	_, err := os.Stat(path) // #nosec G304 -- the path comes from WSL itself, not from user input
	return err != nil
}

// dropEnvEntry returns env without any assignment to key.
func dropEnvEntry(env []string, key string) []string {
	prefix := key + "="
	kept := make([]string, 0, len(env))
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		kept = append(kept, entry)
	}
	return kept
}

// Run executes the CLI and returns its stdout.
func (c CLI) Run(ctx context.Context, args ...string) ([]byte, error) {
	return c.RunWithStdin(ctx, nil, args...)
}

// wslInteropRetryDelays are the pauses before re-spawning after the WSL interop
// relay failed to start the Windows process. The first failure costs a full
// 10s accept window, so by the time the retry runs the other invocations that
// were crowding the relay have usually finished and the retry lands clean.
var wslInteropRetryDelays = []time.Duration{250 * time.Millisecond, time.Second}

// wslInteropFailureMarkers are the fragments the WSL interop relay prints when
// it cannot hand a spawn across to Windows at all, as opposed to op running and
// reporting a failure of its own.
//
// The relay gives up after a fixed accept window and reports errno 110
// (ETIMEDOUT), so the signature is a 10s duration plus one of these strings.
// The race is a function of how many long-lived Windows processes already sit
// in the relay, not of what was asked for: measured from WSL, spawning op.exe
// is clean with two such processes alive and starts timing out at four, which
// is why `ops 1p backup`'s default concurrency is lowered on this path.
//
// A spawn that never reached Windows has done nothing, so repeating it cannot
// double-apply a write. That is what makes a retry safe here, unlike retrying a
// call that ran and failed.
var wslInteropFailureMarkers = []string{
	"UtilAcceptVsock",
	"accept4 failed 110",
}

// WSLInteropFailure reports whether err is the WSL interop relay refusing to
// start the process, rather than the process running and failing on its own.
//
// The markers are emitted by WSL's own interop init, so a match means the
// Windows binary never started. It is deliberately narrow: a false positive
// would re-run an invocation that had already taken effect.
func WSLInteropFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, marker := range wslInteropFailureMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// retryableInteropFailure narrows WSLInteropFailure to the only CLI that can
// hit it. No platform check is needed: these markers come from WSL's interop
// init, so nothing else can produce them.
func (c CLI) retryableInteropFailure(err error) bool {
	return c.IsWindowsBinary && WSLInteropFailure(err)
}

// RunWithStdin executes the CLI, feeding stdin to the child process, and
// returns its stdout.
//
// Piping the payload is not a style choice. `op` refuses to combine a --template
// file with a redirected stdin ("cannot create an item from template and stdin
// at the same time"), and a child process spawned by Go always sees a non-tty
// stdin, so the --template form can never work from OpsPulse. 1Password's own
// documented workaround is to pipe the item JSON instead:
//
//	op item create --vault Vault -
//	cat item.json | op item edit <item>
//
// A side benefit is that the private key never touches the disk on its way to
// 1Password.
//
// A spawn the WSL interop relay refused is retried; see wslInteropFailureMarkers.
func (c CLI) RunWithStdin(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	if !c.Available() {
		return nil, ErrCLINotFound
	}
	out, err := c.runOnce(ctx, stdin, args...)
	for attempt := 0; err != nil && attempt < len(wslInteropRetryDelays) && c.retryableInteropFailure(err); attempt++ {
		if !waitForRetry(ctx, wslInteropRetryDelays[attempt]) {
			break
		}
		out, err = c.runOnce(ctx, stdin, args...)
	}
	return out, err
}

// waitForRetry pauses for d, reporting false if ctx was cancelled first.
func waitForRetry(ctx context.Context, d time.Duration) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// runOnce performs a single op invocation.
func (c CLI) runOnce(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	cmd := c.Exec(ctx, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if len(stdin) > 0 {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if platform.IsWSL() && (errors.Is(err, os.ErrPermission) || strings.Contains(err.Error(), "permission denied")) {
			return nil, fmt.Errorf("1Password CLI at %s cannot be executed (permission denied).\n💡 Fix: Run 'sudo chmod +x %s' or change 'fmask=011' to 'fmask=000' in /etc/wsl.conf: %w", c.Path, c.Path, err)
		}
		if msg == "" {
			// A silent failure is not a sign-in problem. `op` prints a reason
			// whenever it refuses on its own, so an exit with no output at all
			// means it never got to answer -- the shape of an approval prompt
			// nobody responded to, or the WSL interop relay timing out while it
			// waited. Say that, instead of letting the empty stderr render as
			// "(stderr: )".
			return stdout.Bytes(), fmt.Errorf("op %s: %w (no output)", strings.Join(args, " "), err)
		}
		return stdout.Bytes(), fmt.Errorf("op %s: %w (stderr: %s)", strings.Join(args, " "), err, msg)
	}
	return stdout.Bytes(), nil
}

// authFailureMarkers are the fragments `op` prints when it refuses to run
// because no usable account is signed in. Matching is case-insensitive.
//
// The list is deliberately narrow, and a silent failure is absent on purpose:
// see AuthFailure. Every entry is a wording the CLI is known to use, not a
// guess at a pattern, because the cost of widening it is a confident wrong
// answer rather than a missed one.
var authFailureMarkers = []string{
	// Nobody is signed in, in the several wordings the CLI uses.
	"not signed in",
	"not currently signed in",
	"no accounts configured",
	"no account configured",
	// A session that existed and no longer does.
	"session expired",
	"session has expired",
	"invalid access token",
	// The Desktop App integration is off or unreachable, which is exactly what
	// the Desktop App hint tells the user to enable.
	"not authorised",
	"not authorized",
	"connect to the 1password desktop app",
	"desktop app integration",
	// A raw rejection from the API.
	"unauthorized",
	"unauthorised",
}

// AuthFailure reports whether err is the CLI refusing to run because nobody is
// signed in, as opposed to any other reason a call can fail: a timeout, a locked
// Desktop App, an item that does not exist, a vault the account cannot see.
//
// Callers use this to choose between two mutually exclusive remedies, so a false
// positive costs more than a false negative. An unrecognised failure falls
// through to the generic "the CLI did not answer" text, which still tells the
// user to check the approval prompt -- and never tells someone who merely walked
// away from a prompt to go re-check a setting that was already correct.
func AuthFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range authFailureMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// Detect locates the 1Password CLI.
//
// Inside WSL the Windows build (op.exe) is preferred over a Linux build: it
// reuses the Desktop App session, which is what keeps the native biometric
// unlock prompt working. A Linux build in WSL only works with an explicit
// service account token or a manual `op account add`.
//
// The preference is not cosmetic. A Linux `op` under WSL never has a session,
// so every real operation stops at
//
//	No accounts configured for use with 1Password CLI.
//	Do you want to add an account manually now? [Y/n]
//
// which the user experiences as an unexplained password prompt. WSL therefore
// exhausts every Windows avenue before it will consider a Linux build.
//
// Set OPSPULSE_OP_PATH to force a specific executable.
func Detect() CLI {
	if forced := strings.TrimSpace(os.Getenv("OPSPULSE_OP_PATH")); forced != "" {
		return CLI{Path: forced, IsWindowsBinary: looksLikeWindowsBinary(forced)}
	}
	if platform.IsWSL() {
		return detectInWSL()
	}
	return detectNative()
}

// detectInWSL resolves the CLI inside WSL, where only the Windows build can
// reuse the Desktop App's unlock state.
func detectInWSL() CLI {
	for _, probe := range wslWindowsCLIProbes {
		if p, ok := probe(); ok {
			return CLI{Path: p, IsWindowsBinary: true}
		}
	}
	// Nothing Windows-shaped is reachable, so a Linux build is all that is
	// left. It works, but only after `op account add` or with a service
	// account token.
	if cli := detectNative(); cli.Available() {
		return cli
	}
	if p, ok := firstExistingWSLBin("op"); ok {
		return CLI{Path: p, IsWindowsBinary: looksLikeWindowsBinary(p)}
	}
	return CLI{}
}

// wslWindowsCLIProbes are consulted, in order, before WSL will fall back to a
// Linux build. Every entry must yield a Windows op.exe: none of them may look
// up the bare `op` name, because on WSL that resolves to a Linux build which
// can never reach the Desktop App.
var wslWindowsCLIProbes = []func() (string, bool){
	// The Windows CLI is usually already on the WSL PATH via Windows interop.
	func() (string, bool) {
		p, err := exec.LookPath("op.exe")
		return p, err == nil
	},
	// The Windows PATH is not always mirrored into WSL, so ask Windows itself.
	func() (string, bool) { return locateWindowsExecutable("op.exe") },
	// winget does not reliably materialise its Links shim.
	locateWingetCLI,
	// An op.exe copied or symlinked into a WSL bin directory.
	func() (string, bool) { return firstExistingWSLBin("op.exe") },
}

// firstExistingWSLBin looks for name in the bin directories a WSL user is
// likely to have installed or symlinked a 1Password CLI into.
func firstExistingWSLBin(name string) (string, bool) {
	for _, dir := range wslBinDirs() {
		candidate := filepath.Join(dir, name)
		if platform.FileExists(candidate) {
			return candidate, true
		}
	}
	return "", false
}

// wslBinDirs lists the directories searched for a manually installed CLI
// inside WSL.
func wslBinDirs() []string {
	dirs := []string{"/usr/local/bin"}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".local", "bin"))
	}
	return dirs
}

// detectNative locates the CLI on a host where `op` and `op.exe` belong to the
// same platform, so the plain name is tried first.
func detectNative() CLI {
	if p, err := exec.LookPath("op"); err == nil {
		return CLI{Path: p, IsWindowsBinary: looksLikeWindowsBinary(p)}
	}
	if p, err := exec.LookPath("op.exe"); err == nil {
		return CLI{Path: p, IsWindowsBinary: true}
	}
	if p, ok := locateWingetCLI(); ok {
		return CLI{Path: p, IsWindowsBinary: true}
	}
	return CLI{}
}

// locateWingetCLI probes the well-known winget install locations for the
// 1Password CLI.
//
// winget does not reliably materialise the %LOCALAPPDATA%\Microsoft\WinGet\Links
// shim, so a CLI that is installed to the package directory can still be
// invisible to `where op.exe`. Without this fallback a WSL user who installed
// the Windows CLI ends up silently falling back to a Linux `op`, which can
// never talk to the Desktop App.
func locateWingetCLI() (string, bool) {
	local, err := platform.WindowsLocalAppData()
	if err != nil {
		return "", false
	}
	return findWingetCLIIn(local)
}

// findWingetCLIIn looks for the 1Password CLI underneath a Windows
// %LOCALAPPDATA% directory.
func findWingetCLIIn(localAppData string) (string, bool) {
	if localAppData == "" {
		return "", false
	}
	candidates := []string{
		filepath.Join(localAppData, "Microsoft", "WinGet", "Links", "op.exe"),
	}
	if matches, globErr := filepath.Glob(filepath.Join(localAppData, "Microsoft", "WinGet", "Packages", "AgileBits.1Password.CLI_*", "op.exe")); globErr == nil {
		candidates = append(candidates, matches...)
	}
	for _, candidate := range candidates {
		if platform.FileExists(candidate) {
			if platform.IsWSL() {
				_ = os.Chmod(candidate, 0o755) // #nosec G302 -- the Windows op.exe shim must stay executable across the WSL boundary
			}
			return candidate, true
		}
	}
	return "", false
}

func looksLikeWindowsBinary(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".exe") || strings.HasPrefix(path, "/mnt/")
}

// locateWindowsExecutable resolves a Windows executable that is not exposed on
// the WSL PATH. cmd.exe is only used to *locate* the binary; it is never used to
// run it, because routing arguments through cmd.exe corrupts values that contain
// newlines or other shell metacharacters.
func locateWindowsExecutable(name string) (string, bool) {
	out, err := exec.Command("cmd.exe", "/c", "where "+name).Output() // #nosec G204 -- name is a fixed literal supplied by callers
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(out), "\n") {
		winPath := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if winPath == "" {
			continue
		}
		if candidate := resolveWindowsPath(winPath); platform.FileExists(candidate) {
			return candidate, true
		}
	}
	return "", false
}

// resolveWindowsPath converts a native Windows path into the path WSL should use
// to execute it, preferring the canonical wslpath translation when available.
func resolveWindowsPath(winPath string) string {
	if out, err := exec.Command("wslpath", "-u", winPath).Output(); err == nil { // #nosec G204
		if p := strings.TrimSpace(string(out)); p != "" {
			return p
		}
	}
	return platform.ToWSLPath(winPath)
}
