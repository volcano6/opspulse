package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/secret"
	"github.com/volcano6/opspulse/internal/server"
	"golang.org/x/crypto/ssh"
)

// testPrivateKeyPEM builds a deterministic OpenSSH-format ed25519 private key.
// Generating it here keeps key material out of the repository and means the
// tests never read or write the developer's real ~/.ssh.
func testPrivateKeyPEM(t *testing.T, seed byte) []byte {
	t.Helper()
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
	block, err := ssh.MarshalPrivateKey(priv, "opspulse:test")
	if err != nil {
		t.Fatalf("marshal test key: %v", err)
	}
	return pem.EncodeToMemory(block)
}

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

func TestWritePublicKeyFileIgnoresInvalidMaterial(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "junk")
	writePublicKeyFile(keyPath, []byte("definitely not a key"))

	if _, err := os.Stat(keyPath + ".pub"); !os.IsNotExist(err) {
		t.Error("no .pub file should be written for unparsable key material")
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
	// The retired names stay registered so that an old invocation reaches the
	// message naming its replacement instead of an "unknown command" error.
	for _, want := range []string{"push", "pull"} {
		if !names[want] {
			t.Errorf("missing retired subcommand %q", want)
		}
	}
	if onePasswordRestoreCmd.ValidArgsFunction == nil {
		t.Error("restore should complete server names")
	}
	if !onePasswordLegacyPushCmd.Hidden || !onePasswordLegacyPullCmd.Hidden {
		t.Error("the retired push/pull commands should be hidden from help")
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

// TestLegacyPushPullAreRetired pins the retirement contract: the old names must
// fail with a pointer at the replacement, and the old flags must still parse so
// that the failure is the message rather than Cobra's "unknown flag".
func TestLegacyPushPullAreRetired(t *testing.T) {
	if err := onePasswordLegacyPushCmd.RunE(nil, nil); err == nil || !strings.Contains(err.Error(), "ops 1p backup") {
		t.Errorf("push should be retired in favour of backup, got %v", err)
	}
	if err := onePasswordLegacyPullCmd.RunE(nil, nil); err == nil || !strings.Contains(err.Error(), "ops 1p restore") {
		t.Errorf("pull should be retired in favour of restore, got %v", err)
	}

	for _, cmd := range []*cobra.Command{onePasswordLegacyPushCmd, onePasswordLegacyPullCmd} {
		for _, name := range []string{"materialize", "from-vault", "delete-local", "inventory"} {
			f := cmd.Flags().Lookup(name)
			if f == nil {
				t.Fatalf("%s should still accept the retired --%s flag", cmd.Name(), name)
			}
			if !f.Hidden {
				t.Errorf("%s --%s should be hidden", cmd.Name(), name)
			}
		}
	}
}

// TestVaultDiscoveryMatchesByExactTitle pins the matching rule that makes item
// discovery safe: a server is claimed only by the item title derived from its
// exact name, so "web" can never be wired to "web2"'s key.
func TestVaultDiscoveryMatchesByExactTitle(t *testing.T) {
	discovery := &vaultDiscovery{
		vault: "Personal",
		titles: map[string]struct{}{
			"opspulse_web_key":         {},
			"opspulse_web2_key":        {},
			"opspulse_web_password":    {},
			"unrelated_item":           {},
			"opspulse_only_a_password": {},
		},
	}

	if got, want := discovery.keyRef("web"), "op://Personal/opspulse_web_key/opspulse_private_key"; got != want {
		t.Errorf("keyRef(web) = %q, want %q", got, want)
	}
	if got, want := discovery.keyRef("web2"), "op://Personal/opspulse_web2_key/opspulse_private_key"; got != want {
		t.Errorf("keyRef(web2) = %q, want %q", got, want)
	}
	if got, want := discovery.passwordRef("web"), "op://Personal/opspulse_web_password/password"; got != want {
		t.Errorf("passwordRef(web) = %q, want %q", got, want)
	}

	// A prefix of a real title must not match, and a vault with no such item
	// yields no reference at all rather than a guess.
	if got := discovery.keyRef("we"); got != "" {
		t.Errorf("keyRef(we) = %q, want no match", got)
	}
	if got := discovery.keyRef("only_a"); got != "" {
		t.Errorf("keyRef(only_a) = %q, want no match (only the password item exists)", got)
	}
	if got := discovery.keyRef("absent"); got != "" {
		t.Errorf("keyRef(absent) = %q, want no match", got)
	}

	var nilDiscovery *vaultDiscovery
	if got := nilDiscovery.keyRef("web"); got != "" {
		t.Errorf("a nil discovery must yield no reference, got %q", got)
	}
}

// TestPlanRestore pins where a restore reads each credential from: a legacy
// op:// reference is used as-is (so a non-standard item name is not missed),
// item discovery covers everything else, and a password that is already local
// is left alone.
func TestPlanRestore(t *testing.T) {
	discovery := &vaultDiscovery{
		vault: "Personal",
		titles: map[string]struct{}{
			"opspulse_web_key":      {},
			"opspulse_web_password": {},
		},
	}

	t.Run("a local key path is matched by item name", func(t *testing.T) {
		srv := &server.Server{Name: "web", KeyPath: "~/.ssh/opspulse_web"}
		plan := planRestore(srv, discovery, nil)

		if want := "op://Personal/opspulse_web_key/opspulse_private_key"; plan.keyRef != want {
			t.Errorf("keyRef = %q, want %q", plan.keyRef, want)
		}
		if plan.keyWasLegacy {
			t.Error("a local path is not a legacy reference")
		}
	})

	t.Run("a legacy key reference is used verbatim", func(t *testing.T) {
		srv := &server.Server{Name: "web", KeyPath: "op://Other/opspulse_web_key/opspulse_private_key"}
		plan := planRestore(srv, discovery, nil)

		if want := "op://Other/opspulse_web_key/opspulse_private_key"; plan.keyRef != want {
			t.Errorf("keyRef = %q, want the servers.yaml reference as-is", want)
		}
		if !plan.keyWasLegacy {
			t.Error("an op:// reference must be flagged as a migration")
		}
	})

	t.Run("an empty key path is not restored", func(t *testing.T) {
		srv := &server.Server{Name: "web"}
		if plan := planRestore(srv, discovery, nil); plan.keyRef != "" {
			t.Errorf("keyRef = %q, want none for a server on the default key", plan.keyRef)
		}
	})

	t.Run("a plaintext password is left alone", func(t *testing.T) {
		srv := &server.Server{Name: "web", Password: "plaintext"}
		if plan := planRestore(srv, discovery, nil); plan.passRef != "" {
			t.Errorf("passRef = %q, want none: the local value is already the source of truth", plan.passRef)
		}
	})

	t.Run("a missing password is restored from the vault", func(t *testing.T) {
		srv := &server.Server{Name: "web"}
		plan := planRestore(srv, discovery, nil)

		if want := "op://Personal/opspulse_web_password/password"; plan.passRef != want {
			t.Errorf("passRef = %q, want %q", plan.passRef, want)
		}
		if plan.passWasLegacy {
			t.Error("an item found by name is not a legacy reference")
		}
	})

	t.Run("a legacy password reference is used verbatim", func(t *testing.T) {
		srv := &server.Server{Name: "web", Password: "op://Other/opspulse_web_password/password"}
		plan := planRestore(srv, discovery, nil)

		if want := "op://Other/opspulse_web_password/password"; plan.passRef != want {
			t.Errorf("passRef = %q, want the servers.yaml reference as-is", want)
		}
		if !plan.passWasLegacy {
			t.Error("an op:// reference must be flagged as a migration")
		}
	})

	t.Run("no discovery still restores legacy references", func(t *testing.T) {
		srv := &server.Server{Name: "web", KeyPath: "op://Personal/opspulse_web_key/opspulse_private_key"}
		if plan := planRestore(srv, nil, nil); plan.keyRef == "" {
			t.Error("an existing op:// reference should still be restored")
		}
	})

	t.Run("the backup document is preferred over a per-server item", func(t *testing.T) {
		srv := &server.Server{Name: "web", KeyPath: "~/.ssh/opspulse_web"}
		creds := map[string]backupCredentials{
			"web": {key: []byte("KEY MATERIAL"), password: "from-blob"},
		}
		plan := planRestore(srv, discovery, creds)

		if string(plan.keyFromBlob) != "KEY MATERIAL" {
			t.Errorf("keyFromBlob = %q, want the document's key", plan.keyFromBlob)
		}
		if plan.keyRef != "" {
			t.Errorf("keyRef = %q, want none: the document already carries the key", plan.keyRef)
		}
		if plan.passFromBlob != "from-blob" {
			t.Errorf("passFromBlob = %q, want the document's password", plan.passFromBlob)
		}
		if plan.passRef != "" {
			t.Errorf("passRef = %q, want none: the document already carries the password", plan.passRef)
		}
	})

	t.Run("a document without the key falls back to item discovery", func(t *testing.T) {
		srv := &server.Server{Name: "web", KeyPath: "~/.ssh/opspulse_web"}
		creds := map[string]backupCredentials{"web": {password: "from-blob"}}
		plan := planRestore(srv, discovery, creds)

		if len(plan.keyFromBlob) != 0 {
			t.Errorf("keyFromBlob = %q, want none: the document has no key for this server", plan.keyFromBlob)
		}
		if want := "op://Personal/opspulse_web_key/opspulse_private_key"; plan.keyRef != want {
			t.Errorf("keyRef = %q, want %q", plan.keyRef, want)
		}
	})

	t.Run("a legacy reference outranks the backup document", func(t *testing.T) {
		srv := &server.Server{Name: "web", KeyPath: "op://Other/opspulse_web_key/opspulse_private_key"}
		creds := map[string]backupCredentials{"web": {key: []byte("KEY MATERIAL")}}
		plan := planRestore(srv, discovery, creds)

		if len(plan.keyFromBlob) != 0 {
			t.Error("a legacy op:// reference must be honoured rather than replaced by the document")
		}
		if !plan.keyWasLegacy {
			t.Error("the legacy reference must still be flagged as a migration")
		}
	})
}

func TestReportUnmatchedVaultItems(t *testing.T) {
	discovery := &vaultDiscovery{
		vault: "Personal",
		titles: map[string]struct{}{
			"opspulse_web_key":    {},
			"opspulse_web2_key":   {},
			"opspulse_orphan_key": {},
			"opspulse_inventory":  {},
			"some_user_item":      {},
		},
	}

	var out bytes.Buffer
	reportUnmatchedVaultItems(&out, discovery, []server.Server{{Name: "web"}, {Name: "web2"}})
	got := out.String()

	if !strings.Contains(got, "opspulse_orphan_key") {
		t.Errorf("an unclaimed opspulse item should be reported:\n%s", got)
	}
	if strings.Contains(got, "some_user_item") {
		t.Errorf("items OpsPulse did not create must stay out of the report:\n%s", got)
	}
	if strings.Contains(got, "opspulse_web_key") || strings.Contains(got, "opspulse_web2_key") {
		t.Errorf("claimed items must not be reported as unmatched:\n%s", got)
	}
	// The inventory backup belongs to no single server, so without an explicit
	// exclusion its opspulse_ prefix would make it look orphaned forever.
	if strings.Contains(got, secret.InventoryItemTitle) {
		t.Errorf("the inventory backup must not be reported as an orphaned credential:\n%s", got)
	}

	out.Reset()
	reportUnmatchedVaultItems(&out, nil, nil)
	if out.Len() != 0 {
		t.Errorf("with no discovery nothing should be printed, got %q", out.String())
	}
}

func TestPublicKeyLine(t *testing.T) {
	line, ok := publicKeyLine(testPrivateKeyPEM(t, 3))
	if !ok {
		t.Fatal("publicKeyLine() should parse a generated key")
	}
	if !strings.HasPrefix(line, "ssh-ed25519 ") {
		t.Errorf("publicKeyLine() = %q, want an ssh-ed25519 authorized_keys line", line)
	}
	if _, ok := publicKeyLine([]byte("not a key")); ok {
		t.Error("publicKeyLine() should report failure for unparsable material")
	}
}

// TestMayReplaceLocalKey pins the guard that stops a batch restore from
// silently destroying a key file that already lives at the managed path.
func TestMayReplaceLocalKey(t *testing.T) {
	keyA := testPrivateKeyPEM(t, 1)
	keyB := testPrivateKeyPEM(t, 2)

	writeFixture := func(t *testing.T, data []byte) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "opspulse_web")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		return path
	}

	assertDecision := func(t *testing.T, got bool, err error, want bool) {
		t.Helper()
		if err != nil {
			t.Fatalf("mayReplaceLocalKey() error = %v, want nil", err)
		}
		if got != want {
			t.Errorf("mayReplaceLocalKey() = %v, want %v", got, want)
		}
	}

	t.Run("a missing destination is free to write", func(t *testing.T) {
		got, err := mayReplaceLocalKey(filepath.Join(t.TempDir(), "absent"), keyA)
		assertDecision(t, got, err, true)
	})

	t.Run("an identical key is written without ceremony", func(t *testing.T) {
		got, err := mayReplaceLocalKey(writeFixture(t, keyA), keyA)
		assertDecision(t, got, err, true)
	})

	t.Run("the same key with different surrounding bytes is still the same key", func(t *testing.T) {
		padded := append([]byte("\n"), keyA...)
		padded = append(padded, '\n', '\n')
		got, err := mayReplaceLocalKey(writeFixture(t, padded), keyA)
		assertDecision(t, got, err, true)
	})

	t.Run("a different key is left alone", func(t *testing.T) {
		got, err := mayReplaceLocalKey(writeFixture(t, keyB), keyA)
		assertDecision(t, got, err, false)
	})

	t.Run("--force overrides the guard", func(t *testing.T) {
		onePasswordRestoreForce = true
		t.Cleanup(func() { onePasswordRestoreForce = false })

		got, err := mayReplaceLocalKey(writeFixture(t, keyB), keyA)
		assertDecision(t, got, err, true)
	})

	t.Run("unparsable content falls back to a byte comparison", func(t *testing.T) {
		path := writeFixture(t, []byte("not a key"))
		got, err := mayReplaceLocalKey(path, keyA)
		assertDecision(t, got, err, false)

		got, err = mayReplaceLocalKey(path, []byte("not a key"))
		assertDecision(t, got, err, true)
	})
}

