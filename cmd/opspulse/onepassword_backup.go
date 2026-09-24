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
)

// 'ops 1p backup': putting this machine's servers.yaml and private keys into its
// backup document, and putting back the document it replaced when the write
// cannot be trusted. What that document looks like is in onepassword_inventory.go.

// runBackupToOnePassword uploads this machine's credentials and server list to
// 1Password. It never writes servers.yaml: local disk stays the source of truth,
// so a backup cannot change how 'ops ssh' connects.
func runBackupToOnePassword(ctx context.Context) error {
	store := server.NewDefaultStore()
	servers, err := store.List()
	if err != nil {
		return err
	}
	if len(servers) == 0 {
		return fmt.Errorf("nothing to back up: servers.yaml holds no servers")
	}

	// Fail fast, before 1Password is touched. A server that still holds an
	// op:// reference would be uploaded as that reference, planting a pointer
	// the runtime no longer resolves into the backup.
	if legacy := serversWithLegacy1PRefs(servers); len(legacy) > 0 {
		return fmt.Errorf("servers.yaml still holds 'op://' references for %d server(s): %s\n\nrun 'ops 1p restore' to migrate them to local credentials, then back up again",
			len(legacy), strings.Join(legacy, ", "))
	}

	cli, err := ensure1PCLI()
	if err != nil {
		return err
	}
	vault, err := resolveBackupVault(ctx, cli, onePasswordVault)
	if err != nil {
		return err
	}

	return backupMachineToOnePassword(ctx, cli, vault, servers, os.Stdout)
}

// serversWithLegacy1PRefs lists the servers whose servers.yaml entry still
// holds an op:// reference, which the runtime no longer resolves.
func serversWithLegacy1PRefs(servers []server.Server) []string {
	var names []string
	for i := range servers {
		if secret.Is1PRef(servers[i].KeyPath) || secret.Is1PRef(servers[i].Password) {
			names = append(names, servers[i].Name)
		}
	}
	return names
}

// writeBackupItem stores the payload in this machine's item, in one call.
//
// `op item edit` goes first because it is the only form that can update an
// existing item without reading it first: the title resolves the item, so no
// `op item list` and no `op item get` is needed. `op item create` is deliberately
// NOT the first attempt - it does not deduplicate by title, so calling it for an
// item that already exists quietly produces a second item with the same name,
// after which the read-back below has no way to say which one it read.
//
// The document is built locally instead of fetched with `op item template get`,
// which is what keeps the write to a single call. See secret.BuildInventoryItem
// for what that trades away.
func writeBackupItem(ctx context.Context, cli secret.CLI, vault, title string, payload []byte) error {
	doc, err := secret.BuildInventoryItem(title, string(payload))
	if err != nil {
		return err
	}

	_, editErr := cli.RunWithStdin(ctx, doc, "item", "edit", title, "--vault", vault)
	if editErr == nil {
		return nil
	}
	if !strings.Contains(editErr.Error(), itemMissingMarker) {
		return fmt.Errorf("%s\n\nupdate 1Password item %q: %w", onePasswordFailureHint(cli, editErr), title, editErr)
	}

	if _, err := cli.RunWithStdin(ctx, doc, "item", "create", "--vault", vault, "-"); err != nil {
		return fmt.Errorf("%s\n\ncreate 1Password item %q: %w", onePasswordFailureHint(cli, err), title, err)
	}
	return nil
}

// verifyBackupItem reads the item back and refuses to call the backup done
// unless the vault holds the payload byte for byte.
//
// 1Password's CLI has silently discarded fields before: it accepts an SSH Key
// document, echoes the key, exits 0, and stores nothing. A backup that is
// quietly wrong is worse than no backup, because it is only discovered on the
// new machine, with nothing left to fall back on.
func verifyBackupItem(ctx context.Context, cli secret.CLI, vault, title string, payload []byte) error {
	ref := secret.BuildInventoryRef(vault, title)
	out, err := cli.Run(ctx, "read", ref, "--no-newline")
	if err != nil {
		return fmt.Errorf("%s\n\nread the backup back to verify it: %w", onePasswordFailureHint(cli, err), err)
	}
	if string(out) != string(payload) {
		return fmt.Errorf("1Password did not store the backup verbatim (%d bytes written, %d read back); not trusting the backup", len(payload), len(out))
	}
	return nil
}

