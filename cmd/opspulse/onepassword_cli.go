package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/volcano6/opspulse/internal/platform"
	"github.com/volcano6/opspulse/internal/secret"
)

// The 1Password CLI itself: finding it, explaining a call that was refused or
// never came back, installing it, and describing the build in use.

// onePasswordCLIVersion is the Linux build OpsPulse downloads when the user
// asks for an automatic installation. WSL users normally use the Windows build
// instead, which shares the Desktop App's unlock state.
const onePasswordCLIVersion = "2.30.0"

// onePasswordDesktopHint explains the two supported ways of authenticating the CLI.
const onePasswordDesktopHint = `1Password CLI is installed but not authorised. To let it reuse the Desktop App
session (and therefore get the native Windows Hello / Touch ID prompt):

  1. Open the 1Password Desktop App
  2. Go to Settings -> Developer
  3. Enable "Integrate with 1Password CLI"

Alternatively sign in from the terminal with: op account add`

// onePasswordLinuxInWSLHint is shown when the only CLI available is a Linux
// build running inside WSL. That combination can never use the Desktop App
// integration, so pointing the user at the "Integrate with 1Password CLI"
// toggle would send them down a dead end.
const onePasswordLinuxInWSLHint = `The 1Password CLI OpsPulse found is a Linux build running inside WSL, and it is
not signed in.

Note: the Desktop App integration ("Integrate with 1Password CLI") only works
with the Windows build of the CLI. A Linux op under WSL cannot reach the Windows
Desktop App, so that toggle has no effect here.

Install the Windows build once, from Windows PowerShell:

  winget install AgileBits.1Password.CLI

OpsPulse picks up op.exe automatically once it is installed. To stay on the Linux
build instead, sign in manually with: op account add`

// onePasswordStalledHint is shown when the CLI failed for a reason that is not a
// sign-in problem. The auth hints above would send the user to re-check a setting
// that is already correct, which is the misdiagnosis this text exists to prevent.
//
// The usual cause is an approval prompt nobody answered: `op` blocks until the
// Desktop App responds, and the call then dies on a timeout. In WSL that timeout
// is the interop relay giving up with "UtilAcceptVsock: accept4 failed 110". The
// relay's message reaches OpsPulse as stderr on some runs and not at all on
// others, so this text deliberately does not claim the CLI was silent.
const onePasswordStalledHint = `The 1Password CLI did not complete this call, and what it reported does not
point at a sign-in problem -- so re-checking your sign-in settings would not help.

The usual cause is an approval prompt waiting in the 1Password Desktop App, or a
locked app: 'op' blocks until someone answers it, and the call then dies on a
timeout.

  1. Open the 1Password Desktop App and answer any pending prompt
  2. Make sure it is unlocked, then run the command again

Inside WSL the Windows build reaches the Desktop App through the Windows/WSL
relay, which gives up with "UtilAcceptVsock: accept4 failed 110" when the wait
runs long. That message is this same timeout, not a separate fault.`

// onePasswordAuthHint picks the right remediation text for the CLI in use.
func onePasswordAuthHint(cli secret.CLI) string {
	if platform.IsWSL() && !cli.IsWindowsBinary {
		return onePasswordLinuxInWSLHint
	}
	return onePasswordDesktopHint
}

// onePasswordFailureHint picks the remediation text for a failed CLI call.
//
// The two cases call for opposite reactions, so they must not share one message:
// a real sign-in problem is fixed by enabling the Desktop App integration, while
// a stalled call is fixed by answering the prompt that is already on screen.
func onePasswordFailureHint(cli secret.CLI, err error) string {
	if secret.AuthFailure(err) {
		return onePasswordAuthHint(cli)
	}
	return onePasswordStalledHint
}

