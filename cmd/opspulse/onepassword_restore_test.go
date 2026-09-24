package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/volcano6/opspulse/internal/secret"
	"github.com/volcano6/opspulse/internal/server"
)

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