// TestMayReplaceLocalKeyNormalisesKeyFormats is the reason the comparison is by
// public key rather than by bytes: 1Password hands a key back in OpenSSH form
// even when it was uploaded as classic PEM, so a byte comparison would demand
// --force for a key that never changed.
func TestMayReplaceLocalKeyNormalisesKeyFormats(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	classicPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(rsaKey),
	})
	block, err := ssh.MarshalPrivateKey(rsaKey, "")
	if err != nil {
		t.Fatalf("marshal OpenSSH key: %v", err)
	}
	openSSHForm := pem.EncodeToMemory(block)

	if bytes.Equal(classicPEM, openSSHForm) {
		t.Fatal("fixture assumption broken: the two encodings should differ byte for byte")
	}

	path := filepath.Join(t.TempDir(), "rsa_key")
	if err := os.WriteFile(path, classicPEM, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	got, err := mayReplaceLocalKey(path, openSSHForm)
	if err != nil {
		t.Fatalf("mayReplaceLocalKey() error = %v, want nil", err)
	}
	if !got {
		t.Error("the same RSA key in a different encoding must not be reported as a conflict")
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

func TestCountRestorePasswords(t *testing.T) {
	plans := []restorePlan{
		{server: &server.Server{Name: "a"}, passRef: "op://Private/opspulse_a_password/password"},
		{server: &server.Server{Name: "b"}},
		{server: &server.Server{Name: "c"}, keyRef: "op://Private/opspulse_c_key/opspulse_private_key"},
		{server: &server.Server{Name: "d"}, passRef: "op://Private/opspulse_d_password/password"},
		// A password from the backup document lands in servers.yaml just as
		// plaintext as one read from an item, so it must be counted too.
		{server: &server.Server{Name: "e"}, passFromBlob: "hunter2"},
	}
	if got := countRestorePasswords(plans); got != 3 {
		t.Errorf("countRestorePasswords() = %d, want 3", got)
	}
	if got := countRestorePasswords(nil); got != 0 {
		t.Errorf("countRestorePasswords(nil) = %d, want 0", got)
	}
}

func TestCountMigratedServers(t *testing.T) {
	outcomes := []restoreOutcome{
		{name: "web", keyRestored: true, migrated: true},
		{name: "db", keyRestored: true},
		{name: "staging", passRestored: true, migrated: true},
		{name: "idle"},
	}
	if got := countMigratedServers(outcomes); got != 2 {
		t.Errorf("countMigratedServers() = %d, want 2", got)
	}
}

func TestSelectRestoreTargets(t *testing.T) {
	store := server.NewStore(filepath.Join(t.TempDir(), "servers.yaml"))
	for _, s := range []server.Server{
		{Name: "web", Host: "10.0.0.1"},
		{Name: "guarded", Host: "10.0.0.2", SkipBatch: true},
	} {
		if err := store.Save(s); err != nil {
			t.Fatalf("seed store: %v", err)
		}
	}

	namesOf := func(targets []*server.Server) []string {
		out := make([]string, 0, len(targets))
		for _, s := range targets {
			out = append(out, s.Name)
		}
		return out
	}

	t.Run("no arguments selects every server", func(t *testing.T) {
		targets, err := selectRestoreTargets(store, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := namesOf(targets); len(got) != 2 {
			t.Errorf("targets = %v, want both servers", got)
		}
	})

	t.Run("an explicit name is honoured even when the server carries skip_batch", func(t *testing.T) {
		targets, err := selectRestoreTargets(store, []string{"guarded"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := namesOf(targets); len(got) != 1 || got[0] != "guarded" {
			t.Errorf("targets = %v, want [guarded]", got)
		}
	})

	t.Run("an unknown name points at the no-argument form", func(t *testing.T) {
		_, err := selectRestoreTargets(store, []string{"nope"})
		if err == nil || !strings.Contains(err.Error(), "ops 1p restore") {
			t.Fatalf("expected an error naming the fix, got %v", err)
		}
	})
}

func TestConfirmPlaintextRestore(t *testing.T) {
	t.Run("--yes accepts without prompting", func(t *testing.T) {
		var out bytes.Buffer
		if err := confirmPlaintextRestore(strings.NewReader(""), &out, 2, true, true); err != nil {
			t.Fatalf("confirmPlaintextRestore() error = %v, want nil", err)
		}
		if out.Len() != 0 {
			t.Errorf("--yes should not prompt, got %q", out.String())
		}
	})

	t.Run("a non-interactive shell is refused rather than prompted at", func(t *testing.T) {
		var out bytes.Buffer
		err := confirmPlaintextRestore(strings.NewReader(""), &out, 2, false, false)
		if err == nil || !strings.Contains(err.Error(), "--yes") {
			t.Fatalf("confirmPlaintextRestore() error = %v, want it to point at --yes", err)
		}
	})

	t.Run("an explicit yes proceeds and spells out the consequence", func(t *testing.T) {
		var out bytes.Buffer
		if err := confirmPlaintextRestore(strings.NewReader("y\n"), &out, 2, true, false); err != nil {
			t.Fatalf("confirmPlaintextRestore() error = %v, want nil", err)
		}
		if !strings.Contains(out.String(), "plaintext password") {
			t.Errorf("the warning should spell out the consequence, got %q", out.String())
		}
	})

	t.Run("an explicit no cancels", func(t *testing.T) {
		var out bytes.Buffer
		if err := confirmPlaintextRestore(strings.NewReader("n\n"), &out, 2, true, false); err == nil {
			t.Error("confirmPlaintextRestore() should refuse when the user says no")
		}
	})

	t.Run("an empty answer defaults to refusing", func(t *testing.T) {
		var out bytes.Buffer
		if err := confirmPlaintextRestore(strings.NewReader("\n"), &out, 2, true, false); err == nil {
			t.Error("confirmPlaintextRestore() should default to refusing")
		}
	})
}

func TestReportRestoreOutcomes(t *testing.T) {
	t.Run("a clean batch summarises and succeeds", func(t *testing.T) {
		var out bytes.Buffer
		err := reportRestoreOutcomes(&out, []restoreOutcome{
			{name: "web", keyRestored: true},
			{name: "db", passRestored: true},
			{name: "idle", reason: "no credential is backed up in 1Password"},
		})
		if err != nil {
			t.Fatalf("reportRestoreOutcomes() error = %v, want nil", err)
		}
		got := out.String()
		for _, want := range []string{"2 restored, 1 skipped, 0 blocked, 0 failed", "1 plaintext password(s)", "idle"} {
			if !strings.Contains(got, want) {
				t.Errorf("summary should contain %q:\n%s", want, got)
			}
		}
	})

	t.Run("a failure is surfaced and fails the batch", func(t *testing.T) {
		var out bytes.Buffer
		err := reportRestoreOutcomes(&out, []restoreOutcome{
			{name: "web", keyRestored: true},
			{name: "db", err: errors.New("1Password is unreachable")},
		})
		if err == nil {
			t.Fatal("reportRestoreOutcomes() should fail the batch when a server fails")
		}
		got := out.String()
		if !strings.Contains(got, "1 restored, 0 skipped, 0 blocked, 1 failed") {
			t.Errorf("the summary should count the failure:\n%s", got)
		}
		if !strings.Contains(got, "1Password is unreachable") {
			t.Errorf("the per-server error should be surfaced:\n%s", got)
		}
	})

	// A blocked credential is not a technical failure, but it does mean the
	// off-ramp is incomplete, so the run must not report success.
	t.Run("a blocked key fails the batch even when the password came back", func(t *testing.T) {
		var out bytes.Buffer
		err := reportRestoreOutcomes(&out, []restoreOutcome{
			{
				name:         "web",
				passRestored: true,
				blocked:      true,
				reason:       "a different key already occupies the local path; re-run with --force to replace it",
			},
		})
		if err == nil {
			t.Fatal("reportRestoreOutcomes() should fail when a credential is still stranded")
		}
		got := out.String()
		if !strings.Contains(got, "0 restored, 0 skipped, 1 blocked, 0 failed") {
			t.Errorf("a server with a stranded key is not a clean restore:\n%s", got)
		}
		if !strings.Contains(got, "1 plaintext password(s)") {
			t.Errorf("the password that did come back should still be reported:\n%s", got)
		}
		if !strings.Contains(got, "--force") {
			t.Errorf("the blocked key should be explained:\n%s", got)
		}
	})

	t.Run("a purely blocked server counts as blocked rather than skipped", func(t *testing.T) {
		var out bytes.Buffer
		err := reportRestoreOutcomes(&out, []restoreOutcome{
			{name: "web", blocked: true, reason: "a different key already occupies the local path"},
		})
		if err == nil {
			t.Fatal("reportRestoreOutcomes() should fail when nothing could be restored")
		}
		got := out.String()
		if !strings.Contains(got, "0 restored, 0 skipped, 1 blocked, 0 failed") {
			t.Errorf("a block is not a benign skip:\n%s", got)
		}
		if strings.Contains(got, "plaintext password") {
			t.Errorf("no password was restored, so none should be announced:\n%s", got)
		}
	})
}
