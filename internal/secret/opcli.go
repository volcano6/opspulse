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
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	return cmd
}

// Run executes the CLI and returns its stdout.
func (c CLI) Run(ctx context.Context, args ...string) ([]byte, error) {
	return c.RunWithStdin(ctx, nil, args...)
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
func (c CLI) RunWithStdin(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	if !c.Available() {
		return nil, ErrCLINotFound
	}
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
		return stdout.Bytes(), fmt.Errorf("op %s: %w (stderr: %s)", strings.Join(args, " "), err, msg)
	}
	return stdout.Bytes(), nil
}

// writeSecretTemp writes data to a freshly created 0600 file inside dir (the
// system temp directory when dir is empty) and returns its path plus a cleanup
// function.
func writeSecretTemp(dir, pattern string, data []byte) (string, func(), error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", func() {}, fmt.Errorf("create temporary file: %w", err)
	}
	name := f.Name()
	cleanup := func() { _ = os.Remove(name) }

	// CreateTemp already uses 0600, but make the intent explicit: this file may
	// hold a private key.
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("secure temporary file: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("write temporary file: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("close temporary file: %w", err)
	}

	return name, cleanup, nil
}

// Detect locates the 1Password CLI.
//
// Inside WSL the Windows build (op.exe) is preferred over a Linux build: it
// reuses the Desktop App session, which is what keeps the native biometric
// unlock prompt working. A Linux build in WSL only works with an explicit
// service account token or a manual `op account add`.
//
// Set OPSPULSE_OP_PATH to force a specific executable.
func Detect() CLI {
	if forced := strings.TrimSpace(os.Getenv("OPSPULSE_OP_PATH")); forced != "" {
		return CLI{Path: forced, IsWindowsBinary: looksLikeWindowsBinary(forced)}
	}
	if platform.IsWSL() {
		if p, err := exec.LookPath("op.exe"); err == nil {
			return CLI{Path: p, IsWindowsBinary: true}
		}
		if p, ok := locateWindowsExecutable("op.exe"); ok {
			return CLI{Path: p, IsWindowsBinary: true}
		}
		if p, ok := locateWingetCLI(); ok {
			return CLI{Path: p, IsWindowsBinary: true}
		}
	}
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
