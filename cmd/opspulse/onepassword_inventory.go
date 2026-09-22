package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/volcano6/opspulse/internal/secret"
	"github.com/volcano6/opspulse/internal/server"
	"gopkg.in/yaml.v3"
)

// The 1Password backup is one document per machine: the whole servers.yaml plus
// every private key that machine holds, in a single Secure Note. It lives in
// this file so that the whole-document logic - build, write, read back, merge,
// prompt - stays together instead of swelling onepassword.go.
//
// One document rather than one item per credential because every op call is a
// full round trip through the Desktop App (3-9s, uncached on Windows): a
// large fleet ran to dozens of calls and over two minutes backing up, against two
// calls and a few seconds now.

// errInventoryAborted reports that the user quit at a conflict prompt. It is a
// cancellation, not a failure, but it must still reach the exit code: a merge
// that was declined halfway must not be written.
var errInventoryAborted = errors.New("inventory merge cancelled by user")

// validatePreferFlags rejects --prefer-* combinations that contradict each
// other, so a typo cannot look like a decision that was honoured.
func validatePreferFlags(preferLocal, preferRemote bool) error {
	if preferLocal && preferRemote {
		return fmt.Errorf("--prefer-local and --prefer-remote contradict each other; pick one")
	}
	return nil
}

// inventoryConflictError adds the way out to the conflict report that
// MergeInventories returns. The raw error names the servers and the differing
// fields; what it cannot know is which flags this command exposes.
func inventoryConflictError(err error) error {
	return fmt.Errorf("%w\n\nuse --prefer-local or --prefer-remote to decide every conflict the same way, or edit the %q item in 1Password by hand", err, secret.InventoryItemTitle)
}

// inventoryConflictPrompt resolves merge conflicts, either from the --prefer-*
// flags or by asking.
//
// It is a struct rather than a bare closure because an interactive session has
// state to carry: a sticky "all" answer that covers the remaining conflicts, and
// a quit that the caller has to notice before writing anything.
type inventoryConflictPrompt struct {
	in  *bufio.Reader
	out io.Writer

	interactive  bool
	preferLocal  bool
	preferRemote bool

	sticky    server.MergeDecision
	hasSticky bool
	aborted   bool

	printedLegend bool
}

func newInventoryConflictPrompt(in io.Reader, out io.Writer, interactive, preferLocal, preferRemote bool) *inventoryConflictPrompt {
	if in == nil {
		in = strings.NewReader("")
	}
	if out == nil {
		out = io.Discard
	}
	return &inventoryConflictPrompt{
		// One reader for the whole session: a fresh bufio.Scanner per prompt
		// would over-read and swallow the answers to the prompts after it.
		in:           bufio.NewReader(in),
		out:          out,
		interactive:  interactive,
		preferLocal:  preferLocal,
		preferRemote: preferRemote,
	}
}

// decider returns the callback MergeInventories should use, or nil when a
// conflict must be reported instead of guessed at.
//
// A non-interactive shell with no --prefer-* flag is exactly that case.
// Defaulting there would silently pick a host definition nobody approved, and
// the user would only find out when a connection went to the wrong machine.
func (p *inventoryConflictPrompt) decider() func(server.MergeConflict) server.MergeDecision {
	if !p.interactive && !p.preferLocal && !p.preferRemote {
		return nil
	}
	return p.decide
}

func (p *inventoryConflictPrompt) decide(c server.MergeConflict) server.MergeDecision {
	switch {
	case p.preferRemote:
		return server.TakeRemote
	case p.preferLocal:
		return server.KeepLocal
	case p.hasSticky:
		return p.sticky
	}

	if !p.printedLegend {
		_, _ = fmt.Fprintln(p.out, "\nlocal  = this machine's servers.yaml")
		_, _ = fmt.Fprintln(p.out, "remote = the 1Password backup")
		p.printedLegend = true
	}

	for {
		_, _ = fmt.Fprintf(p.out, "\n⚠️  %s\n", c.Summary())
		_, _ = fmt.Fprint(p.out, "   [l]ocal / [r]emote / [a]ll-remote / [A]ll-local / [q]uit: ")
		choice, ok := p.readLine()
		if !ok {
			_, _ = fmt.Fprintln(p.out)
			p.aborted = true
			return server.KeepLocal
		}
		switch choice {
		case "", "l", "local":
			return server.KeepLocal
		case "r", "remote":
			return server.TakeRemote
		case "a", "all-remote":
			p.sticky, p.hasSticky = server.TakeRemote, true
			return server.TakeRemote
		case "A", "all", "all-local":
			p.sticky, p.hasSticky = server.KeepLocal, true
			return server.KeepLocal
		case "q", "quit":
			p.aborted = true
			return server.KeepLocal
		default:
			_, _ = fmt.Fprintln(p.out, "   Please answer l, r, a, A or q.")
		}
	}
}

