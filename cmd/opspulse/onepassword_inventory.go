package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/volcano6/opspulse/internal/secret"
	"github.com/volcano6/opspulse/internal/server"
	"gopkg.in/yaml.v3"
)

// The inventory backup is a different shape of thing from the per-server
// credential backup and restore it shares a command with. Credentials move per
// server and are named one at a time; the inventory is the whole servers.yaml
// in one item, because the file is machine-local and a new machine has nothing
// to name. It lives in this file so that the whole-file logic - merge, prompt,
// read-back verification - stays together instead of swelling onepassword.go.

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
		_, _ = fmt.Fprintln(p.out, "remote = the 1Password inventory backup")
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

// readInventoryFromOnePassword reads the servers recorded in the vault backup.
//
// exists distinguishes "no backup yet" from "a backup that failed to load",
// because the two call for opposite reactions: a push should simply create the
// item, while a pull has to tell the user to push from somewhere else first.
//
// The item is located by title before it is read, rather than inferring its
// absence from an `op read` failure: a wrong field name and a missing item both
// exit non-zero, and only one of them means "no backup".
func readInventoryFromOnePassword(ctx context.Context, cli secret.CLI, vault string, index *itemIndex) (servers []server.Server, exists bool, err error) {
	id, err := index.id(ctx, secret.InventoryItemTitle)
	if err != nil {
		return nil, false, err
	}
	if id == "" {
		return nil, false, nil
	}

	out, err := cli.Run(ctx, "read", secret.BuildInventoryRef(vault), "--no-newline")
	if err != nil {
		return nil, true, fmt.Errorf("%s\n\nread the inventory backup from vault %q: %w", onePasswordFailureHint(cli, err), vault, err)
	}
	if strings.TrimSpace(string(out)) == "" {
		// The item exists but was never filled in. That is an empty backup, not
		// a parse failure, and a push should be allowed to fill it.
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

// backupInventoryToOnePassword backs the whole servers.yaml up into one shared
// 1Password item, merging with whatever is already there.
//
// The merge is a union on purpose. Two machines backing up in turn must both
// keep their servers, so the backup can only grow from a backup; deleting a
// server is a deliberate act done by hand (see docs/reference/onepassword.md).
func backupInventoryToOnePassword(ctx context.Context, cli secret.CLI, vault string, index *itemIndex) error {
	store := server.NewDefaultStore()
	local, err := store.List()
	if err != nil {
		return err
	}
	if len(local) == 0 {
		return fmt.Errorf("nothing to back up: servers.yaml holds no servers")
	}

	remote, exists, err := readInventoryFromOnePassword(ctx, cli, vault, index)
	if err != nil {
		return err
	}

	prompt := newInventoryConflictPrompt(os.Stdin, os.Stdout, stdinIsInteractive(), onePasswordPreferLocal, onePasswordPreferRemote)
	merged, _, updated, _, err := server.MergeInventories(local, remote, prompt.decider())
	if err != nil {
		return inventoryConflictError(err)
	}
	if prompt.aborted {
		return errInventoryAborted
	}

	// Compare the merge result against the backup, not the counters: a server
	// only this machine knows about is not "added", but the backup still has to
	// learn about it.
	if exists && server.SameInventory(merged, remote) {
		fmt.Printf("✨ The inventory backup in vault %q already contains every server in servers.yaml (%d server(s)); nothing to write.\n", vault, len(merged))
		return nil
	}

	payload, err := marshalInventory(merged)
	if err != nil {
		return err
	}
	if err := writeOnePasswordItem(ctx, cli, vault, secret.InventoryItemTitle, secret.InventoryItemCategory, index, func(doc []byte) ([]byte, error) {
		return secret.FillInventoryItem(doc, string(payload))
	}); err != nil {
		return err
	}

	// Read the backup back before reporting success. 1Password's CLI has
	// silently discarded fields before (it accepts an SSH Key document, echoes
	// the key, exits 0, and stores nothing), and a backup that is quietly wrong
	// is worse than no backup: the user would only discover it on the new
	// machine, with nothing left to fall back on.
	stored, err := onePasswordResolver().Resolve(ctx, secret.BuildInventoryRef(vault))
	if err != nil {
		return fmt.Errorf("read the inventory back to verify it: %w", err)
	}
	if stored != string(payload) {
		return fmt.Errorf("1Password did not store the inventory verbatim (%d bytes written, %d read back); not trusting the backup", len(payload), len(stored))
	}

	fmt.Printf("🎉 Backed up the server list (%d server(s)) to %q in vault %q.\n", len(merged), secret.InventoryItemTitle, vault)
	if exists {
		if gained := len(merged) - len(remote); gained > 0 {
			fmt.Printf("   %d server(s) were not in the backup yet.\n", gained)
		}
		if updated > 0 {
			fmt.Printf("   %d definition(s) were updated.\n", updated)
		}
	}
	fmt.Println("   Restore on another machine with: ops 1p restore")
	return nil
}

// restoreInventoryFromOnePassword restores servers.yaml from the shared backup,
// merging into whatever this machine already has.
//
// The merge never deletes: a server that only this machine knows about stays,
// because a machine that holds a definition is evidence that the server exists,
// and losing it to a restore would be the worst possible outcome of asking for
// one.
//
// found reports whether a backup exists at all, so that the caller can carry on
// with the local servers.yaml instead of failing the whole restore.
func restoreInventoryFromOnePassword(ctx context.Context, cli secret.CLI, vault string, index *itemIndex) (found bool, err error) {
	store := server.NewDefaultStore()

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

	local, err := store.List()
	if err != nil {
		return true, err
	}

	prompt := newInventoryConflictPrompt(os.Stdin, os.Stdout, stdinIsInteractive(), onePasswordPreferLocal, onePasswordPreferRemote)
	merged, added, updated, _, err := server.MergeInventories(local, remote, prompt.decider())
	if err != nil {
		return true, inventoryConflictError(err)
	}
	if prompt.aborted {
		return true, errInventoryAborted
	}

	keptLocal := len(merged) - len(remote)
	// Compare the merge result against this machine's file: that is what decides
	// whether there is anything to write.
	if server.SameInventory(merged, local) {
		fmt.Printf("✨ servers.yaml already contains every server in the backup (%d server(s)); nothing to restore.\n", len(local))
		return true, nil
	}

	payload, err := marshalInventory(merged)
	if err != nil {
		return true, err
	}
	if err := store.Replace(payload); err != nil {
		return true, fmt.Errorf("write servers.yaml: %w", err)
	}

	fmt.Printf("🎉 Restored the server list from vault %q: %d server(s) total, %d added, %d updated.\n", vault, len(merged), added, updated)
	if keptLocal > 0 {
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
