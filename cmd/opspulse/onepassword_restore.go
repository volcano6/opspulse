package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/volcano6/opspulse/internal/secret"
	"github.com/volcano6/opspulse/internal/server"
	"github.com/volcano6/opspulse/internal/sftp"
)

// 'ops 1p restore': what to write back onto this machine, the confirmation that
// stands in front of plaintext passwords, and the summary of what happened. The
// functions that write one credential to disk are in
// onepassword_restore_credentials.go.

// restoreOutcome summarises what happened to one server, so that a batch can
// carry on past a failure and still print an honest summary at the end.
type restoreOutcome struct {
	name         string
	keyRestored  bool
	passRestored bool
	// migrated marks a server whose servers.yaml held an op:// reference that
	// this run replaced with a local credential. It drives the one-time cleanup
	// of the historical temp directory.
	migrated bool
	// reason explains a partial, blocked or skipped result. It is never a
	// failure: an error is reported through err, which is what decides the exit
	// code.
	reason string
	// blocked marks a credential that could not be restored without a human
	// decision (currently: --force). It is kept apart from a plain skip because
	// a skip is benign while a block means the off-ramp is incomplete.
	blocked bool
	err     error
}

// restored reports whether anything actually came back to local disk.
func (o restoreOutcome) restored() bool { return o.keyRestored || o.passRestored }

// restorePlan is what a restore will do for one server: the 1Password
// references to read, and whether each one is a legacy op:// reference taken
// from servers.yaml rather than found by item name.
type restorePlan struct {
	server  *server.Server
	keyRef  string
	passRef string
	// keyFromBlob / passFromBlob carry a credential taken from this machine's
	// backup document instead of a per-server item. They are preferred because
	// the document is already in memory, so restoring from it costs no further
	// op call, while the per-server items are historical leftovers that nothing
	// writes any more.
	keyFromBlob  []byte
	passFromBlob string
	// keyWasLegacy / passWasLegacy mark a reference that came from a legacy
	// op:// entry in servers.yaml. Restoring one is a migration, and it is what
	// makes the historical temp directory worth cleaning afterwards.
	keyWasLegacy  bool
	passWasLegacy bool
}

// vaultDiscovery is what item discovery found in one vault: the set of item
// titles, so a server can be matched by the deterministic name OpsPulse gives
// its items.
type vaultDiscovery struct {
	vault  string
	titles map[string]struct{}
	// machineBackups are the per-machine backup documents, when the caller paid
	// to read them. Without them the only evidence of a backup would be the
	// historical per-server items, and every server backed up since would be
	// reported as never backed up.
	machineBackups []backupBlob
}

// backedUpIn returns the titles of the machine backups that carry this server.
func (d *vaultDiscovery) backedUpIn(serverName string) []string {
	if d == nil {
		return nil
	}
	var titles []string
	for _, blob := range d.machineBackups {
		for _, srv := range blob.file.Servers {
			if srv.Name == serverName {
				titles = append(titles, blob.title)
				break
			}
		}
	}
	return titles
}

// keyRef returns the op:// reference to a server's backed-up private key, or ""
// when the vault holds no such item.
func (d *vaultDiscovery) keyRef(serverName string) string {
	if d == nil {
		return ""
	}
	title := secret.SSHKeyItemTitle(serverName)
	if _, ok := d.titles[title]; !ok {
		return ""
	}
	return secret.BuildSSHKeyRef(d.vault, title)
}

// passwordRef returns the op:// reference to a server's backed-up password, or
// "" when the vault holds no such item.
func (d *vaultDiscovery) passwordRef(serverName string) string {
	if d == nil {
		return ""
	}
	title := secret.PasswordItemTitle(serverName)
	if _, ok := d.titles[title]; !ok {
		return ""
	}
	return secret.BuildPasswordRef(d.vault, title)
}

