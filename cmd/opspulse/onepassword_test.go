package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/volcano6/opspulse/internal/secret"
	"github.com/volcano6/opspulse/internal/server"
)

func TestOnePasswordRefDisplay(t *testing.T) {
	got := onePasswordRefDisplay("op://Private/opspulse_web/private key")
	if got != "Private/opspulse_web" {
		t.Errorf("onePasswordRefDisplay() = %q, want Private/opspulse_web", got)
	}
	// An unparsable reference must be shown verbatim rather than swallowed.
	if got := onePasswordRefDisplay("weird"); got != "weird" {
		t.Errorf("onePasswordRefDisplay() = %q, want weird", got)
	}
}

func TestDescribeCredentialSources(t *testing.T) {
	tests := []struct {
		name     string
		srv      server.Server
		wantKey  string
		wantPass string
	}{
		{
			// A residual op:// reference is a problem to fix, not a working
			// configuration, so it is labelled "legacy" rather than "1password".
			name:    "legacy op:// key reference",
			srv:     server.Server{KeyPath: "op://Private/opspulse_web/private key"},
			wantKey: "legacy 1password ref (Private/opspulse_web)",
		},
		{
			name:    "managed local key",
			srv:     server.Server{KeyPath: "~/.ssh/opspulse_web"},
			wantKey: "local file (~/.ssh/opspulse_web, managed)",
		},
		{
			name:    "unmanaged local key",
			srv:     server.Server{KeyPath: "~/.ssh/id_ed25519"},
			wantKey: "local file (~/.ssh/id_ed25519)",
		},
		{
			name:     "plaintext password",
			srv:      server.Server{Password: "hunter2"},
			wantPass: "plaintext (servers.yaml)",
		},
		{
			name:     "legacy op:// password reference",
			srv:      server.Server{Password: "op://Personal/opspulse_vps_01_password/password"},
			wantPass: "legacy 1password ref (Personal/opspulse_vps_01_password)",
		},
		{
			name: "nothing configured",
			srv:  server.Server{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wantKey, wantPass := tc.wantKey, tc.wantPass
			if wantKey == "" {
				wantKey = "-"
			}
			if wantPass == "" {
				wantPass = "-"
			}
			if got := describeKeySource(tc.srv); got != wantKey {
				t.Errorf("describeKeySource() = %q, want %q", got, wantKey)
			}
			if got := describePasswordSource(tc.srv); got != wantPass {
				t.Errorf("describePasswordSource() = %q, want %q", got, wantPass)
			}
		})
	}
}

func TestOnePasswordAuthHintPicksContextAppropriateRemedy(t *testing.T) {
	linuxCLI := secret.CLI{Path: "/home/user/.local/bin/op"}
	windowsCLI := secret.CLI{Path: "/mnt/c/tools/op.exe", IsWindowsBinary: true}

	t.Run("linux build inside WSL must not blame the app toggle", func(t *testing.T) {
		t.Setenv("WSL_DISTRO_NAME", "Ubuntu")
		got := onePasswordAuthHint(linuxCLI)
		if got != onePasswordLinuxInWSLHint {
			t.Errorf("onePasswordAuthHint() = %q, want the WSL/Linux hint", got)
		}
		if !strings.Contains(got, "Windows build") || !strings.Contains(got, "winget install AgileBits.1Password.CLI") {
			t.Errorf("the WSL hint should tell the user to install the Windows build:\n%s", got)
		}
	})

	t.Run("windows build gets the Desktop App hint", func(t *testing.T) {
		t.Setenv("WSL_DISTRO_NAME", "Ubuntu")
		if got := onePasswordAuthHint(windowsCLI); got != onePasswordDesktopHint {
			t.Errorf("onePasswordAuthHint() = %q, want the Desktop App hint", got)
		}
	})
}