// readLine returns the trimmed answer, and false only at end of input. A blank
// line is a real answer (the default), so it must not be confused with EOF.
func (p *inventoryConflictPrompt) readLine() (string, bool) {
	line, err := p.in.ReadString('\n')
	if err != nil && line == "" {
		return "", false
	}
	return strings.TrimSpace(line), true
}

// backupTitle is the vault item this machine backs up into.
func backupTitle() string {
	return secret.InventoryItemTitleFor(machineName())
}

// readLocalKeys collects the private key of every server that names one.
//
// A key file that cannot be read is reported and skipped rather than failing the
// backup: a missing file is exactly the state a restore is meant to repair, and
// refusing to back up the other twelve servers over it would be perverse. The
// server still travels in the payload, so its host, user and password are not
// lost - only the key is.
func readLocalKeys(servers []server.Server, out io.Writer) map[string]string {
	keys := make(map[string]string, len(servers))
	for i := range servers {
		srv := &servers[i]
		if strings.TrimSpace(srv.KeyPath) == "" {
			continue
		}
		if secret.Is1PRef(srv.KeyPath) {
			// Refused before this point; belt and braces, since uploading the
			// literal reference would plant a pointer nothing resolves.
			continue
		}
		data, err := os.ReadFile(expandHome(srv.KeyPath))
		if err != nil {
			_, _ = fmt.Fprintf(out, "⚠️  %q: no private key in the backup: cannot read %s: %v\n", srv.Name, srv.KeyPath, err)
			continue
		}
		keys[srv.Name] = string(data)
	}
	return keys
}

// buildBackup assembles this machine's backup document, returning the payload
// and the number of private keys that made it in.
//
// Passwords need no special handling: servers.yaml holds them as plaintext, so
// they travel inside Servers. Only private keys live outside the file, which is
// what readLocalKeys adds - and the count is what the progress line reports, so
// a key that could not be read is visibly missing rather than silently absent.
func buildBackup(servers []server.Server, out io.Writer) ([]byte, int, error) {
	keys := readLocalKeys(servers, out)
	file := server.BackupFile{
		Machine: machineName(),
		Servers: servers,
		Keys:    keys,
	}
	payload, err := server.MarshalBackup(file)
	if err != nil {
		return nil, 0, err
	}
	return payload, len(keys), nil
}

// itemMissingMarker is the fragment `op item edit` reports when the title names
// no item. Verified against op 2.39.0: "unable to process line 1: could not find
// item to edit".
//
// It is the only signal available for "this is a first backup", and it has to be
// matched on the message rather than the exit code, which is 1 for every kind of
// failure.
const itemMissingMarker = "could not find item"

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

// backupMachineToOnePassword writes this machine's backup and verifies it, in
// two op calls in the steady state (three on the very first backup, where the
// item has to be created).
func backupMachineToOnePassword(ctx context.Context, cli secret.CLI, vault string, servers []server.Server, out io.Writer) error {
	title := backupTitle()
	payload, keys, err := buildBackup(servers, out)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(out, "⬆️  Backing up %d server(s) and %d private key(s) into %q in vault %q...\n",
		len(servers), keys, title, vault)
	if err := writeBackupItem(ctx, cli, vault, title, payload); err != nil {
		return err
	}
	if err := verifyBackupItem(ctx, cli, vault, title, payload); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(out, "🎉 Backed up %d server(s) to %q in vault %q (%d bytes, verified byte for byte).\n", len(servers), title, vault, len(payload))
	_, _ = fmt.Fprintln(out, "   servers.yaml was left unchanged: local disk stays the source of truth.")
	_, _ = fmt.Fprintln(out, "   Restore on another machine with: ops 1p restore")
	return nil
}

// backupBlob is one machine's backup as read from the vault, with the title it
// came from so that a report can name it.
type backupBlob struct {
	title string
	file  server.BackupFile
}