// previousBackup is the document this machine's item holds right now, kept both
// as it was read and as it parsed.
//
// The raw bytes matter as much as the parsed form: they are what a failed write
// is rolled back to, and re-encoding a parsed document would not put the vault
// back exactly as it was.
type previousBackup struct {
	raw  []byte
	file server.BackupFile
}

// readPreviousBackup reads the document this machine's item already holds,
// before anything replaces it.
//
// It is what separates a first backup from an update. A first backup has nothing
// to lose; an update holds the only off-machine copy of every key this machine
// had when it last ran - which is exactly the copy a machine that has since lost
// a key file needs. Reading it first is also what makes it possible to notice
// that the write about to happen would shrink the document.
//
// A read that fails for any other reason is fatal rather than ignored: writing
// anyway would replace a document nobody managed to look at, and the keys in it
// would be gone without an error to show for it.
//
// nil, nil means the item is not there yet, or is there but empty: nothing to
// preserve and nothing to inherit from.
func readPreviousBackup(ctx context.Context, cli secret.CLI, vault, title string) (*previousBackup, error) {
	out, err := cli.Run(ctx, "read", secret.BuildInventoryRef(vault, title), "--no-newline")
	if err != nil {
		if backupItemAbsent(err) {
			return nil, nil
		}
		// One message, ending with the wrapped error: it reads like prose for
		// the operator, but it is still an error value, so it keeps the shape
		// revive's error-strings rule asks for (no leading capital, no trailing
		// punctuation) and stays composable with %w.
		return nil, fmt.Errorf("%s\n\nrefusing to overwrite the backup %q in vault %q blind, because the document it holds may carry private keys this machine no longer has: fix the read failure, or deal with that item by hand and run the backup again: %w",
			onePasswordFailureHint(cli, err), title, vault, err)
	}
	if strings.TrimSpace(string(out)) == "" {
		return nil, nil
	}
	file, err := server.ParseBackup(out)
	if err != nil {
		return nil, fmt.Errorf("refusing to overwrite the backup %q in vault %q blind: it is there but could not be read as a backup document, and a document nobody can read is a document whose keys cannot be checked: fix it by hand in 1Password, or remove it and back up again: %w",
			title, vault, err)
	}
	return &previousBackup{raw: out, file: file}, nil
}

// backupItemAbsent reports whether a failed `op read` means the item simply is
// not there yet, which is the normal state of a first backup.
//
// The wording is the only signal available: the CLI exits 1 for every kind of
// failure. "could not find item" is the same text `op item edit` reports for a
// title that names nothing (see itemMissingMarker), and it is what scripts/opstub
// answers for a read of an item the vault does not hold; "item not found" covers
// the phrasing other CLI versions use for the same state. Anything else - a
// locked vault, a stalled call, a wrong account - fails closed, because the
// alternative is overwriting a document that was never read.
func backupItemAbsent(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, itemMissingMarker) || strings.Contains(msg, "item not found")
}

// inheritMissingKeys folds the private keys this machine can no longer read into
// the document about to be written, from the copy the vault already holds.
//
// Without it a local read failure is not an inconvenience but a deletion: the
// payload leaves the key out, `item edit` replaces the whole document, and the
// only remaining copy of that key is gone. The key was put in the vault for
// exactly this moment.
//
// dropped names the keys the previous document holds for servers that are no
// longer in servers.yaml at all. Those are deliberately not carried over - the
// user removed the server - but they are reported, because this write is what
// takes them out of the item.
func inheritMissingKeys(payload server.BackupFile, previous *server.BackupFile, unreadable []string) (file server.BackupFile, inherited, dropped []string) {
	if previous == nil || len(previous.Keys) == 0 {
		return payload, nil, nil
	}

	for _, name := range unreadable {
		if _, carried := payload.Keys[name]; carried {
			continue
		}
		key := previous.Keys[name]
		if key == "" {
			continue
		}
		if payload.Keys == nil {
			payload.Keys = make(map[string]string, len(previous.Keys))
		}
		payload.Keys[name] = key
		inherited = append(inherited, name)
	}

	names := make([]string, 0, len(previous.Keys))
	for name := range previous.Keys {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, carried := payload.Keys[name]; !carried {
			dropped = append(dropped, name)
		}
	}
	return payload, inherited, dropped
}