// TestOnePasswordFailureHintSeparatesAuthFromSilence is the regression guard for
// the misdiagnosis that prompted this: `ops 1p backup` failed because an approval
// prompt went unanswered, and OpsPulse blamed the Desktop App integration setting
// -- sending the user to re-check a toggle that was never the problem.
func TestOnePasswordFailureHintSeparatesAuthFromSilence(t *testing.T) {
	t.Setenv("WSL_DISTRO_NAME", "Ubuntu")
	cli := secret.CLI{Path: "/mnt/c/tools/op.exe", IsWindowsBinary: true}

	t.Run("a stalled call does not blame the sign-in settings", func(t *testing.T) {
		// The shape seen in practice: the WSL relay's own message lands on
		// stderr, with no sign-in wording anywhere in it.
		err := errors.New("op vault list --format json: exit status 1 (stderr: <3>WSL ERROR: UtilAcceptVsock:273: accept4 failed 110)")
		got := onePasswordFailureHint(cli, err)
		if got != onePasswordStalledHint {
			t.Fatalf("onePasswordFailureHint() = %q, want the stalled hint", got)
		}
		if !strings.Contains(got, "approval prompt") {
			t.Errorf("the stalled hint should name the pending approval prompt:\n%s", got)
		}
		if strings.Contains(got, "Integrate with 1Password CLI") {
			t.Errorf("a stalled call must not send the user to the integration toggle:\n%s", got)
		}
	})

	t.Run("a silent failure also gets the stalled hint", func(t *testing.T) {
		err := errors.New("op vault list --format json: exit status 1 (no output)")
		if got := onePasswordFailureHint(cli, err); got != onePasswordStalledHint {
			t.Fatalf("onePasswordFailureHint() = %q, want the stalled hint", got)
		}
	})

	t.Run("a real sign-in failure still gets the auth remedy", func(t *testing.T) {
		err := errors.New("op vault list: exit status 1 (stderr: You are not currently signed in.)")
		if got := onePasswordFailureHint(cli, err); got != onePasswordDesktopHint {
			t.Fatalf("onePasswordFailureHint() = %q, want the Desktop App hint", got)
		}
	})
}

// TestChooseVault pins the "you should not have to pass --vault" behaviour: the
// common case resolves on its own, and every other case fails with a way out.
func TestChooseVault(t *testing.T) {
	tests := []struct {
		name         string
		names        []string
		explicit     string
		fallbacks    []string
		want         string
		wantWarnings int
		wantErr      string
	}{
		{
			name:  "the only accessible vault is used silently",
			names: []string{"Personal"},
			want:  "Personal",
		},
		{
			name:     "an explicit vault wins",
			names:    []string{"Personal", "Employee"},
			explicit: "Employee",
			want:     "Employee",
		},
		{
			name:     "an explicit vault that does not exist is a hard error",
			names:    []string{"Personal"},
			explicit: "Private",
			wantErr:  `vault "Private" was not found`,
		},
		{
			name:         "a stale preference warns and falls through",
			names:        []string{"Personal", "Employee"},
			fallbacks:    []string{"Private", "Employee"},
			want:         "Employee",
			wantWarnings: 1,
		},
		{
			name:         "a stale preference still resolves when one vault remains",
			names:        []string{"Personal"},
			fallbacks:    []string{"Private"},
			want:         "Personal",
			wantWarnings: 1,
		},
		{
			name:    "genuine ambiguity explains how to choose",
			names:   []string{"Personal", "Employee"},
			wantErr: "several 1Password vaults are available",
		},
		{
			name:    "no accessible vault",
			names:   nil,
			wantErr: "no 1Password vault is accessible",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, warnings, err := chooseVault(tc.names, tc.explicit, tc.fallbacks)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("chooseVault() error = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("chooseVault() unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("chooseVault() = %q, want %q", got, tc.want)
			}
			if len(warnings) != tc.wantWarnings {
				t.Errorf("chooseVault() produced %d warning(s), want %d: %v", len(warnings), tc.wantWarnings, warnings)
			}
		})
	}
}

func TestDedupeNonEmpty(t *testing.T) {
	got := dedupeNonEmpty([]string{" Employee ", "", "Employee", "Personal", "   "})
	want := []string{"Employee", "Personal"}
	if len(got) != len(want) {
		t.Fatalf("dedupeNonEmpty() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dedupeNonEmpty() = %v, want %v", got, want)
		}
	}
}

func TestSettingsSummary(t *testing.T) {
	if got := settingsSummary(secret.Settings{}); got != "" {
		t.Errorf("settingsSummary(empty) = %q, want an empty string", got)
	}
	if got := settingsSummary(secret.Settings{Vault: "Personal"}); got != "vault Personal" {
		t.Errorf("settingsSummary(vault only) = %q", got)
	}
	got := settingsSummary(secret.Settings{Vault: "Personal", Account: "example.1password.com"})
	if got != "vault Personal, account example.1password.com" {
		t.Errorf("settingsSummary() = %q", got)
	}
}

