package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/platform"
	"github.com/volcano6/opspulse/internal/secret"
	"github.com/volcano6/opspulse/internal/server"
	"github.com/volcano6/opspulse/internal/sftp"
	"golang.org/x/crypto/ssh"
)

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

// Flags shared by 'ops 1p backup' and 'ops 1p restore'. The two commands never
// run together, and both mean the same thing by every one of these, so sharing
// them keeps the two flag surfaces from drifting apart.
var (
	onePasswordVault        string
	onePasswordAccount      string
	onePasswordPreferLocal  bool
	onePasswordPreferRemote bool
)

// onePasswordBackupParallel caps how many servers 'ops 1p backup' uploads at
// once.
//
// Every op invocation costs a full round trip through the Desktop App, and the
// Windows build has no cache, so a sequential batch spends nearly all of its
// wall time waiting. Overlapping the servers is the only large win available:
// measured on WSL, eight concurrent `op item get` calls take 20s against 90s
// for the same eight run one after another. It is not linear - the Desktop App
// queues authorisations - so the default stays deliberately low rather than
// saturating it.
var onePasswordBackupParallel int

const defaultOnePasswordBackupParallel = 4

// wslWindowsBackupParallel replaces the default when the CLI is the Windows
// build driven from WSL.
//
// There the ceiling is not the Desktop App but the WSL interop relay, which
// starts refusing spawns with "accept4 failed 110" (ETIMEDOUT) once enough
// long-lived Windows processes are alive: measured from WSL, spawning op.exe is
// clean with two such processes and fails intermittently at four. A four-wide
// batch therefore lost servers outright, so the default is lowered on this path
// alone. An explicit -p still wins, and secret.CLI retries a refused spawn.
const wslWindowsBackupParallel = 2

// backupParallel decides how many servers 'ops 1p backup' uploads at once.
//
// requested is the -p value: positive means the user chose it and it is taken
// as given, while zero or less means the default for this CLI applies. The
// result never exceeds the number of servers, so a small inventory does not
// leave idle workers waiting on a semaphore nobody can fill.
func backupParallel(requested int, cli secret.CLI, servers int) int {
	if requested <= 0 {
		requested = defaultOnePasswordBackupParallel
		// The Windows build driven from WSL is limited by the interop relay,
		// not by the Desktop App; see wslWindowsBackupParallel.
		if cli.IsWindowsBinary && platform.IsWSL() {
			requested = wslWindowsBackupParallel
		}
	}
	if requested > servers {
		requested = servers
	}
	return requested
}

// itemIndex memoises a vault's item titles for the duration of one command.
//
// `op item list` is a full round trip through the Desktop App, and a batch used
// to pay it once per credential. A single snapshot is enough because OpsPulse
// only ever looks up the items it creates itself, whose titles are unique per
// server, so nothing can be created underneath the snapshot mid-run.
type itemIndex struct {
	mu      sync.Mutex
	cli     secret.CLI
	vault   string
	loaded  bool
	byTitle map[string]string
}

func newItemIndex(cli secret.CLI, vault string) *itemIndex {
	return &itemIndex{cli: cli, vault: vault, byTitle: make(map[string]string)}
}

// load fetches the vault's item list once. The first caller pays for the round
// trip; every later caller observes the same snapshot.
func (ix *itemIndex) load(ctx context.Context) error {
	ix.mu.Lock()
	defer ix.mu.Unlock()

	if ix.loaded {
		return nil
	}
	out, err := ix.cli.Run(ctx, "item", "list", "--vault", ix.vault, "--format", "json")
	if err != nil {
		return fmt.Errorf("%s\n\nlist items in 1Password vault %q: %w", onePasswordFailureHint(ix.cli, err), ix.vault, err)
	}
	var items []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(out, &items); err != nil {
		return fmt.Errorf("parse 1Password item list: %w", err)
	}
	for _, item := range items {
		ix.byTitle[item.Title] = item.ID
	}
	ix.loaded = true
	return nil
}