// readBackupBlobs reads every per-machine backup in the vault.
//
// The titles come from the index the command has already paid for, so finding
// the backups costs nothing; only the bodies are read, one call each. The
// historical shared item is deliberately excluded here - it holds a bare
// servers.yaml rather than a backup document, and it is read by
// readInventoryFromOnePassword on the fallback path.
//
// Titles are visited in sorted order so that a run is reproducible: with two
// machines holding a key for the same server, which one wins must not depend on
// Go's map iteration order.
func readBackupBlobs(ctx context.Context, cli secret.CLI, vault string, index *itemIndex) ([]backupBlob, error) {
	titles, err := index.titles(ctx)
	if err != nil {
		return nil, err
	}

	var names []string
	for title := range titles {
		if title != secret.InventoryItemTitle && secret.IsInventoryItemTitle(title) {
			names = append(names, title)
		}
	}
	sort.Strings(names)

	blobs := make([]backupBlob, 0, len(names))
	for _, title := range names {
		out, err := cli.Run(ctx, "read", secret.BuildInventoryRef(vault, title), "--no-newline")
		if err != nil {
			return nil, fmt.Errorf("%s\n\nread the backup %q from vault %q: %w", onePasswordFailureHint(cli, err), title, vault, err)
		}
		if strings.TrimSpace(string(out)) == "" {
			// The item exists but was never filled in. That is an empty backup,
			// not a parse failure.
			continue
		}
		file, err := server.ParseBackup(out)
		if err != nil {
			return nil, fmt.Errorf("the backup %q in vault %q is not usable: %w", title, vault, err)
		}
		blobs = append(blobs, backupBlob{title: title, file: file})
	}
	return blobs, nil
}

// mergeBackupServers folds every machine's server list into this machine's.
//
// The merge is a union on purpose, exactly as the single shared item was: two
// machines that have each backed up must both keep their servers, so a restore
// can only ever add. Deleting a server is a deliberate act done by hand (see
// docs/reference/onepassword.md).
//
// Private keys are deliberately not touched here. They are collected separately
// by collectBackupCredentials, because deciding whether one may be written is a
// per-server question that belongs with the write - the local file may hold the
// same key, a different one, or nothing at all.
func mergeBackupServers(local []server.Server, blobs []backupBlob, prompt *inventoryConflictPrompt) (merged []server.Server, added, updated int, err error) {
	merged = local

	for _, blob := range blobs {
		var blobAdded, blobUpdated int
		merged, blobAdded, blobUpdated, _, err = server.MergeInventories(merged, blob.file.Servers, prompt.decider())
		if err != nil {
			return nil, 0, 0, inventoryConflictError(err)
		}
		if prompt.aborted {
			return nil, 0, 0, errInventoryAborted
		}
		added += blobAdded
		updated += blobUpdated
	}
	return merged, added, updated, nil
}

// backupCredentials is what the per-machine backups hold for one server.
//
// Either field may be absent: a key file that could not be read at backup time
// leaves the key out, and a server that authenticates by password alone never
// had one.
type backupCredentials struct {
	key      []byte
	password string
}

// collectBackupCredentials indexes every server's credentials across all the
// machine backups.
//
// The first blob in sorted title order wins for a server that appears in more
// than one, so that which machine's key a restore uses is reproducible rather
// than dependent on Go's map iteration order. Passwords travel inside Servers,
// because servers.yaml holds them as plaintext.
func collectBackupCredentials(blobs []backupBlob) map[string]backupCredentials {
	creds := make(map[string]backupCredentials)
	for _, blob := range blobs {
		for name, key := range blob.file.Keys {
			c := creds[name]
			if len(c.key) == 0 {
				c.key = []byte(key)
			}
			creds[name] = c
		}
		for _, srv := range blob.file.Servers {
			c := creds[srv.Name]
			if c.password == "" {
				c.password = srv.Password
			}
			creds[srv.Name] = c
		}
	}
	return creds
}

// readInventoryFromOnePassword reads the servers recorded in the historical
// shared vault item.
//
// This is the fallback for a vault that only holds the old format. It is read
// side only: nothing writes this item any more, so an upgraded machine stops
// refreshing it, and a restore from it can never see a server added since.
//
// exists distinguishes "no backup yet" from "a backup that failed to load",
// because the two call for opposite reactions.
func readInventoryFromOnePassword(ctx context.Context, cli secret.CLI, vault string, index *itemIndex) (servers []server.Server, exists bool, err error) {
	id, err := index.id(ctx, secret.InventoryItemTitle)
	if err != nil {
		return nil, false, err
	}
	if id == "" {
		return nil, false, nil
	}

	out, err := cli.Run(ctx, "read", secret.BuildInventoryRef(vault, secret.InventoryItemTitle), "--no-newline")
	if err != nil {
		return nil, true, fmt.Errorf("%s\n\nread the inventory backup from vault %q: %w", onePasswordFailureHint(cli, err), vault, err)
	}
	if strings.TrimSpace(string(out)) == "" {
		// The item exists but was never filled in. That is an empty backup, not
		// a parse failure.
		return nil, true, nil
	}

	servers, err = server.ParseConfig(out)
	if err != nil {
		return nil, true, fmt.Errorf("the inventory backup in vault %q is not a valid servers.yaml: %w", vault, err)
	}
	return servers, true, nil
}

// marshalInventory renders servers the way servers.yaml stores them, so that a
// restore is byte-comparable with the file it came from.
func marshalInventory(servers []server.Server) ([]byte, error) {
	payload, err := yaml.Marshal(server.ConfigFile{Servers: servers})
	if err != nil {
		return nil, fmt.Errorf("encode the server inventory: %w", err)
	}
	return payload, nil
}