func TestOnePasswordConfigCommandWiring(t *testing.T) {
	names := map[string]bool{}
	for _, sub := range onePasswordCmd.Commands() {
		names[sub.Name()] = true
	}
	if !names["config"] {
		t.Fatal("ops 1p should expose a config subcommand")
	}
	for _, flag := range []string{"vault", "account", "unset", "offline"} {
		if onePasswordConfigCmd.Flags().Lookup(flag) == nil {
			t.Errorf("ops 1p config should expose --%s", flag)
		}
	}
	if onePasswordCmd.PersistentFlags().Lookup("account") == nil {
		t.Error("ops 1p should expose a persistent --account flag")
	}
}

func TestOnePasswordAccountFlagRegistered(t *testing.T) {
	if onePasswordCmd.PersistentFlags().Lookup("account") == nil {
		t.Error("ops 1p should expose a persistent --account flag")
	}
}

func TestOnePasswordCommandWiring(t *testing.T) {
	if onePasswordCmd.Use != "1p" {
		t.Errorf("onePasswordCmd.Use = %q, want 1p", onePasswordCmd.Use)
	}
	names := map[string]bool{}
	for _, sub := range onePasswordCmd.Commands() {
		names[sub.Name()] = true
	}
	for _, want := range []string{"backup", "restore", "status", "config"} {
		if !names[want] {
			t.Errorf("missing subcommand %q", want)
		}
	}
	if onePasswordRestoreCmd.ValidArgsFunction == nil {
		t.Error("restore should complete server names")
	}
}

func TestOnePasswordBackupCommandFlags(t *testing.T) {
	for _, flag := range []string{"vault"} {
		if onePasswordBackupCmd.Flags().Lookup(flag) == nil {
			t.Errorf("ops 1p backup should expose --%s", flag)
		}
	}
	// backup is deliberately unconditional: no selector, no skip list, no
	// concurrency knob (a machine's whole inventory goes up in one item), and
	// no conflict flags (it merges nothing - only restore does).
	for _, flag := range []string{"all", "filter", "include-skipped", "delete-local", "inventory", "parallel", "prefer-local", "prefer-remote"} {
		if onePasswordBackupCmd.Flags().Lookup(flag) != nil {
			t.Errorf("ops 1p backup should not expose --%s", flag)
		}
	}
	if onePasswordBackupCmd.Args == nil {
		t.Error("ops 1p backup should reject positional arguments")
	}
}

func TestOnePasswordRestoreCommandFlags(t *testing.T) {
	for _, flag := range []string{"vault", "yes", "force", "prefer-local", "prefer-remote"} {
		if onePasswordRestoreCmd.Flags().Lookup(flag) == nil {
			t.Errorf("ops 1p restore should expose --%s", flag)
		}
	}
	if onePasswordRestoreCmd.Flags().ShorthandLookup("y") == nil {
		t.Error("ops 1p restore should expose -y as a shorthand for --yes")
	}
	for _, flag := range []string{"from-vault", "materialize", "all", "filter", "inventory"} {
		if onePasswordRestoreCmd.Flags().Lookup(flag) != nil {
			t.Errorf("ops 1p restore should not expose --%s", flag)
		}
	}
}

func TestServersWithLegacy1PRefs(t *testing.T) {
	servers := []server.Server{
		{Name: "web", KeyPath: "~/.ssh/opspulse_web"},
		{Name: "db", KeyPath: "op://Private/opspulse_db_key/opspulse_private_key"},
		{Name: "staging", Password: "op://Private/opspulse_staging_password/password"},
		{Name: "plain", Password: "hunter2"},
	}
	got := serversWithLegacy1PRefs(servers)
	if len(got) != 2 || got[0] != "db" || got[1] != "staging" {
		t.Errorf("serversWithLegacy1PRefs() = %v, want [db staging]", got)
	}
	if got := serversWithLegacy1PRefs(nil); len(got) != 0 {
		t.Errorf("serversWithLegacy1PRefs(nil) = %v, want none", got)
	}
}