// runRestoreFromOnePassword is the off-ramp: it writes 1Password-hosted
// credentials back onto local disk so that nothing depends on 1Password being
// unlocked any more.
//
// Without arguments it restores the whole machine: servers.yaml first, then
// every server's credentials. That is all a new machine needs after installing
// the CLI. With arguments it restores only the named servers and leaves
// servers.yaml alone.
func runRestoreFromOnePassword(ctx context.Context, args []string) error {
	if err := validatePreferFlags(onePasswordPreferLocal, onePasswordPreferRemote); err != nil {
		return err
	}

	store := server.NewDefaultStore()
	explicit := len(args) > 0

	// Named servers are resolved before the first op call. servers.yaml is a
	// local file, so a misspelled name can be reported without first paying for
	// every 1Password round trip the rest of this function makes.
	var namedTargets []*server.Server
	if explicit {
		var err error
		if namedTargets, err = selectRestoreTargets(store, args); err != nil {
			return err
		}
	}

	cli, err := ensure1PCLI()
	if err != nil {
		return err
	}
	vault, err := resolveAndValidateVault(ctx, cli, onePasswordVault, false)
	if err != nil {
		return err
	}

	// One vault listing for the whole command: the inventory restore and the
	// credential matching below both read it, and a second `op item list` would
	// be a second Desktop App authorisation on Windows, where nothing is cached.
	index := newItemIndex(cli, vault)

	// The per-machine backups are read once, before anything else, because both
	// halves of the restore need them: the inventory merge takes their server
	// lists, and the credential pass takes the private keys they carry. Reading
	// them twice would be the only remaining reason a restore costs more than a
	// handful of calls.
	blobs, err := readBackupBlobs(ctx, cli, vault, index)
	if err != nil {
		return err
	}
	creds := collectBackupCredentials(blobs)

	if !explicit {
		// The inventory comes first so that a fresh machine has the server
		// names before anything tries to match item names against them.
		found, err := restoreInventoryFromOnePassword(ctx, cli, vault, index, blobs, os.Stdin, os.Stdout, stdinIsInteractive(), onePasswordRestoreYes)
		if err != nil {
			return err
		}
		if !found {
			fmt.Printf("ℹ️  No inventory backup in vault %q; restoring credentials for the servers already in servers.yaml.\n", vault)
		}
	}

	targets := namedTargets
	if !explicit {
		// The no-argument form waits for the inventory restore above: on a
		// fresh machine the server names exist only once it has run.
		if targets, err = selectRestoreTargets(store, nil); err != nil {
			return err
		}
	}
	if len(targets) == 0 {
		return fmt.Errorf("nothing to restore: servers.yaml holds no servers")
	}

	discovery, err := discoverVaultItems(ctx, index)
	if err != nil {
		return err
	}

	plans := make([]restorePlan, 0, len(targets))
	for _, srv := range targets {
		plans = append(plans, planRestore(srv, discovery, creds))
	}

	// Ask up front rather than halfway through, so a declined confirmation
	// cannot leave the batch half-applied. On the no-argument path the
	// plaintext passwords already came back with the inventory write above -
	// gated there, before servers.yaml was touched - so what is counted here is
	// the credentials a named restore still has to read from the vault.
	if passwords := countRestorePasswords(plans); passwords > 0 {
		if err := confirmPlaintextRestore(os.Stdin, os.Stdout, passwords, stdinIsInteractive(), onePasswordRestoreYes); err != nil {
			return err
		}
	}

	outcomes := make([]restoreOutcome, 0, len(plans))
	for _, plan := range plans {
		outcomes = append(outcomes, restoreOneServer(ctx, store, plan))
	}
	if err := reportRestoreOutcomes(os.Stdout, outcomes); err != nil {
		return err
	}

	if all, err := store.List(); err == nil {
		reportUnmatchedVaultItems(os.Stdout, discovery, all)
	}

	// The historical temp directory is only touched when this run actually
	// migrated something: a routine restore has no residue to clean, and the
	// purge is total, so it must not fire by accident.
	if migrated := countMigratedServers(outcomes); migrated > 0 {
		fmt.Printf("\n⚠️  Migrated %d server(s) from 'op://' references to local credentials. This compatibility path will be removed in a future release.\n", migrated)
		if removed, err := sftp.PurgeMaterialized1PKeys(""); err == nil && len(removed) > 0 {
			fmt.Printf("🧹 Cleaned up %d historical temporary private key(s) in ~/.ssh/opspulse-1p.\n", len(removed))
		}
		fmt.Println("ℹ️  If the ops daemon is running, restart it to load the new configuration.")
	}
	return nil
}