// id returns the item ID for title, or "" when the vault holds no such item.
func (ix *itemIndex) id(ctx context.Context, title string) (string, error) {
	if err := ix.load(ctx); err != nil {
		return "", err
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.byTitle[title], nil
}

// titles returns the set of item titles in the vault, sharing the same single
// listing as id. Restore needs the whole set to match servers by item name, and
// paying for a second `op item list` to get it would be a second Desktop App
// authorisation on a platform that has no cache to absorb it.
func (ix *itemIndex) titles(ctx context.Context) (map[string]struct{}, error) {
	if err := ix.load(ctx); err != nil {
		return nil, err
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	set := make(map[string]struct{}, len(ix.byTitle))
	for title := range ix.byTitle {
		set[title] = struct{}{}
	}
	return set, nil
}

// Flags that only make sense while writing values back onto this machine.
var (
	onePasswordRestoreYes   bool
	onePasswordRestoreForce bool
)

// onePasswordFilter narrows 'ops 1p status'. It is the only command that still
// needs a selector: backup is always the whole machine, and restore takes
// server names positionally.
var onePasswordFilter string

var onePasswordCmd = &cobra.Command{
	Use:     "1p",
	Aliases: []string{"1password", "onepassword"},
	Short:   "Back up local SSH credentials to 1Password, or restore them onto a new machine",
	Long: `1Password is a backup and cross-machine sync target, not a runtime dependency.

  ops 1p backup             Upload every local key/password and the whole servers.yaml
  ops 1p restore            Write them back to local disk (and restore servers.yaml)
  ops 1p status             Show which servers have local credentials
  ops 1p config             Show or change the remembered vault and account

Credentials normally live on local disk: servers.yaml holds a key path or a
plaintext password, and 'ops ssh' / 'ops exec' / 'ops cp' read them directly with
no 1Password round trip. Nothing here runs during a normal connection, which is
what keeps those commands from prompting.

Keys are stored in Login items titled opspulse_<server>_key, inside a custom
concealed field; passwords go into Login items titled opspulse_<server>_password.
servers.yaml itself is backed up as one shared item, opspulse_inventory.

You normally do not have to name a vault at all: OpsPulse uses the one you
remembered with 'ops 1p config --vault <name>', then $OP_VAULT, and otherwise the
only vault the account can see. The account works the same way, with $OP_ACCOUNT
taking precedence over the remembered value.`,
}

var onePasswordBackupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Upload every local credential and the whole servers.yaml to 1Password",
	Long: `Back up this machine's credentials and server list to 1Password.

Every server with a local key file or a plaintext password is uploaded, and the
whole servers.yaml is stored in the shared opspulse_inventory item. It is
deliberately unconditional: no server selection, no skip list.

servers.yaml is NOT rewritten. Local disk stays the source of truth, so a backup
never changes how 'ops ssh' connects and never turns a working server into one
that depends on 1Password being unlocked.

  ops 1p backup
  ops 1p backup --vault Private
  ops 1p backup --prefer-remote    # the backup wins inventory conflicts

A server still holding an 'op://' reference is refused outright: uploading it
would push a stale reference into the backup. Run 'ops 1p restore' to migrate it
to a local credential first.

The inventory backup merges rather than overwrites, so two machines can both
back up without losing each other's servers. Deleting a server from the backup
is therefore done by hand: remove it locally first, then edit the item.

Servers are backed up several at a time because every op call is a round trip
through the Desktop App. The default is 4, or 2 when OpsPulse is driving the
Windows op.exe from WSL, where the interop relay starts refusing spawns before
the Desktop App becomes the bottleneck. An explicit -p always wins.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runBackupToOnePassword(cmd.Context())
	},
}

var onePasswordRestoreCmd = &cobra.Command{
	Use:   "restore [server...]",
	Short: "Write 1Password-hosted credentials back onto local disk",
	Long: `Restore credentials from 1Password onto this machine.

Without arguments this is the whole off-ramp: servers.yaml is restored from the
shared opspulse_inventory item first, then every server's key is written to
~/.ssh/opspulse_<server> and every password into servers.yaml. That is all a new
machine needs after installing the 1Password CLI.

  ops 1p restore               # the server list and every credential
  ops 1p restore web db-01     # only these servers' credentials
  ops 1p restore --yes         # unattended

With arguments only the named servers' credentials are restored, and servers.yaml
is left alone. A name that is not in servers.yaml is an error rather than a
silent skip, since the usual cause is restoring credentials before the list.

Because a password can only come back as plaintext, OpsPulse asks for
confirmation whenever the restore would write one. In a non-interactive shell the
command refuses instead of hanging, unless --yes says the answer up front.

A local key file is only replaced when it holds a different key. The comparison
is by public key, so a key that 1Password returns in another format is recognised
as the same key rather than treated as a conflict; --force overrides.

Servers whose servers.yaml still holds 'op://' references are migrated to local
credentials as part of the restore. That compatibility path is temporary.`,
	Args: cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRestoreFromOnePassword(cmd.Context(), args)
	},
}

// legacyPushCmd and legacyPullCmd retire the old names loudly.
//
// They are not aliases: push/pull carried flags that no longer exist, and
// forwarding would mean either parsing a dead flag set or silently ignoring it.
// Their flags stay registered (hidden) so that 'ops 1p push --materialize'
// reports the rename instead of Cobra's "unknown flag" error.
var onePasswordLegacyPushCmd = &cobra.Command{
	Use:    "push",
	Short:  "Retired: use 'ops 1p backup'",
	Hidden: true,
	Args:   cobra.ArbitraryArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		return fmt.Errorf("'ops 1p push' has been retired; use 'ops 1p backup' instead")
	},
}

var onePasswordLegacyPullCmd = &cobra.Command{
	Use:    "pull",
	Short:  "Retired: use 'ops 1p restore'",
	Hidden: true,
	Args:   cobra.ArbitraryArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		return fmt.Errorf("'ops 1p pull' has been retired; use 'ops 1p restore' instead")
	},
}

var onePasswordStatusRemote bool

var onePasswordStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show which servers have local credentials, and which are backed up",
	Long: `Show where each server's credentials live.

Offline by default: it only reads servers.yaml, so it never contacts 1Password
and never prompts. Pass --remote to also ask the vault which servers have a
backup, which does require authentication.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runOnePasswordStatus(cmd.Context())
	},
}

var (
	onePasswordConfigVault   string
	onePasswordConfigAccount string
	onePasswordConfigUnset   bool
	onePasswordConfigOffline bool
)

var onePasswordConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "Show or change the remembered 1Password vault and account",
	Long: `Show or change the 1Password defaults OpsPulse remembers.

Without flags it prints the effective target plus the accounts and vaults the
current account can see. With flags it records a default so that 'ops 1p backup'
stops asking for --vault/--account every time.

Pass --offline to read or change the remembered defaults without contacting the
CLI, which is what you want when the vault cannot be unlocked right now.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runOnePasswordConfig(cmd.Context())
	},
}

// runBackupToOnePassword uploads this machine's credentials and server list to
// 1Password. It never writes servers.yaml: local disk stays the source of truth,
// so a backup cannot change how 'ops ssh' connects.
func runBackupToOnePassword(ctx context.Context) error {
	if err := validatePreferFlags(onePasswordPreferLocal, onePasswordPreferRemote); err != nil {
		return err
	}

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
	vault, err := resolveAndValidateVault(ctx, cli, onePasswordVault, true)
	if err != nil {
		return err
	}

	parallel := backupParallel(onePasswordBackupParallel, cli, len(servers))

	// One snapshot of the vault, shared by every server, instead of an
	// `op item list` per credential.
	index := newItemIndex(cli, vault)

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		sem      = make(chan struct{}, parallel)
		backedUp int
		failed   []string
	)

	for i := range servers {
		wg.Add(1)
		go func(srv *server.Server) {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				mu.Lock()
				failed = append(failed, srv.Name)
				mu.Unlock()
				return
			}

			// Each server's report is built up privately and printed in one
			// piece, so concurrent servers cannot interleave halfway through a
			// line. The cost is that a server's progress only appears once it
			// finishes; with several in flight that still lands regularly.
			var report bytes.Buffer
			uploaded, err := backupServerToOnePassword(ctx, cli, srv, vault, index, &report)

			mu.Lock()
			defer mu.Unlock()
			_, _ = os.Stdout.Write(report.Bytes())
			if err != nil {
				// One server's failure must not strand the rest of the batch. The
				// servers are independent, and re-running finishes a partial
				// backup; aborting instead would leave the user guessing which
				// ones were already uploaded.
				fmt.Printf("❌ %q: %v\n", srv.Name, err)
				failed = append(failed, srv.Name)
				return
			}
			if uploaded {
				backedUp++
			}
		}(&servers[i])
	}
	wg.Wait()

	// The inventory goes last, so that a per-server failure has already been
	// reported by the time the shared item is refreshed. It shares the same
	// snapshot: the titles are disjoint, so one listing covers the whole run.
	if err := backupInventoryToOnePassword(ctx, cli, vault, index); err != nil {
		return err
	}

	fmt.Println()
	if backedUp > 0 {
		fmt.Printf("🎉 Backed up credentials for %d server(s) to vault %q.\n", backedUp, vault)
	}
	if backedUp == 0 && len(failed) == 0 {
		fmt.Println("Nothing to back up: no server has a local private key or password.")
	}
	fmt.Println("   servers.yaml was left unchanged: local disk stays the source of truth.")
	if len(failed) > 0 {
		return fmt.Errorf("failed to back up %d of %d server(s): %s", len(failed), len(servers), strings.Join(failed, ", "))
	}
	return nil
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

// backupServerToOnePassword uploads whatever local credentials a server keeps -
// a private key file, a plaintext password, or both. It reports whether
// anything was uploaded. Progress goes to out rather than stdout so that a
// concurrent batch can print each server's report as one piece.
func backupServerToOnePassword(ctx context.Context, cli secret.CLI, srv *server.Server, vault string, index *itemIndex, out io.Writer) (bool, error) {
	keyUploaded, err := uploadPrivateKeyToOnePassword(ctx, cli, srv, vault, index, out)
	if err != nil {
		return false, err
	}
	passwordUploaded, err := uploadPasswordToOnePassword(ctx, cli, srv, vault, index, out)
	if err != nil {
		return false, err
	}
	if keyUploaded || passwordUploaded {
		return true, nil
	}
	if strings.TrimSpace(srv.KeyPath) == "" && strings.TrimSpace(srv.Password) == "" {
		_, _ = fmt.Fprintf(out, "⏭️  Skipping %q: no private key and no password configured (it relies on the default SSH key).\n", srv.Name)
	}
	return false, nil
}

// uploadPrivateKeyToOnePassword copies a server's key file into a Login item's
// concealed field, leaving servers.yaml alone.
//
// The item is a Login rather than an "SSH Key" because the 1Password CLI cannot
// write SSH Key items: `op item create` drops the private_key field while
// exiting 0, and `op item edit` refuses outright. See secret.sshKeyManagedFieldID.
func uploadPrivateKeyToOnePassword(ctx context.Context, cli secret.CLI, srv *server.Server, vault string, index *itemIndex, out io.Writer) (bool, error) {
	if strings.TrimSpace(srv.KeyPath) == "" {
		return false, nil
	}

	expanded := expandHome(srv.KeyPath)
	keyData, err := os.ReadFile(filepath.Clean(expanded)) // #nosec G304 -- path comes from the user's own servers.yaml
	if err != nil {
		_, _ = fmt.Fprintf(out, "⚠️  Skipping the key for %q: cannot read %s: %v\n", srv.Name, srv.KeyPath, err)
		return false, nil
	}

	title := secret.SSHKeyItemTitle(srv.Name)
	ref := secret.BuildSSHKeyRef(vault, title)
	// Derive the expected public key from the bytes being uploaded, not from the
	// sibling .pub file: a stale .pub would make the read-back below pass or
	// fail for the wrong reason. crypto/ssh parses fewer formats than ssh(1), so
	// fall back to the .pub file when the private key is one Go cannot read.
	expectedPublic, parsed := publicKeyLine(keyData)
	if !parsed {
		expectedPublic = authorizedKeyFor(expanded, keyData)
	}

	_, _ = fmt.Fprintf(out, "⬆️  Backing up key for %q (%s) into 1Password vault %q...\n", srv.Name, srv.KeyPath, vault)
	if err := writeOnePasswordItem(ctx, cli, vault, title, onePasswordLoginCategory, index, func(doc []byte) ([]byte, error) {
		return secret.FillSSHKeyItem(doc, title, string(keyData))
	}); err != nil {
		return false, err
	}

	// The CLI exits 0 on a write it silently discarded, so "no error" says
	// nothing about whether the key is there. A backup that is quietly wrong is
	// worse than no backup, because it is only discovered on the new machine.
	if err := verifyPushedSSHKey(ctx, ref, string(keyData), expectedPublic); err != nil {
		return false, fmt.Errorf("uploaded the key for %q but could not read it back, so the backup is not trustworthy: %w", srv.Name, err)
	}
	_, _ = fmt.Fprintf(out, "✅ %q: key backed up as %s\n", srv.Name, ref)
	return true, nil
}

// uploadPasswordToOnePassword copies a server's plaintext password into a
// "Login" item, leaving servers.yaml alone.
func uploadPasswordToOnePassword(ctx context.Context, cli secret.CLI, srv *server.Server, vault string, index *itemIndex, out io.Writer) (bool, error) {
	if strings.TrimSpace(srv.Password) == "" {
		return false, nil
	}

	title := secret.PasswordItemTitle(srv.Name)
	ref := secret.BuildPasswordRef(vault, title)
	_, _ = fmt.Fprintf(out, "⬆️  Backing up password for %q (user %s) into 1Password vault %q...\n", srv.Name, srv.User, vault)
	if err := writeOnePasswordItem(ctx, cli, vault, title, onePasswordLoginCategory, index, func(doc []byte) ([]byte, error) {
		return secret.FillLoginItem(doc, title, srv.User, srv.Password)
	}); err != nil {
		return false, err
	}
	_, _ = fmt.Fprintf(out, "✅ %q: password backed up as %s\n", srv.Name, ref)
	return true, nil
}

// Item categories OpsPulse stores credentials in.
//
// Private keys go into a Login item too, in a custom concealed field, because
// the CLI cannot write "SSH Key" items - see secret.sshKeyManagedFieldID.
const (
	onePasswordLoginCategory = "Login"
)

// verifyPushedSSHKey reads a just-written key back out of 1Password and
// confirms it holds the key that was uploaded.
//
// The public key is the comparison of choice because it is format-insensitive,
// but it only exists when the material is something crypto/ssh understands -
// ssh(1) accepts more formats than the Go library. When either side cannot be
// parsed the raw text is compared instead, CRLF-normalised, which can only
// over-report a problem: it never silently accepts a wrong key.
func verifyPushedSSHKey(ctx context.Context, ref, localKey, expectedPublic string) error {
	stored, err := onePasswordResolver().ResolveSSHKey(ctx, ref)
	if err != nil {
		return err
	}
	if strings.TrimSpace(stored) == "" {
		return fmt.Errorf("%s is empty", ref)
	}

	if got, ok := publicKeyLine([]byte(stored)); ok && expectedPublic != "" {
		if got != expectedPublic {
			return fmt.Errorf("%s holds a different key than the one uploaded", ref)
		}
		return nil
	}
	if normaliseKeyText(stored) != normaliseKeyText(localKey) {
		return fmt.Errorf("%s does not hold the key that was uploaded", ref)
	}
	return nil
}

// normaliseKeyText reduces a key to the form a byte comparison should use:
// line endings unified, surrounding whitespace dropped. 1Password can hand a
// Windows-authored key back with CRLF, which is not a difference in the key.
func normaliseKeyText(key string) string {
	return strings.TrimSpace(strings.ReplaceAll(key, "\r\n", "\n"))
}

// writeOnePasswordItem creates the named item when it does not exist yet, and
// updates it in place when it does. build receives the document to fill in: the
// stock category template for a create, or the item's current JSON for an
// update, so that fields the user added in 1Password survive a re-push.
//
// The payload travels on stdin rather than through a temporary file and
// --template. That is not a preference: `op` rejects the combination of
// --template and a redirected stdin ("cannot create an item from template and
// stdin at the same time"), and a child process spawned by Go always sees a
// redirected stdin, so the --template form can never work from OpsPulse. Piping
// is 1Password's documented workaround, and it has the pleasant side effect of
// keeping the secret off the disk entirely. Note that `op item edit` only reads
// stdin for this - unlike `op item create`, it takes no "-" argument.
func writeOnePasswordItem(ctx context.Context, cli secret.CLI, vault, title, category string, index *itemIndex, build func(doc []byte) ([]byte, error)) error {
	existingID, err := index.id(ctx, title)
	if err != nil {
		return err
	}

	if existingID == "" {
		doc, err := cli.Run(ctx, "item", "template", "get", category)
		if err != nil {
			return fmt.Errorf("fetch the 1Password %q item template: %w", category, err)
		}
		payload, err := build(doc)
		if err != nil {
			return fmt.Errorf("build the 1Password item %q: %w", title, err)
		}
		if _, err := cli.RunWithStdin(ctx, payload, "item", "create", "--vault", vault, "-"); err != nil {
			return fmt.Errorf("create 1Password item %q: %w", title, err)
		}
		return nil
	}

	doc, err := cli.Run(ctx, "item", "get", existingID, "--vault", vault, "--format", "json")
	if err != nil {
		return fmt.Errorf("read the existing 1Password item %q: %w", title, err)
	}
	payload, err := build(doc)
	if err != nil {
		return fmt.Errorf("build the 1Password item %q: %w", title, err)
	}
	if _, err := cli.RunWithStdin(ctx, payload, "item", "edit", existingID, "--vault", vault); err != nil {
		return fmt.Errorf("update 1Password item %q: %w", title, err)
	}
	return nil
}

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

	cli, err := ensure1PCLI()
	if err != nil {
		return err
	}
	vault, err := resolveAndValidateVault(ctx, cli, onePasswordVault, false)
	if err != nil {
		return err
	}

	store := server.NewDefaultStore()
	explicit := len(args) > 0

	// One vault listing for the whole command: the inventory restore and the
	// credential matching below both read it, and a second `op item list` would
	// be a second Desktop App authorisation on Windows, where nothing is cached.
	index := newItemIndex(cli, vault)

	if !explicit {
		// The inventory comes first so that a fresh machine has the server
		// names before anything tries to match item names against them.
		found, err := restoreInventoryFromOnePassword(ctx, cli, vault, index)
		if err != nil {
			return err
		}
		if !found {
			fmt.Printf("ℹ️  No inventory backup in vault %q; restoring credentials for the servers already in servers.yaml.\n", vault)
		}
	}

	targets, err := selectRestoreTargets(store, args)
	if err != nil {
		return err
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
		plans = append(plans, planRestore(srv, discovery))
	}

	// Ask up front rather than halfway through, so a declined confirmation
	// cannot leave the batch half-applied.
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
// before the server list, which the no-argument form fixes.
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
// Everything else is matched by the deterministic name OpsPulse gives its
// items, which is the only thing that survives a machine change.
//
// A password is only restored when the local value is absent or a legacy
// reference. The inventory backup already carries a plaintext password, so
// rewriting it here would ask the user to approve a value they already have.
func planRestore(srv *server.Server, discovery *vaultDiscovery) restorePlan {
	plan := restorePlan{server: srv}

	switch {
	case secret.Is1PRef(srv.KeyPath):
		plan.keyRef = srv.KeyPath
		plan.keyWasLegacy = true
	case strings.TrimSpace(srv.KeyPath) != "":
		plan.keyRef = discovery.keyRef(srv.Name)
	}

	switch {
	case secret.Is1PRef(srv.Password):
		plan.passRef = srv.Password
		plan.passWasLegacy = true
	case strings.TrimSpace(srv.Password) == "":
		plan.passRef = discovery.passwordRef(srv.Name)
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
	// name; without this it would be reported as orphaned on every restore.
	claimed[secret.InventoryItemTitle] = struct{}{}

	var unmatched []string
	for title := range discovery.titles {
		if !strings.HasPrefix(title, "opspulse_") {
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
// servers.yaml as plaintext, so the warning can name a number.
func countRestorePasswords(plans []restorePlan) int {
	count := 0
	for _, plan := range plans {
		if plan.passRef != "" {
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

// restoreOneServer brings back every credential a single server keeps in
// 1Password, writing it to local disk.
//
// Key and password are checked independently on purpose: a server can hold
// both, and restoring only one of them would strand an op:// reference once the
// 1Password account is gone.
func restoreOneServer(ctx context.Context, store *server.Store, plan restorePlan) restoreOutcome {
	srv := plan.server
	out := restoreOutcome{name: srv.Name}

	if plan.keyRef == "" && plan.passRef == "" {
		out.reason = "no credential is backed up in 1Password"
		return out
	}

	if plan.keyRef != "" {
		written, err := restoreKeyFromOnePassword(ctx, store, srv, plan.keyRef)
		switch {
		case err != nil:
			out.err = err
			return out
		case !written:
			out.blocked = true
			out.reason = "a different key already occupies the local path; re-run with --force to replace it"
		default:
			out.keyRestored = true
			out.migrated = out.migrated || plan.keyWasLegacy
		}
	}
	if plan.passRef != "" {
		if err := restorePasswordFromOnePassword(ctx, store, srv, plan.passRef); err != nil {
			out.err = err
			return out
		}
		out.passRestored = true
		out.migrated = out.migrated || plan.passWasLegacy
	}
	return out
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

// restoreKeyFromOnePassword writes a 1Password-hosted private key onto local
// disk and rebinds the server to the file, leaving the item in 1Password alone.
//
// It reports whether the file was written: false with a nil error means the
// destination already holds a different key and was deliberately left alone.
func restoreKeyFromOnePassword(ctx context.Context, store *server.Store, srv *server.Server, ref string) (bool, error) {
	vault, item, _, _ := secret.Parse1PRef(ref)
	fmt.Printf("⬇️  Restoring key for %q from 1Password (%s/%s)...\n", srv.Name, vault, item)

	keyData, err := onePasswordResolver().ResolveSSHKey(ctx, ref)
	if err != nil {
		return false, err
	}
	if err := validatePrivateKeyContent([]byte(keyData)); err != nil {
		return false, fmt.Errorf("the 1Password item %q does not contain a usable SSH private key: %w", item, err)
	}

	storedDest, expandedDest, err := setupKeyPath(srv.Name)
	if err != nil {
		return false, err
	}

	body := []byte(keyData)
	if !strings.HasSuffix(keyData, "\n") {
		body = append(body, '\n')
	}

	replace, err := mayReplaceLocalKey(expandedDest, body)
	if err != nil {
		return false, err
	}
	if !replace {
		return false, nil
	}

	if err := os.MkdirAll(filepath.Dir(expandedDest), 0o700); err != nil {
		return false, fmt.Errorf("create ~/.ssh directory: %w", err)
	}
	if err := os.WriteFile(expandedDest, body, 0o600); err != nil {
		return false, fmt.Errorf("write private key to %s: %w", expandedDest, err)
	}
	writePublicKeyFile(expandedDest, body)

	if srv.KeyPath != storedDest {
		srv.KeyPath = storedDest
		if err := store.Save(*srv); err != nil {
			return false, fmt.Errorf("rebind server %q to the local key: %w", srv.Name, err)
		}
	}

	fmt.Printf("✅ %q: key restored to %s.\n", srv.Name, storedDest)
	return true, nil
}

// mayReplaceLocalKey reports whether an incoming key may overwrite whatever
// already sits at path.
//
// The comparison is by public key rather than by bytes. 1Password normalises a
// key on the way back out - an RSA key stored as classic PEM returns in OpenSSH
// format - so a byte comparison would report a conflict for a key that is in
// fact identical, and send the user to --force for no reason.
func mayReplaceLocalKey(path string, incoming []byte) (bool, error) {
	if onePasswordRestoreForce {
		return true, nil
	}
	existing, err := os.ReadFile(filepath.Clean(path)) // #nosec G304 -- OpsPulse's own managed key location
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect %s: %w", path, err)
	}

	existingPub, existingOK := publicKeyLine(existing)
	incomingPub, incomingOK := publicKeyLine(incoming)
	if existingOK && incomingOK {
		return existingPub == incomingPub, nil
	}
	// One side is unparsable, so fall back to a byte comparison: that can only
	// over-report a conflict, never silently overwrite something unrecognised.
	return bytes.Equal(bytes.TrimSpace(existing), bytes.TrimSpace(incoming)), nil
}

// publicKeyLine derives the OpenSSH authorized_keys line of a private key,
// reporting false when the material cannot be parsed.
func publicKeyLine(keyData []byte) (string, bool) {
	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))), true
}

// restorePasswordFromOnePassword writes a 1Password-hosted password back into
// servers.yaml as plaintext, leaving the item in 1Password alone.
func restorePasswordFromOnePassword(ctx context.Context, store *server.Store, srv *server.Server, ref string) error {
	vault, item, _, _ := secret.Parse1PRef(ref)
	fmt.Printf("⬇️  Restoring password for %q from 1Password (%s/%s)...\n", srv.Name, vault, item)

	password, err := onePasswordResolver().ResolvePassword(ctx, ref)
	if err != nil {
		return err
	}
	if password == "" {
		return fmt.Errorf("the 1Password item %q returned an empty password", item)
	}

	srv.Password = password
	if err := store.Save(*srv); err != nil {
		return fmt.Errorf("write the password back into servers.yaml: %w", err)
	}

	fmt.Printf("✅ %q: password restored into servers.yaml as plaintext.\n", srv.Name)
	return nil
}

// onePasswordAccountInfo mirrors the subset of `op account list --format json`
// worth showing to a human.
type onePasswordAccountInfo struct {
	URL   string `json:"url"`
	Email string `json:"email"`
}

func listOnePasswordAccounts(ctx context.Context, cli secret.CLI) ([]onePasswordAccountInfo, error) {
	out, err := cli.Run(ctx, "account", "list", "--format", "json")
	if err != nil {
		return nil, err
	}
	var raw []onePasswordAccountInfo
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse 1Password account list: %w", err)
	}
	return raw, nil
}

func runOnePasswordConfig(ctx context.Context) error {
	settings, err := secret.LoadSettings()
	if err != nil {
		return err
	}

	changed := onePasswordConfigUnset ||
		strings.TrimSpace(onePasswordConfigVault) != "" ||
		strings.TrimSpace(onePasswordConfigAccount) != ""
	if changed {
		if onePasswordConfigUnset {
			settings = secret.Settings{}
		}
		if v := strings.TrimSpace(onePasswordConfigVault); v != "" {
			settings.Vault = v
		}
		if a := strings.TrimSpace(onePasswordConfigAccount); a != "" {
			settings.Account = a
		}
		if err := settings.Save(); err != nil {
			return err
		}
	}

	return printOnePasswordConfig(ctx, settings)
}

func printOnePasswordConfig(ctx context.Context, settings secret.Settings) error {
	fmt.Printf("1Password defaults  (%s)\n\n", secret.SettingsPath())

	vault := settings.Vault
	if vault == "" {
		vault = "(none — falls back to $OP_VAULT, then to the only accessible vault)"
	}
	account := settings.Account
	if account == "" {
		account = "(none — uses op's own default account)"
	}
	fmt.Printf("  vault   : %s\n", vault)
	fmt.Printf("  account : %s\n", account)

	// An exported variable wins over the remembered setting, which is easy to
	// forget about two months later: say so explicitly.
	if v := strings.TrimSpace(os.Getenv("OP_VAULT")); v != "" {
		fmt.Printf("  note    : $OP_VAULT=%s overrides the remembered vault\n", v)
	}
	if a := strings.TrimSpace(os.Getenv("OP_ACCOUNT")); a != "" {
		fmt.Printf("  note    : $OP_ACCOUNT=%s overrides the remembered account\n", a)
	}

	// --offline stops here: the point of the flag is to inspect or set the
	// remembered defaults without an unlock prompt.
	if onePasswordConfigOffline {
		return nil
	}

	cli := secret.Detect()
	if !cli.Available() {
		fmt.Println("\n⚠️  1Password CLI not found, so accounts and vaults cannot be listed.")
		return nil
	}
	cli = cli.WithAccountFallback(settings.Account)

	printOnePasswordCLIDetails(cli)

	if accounts, err := listOnePasswordAccounts(ctx, cli); err == nil && len(accounts) > 0 {
		fmt.Println("\nAccounts visible to the CLI:")
		for _, a := range accounts {
			fmt.Printf("  %-30s %s\n", a.URL, a.Email)
		}
	}

	names, err := listVaults(ctx, cli)
	if err != nil {
		fmt.Printf("\n❌ Authentication failed, so no vault can be listed:\n   %v\n", err)
		return nil
	}
	fmt.Printf("\nVaults visible to the current account: %s\n", strings.Join(names, ", "))

	if settings.IsZero() {
		fmt.Println("\n💡 Remember a default so 'ops 1p backup' needs no --vault:")
		fmt.Println("   ops 1p config --vault <name> [--account <sign-in-address>]")
	}
	return nil
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

// runOnePasswordStatus reports where each server's credentials live.
//
// It is offline by default: only servers.yaml is read, so it never contacts
// 1Password and never prompts. That is the point of the command - checking
// which servers still need migrating must not itself require an unlock. Pass
// --remote to also ask the vault which servers have a backup.
func runOnePasswordStatus(ctx context.Context) error {
	store := server.NewDefaultStore()
	servers, err := store.List()
	if err != nil {
		return fmt.Errorf("failed to list servers: %w", err)
	}

	filtered := make([]server.Server, 0, len(servers))
	for _, s := range servers {
		if s.MatchFilter(onePasswordFilter) {
			filtered = append(filtered, s)
		}
	}
	if len(filtered) == 0 {
		fmt.Println("No servers matched.")
		return nil
	}

	var discovery *vaultDiscovery
	if onePasswordStatusRemote {
		if discovery, err = statusVaultDiscovery(ctx); err != nil {
			return err
		}
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	if discovery == nil {
		_, _ = fmt.Fprintln(tw, "NAME\tKEY\tPASSWORD")
	} else {
		_, _ = fmt.Fprintln(tw, "NAME\tLOCAL KEY\t1P BACKUP")
	}
	legacy := 0
	for _, s := range filtered {
		if secret.Is1PRef(s.KeyPath) || secret.Is1PRef(s.Password) {
			legacy++
		}
		if discovery == nil {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", s.Name, describeKeySource(s), describePasswordSource(s))
			continue
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", s.Name, describeKeySource(s), describeBackupState(discovery, s))
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	fmt.Println()
	// A server can be configured correctly and still be unusable: the key file
	// it names may not be on this disk. Reporting "all local" without checking
	// would be a green light that the first connection then disproves.
	missingKeys := serversWithMissingKeyFiles(filtered)
	if legacy > 0 {
		fmt.Printf("⚠️  %d/%d server(s) still hold an 'op://' reference, which the runtime no longer resolves. Run 'ops 1p restore' to migrate them.\n", legacy, len(filtered))
	} else if len(missingKeys) == 0 {
		fmt.Printf("✅ All %d server(s) resolve their credentials from local disk.\n", len(filtered))
	}
	if len(missingKeys) > 0 {
		fmt.Printf("⚠️  %d server(s) point at a private key file that is not on this machine: %s\n", len(missingKeys), strings.Join(missingKeys, ", "))
		fmt.Println("   Run 'ops 1p restore <name>' to write the backed-up key to disk.")
	}
	if settings, err := secret.LoadSettings(); err == nil && !settings.IsZero() {
		fmt.Printf("Remembered defaults: %s\n", settingsSummary(settings))
	}
	if discovery == nil {
		fmt.Println("Tip: pass --remote to also check which servers have a 1Password backup.")
	}

	if details, err := sftp.ListMaterialized1PKeyDetails(); err == nil && len(details) > 0 {
		fmt.Printf("\n⚠️  Found %d leftover key file(s) in ~/.ssh/opspulse-1p from the op:// era:\n", len(details))
		for _, d := range details {
			fmt.Printf("   - %s\n", d.Name)
		}
		fmt.Println("   These are leftovers from the op:// era; 'ops 1p restore' purges them automatically.")
	}
	return nil
}

// statusVaultDiscovery lists the vault's items for the --remote column.
func statusVaultDiscovery(ctx context.Context) (*vaultDiscovery, error) {
	cli, err := ensure1PCLI()
	if err != nil {
		return nil, err
	}
	vault, err := resolveAndValidateVault(ctx, cli, onePasswordVault, false)
	if err != nil {
		return nil, err
	}
	return discoverVaultItems(ctx, newItemIndex(cli, vault))
}

// describeBackupState renders the --remote column for one server.
func describeBackupState(discovery *vaultDiscovery, s server.Server) string {
	if ref := discovery.keyRef(s.Name); ref != "" {
		return "✅ " + onePasswordRefDisplay(ref)
	}
	return "❌ not backed up"
}

// onePasswordRefDisplay renders an op://<vault>/<item>/<field> reference in a
// compact "vault/item" form for tables.
func onePasswordRefDisplay(ref string) string {
	if vault, item, _, ok := secret.Parse1PRef(ref); ok {
		return vault + "/" + item
	}
	return ref
}

// describeKeySource renders where a server's private key comes from.
//
// A residual op:// reference is labelled "legacy" rather than "1password": the
// runtime refuses it, so it is a problem to fix, not a working configuration.
func describeKeySource(s server.Server) string {
	switch {
	case secret.Is1PRef(s.KeyPath):
		return "legacy 1password ref (" + onePasswordRefDisplay(s.KeyPath) + ")"
	case strings.TrimSpace(s.KeyPath) != "":
		suffix := ""
		if isManagedKey(s.KeyPath) {
			suffix = ", managed"
		}
		return fmt.Sprintf("local file (%s%s)", s.KeyPath, suffix)
	default:
		return "-"
	}
}

// describePasswordSource renders where a server's password comes from.
func describePasswordSource(s server.Server) string {
	switch {
	case secret.Is1PRef(s.Password):
		return "legacy 1password ref (" + onePasswordRefDisplay(s.Password) + ")"
	case strings.TrimSpace(s.Password) != "":
		return "plaintext (servers.yaml)"
	default:
		return "-"
	}
}

// listVaults returns the names of every vault the current account can see.
func listVaults(ctx context.Context, cli secret.CLI) ([]string, error) {
	out, err := cli.Run(ctx, "vault", "list", "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("%s\n\n%w", onePasswordFailureHint(cli, err), err)
	}

	var raw []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse 1Password vault list: %w", err)
	}
	names := make([]string, 0, len(raw))
	for _, v := range raw {
		if name := strings.TrimSpace(v.Name); name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

// resolveAndValidateVault decides which vault to operate on.
//
// Precedence, from strongest to weakest:
//  1. explicitVault: an explicit instruction, so an unknown name is a hard error.
//  2. $OP_VAULT, then the remembered setting: a name that no longer exists warns
//     and falls through rather than wedging every future command.
//  3. the only vault the account can see: no point demanding a choice.
//
// remember records an explicit choice as the new default. Push wants that, since
// the vault is where items are created; pull's --vault only scopes a search, and
// remembering a read-only archive vault would silently break the next push.
func resolveAndValidateVault(ctx context.Context, cli secret.CLI, explicitVault string, remember bool) (string, error) {
	names, err := listVaults(ctx, cli)
	if err != nil {
		return "", err
	}

	explicit := strings.TrimSpace(explicitVault)
	vault, warnings, err := chooseVault(names, explicit, rememberedVaultCandidates())
	for _, warning := range warnings {
		fmt.Printf("⚠️  %s\n", warning)
	}
	if err != nil {
		return "", err
	}
	if explicit != "" && remember {
		rememberOnePasswordSetting("vault", vault)
	}
	return vault, nil
}

// chooseVault picks the vault to operate on and returns a warning for every
// preference it had to skip because the vault no longer exists.
//
// The rules exist so that the common case needs no flag at all: a single
// accessible vault is chosen silently, while a genuine ambiguity is reported
// with a way out rather than a bare failure.
func chooseVault(names []string, explicit string, fallbacks []string) (vault string, warnings []string, err error) {
	if len(names) == 0 {
		return "", nil, fmt.Errorf("no 1Password vault is accessible with the current account")
	}

	if explicit != "" {
		if !containsString(names, explicit) {
			return "", nil, fmt.Errorf("vault %q was not found; available vaults: %s", explicit, strings.Join(names, ", "))
		}
		return explicit, nil, nil
	}

	for _, candidate := range fallbacks {
		if containsString(names, candidate) {
			return candidate, warnings, nil
		}
		warnings = append(warnings, fmt.Sprintf("1Password vault %q was not found; ignoring it. Available vaults: %s", candidate, strings.Join(names, ", ")))
	}

	if len(names) == 1 {
		return names[0], warnings, nil
	}
	return "", warnings, fmt.Errorf("several 1Password vaults are available (%s); pick one with --vault, or remember it once with 'ops 1p config --vault <name>'", strings.Join(names, ", "))
}

// rememberedVaultCandidates lists the exported and stored vault preferences, in
// the order they should be consulted. The environment comes first so that
// OP_VAULT keeps its usual meaning of "just for this shell".
func rememberedVaultCandidates() []string {
	candidates := []string{strings.TrimSpace(os.Getenv("OP_VAULT"))}
	if settings, err := secret.LoadSettings(); err == nil {
		candidates = append(candidates, settings.Vault)
	}
	return dedupeNonEmpty(candidates)
}

func containsString(haystack []string, needle string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}

func dedupeNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// rememberOnePasswordSetting records a default so the next run can omit the
// flag. Failures are reported but never fatal: not remembering a preference
// must not break an otherwise successful push.
func rememberOnePasswordSetting(field, value string) {
	settings, err := secret.LoadSettings()
	if err != nil {
		fmt.Printf("⚠️  Could not read %s: %v\n", secret.SettingsPath(), err)
		return
	}
	if (field == "vault" && settings.Vault == value) || (field == "account" && settings.Account == value) {
		return
	}
	if field == "vault" {
		settings.Vault = value
	} else {
		settings.Account = value
	}
	if err := settings.Save(); err != nil {
		fmt.Printf("⚠️  Could not remember the %s: %v\n", field, err)
		return
	}
	fmt.Printf("💡 Remembered %s %q; future runs use it without --%s.\n", field, value, field)
}

// authorizedKeyFor returns the OpenSSH "authorized_keys" line for a private key,
// preferring the sibling .pub file and falling back to deriving it in memory.
func authorizedKeyFor(privateKeyPath string, keyData []byte) string {
	if pub, err := os.ReadFile(privateKeyPath + ".pub"); err == nil { // #nosec G304 -- sibling of a user provided key
		line := strings.TrimSpace(string(pub))
		if idx := strings.IndexByte(line, '\n'); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
		}
		if line != "" {
			return line
		}
	}
	line, _ := publicKeyLine(keyData)
	return line
}

func writePublicKeyFile(privateKeyPath string, keyData []byte) {
	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		return
	}
	line := ssh.MarshalAuthorizedKey(signer.PublicKey())
	_ = os.WriteFile(privateKeyPath+".pub", line, 0o600)
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

// legacyFlagNames are the flags the retired push/pull commands used to carry.
// They are registered on the hidden stubs and never read: without them, an old
// invocation like 'ops 1p push --materialize' would die on Cobra's "unknown
// flag" error instead of reaching the message that names its replacement.
var legacyFlagNames = []struct {
	name      string
	shorthand string
	isBool    bool
}{
	{"vault", "", false},
	{"all", "", true},
	{"filter", "f", false},
	{"include-skipped", "", true},
	{"delete-local", "", true},
	{"inventory", "", true},
	{"prefer-local", "", true},
	{"prefer-remote", "", true},
	{"yes", "y", true},
	{"force", "", true},
	{"from-vault", "", true},
	{"materialize", "", true},
}

// registerLegacyFlags mirrors the old flag surface onto a retired stub.
func registerLegacyFlags(cmd *cobra.Command) {
	for _, f := range legacyFlagNames {
		if f.isBool {
			cmd.Flags().BoolP(f.name, f.shorthand, false, "retired; ignored")
		} else {
			cmd.Flags().StringP(f.name, f.shorthand, "", "retired; ignored")
		}
		_ = cmd.Flags().MarkHidden(f.name)
	}
}

func init() {
	onePasswordCmd.PersistentFlags().StringVar(&onePasswordAccount, "account", "", "1Password account (sign-in address or ID); remembered for future runs")

	onePasswordBackupCmd.Flags().StringVar(&onePasswordVault, "vault", "", "Vault to back up into (default: remembered setting, then $OP_VAULT, then the only accessible vault)")
	// The default is left at 0 rather than 4 so that backupParallel can pick the
	// right one for the CLI: a non-zero value is indistinguishable from a user
	// who typed -p, and would silently disable the WSL reduction below.
	onePasswordBackupCmd.Flags().IntVarP(&onePasswordBackupParallel, "parallel", "p", 0, "Maximum number of servers backed up concurrently (default 4, or 2 when driving the Windows op.exe from WSL)")
	onePasswordBackupCmd.Flags().BoolVar(&onePasswordPreferLocal, "prefer-local", false, "Resolve every inventory conflict in favour of this machine's servers.yaml")
	onePasswordBackupCmd.Flags().BoolVar(&onePasswordPreferRemote, "prefer-remote", false, "Resolve every inventory conflict in favour of the 1Password backup")

	onePasswordRestoreCmd.Flags().StringVar(&onePasswordVault, "vault", "", "Vault to restore from (default: remembered setting, then $OP_VAULT, then the only accessible vault)")
	onePasswordRestoreCmd.Flags().BoolVarP(&onePasswordRestoreYes, "yes", "y", false, "Write plaintext passwords into servers.yaml without asking for confirmation")
	onePasswordRestoreCmd.Flags().BoolVar(&onePasswordRestoreForce, "force", false, "Overwrite a local key file even when it holds a different key")
	onePasswordRestoreCmd.Flags().BoolVar(&onePasswordPreferLocal, "prefer-local", false, "Resolve every inventory conflict in favour of this machine's servers.yaml")
	onePasswordRestoreCmd.Flags().BoolVar(&onePasswordPreferRemote, "prefer-remote", false, "Resolve every inventory conflict in favour of the 1Password backup")
	onePasswordRestoreCmd.ValidArgsFunction = completeServerNames

	onePasswordStatusCmd.Flags().StringVarP(&onePasswordFilter, "filter", "f", "", "Filter servers by label (key=val), tag, or name")
	onePasswordStatusCmd.Flags().BoolVar(&onePasswordStatusRemote, "remote", false, "Also ask 1Password which servers have a backup (requires authentication)")

	onePasswordConfigCmd.Flags().StringVar(&onePasswordConfigVault, "vault", "", "Remember this vault as the default target")
	onePasswordConfigCmd.Flags().StringVar(&onePasswordConfigAccount, "account", "", "Remember this account as the default")
	onePasswordConfigCmd.Flags().BoolVar(&onePasswordConfigUnset, "unset", false, "Forget the remembered vault and account")
	onePasswordConfigCmd.Flags().BoolVar(&onePasswordConfigOffline, "offline", false, "Only read or write the local config; do not contact the 1Password CLI")

	registerLegacyFlags(onePasswordLegacyPushCmd)
	registerLegacyFlags(onePasswordLegacyPullCmd)

	onePasswordCmd.AddCommand(
		onePasswordBackupCmd,
		onePasswordRestoreCmd,
		onePasswordStatusCmd,
		onePasswordConfigCmd,
		onePasswordLegacyPushCmd,
		onePasswordLegacyPullCmd,
	)
	rootCmd.AddCommand(onePasswordCmd)
}
