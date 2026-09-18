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
			Host:    "198.51.100.20",
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
	restoreAll, restoreFilter := onePasswordAll, onePasswordPushFilter
	onePasswordAll, onePasswordPushFilter = false, ""
	t.Cleanup(func() { onePasswordAll, onePasswordPushFilter = restoreAll, restoreFilter })

	store := server.NewStore(filepath.Join(t.TempDir(), "servers.yaml"))
	if _, _, err := selectOnePasswordTargets(store, nil); err == nil {
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

func TestOnePasswordPullCommandFlags(t *testing.T) {
	for _, flag := range []string{"all", "yes", "force", "include-skipped", "filter", "from-vault", "materialize", "vault"} {
		if onePasswordPullCmd.Flags().Lookup(flag) == nil {
			t.Errorf("ops 1p pull should expose --%s", flag)
		}
	}
	if onePasswordPullCmd.Flags().ShorthandLookup("y") == nil {
		t.Error("ops 1p pull should expose -y as a shorthand for --yes")
	}
	if onePasswordPullCmd.Flags().ShorthandLookup("f") == nil {
		t.Error("ops 1p pull should expose -f as a shorthand for --filter")
	}
}

func TestOnePasswordPushCommandFlags(t *testing.T) {
	for _, flag := range []string{"all", "vault", "delete-local", "filter", "include-skipped"} {
		if onePasswordPushCmd.Flags().Lookup(flag) == nil {
			t.Errorf("ops 1p push should expose --%s", flag)
		}
	}
	if onePasswordPushCmd.Flags().ShorthandLookup("f") == nil {
		t.Error("ops 1p push should expose -f as a shorthand for --filter")
	}
}

// TestVaultDiscoveryMatchesByExactTitle pins the matching rule that makes
// --from-vault safe: a server is claimed only by the item title derived from its
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

// TestPlanPull pins the precedence between the vault and servers.yaml: the vault
// wins where it has an item, and servers.yaml still supplies what the vault does
// not, so --from-vault widens the search instead of replacing it.
func TestPlanPull(t *testing.T) {
	discovery := &vaultDiscovery{
		vault:  "Personal",
		titles: map[string]struct{}{"opspulse_web_key": {}},
	}

	t.Run("the vault overrides a stale local reference", func(t *testing.T) {
		srv := &server.Server{Name: "web", KeyPath: "~/.ssh/opspulse_web", Password: "op://Other/opspulse_web_password/password"}
		plan := planPull(srv, discovery)

		if want := "op://Personal/opspulse_web_key/opspulse_private_key"; plan.keyRef != want {
			t.Errorf("keyRef = %q, want %q", plan.keyRef, want)
		}
		if want := "op://Other/opspulse_web_password/password"; plan.passRef != want {
			t.Errorf("passRef = %q, want the servers.yaml reference as a fallback", want)
		}
	})

	t.Run("without discovery only servers.yaml counts", func(t *testing.T) {
		srv := &server.Server{Name: "web", KeyPath: "op://Personal/opspulse_web_key/opspulse_private_key", Password: "plaintext"}
		plan := planPull(srv, nil)

		if plan.keyRef == "" {
			t.Error("an existing op:// reference should still be pulled")
		}
		if plan.passRef != "" {
			t.Error("a plaintext password is not something to pull")
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

	out.Reset()
	reportUnmatchedVaultItems(&out, nil, nil)
	if out.Len() != 0 {
		t.Errorf("without --from-vault nothing should be printed, got %q", out.String())
	}
}

// TestNormaliseKeyText pins the fallback comparison used when a key is in a
// format crypto/ssh cannot parse. Line endings are the one difference that must
// not count: a key that travelled through Windows comes back with CRLF, and
// treating that as a mismatch would fail a push that worked.
func TestNormaliseKeyText(t *testing.T) {
	unixForm := "-----BEGIN KEY-----\nabc\ndef\n-----END KEY-----\n"
	dosForm := "-----BEGIN KEY-----\r\nabc\r\ndef\r\n-----END KEY-----\r\n"

	if normaliseKeyText(unixForm) != normaliseKeyText(dosForm) {
		t.Errorf("CRLF must not count as a difference:\n%q\n%q", normaliseKeyText(unixForm), normaliseKeyText(dosForm))
	}
	if normaliseKeyText(unixForm) == normaliseKeyText("-----BEGIN KEY-----\nabc\nghi\n-----END KEY-----\n") {
		t.Error("different key material must not normalise to the same text")
	}
	if got := normaliseKeyText("  spaced  \n"); got != "spaced" {
		t.Errorf("normaliseKeyText() = %q, want surrounding whitespace dropped", got)
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

// TestMayReplaceLocalKey pins the guard that stops a batch pull from silently
// destroying a key file that already lives at the managed path.
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
		onePasswordPullForce = true
		t.Cleanup(func() { onePasswordPullForce = false })

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

func TestCountPullablePasswords(t *testing.T) {
	plans := []pullPlan{
		{server: &server.Server{Name: "a"}, passRef: "op://Private/opspulse_a_password/password"},
		{server: &server.Server{Name: "b", Password: "plaintext"}},
		{server: &server.Server{Name: "c"}, keyRef: "op://Private/opspulse_c_key/opspulse_private_key"},
		{server: &server.Server{Name: "d"}, passRef: "op://Private/opspulse_d_password/password"},
	}
	if got := countPullablePasswords(plans); got != 2 {
		t.Errorf("countPullablePasswords() = %d, want 2", got)
	}

	// Adoption writes a reference, not a secret, so it needs no confirmation.
	restoreFromVault, restoreMaterialize := onePasswordPullFromVault, onePasswordPullMaterialize
	onePasswordPullFromVault, onePasswordPullMaterialize = true, false
	t.Cleanup(func() { onePasswordPullFromVault, onePasswordPullMaterialize = restoreFromVault, restoreMaterialize })

	if got := countPullablePasswords(plans); got != 0 {
		t.Errorf("countPullablePasswords() in reference-only mode = %d, want 0", got)
	}
}

// TestResolveBatchFilter pins the combinations that mean something, and the ones
// that do not. Silently letting one selector win would target a different set of
// servers than the user asked for, which is worse than refusing.
func TestResolveBatchFilter(t *testing.T) {
	tests := []struct {
		name    string
		filter  string
		all     bool
		named   bool
		want    string
		wantErr bool
	}{
		{name: "names alone select nothing extra", named: true, want: ""},
		{name: "names plus a filter is refused", named: true, filter: "prod", wantErr: true},
		{name: "--all becomes the all selector", all: true, want: "all"},
		{name: "a bare filter is used as given", filter: "prod", want: "prod"},
		{name: "an empty filter counts as unset", filter: "   ", wantErr: true},
		{name: "--all with the matching filter agrees", all: true, filter: "all", want: "all"},
		{name: "--all with the matching filter is case insensitive", all: true, filter: "ALL", want: "ALL"},
		{name: "--all with a different filter is refused", all: true, filter: "prod", wantErr: true},
		{name: "nothing at all is refused", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveBatchFilter(tc.filter, tc.all, tc.named)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("resolveBatchFilter() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSelectOnePasswordTargetsByFilter(t *testing.T) {
	store := server.NewStore(filepath.Join(t.TempDir(), "servers.yaml"))
	for _, s := range []server.Server{
		{Name: "web", Host: "10.0.0.1", Labels: map[string]string{"env": "prod"}},
		{Name: "db", Host: "10.0.0.2", Labels: map[string]string{"env": "prod"}, SkipBatch: true},
		{Name: "dev", Host: "10.0.0.3", Labels: map[string]string{"env": "dev"}},
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

	restoreFilter, restoreAll, restoreSkip := onePasswordPushFilter, onePasswordAll, onePasswordPushInclSkip
	t.Cleanup(func() {
		onePasswordPushFilter, onePasswordAll, onePasswordPushInclSkip = restoreFilter, restoreAll, restoreSkip
	})

	t.Run("a filter selects several servers and skips skip_batch ones", func(t *testing.T) {
		onePasswordPushFilter, onePasswordAll, onePasswordPushInclSkip = "env=prod", false, false

		targets, skipped, err := selectOnePasswordTargets(store, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := namesOf(targets); len(got) != 1 || got[0] != "web" {
			t.Errorf("targets = %v, want [web]", got)
		}
		if len(skipped) != 1 || skipped[0] != "db" {
			t.Errorf("skipped = %v, want [db]", skipped)
		}
	})

	t.Run("--include-skipped adds the guarded server", func(t *testing.T) {
		onePasswordPushFilter, onePasswordAll, onePasswordPushInclSkip = "env=prod", false, true

		targets, skipped, err := selectOnePasswordTargets(store, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(skipped) != 0 {
			t.Errorf("skipped = %v, want none", skipped)
		}
		if got := namesOf(targets); len(got) != 2 {
			t.Errorf("targets = %v, want [web db]", got)
		}
	})

	t.Run("a filter matching nothing is not an error", func(t *testing.T) {
		onePasswordPushFilter, onePasswordAll, onePasswordPushInclSkip = "env=staging", false, false

		targets, _, err := selectOnePasswordTargets(store, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(targets) != 0 {
			t.Errorf("targets = %v, want none", namesOf(targets))
		}
	})

	t.Run("a filter alongside server names is refused", func(t *testing.T) {
		onePasswordPushFilter, onePasswordAll, onePasswordPushInclSkip = "env=prod", false, false

		if _, _, err := selectOnePasswordTargets(store, []string{"web"}); err == nil {
			t.Error("expected an error when both names and --filter are given")
		}
	})
}

func TestSelectOnePasswordPullTargets(t *testing.T) {
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

	t.Run("an explicit name is honoured even when the server carries skip_batch", func(t *testing.T) {
		targets, explicit, skipped, err := selectOnePasswordPullTargets(store, []string{"guarded"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !explicit {
			t.Error("naming servers should be reported as an explicit selection")
		}
		if len(skipped) != 0 {
			t.Errorf("skipped = %v, want none", skipped)
		}
		if got := namesOf(targets); len(got) != 1 || got[0] != "guarded" {
			t.Errorf("targets = %v, want [guarded]", got)
		}
	})

	t.Run("--all leaves skip_batch servers out and names them", func(t *testing.T) {
		onePasswordPullAll = true
		t.Cleanup(func() { onePasswordPullAll = false })

		targets, explicit, skipped, err := selectOnePasswordPullTargets(store, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if explicit {
			t.Error("--all should not be reported as an explicit selection")
		}
		if got := namesOf(targets); len(got) != 1 || got[0] != "web" {
			t.Errorf("targets = %v, want [web]", got)
		}
		if len(skipped) != 1 || skipped[0] != "guarded" {
			t.Errorf("skipped = %v, want [guarded]", skipped)
		}
	})

	t.Run("--include-skipped pulls the guarded server too", func(t *testing.T) {
		onePasswordPullAll, onePasswordPullInclSkip = true, true
		t.Cleanup(func() { onePasswordPullAll, onePasswordPullInclSkip = false, false })

		targets, _, skipped, err := selectOnePasswordPullTargets(store, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(skipped) != 0 {
			t.Errorf("skipped = %v, want none", skipped)
		}
		if got := namesOf(targets); len(got) != 2 {
			t.Errorf("targets = %v, want both servers", got)
		}
	})

	t.Run("neither names nor --all is an error", func(t *testing.T) {
		if _, _, _, err := selectOnePasswordPullTargets(store, nil); err == nil {
			t.Error("expected an error when nothing selects a target")
		}
	})

	t.Run("an unknown name fails before any work happens", func(t *testing.T) {
		if _, _, _, err := selectOnePasswordPullTargets(store, []string{"nope"}); err == nil {
			t.Error("expected an error for an unknown server name")
		}
	})
}

func TestConfirmPlaintextPull(t *testing.T) {
	t.Run("--yes accepts without prompting", func(t *testing.T) {
		var out bytes.Buffer
		if err := confirmPlaintextPull(strings.NewReader(""), &out, 2, true, true); err != nil {
			t.Fatalf("confirmPlaintextPull() error = %v, want nil", err)
		}
		if out.Len() != 0 {
			t.Errorf("--yes should not prompt, got %q", out.String())
		}
	})

	t.Run("a non-interactive shell is refused rather than prompted at", func(t *testing.T) {
		var out bytes.Buffer
		err := confirmPlaintextPull(strings.NewReader(""), &out, 2, false, false)
		if err == nil || !strings.Contains(err.Error(), "--yes") {
			t.Fatalf("confirmPlaintextPull() error = %v, want it to point at --yes", err)
		}
	})

	t.Run("an explicit yes proceeds and spells out the consequence", func(t *testing.T) {
		var out bytes.Buffer
		if err := confirmPlaintextPull(strings.NewReader("y\n"), &out, 2, true, false); err != nil {
			t.Fatalf("confirmPlaintextPull() error = %v, want nil", err)
		}
		if !strings.Contains(out.String(), "plaintext password") {
			t.Errorf("the warning should spell out the consequence, got %q", out.String())
		}
	})

	t.Run("an explicit no cancels", func(t *testing.T) {
		var out bytes.Buffer
		if err := confirmPlaintextPull(strings.NewReader("n\n"), &out, 2, true, false); err == nil {
			t.Error("confirmPlaintextPull() should refuse when the user says no")
		}
	})

	t.Run("an empty answer defaults to refusing", func(t *testing.T) {
		var out bytes.Buffer
		if err := confirmPlaintextPull(strings.NewReader("\n"), &out, 2, true, false); err == nil {
			t.Error("confirmPlaintextPull() should default to refusing")
		}
	})
}

func TestReportPullOutcomes(t *testing.T) {
	t.Run("a clean batch summarises and succeeds", func(t *testing.T) {
		var out bytes.Buffer
		err := reportPullOutcomes(&out, []pullOutcome{
			{name: "web", keyPulled: true},
			{name: "db", passPulled: true},
			{name: "idle", reason: "no credential is managed in 1Password"},
		})
		if err != nil {
			t.Fatalf("reportPullOutcomes() error = %v, want nil", err)
		}
		got := out.String()
		for _, want := range []string{"2 restored, 0 adopted, 1 skipped, 0 blocked, 0 failed", "1 plaintext password(s)", "idle"} {
			if !strings.Contains(got, want) {
				t.Errorf("summary should contain %q:\n%s", want, got)
			}
		}
	})

	t.Run("a failure is surfaced and fails the batch", func(t *testing.T) {
		var out bytes.Buffer
		err := reportPullOutcomes(&out, []pullOutcome{
			{name: "web", keyPulled: true},
			{name: "db", err: errors.New("1Password is unreachable")},
		})
		if err == nil {
			t.Fatal("reportPullOutcomes() should fail the batch when a server fails")
		}
		got := out.String()
		if !strings.Contains(got, "1 restored, 0 adopted, 0 skipped, 0 blocked, 1 failed") {
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
		err := reportPullOutcomes(&out, []pullOutcome{
			{
				name:       "web",
				passPulled: true,
				blocked:    true,
				reason:     "a different key already occupies the local path; re-run with --force to replace it",
			},
		})
		if err == nil {
			t.Fatal("reportPullOutcomes() should fail when a credential is still stranded")
		}
		got := out.String()
		if !strings.Contains(got, "0 restored, 0 adopted, 0 skipped, 1 blocked, 0 failed") {
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
		err := reportPullOutcomes(&out, []pullOutcome{
			{name: "web", blocked: true, reason: "a different key already occupies the local path"},
		})
		if err == nil {
			t.Fatal("reportPullOutcomes() should fail when nothing could be restored")
		}
		got := out.String()
		if !strings.Contains(got, "0 restored, 0 adopted, 0 skipped, 1 blocked, 0 failed") {
			t.Errorf("a block is not a benign skip:\n%s", got)
		}
		if strings.Contains(got, "plaintext password") {
			t.Errorf("no password was pulled, so none should be announced:\n%s", got)
		}
	})
}