// rollbackBackupItem puts the document the vault held before this run back over
// the one that failed its read-back check, and reports what that attempt did.
//
// The check exists because the CLI has accepted a write, exited 0 and stored
// something else, and by the time it fires the item has already been replaced.
// Undoing the write is the only honest thing left, and the outcome has to be
// stated without hedging: an item that holds neither the previous document nor a
// verified new one must be treated as untrustworthy rather than as a backup.
func rollbackBackupItem(ctx context.Context, cli secret.CLI, vault, title string, previous *previousBackup, out io.Writer) {
	if previous == nil {
		_, _ = fmt.Fprintf(out, "⚠️  There is no previous document of %q to roll back to.\n", title)
		_, _ = fmt.Fprintln(out, "   Treat that item as untrusted: rewrite or delete it by hand in 1Password, then run the backup again.")
		return
	}
	doc, err := secret.BuildInventoryItem(title, string(previous.raw))
	if err != nil {
		_, _ = fmt.Fprintf(out, "⚠️  Could not rebuild the previous document of %q, so the failed write was NOT rolled back: %v\n", title, err)
		_, _ = fmt.Fprintln(out, "   Treat that item as untrusted: rewrite or delete it by hand in 1Password, then run the backup again.")
		return
	}
	if _, err := cli.RunWithStdin(ctx, doc, "item", "edit", title, "--vault", vault); err != nil {
		_, _ = fmt.Fprintf(out, "⚠️  Rolling %q back failed too, so it now holds neither the previous document nor a verified new one: %v\n", title, err)
		_, _ = fmt.Fprintln(out, "   Treat that item as untrusted: rewrite or delete it by hand in 1Password, then run the backup again.")
		return
	}
	_, _ = fmt.Fprintf(out, "↩️  Wrote the previous document of %q back, undoing the failed write.\n", title)
}

// backupMachineToOnePassword writes this machine's backup and verifies it, in
// three op calls in the steady state: read the document that is there, edit it,
// read it back. The very first backup costs two more, because the read finds
// nothing to inherit from and the edit has to fall back to create.
func backupMachineToOnePassword(ctx context.Context, cli secret.CLI, vault string, servers []server.Server, out io.Writer) error {
	title := backupTitle()

	// Read before write. Whatever the vault holds for this machine is the only
	// copy of the keys it may have lost since the last run, so the document has
	// to be seen before it is replaced - and a read that fails is a reason not
	// to write at all rather than a reason to write blind.
	previous, err := readPreviousBackup(ctx, cli, vault, title)
	if err != nil {
		return err
	}
	var previousFile *server.BackupFile
	if previous != nil {
		previousFile = &previous.file
	}

	file, unreadable := buildBackup(servers, out)
	file, inherited, dropped := inheritMissingKeys(file, previousFile, unreadable)
	payload, err := server.MarshalBackup(file)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(out, "⬆️  Backing up %d server(s) and %d private key(s) into %q in vault %q...\n",
		len(servers), len(file.Keys), title, vault)
	for _, name := range inherited {
		_, _ = fmt.Fprintf(out, "  ↺ %s: this machine cannot read the private key file, so the copy from the previous backup is kept\n", name)
	}
	if len(dropped) > 0 {
		_, _ = fmt.Fprintf(out, "⚠️  %d private key(s) in the previous backup belong to servers that are no longer in servers.yaml and leave this backup: %s\n", len(dropped), strings.Join(dropped, ", "))
		_, _ = fmt.Fprintln(out, "   1Password keeps the replaced document in the item's history, so they can still be recovered from there.")
	}

	if err := writeBackupItem(ctx, cli, vault, title, payload); err != nil {
		return err
	}
	if err := verifyBackupItem(ctx, cli, vault, title, payload); err != nil {
		// The item has been replaced by the time this fires, so the failure
		// report has to carry the state of the item with it.
		rollbackBackupItem(ctx, cli, vault, title, previous, out)
		return err
	}

	_, _ = fmt.Fprintf(out, "🎉 Backed up %d server(s) to %q in vault %q (%d bytes, verified byte for byte).\n", len(servers), title, vault, len(payload))
	_, _ = fmt.Fprintln(out, "   servers.yaml was left unchanged: local disk stays the source of truth.")
	_, _ = fmt.Fprintln(out, "   Restore on another machine with: ops 1p restore")
	return nil
}