// printOnePasswordCLIDetails names the binary OpsPulse actually drives, and how
// it was built.
//
// A shell can easily resolve a different op than OpsPulse does: on WSL the
// Linux build is usually first on PATH while OpsPulse prefers the Windows build
// that shares the Desktop App's unlock state. The symptom is an 'op item get'
// that fails with "No accounts configured" in the shell while 'ops 1p' works
// perfectly. Printing the path and the build side by side is what turns that
// discrepancy into something the user can act on.
func printOnePasswordCLIDetails(cli secret.CLI) {
	fmt.Println("\n1Password CLI driven by OpsPulse:")
	fmt.Printf("  path  : %s\n", cli.Path)
	fmt.Printf("  build : %s\n", onePasswordCLIBuild(cli))
	if platform.IsWSL() {
		fmt.Println("  host  : WSL")
	}
}

// onePasswordCLIBuild describes the binary in terms of what it can talk to,
// which is the part that actually explains an authentication failure.
func onePasswordCLIBuild(cli secret.CLI) string {
	switch {
	case cli.IsWindowsBinary:
		return "Windows — shares the 1Password Desktop App's session"
	case platform.IsWSL():
		return "Linux — cannot reach the 1Password Desktop App from WSL; install the Windows build instead"
	default:
		return "native"
	}
}

func settingsSummary(settings secret.Settings) string {
	parts := make([]string, 0, 2)
	if settings.Vault != "" {
		parts = append(parts, "vault "+settings.Vault)
	}
	if settings.Account != "" {
		parts = append(parts, "account "+settings.Account)
	}
	return strings.Join(parts, ", ")
}

func ensure1PCLI() (secret.CLI, error) {
	cli := secret.Detect()
	if cli.Available() {
		return applyOnePasswordAccount(cli), nil
	}

	fmt.Println("⚠️  1Password CLI ('op') was not found on this host.")
	fmt.Printf("💡 %s\n", secret.InstallHint())

	if platform.IsWSL() {
		if hint := platform.WSLFMaskHint(); hint != "" {
			fmt.Println(hint)
		}
	}

	if !stdinIsInteractive() {
		return cli, fmt.Errorf("1Password CLI is required (install it, then re-run this command in an interactive terminal)")
	}
	fmt.Print("\nTry to install it automatically now? [Y/n]: ")
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return cli, fmt.Errorf("1Password CLI is required")
	}
	answer := strings.TrimSpace(scanner.Text())
	if answer != "" && !strings.EqualFold(answer, "y") && !strings.EqualFold(answer, "yes") {
		return cli, fmt.Errorf("1Password CLI is required")
	}
	if err := install1PCLI(); err != nil {
		return cli, fmt.Errorf("failed to install the 1Password CLI: %w\nInstall it manually and re-run: op --version", err)
	}

	cli = secret.Detect()
	if !cli.Available() {
		return cli, fmt.Errorf("'op' was installed but is not visible on PATH yet; restart your terminal (or reload your shell profile) and re-run")
	}
	fmt.Println("✅ 1Password CLI installed successfully.")
	return applyOnePasswordAccount(cli), nil
}

// applyOnePasswordAccount binds the CLI to the account that should be used.
//
// An explicit --account wins and is remembered; otherwise the stored preference
// applies, unless the user exported OP_ACCOUNT, which is treated as the more
// immediate instruction.
func applyOnePasswordAccount(cli secret.CLI) secret.CLI {
	if explicit := strings.TrimSpace(onePasswordAccount); explicit != "" {
		rememberOnePasswordSetting("account", explicit)
		return cli.WithAccount(explicit)
	}
	settings, err := secret.LoadSettings()
	if err != nil {
		return cli
	}
	return cli.WithAccountFallback(settings.Account)
}

// onePasswordResolver builds a resolver that honours both the remembered account
// and an explicit --account flag.
func onePasswordResolver() *secret.Resolver {
	resolver := secret.NewResolver()
	if explicit := strings.TrimSpace(onePasswordAccount); explicit != "" {
		resolver = resolver.WithAccount(explicit)
	}
	return resolver
}