// selectRestoreTargets decides which servers a restore covers.
//
// Naming a server is an instruction, so a name that is not in servers.yaml is
// an error rather than a silent skip: the usual cause is restoring credentials
// before the server list, which the no-argument form fixes. The named form is
// resolved before any 1Password call, so a typo costs one local lookup.
func selectRestoreTargets(store *server.Store, args []string) ([]*server.Server, error) {
	if len(args) == 0 {
		all, err := store.List()
		if err != nil {
			return nil, err
		}
		targets := make([]*server.Server, 0, len(all))
		for i := range all {
			targets = append(targets, &all[i])
		}
		return targets, nil
	}

	targets := make([]*server.Server, 0, len(args))
	for _, name := range args {
		srv, err := store.Get(name)
		if err != nil {
			return nil, fmt.Errorf("%w\n\nrun 'ops 1p restore' with no arguments first if the server list has not been restored yet", err)
		}
		targets = append(targets, srv)
	}
	return targets, nil
}

// planRestore decides where a server's credentials should be read from.
//
// A legacy op:// reference is used as-is, because it names the exact item the
// old version pushed to and item-name discovery could miss a non-standard one.
// Everything else comes from this machine's backup document when it carries the
// credential, and falls back to the deterministic per-server item name, which
// is what the historical format left behind.
//
// A password is only restored when the local value is absent or a legacy
// reference. The inventory backup already carries a plaintext password, so
// rewriting it here would ask the user to approve a value they already have.
func planRestore(srv *server.Server, discovery *vaultDiscovery, creds map[string]backupCredentials) restorePlan {
	plan := restorePlan{server: srv}
	cred := creds[srv.Name]

	switch {
	case secret.Is1PRef(srv.KeyPath):
		plan.keyRef = srv.KeyPath
		plan.keyWasLegacy = true
	case strings.TrimSpace(srv.KeyPath) != "":
		if len(cred.key) > 0 {
			plan.keyFromBlob = cred.key
		} else {
			plan.keyRef = discovery.keyRef(srv.Name)
		}
	}

	switch {
	case secret.Is1PRef(srv.Password):
		plan.passRef = srv.Password
		plan.passWasLegacy = true
	case strings.TrimSpace(srv.Password) == "":
		if cred.password != "" {
			plan.passFromBlob = cred.password
		} else {
			plan.passRef = discovery.passwordRef(srv.Name)
		}
	}

	return plan
}

// discoverVaultItems builds the vault's title set so that matching a whole
// batch of servers costs no extra call. It reuses the snapshot held by index,
// which a no-argument restore has usually already paid for when it restored the
// inventory.
func discoverVaultItems(ctx context.Context, index *itemIndex) (*vaultDiscovery, error) {
	titles, err := index.titles(ctx)
	if err != nil {
		return nil, err
	}
	return &vaultDiscovery{vault: index.vault, titles: titles}, nil
}

// reportUnmatchedVaultItems tells the user about opspulse_* items that no server
// in servers.yaml claims.
//
// The comparison is against every configured server, not just this run's
// targets: an item belonging to a server that an explicit name left out is
// still matched, and reporting it as orphaned would send the user looking for a
// problem that does not exist.
//
// The list is only ever a hint. Creating servers from it is deliberately not
// done: an item carries no host or user, so OpsPulse would have to invent the
// very details that make a server entry usable.
func reportUnmatchedVaultItems(w io.Writer, discovery *vaultDiscovery, servers []server.Server) {
	if discovery == nil {
		return
	}
	claimed := make(map[string]struct{}, 2*len(servers))
	for _, srv := range servers {
		claimed[secret.SSHKeyItemTitle(srv.Name)] = struct{}{}
		claimed[secret.PasswordItemTitle(srv.Name)] = struct{}{}
	}
	// The inventory backup is not a credential, so no server ever claims it by
	// name; without this it would be reported as orphaned on every restore. The
	// per-machine backup documents are in the same position, and there is one
	// per machine that has ever backed up.
	claimed[secret.InventoryItemTitle] = struct{}{}

	var unmatched []string
	for title := range discovery.titles {
		if !strings.HasPrefix(title, "opspulse_") {
			continue
		}
		if secret.IsInventoryItemTitle(title) {
			continue
		}
		if _, ok := claimed[title]; ok {
			continue
		}
		unmatched = append(unmatched, title)
	}
	if len(unmatched) == 0 {
		return
	}
	sort.Strings(unmatched)
	_, _ = fmt.Fprintf(w, "\nℹ️  %d opspulse item(s) in vault %q match no server in servers.yaml: %s\n", len(unmatched), discovery.vault, strings.Join(unmatched, ", "))
	_, _ = fmt.Fprintln(w, "   OpsPulse does not create servers from vault items, because an item carries no host or user.")
}