// restoreInventoryFromOnePassword merges a vault backup into servers.yaml,
// without ever deleting.
//
// A server that only this machine knows about stays, because a machine that
// holds a definition is evidence that the server exists, and losing it to a
// restore would be the worst possible outcome of asking for one.
//
// found reports whether a backup exists at all, so that the caller can carry on
// with the local servers.yaml instead of failing the whole restore.
//
// The per-machine backups are passed in rather than read here: the caller needs
// them anyway to restore private keys, and reading them twice would double the
// op calls on the one path where they matter.
func restoreInventoryFromOnePassword(ctx context.Context, cli secret.CLI, vault string, index *itemIndex, blobs []backupBlob) (found bool, err error) {
	store := server.NewDefaultStore()
	prompt := newInventoryConflictPrompt(os.Stdin, os.Stdout, stdinIsInteractive(), onePasswordPreferLocal, onePasswordPreferRemote)

	local, err := store.List()
	if err != nil {
		return false, err
	}

	var (
		merged   []server.Server
		added    int
		updated  int
		keptFrom string
	)
	if len(blobs) > 0 {
		merged, added, updated, err = mergeBackupServers(local, blobs, prompt)
		if err != nil {
			return true, err
		}
		keptFrom = fmt.Sprintf("%d backup item(s)", len(blobs))
	} else {
		remote, exists, err := readInventoryFromOnePassword(ctx, cli, vault, index)
		if err != nil {
			return false, err
		}
		if !exists {
			return false, nil
		}
		if len(remote) == 0 {
			fmt.Printf("The inventory backup in vault %q is empty; nothing to restore.\n", vault)
			return true, nil
		}
		merged, added, updated, _, err = server.MergeInventories(local, remote, prompt.decider())
		if err != nil {
			return true, inventoryConflictError(err)
		}
		if prompt.aborted {
			return true, errInventoryAborted
		}
		keptFrom = "the legacy shared item"
	}
	if len(merged) == 0 {
		fmt.Printf("The inventory backup in vault %q is empty; nothing to restore.\n", vault)
		return true, nil
	}

	// Compare the merge result against this machine's file: that is what decides
	// whether there is anything to write.
	if server.SameInventory(merged, local) {
		fmt.Printf("✨ servers.yaml already contains every server in %s (%d server(s)); nothing to restore.\n", keptFrom, len(local))
		return true, nil
	}

	payload, err := marshalInventory(merged)
	if err != nil {
		return true, err
	}
	if err := store.Replace(payload); err != nil {
		return true, fmt.Errorf("write servers.yaml: %w", err)
	}

	fmt.Printf("🎉 Restored the server list from vault %q (%s): %d server(s) total, %d added, %d updated.\n", vault, keptFrom, len(merged), added, updated)
	if keptLocal := len(merged) - len(local); keptLocal > 0 {
		fmt.Printf("   Kept %d server(s) that only this machine had; a restore never deletes.\n", keptLocal)
	}
	fmt.Println("   Note: a restore rewrites servers.yaml, so YAML comments in it are not preserved.")
	warnMissingLocalKeyFiles(os.Stdout, merged)
	return true, nil
}

// serversWithMissingKeyFiles returns the names of servers whose private key is
// a local path that does not exist on this machine.
//
// A server can be configured perfectly and still be unusable: servers.yaml
// survives a reinstall, a rename, or a careless `rm` in ~/.ssh, and nothing
// notices until the first connection dies with a bare "no such file".
func serversWithMissingKeyFiles(servers []server.Server) []string {
	var missing []string
	for _, srv := range servers {
		if srv.KeyPath == "" || secret.Is1PRef(srv.KeyPath) {
			continue
		}
		if _, err := os.Stat(expandHome(srv.KeyPath)); err != nil {
			missing = append(missing, srv.Name)
		}
	}
	return missing
}

// warnMissingLocalKeyFiles points out restored servers whose private key is a
// local path that does not exist here.
//
// This is the expected outcome of a restore onto a fresh machine: the inventory
// travelled, the key files did not. Saying so once, with the command that fixes
// it, is far better than letting the first connection fail with a bare "no such
// file".
func warnMissingLocalKeyFiles(w io.Writer, servers []server.Server) {
	missing := serversWithMissingKeyFiles(servers)
	if len(missing) == 0 {
		return
	}
	_, _ = fmt.Fprintf(w, "\n⚠️  %d restored server(s) point at a private key file that is not on this machine: %s\n", len(missing), strings.Join(missing, ", "))
	_, _ = fmt.Fprintln(w, "   Run 'ops 1p restore <name>' to write the backed-up key to disk.")
}