func stdinIsInteractive() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func install1PCLI() error {
	// WSL must get the Windows build. Installing the Linux build here is a trap:
	// it can never talk to the 1Password Desktop App, so the integration toggle
	// the docs point at would silently do nothing.
	if platform.IsWSL() {
		return installWindows1PCLIFromWSL()
	}
	switch runtime.GOOS {
	case "windows":
		fmt.Println("Running: winget install AgileBits.1Password.CLI")
		cmd := exec.Command("winget", "install", "AgileBits.1Password.CLI") // #nosec G204 -- fixed command
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	case "darwin":
		fmt.Println("Running: brew install 1password-cli")
		cmd := exec.Command("brew", "install", "1password-cli") // #nosec G204 -- fixed command
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	case "linux":
		fmt.Printf("Downloading the 1Password CLI for Linux (amd64) v%s...\n", onePasswordCLIVersion)
		script := fmt.Sprintf(`
set -e
tmp_dir=$(mktemp -d)
cd "$tmp_dir"
curl -sSO https://cache.agilebits.com/dist/1P/op2/pkg/v%[1]s/op_linux_amd64_v%[1]s.zip
unzip -q op_linux_amd64_v%[1]s.zip
mkdir -p ~/.local/bin
rm -f ~/.local/bin/op
cp op ~/.local/bin/
chmod +x ~/.local/bin/op
rm -rf "$tmp_dir"
`, onePasswordCLIVersion)
		cmd := exec.Command("sh", "-c", script) // #nosec G204 -- version is a package level constant
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}

		home, _ := os.UserHomeDir()
		localBin := filepath.Join(home, ".local", "bin")
		path := os.Getenv("PATH")
		if !strings.Contains(path, localBin) {
			_ = os.Setenv("PATH", localBin+string(os.PathListSeparator)+path)
			fmt.Printf("💡 Added %s to this process's PATH.\n", localBin)
		}
		return nil
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

// installWindows1PCLIFromWSL installs the Windows build of the 1Password CLI by
// driving winget.exe across the WSL boundary.
func installWindows1PCLIFromWSL() error {
	winget := ""
	if p, err := exec.LookPath("winget.exe"); err == nil {
		winget = p
	} else if local, err := platform.WindowsLocalAppData(); err == nil {
		candidate := filepath.Join(local, "Microsoft", "WindowsApps", "winget.exe")
		if platform.FileExists(candidate) {
			winget = candidate
		}
	}
	if winget != "" {
		fmt.Println("Running (Windows build): winget.exe install AgileBits.1Password.CLI")
		cmd := exec.Command(winget, // #nosec G204 -- fixed command, no user input
			"install", "--id", "AgileBits.1Password.CLI", "-e",
			"--accept-source-agreements", "--accept-package-agreements")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	// Fallback to driving winget via powershell.exe or cmd.exe across interop
	var lastErr error
	for _, runner := range []string{"powershell.exe", "cmd.exe"} {
		p, err := exec.LookPath(runner)
		if err != nil {
			continue
		}
		fmt.Printf("Running (Windows build via %s): winget install AgileBits.1Password.CLI\n", runner)
		var cmd *exec.Cmd
		if runner == "powershell.exe" {
			cmd = exec.Command(p, "-NoProfile", "-Command", "winget install AgileBits.1Password.CLI -e --accept-source-agreements --accept-package-agreements")
		} else {
			cmd = exec.Command(p, "/c", "winget install AgileBits.1Password.CLI -e --accept-source-agreements --accept-package-agreements")
		}
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		runErr := cmd.Run()
		if runErr == nil {
			return nil
		}
		lastErr = fmt.Errorf("%s: %w", runner, runErr)
	}

	fmt.Println("OpsPulse could not reach winget from WSL.")
	fmt.Println("Install the Windows build from Windows PowerShell instead:")
	fmt.Println("  winget install AgileBits.1Password.CLI")
	if hint := platform.WSLFMaskHint(); hint != "" {
		fmt.Println(hint)
	}
	if lastErr != nil {
		return fmt.Errorf("winget is not reachable from WSL: %w", lastErr)
	}
	return fmt.Errorf("winget is not reachable from WSL")
}
