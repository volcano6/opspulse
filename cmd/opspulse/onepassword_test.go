package main

import (
	"bytes"
	"os"
	"path/filepath"
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
			name:    "key in 1password",
			srv:     server.Server{KeyPath: "op://Private/opspulse_web/private key"},
			wantKey: "1password (Private/opspulse_web)",
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
			name:     "password in 1password",
			srv:      server.Server{Password: "op://Personal/opspulse_vps_01_password/password"},
			wantPass: "1password (Personal/opspulse_vps_01_password)",
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

func TestRenderServerTableShowsOnePasswordBinding(t *testing.T) {
	servers := []server.Server{
		{
			Name:    "tx",
			Host:    "118.89.136.33",
			User:    "ubuntu",
			KeyPath: "op://Private/opspulse_tx/private key",
		},
	}

	var buf bytes.Buffer
	if err := renderServerTable(&buf, servers); err != nil {
		t.Fatalf("renderServerTable error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "1password: Private/opspulse_tx") {
		t.Errorf("AUTH column should advertise the 1Password binding:\n%s", out)
	}
	if strings.Contains(out, "op://") {
		t.Errorf("the raw op:// URI should not be dumped into the table:\n%s", out)
	}
}

func TestAuthorizedKeyForPrefersPubFile(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "opspulse_web")
	if err := os.WriteFile(keyPath, []byte("not a real private key"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	want := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIexample opspulse:web"
	if err := os.WriteFile(keyPath+".pub", []byte(want+"\nsecond line ignored\n"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if got := authorizedKeyFor(keyPath, []byte("not a real private key")); got != want {
		t.Errorf("authorizedKeyFor() = %q, want %q", got, want)
	}
}

func TestAuthorizedKeyForReturnsEmptyForInvalidKey(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "junk")

	// No sibling .pub file and unparsable key material: must not panic, and must
	// signal "unknown" with an empty string so the caller can omit the field.
	if got := authorizedKeyFor(keyPath, []byte("definitely not a key")); got != "" {
		t.Errorf("authorizedKeyFor() = %q, want an empty string", got)
	}
}

func TestWritePublicKeyFileIgnoresInvalidMaterial(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "junk")
	writePublicKeyFile(keyPath, []byte("definitely not a key"))

	if _, err := os.Stat(keyPath + ".pub"); !os.IsNotExist(err) {
		t.Error("no .pub file should be written for unparsable key material")
	}
}

func TestSelectOnePasswordTargetsRequiresSelection(t *testing.T) {
	restore := onePasswordAll
	onePasswordAll = false
	t.Cleanup(func() { onePasswordAll = restore })

	store := server.NewStore(filepath.Join(t.TempDir(), "servers.yaml"))
	if _, err := selectOnePasswordTargets(store, nil); err == nil {
		t.Error("expected an error when neither server names nor --all are given")
	}
}

func TestMaterializeOnePasswordKeysLeavesLocalKeysAlone(t *testing.T) {
	store := server.NewStore(filepath.Join(t.TempDir(), "servers.yaml"))
	srv := &server.Server{Name: "web", Host: "1.2.3.4", KeyPath: "~/.ssh/id_ed25519"}

	jumpKey, cleanup, err := materializeOnePasswordKeys(srv, store)
	if err != nil {
		t.Fatalf("materializeOnePasswordKeys() error: %v", err)
	}
	defer cleanup()

	if jumpKey != "" {
		t.Errorf("jumpKeyPath = %q, want an empty string", jumpKey)
	}
	if srv.KeyPath != "~/.ssh/id_ed25519" {
		t.Errorf("a local key path must not be rewritten, got %q", srv.KeyPath)
	}
}

func TestMaterializeOnePasswordKeysPropagatesResolverFailure(t *testing.T) {
	store := server.NewStore(filepath.Join(t.TempDir(), "servers.yaml"))
	srv := &server.Server{Name: "web", Host: "1.2.3.4", KeyPath: "op://Private/opspulse_web/private key"}

	// Without a working 1Password CLI the resolution has to fail loudly instead
	// of silently falling back to some other authentication method.
	if secret.Detect().Available() {
		t.Skip("1Password CLI is available on this host; failure cannot be simulated")
	}

	if _, cleanup, err := materializeOnePasswordKeys(srv, store); err == nil {
		cleanup()
		t.Error("expected an error when the 1Password CLI is unavailable")
	}
}

func TestOnePasswordAuthHintPicksContextAppropriateRemedy(t *testing.T) {
	linuxCLI := secret.CLI{Path: "/home/vol/.local/bin/op"}
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
	for _, flag := range []string{"vault", "account", "unset"} {
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
	for _, want := range []string{"push", "pull", "status"} {
		if !names[want] {
			t.Errorf("missing subcommand %q", want)
		}
	}
	if onePasswordPushCmd.ValidArgsFunction == nil {
		t.Error("push should complete server names")
	}
	if onePasswordPullCmd.ValidArgsFunction == nil {
		t.Error("pull should complete server names")
	}
}