// countRestorePasswords counts the servers whose password would land in
// servers.yaml as plaintext, so the warning can name a number. A password that
// comes from the backup document counts exactly like one read from an item:
// both end up written to disk in the clear.
//
// It is deliberately not the whole story on the no-argument path. There the
// backup's plaintext passwords reach servers.yaml with the inventory write
// itself, which countPlaintextPasswordsToWrite gates before it happens - so by
// the time a plan exists those passwords are already local and this count is
// only about the credentials still to be read from the vault.
func countRestorePasswords(plans []restorePlan) int {
	count := 0
	for _, plan := range plans {
		if plan.passRef != "" || plan.passFromBlob != "" {
			count++
		}
	}
	return count
}

// confirmPlaintextRestore gates the one irreversible step of a restore: a
// password can only come back as plaintext in servers.yaml.
//
// A non-interactive shell is refused rather than prompted at. Reading from a
// pipe that never closes would hang a script, and defaulting to "yes" would
// write a secret nobody agreed to. The I/O and the interactivity verdict are
// parameters so the policy can be tested without touching the real terminal.
func confirmPlaintextRestore(in io.Reader, out io.Writer, count int, interactive, yes bool) error {
	if yes {
		return nil
	}
	if !interactive {
		return fmt.Errorf("refusing to write %d plaintext password(s) into servers.yaml without confirmation; re-run with --yes to accept this in a non-interactive shell", count)
	}
	prompt := fmt.Sprintf("⚠️  Warning: restoring will write %d plaintext password(s) into servers.yaml.\nAre you sure you want to proceed? [y/N]: ", count)
	if !promptConfirm(in, out, prompt, false) {
		return fmt.Errorf("restore cancelled by user")
	}
	return nil
}

// countMigratedServers counts the servers whose op:// references were replaced
// by local credentials during this run.
func countMigratedServers(outcomes []restoreOutcome) int {
	count := 0
	for _, out := range outcomes {
		if out.migrated {
			count++
		}
	}
	return count
}

// reportRestoreOutcomes prints the per-server detail and the batch summary, and
// turns an incomplete restore into a non-zero exit.
//
// A blocked server counts towards the failure of the run even though nothing
// went wrong technically: its credential is still only in 1Password, so a
// caller who asked for a complete off-ramp has not got one and must be told.
func reportRestoreOutcomes(w io.Writer, outcomes []restoreOutcome) error {
	var restoredCount, skipped, blocked, failed, passwords int
	for _, out := range outcomes {
		if out.err != nil {
			failed++
			_, _ = fmt.Fprintf(w, "❌ %q: %v\n", out.name, out.err)
			continue
		}
		// Counted before the switch so that a server whose key is blocked still
		// gets credit for the password that did come back.
		if out.passRestored {
			passwords++
		}

		switch {
		case out.blocked:
			blocked++
			_, _ = fmt.Fprintf(w, "⚠️  %q: %s\n", out.name, out.reason)
		case out.restored():
			restoredCount++
		default:
			skipped++
			_, _ = fmt.Fprintf(w, "⏭️  %q: %s\n", out.name, out.reason)
		}
	}

	_, _ = fmt.Fprintln(w)
	if passwords > 0 {
		_, _ = fmt.Fprintf(w, "⚠️  Wrote %d plaintext password(s) into servers.yaml. They are the local copy now.\n", passwords)
	}
	_, _ = fmt.Fprintf(w, "Restore finished: %d restored, %d skipped, %d blocked, %d failed.\n", restoredCount, skipped, blocked, failed)
	if failed > 0 {
		return fmt.Errorf("%d server(s) could not be restored", failed)
	}
	if blocked > 0 {
		return fmt.Errorf("%d server(s) still need a decision before their credentials can be restored", blocked)
	}
	return nil
}
