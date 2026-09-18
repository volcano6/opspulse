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

// onePasswordAuthHint picks the right remediation text for the CLI in use.
func onePasswordAuthHint(cli secret.CLI) string {
	if platform.IsWSL() && !cli.IsWindowsBinary {
		return onePasswordLinuxInWSLHint
	}
	return onePasswordDesktopHint
}

var (
	onePasswordVault            string
	onePasswordAll              bool
	onePasswordFilter           string
	onePasswordDeleteLocal      bool
	onePasswordAccount          string
	onePasswordPushFilter       string
	onePasswordPushInclSkip     bool
	onePasswordPushInventory    bool
	onePasswordPushPreferLocal  bool
	onePasswordPushPreferRemote bool
)

// Pull keeps its own flags rather than sharing push's. The two commands mean
// different things by "all" and by "yes", and a shared variable would quietly
// couple them the first time either grows a default.
var (
	onePasswordPullAll          bool
	onePasswordPullYes          bool
	onePasswordPullForce        bool
	onePasswordPullInclSkip     bool
	onePasswordPullFilter       string
	onePasswordPullFromVault    bool
	onePasswordPullMaterialize  bool
	onePasswordPullVault        string
	onePasswordPullInventory    bool
	onePasswordPullPreferLocal  bool
	onePasswordPullPreferRemote bool
)

var onePasswordCmd = &cobra.Command{
	Use:     "1p",
	Aliases: []string{"1password", "onepassword"},
	Short:   "Move SSH private keys and passwords between local disk and 1Password",
	Long: `Push local credentials into 1Password, or pull them back onto local disk.

  ops 1p push <server>...   Upload local keys/passwords and rebind the servers to op:// references
  ops 1p pull <server>...   Write 1Password-hosted credentials back onto local disk
  ops 1p status             Show where each server's credentials currently live
  ops 1p config             Show or change the remembered vault and account

Keys are stored in Login items titled opspulse_<server>_key, inside a custom
concealed field; passwords go into Login items titled opspulse_<server>_password.
Once a credential has been pushed, servers.yaml keeps only an op:// reference and
the value is resolved on demand at connection time, so it never has to stay on
disk.

You normally do not have to name a vault at all: OpsPulse uses the one you
remembered with 'ops 1p config --vault <name>', then $OP_VAULT, and otherwise the
only vault the account can see. The account works the same way, with $OP_ACCOUNT
taking precedence over the remembered value.`,
}

var onePasswordPushCmd = &cobra.Command{
	Use:   "push [server...]",
	Short: "Upload local credentials to 1Password and rebind servers to op:// references",
	Long: `Upload locally stored credentials into 1Password.

A server's private key goes into a Login item's concealed field and its password
into a Login item, and the server is rebound so that key_path/password become
op:// references. Local key files are left in place unless --delete-local is
given; a pushed password always stops being stored in servers.yaml, since keeping
the plaintext next to an op:// reference would defeat the point.

The key is written and then read back before servers.yaml is rebound, so a push
that 1Password silently discarded fails loudly instead of breaking a server that
still worked.

  ops 1p push web                  # one server
  ops 1p push web db-01            # several
  ops 1p push --all                # every server (equivalent to --filter all)
  ops 1p push --filter prod        # by label, tag, or name
  ops 1p push --all --delete-local # remove the local copies once uploaded

servers.yaml itself is machine-local and never syncs, so a new machine cannot be
bootstrapped from credentials alone - an item holds a key but no host. --inventory
backs the whole file up into one shared item (opspulse_inventory), which
'ops 1p pull --inventory' restores on another machine:

  ops 1p push --inventory                    # back up or refresh the whole file
  ops 1p push --inventory --prefer-remote    # unattended: the backup wins conflicts

The backup merges rather than overwrites, so two machines can both push without
losing each other's servers. Deleting a server from the backup is therefore done
by hand: remove it locally first, then edit the item.`,
	Args: cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPushToOnePassword(cmd.Context(), args)
	},
}

var onePasswordPullCmd = &cobra.Command{
	Use:   "pull [server...]",
	Short: "Copy 1Password-hosted credentials back onto local disk",
	Long: `Write 1Password-hosted credentials back to local disk and rebind the servers.

By default this only covers servers whose servers.yaml already references
1Password, and it is the way out of 1Password: every credential such a server
keeps is brought back - a private key to ~/.ssh/opspulse_<server>, a password
into servers.yaml - so the configuration keeps working after the account is gone.
A server that carries both is restored in full; the old single-credential
behaviour would have left a dangling op:// reference behind.

Because a password can only come back as plaintext, OpsPulse asks for
confirmation whenever the pull would write one. In a non-interactive shell the
command refuses instead of hanging, unless --yes says the answer up front.

Note that a pull rewrites servers.yaml and thereby ends the 1Password
management: the credentials are no longer op:// references, so a second pull has
nothing to do. Push again to hand them back.

  ops 1p pull web              # restore one server
  ops 1p pull web db-01        # restore several
  ops 1p pull --all            # restore everything (the off-ramp)
  ops 1p pull --all --yes      # same, unattended

servers.yaml is machine-local and never syncs, so credentials pushed on another
machine are not referenced here. --from-vault finds them by the item names
OpsPulse gives them (opspulse_<server>_key / _password) instead:

  ops 1p pull --all --from-vault               # bind servers.yaml to 1Password, nothing on disk
  ops 1p pull --all --from-vault --materialize # write local copies instead (the off-ramp)

Without --materialize the values stay only in 1Password and 'ops ssh'
materializes the key on demand, so that machine cannot connect while 1Password is
unavailable.

--inventory restores the whole servers.yaml from the shared backup instead, which
is what a new machine needs before any credential can be adopted:

  ops 1p pull --inventory                    # restore the server list itself
  ops 1p pull --inventory --prefer-local     # unattended: this machine wins conflicts

It merges with whatever servers.yaml already holds and never deletes a server
that only this machine knows about.`,
	Args: cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPullFromOnePassword(cmd.Context(), args)
	},
}

var onePasswordStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show where each server's private key currently lives",
	RunE: func(_ *cobra.Command, _ []string) error {
		return runOnePasswordStatus()
	},
}

var onePasswordCleanupServer string

var onePasswordCleanupCmd = &cobra.Command{
	Use:     "cleanup [server]",
	Aliases: []string{"clean", "purge"},
	Short:   "Delete temporary materialized 1Password private keys from local disk",
	Long: `Deletes temporary private key files from ~/.ssh/opspulse-1p that were
materialized for external GUI SFTP clients (WinSCP, FileZilla, Cyberduck, etc.).

If a server name is specified, only that server's key is removed.
Otherwise, all materialized keys in ~/.ssh/opspulse-1p are removed.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		targetServer := onePasswordCleanupServer
		if len(args) > 0 {
			targetServer = args[0]
		}
		deleted, err := sftp.PurgeMaterialized1PKeys(targetServer)
		if err != nil {
			return fmt.Errorf("failed to clean up materialized keys: %w", err)
		}
		if len(deleted) == 0 {
			if targetServer != "" {
				fmt.Printf("✨ No materialized key found for server %q in ~/.ssh/opspulse-1p.\n", targetServer)
			} else {
				fmt.Println("✨ No materialized 1Password keys found on disk (~/.ssh/opspulse-1p is clean).")
			}
			return nil
		}
		if targetServer != "" {
			fmt.Printf("🧹 Successfully removed materialized 1Password key for %q.\n", targetServer)
		} else {
			fmt.Printf("🧹 Successfully removed %d materialized 1Password private key(s) from ~/.ssh/opspulse-1p: %s\n",
				len(deleted), strings.Join(deleted, ", "))
		}
		return nil
	},
}

var (
	onePasswordConfigVault   string
	onePasswordConfigAccount string
	onePasswordConfigUnset   bool
)

var onePasswordConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "Show or change the remembered 1Password vault and account",
	Long: `Show or change the 1Password defaults OpsPulse remembers.

Without flags it prints the effective target plus the accounts and vaults the
current account can see. With flags it records a default so that plain
'ops 1p push --all' stops asking for --vault/--account every time.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runOnePasswordConfig(cmd.Context())
	},
}

func runPushToOnePassword(ctx context.Context, args []string) error {
	// Flag validation comes first so that a contradictory invocation fails
	// before OpsPulse touches 1Password or offers to install its CLI.
	if err := validateInventoryPushFlags(args); err != nil {
		return err
	}
	if err := validatePreferFlags(onePasswordPushInventory, onePasswordPushPreferLocal, onePasswordPushPreferRemote); err != nil {
		return err
	}

	cli, err := ensure1PCLI()
	if err != nil {
		return err
	}

	if onePasswordPushInventory {
		vault, err := resolveAndValidateVault(ctx, cli, onePasswordVault, true)
		if err != nil {
			return err
		}
		return pushInventoryToOnePassword(ctx, cli, vault)
	}

	store := server.NewDefaultStore()
	targets, skippedByBatch, err := selectOnePasswordTargets(store, args)
	if err != nil {
		return err
	}
	if len(skippedByBatch) > 0 {
		fmt.Printf("ℹ️  Skipped %d server(s) marked skip_batch: %s\n", len(skippedByBatch), strings.Join(skippedByBatch, ", "))
		fmt.Println("   Pass --include-skipped to push them as well.")
	}
	if len(targets) == 0 {
		if filter := strings.TrimSpace(onePasswordPushFilter); filter != "" {
			fmt.Printf("No servers matched filter %q.\n", filter)
		} else {
			fmt.Println("Nothing to push: no server matched.")
		}
		return nil
	}

	vault, err := resolveAndValidateVault(ctx, cli, onePasswordVault, true)
	if err != nil {
		return err
	}

	pushed := 0
	var failed []string
	for _, srv := range targets {
		changed, err := pushServerToOnePassword(ctx, cli, store, srv, vault)
		if err != nil {
			// One server's failure must not strand the rest of the batch. The
			// servers are independent, and re-running finishes a partial push;
			// aborting instead would leave the user guessing which ones were
			// already rebound.
			fmt.Printf("❌ %q: %v\n", srv.Name, err)
			failed = append(failed, srv.Name)
			continue
		}
		if changed {
			pushed++
		}
	}

	fmt.Println()
	if pushed == 0 && len(failed) == 0 {
		fmt.Println("Nothing to push: no target server had a local private key or password.")
		return nil
	}
	if pushed > 0 {
		fmt.Printf("🎉 Pushed credentials for %d server(s) into 1Password vault %q.\n", pushed, vault)
		fmt.Println("💡 These servers now resolve their credentials through op://, so the values no longer live in servers.yaml.")
		if !onePasswordDeleteLocal {
			fmt.Println("   Run 'ops 1p push <server> --delete-local' if you want OpsPulse to remove the managed local key copies for you.")
		}
		fmt.Println("   Verify with: ops 1p status")
		// servers.yaml changed, so any existing inventory backup is now behind.
		// Best-effort: a push that worked must not fail over a side errand.
		autoRefreshInventoryBackup(ctx, cli, vault)
	}
	if len(failed) > 0 {
		return fmt.Errorf("failed to push %d of %d server(s): %s", len(failed), len(targets), strings.Join(failed, ", "))
	}
	return nil
}

// pushServerToOnePassword uploads whatever local credentials a server still
// keeps - a private key file, a plaintext password, or both - and rebinds the
// server to the resulting op:// references. It reports whether anything changed.
func pushServerToOnePassword(ctx context.Context, cli secret.CLI, store *server.Store, srv *server.Server, vault string) (bool, error) {
	keyChanged, err := pushPrivateKeyToOnePassword(ctx, cli, store, srv, vault)
	if err != nil {
		return false, err
	}
	passwordChanged, err := pushPasswordToOnePassword(ctx, cli, store, srv, vault)
	if err != nil {
		return false, err
	}
	if keyChanged || passwordChanged {
		return true, nil
	}
	if strings.TrimSpace(srv.KeyPath) == "" && strings.TrimSpace(srv.Password) == "" {
		fmt.Printf("⏭️  Skipping %q: no private key and no password configured (it relies on the default SSH key).\n", srv.Name)
	}
	return false, nil
}

// pushPrivateKeyToOnePassword uploads a server's local key file into a Login
// item's concealed field, and rebinds key_path to it.
//
// The item is a Login rather than an "SSH Key" because the 1Password CLI cannot
// write SSH Key items: `op item create` drops the private_key field while
// exiting 0, and `op item edit` refuses outright. See secret.sshKeyManagedFieldID.
func pushPrivateKeyToOnePassword(ctx context.Context, cli secret.CLI, store *server.Store, srv *server.Server, vault string) (bool, error) {
	switch {
	case secret.Is1PRef(srv.KeyPath):
		fmt.Printf("⏭️  %q: the private key already lives in 1Password (%s)\n", srv.Name, srv.KeyPath)
		return false, nil
	case strings.TrimSpace(srv.KeyPath) == "":
		return false, nil
	}

	expanded := expandHome(srv.KeyPath)
	localKeyPath := srv.KeyPath
	keyData, err := os.ReadFile(filepath.Clean(expanded)) // #nosec G304 -- path comes from the user's own servers.yaml
	if err != nil {
		fmt.Printf("⚠️  Skipping the key for %q: cannot read %s: %v\n", srv.Name, srv.KeyPath, err)
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

	fmt.Printf("⬆️  Pushing key for %q (%s) into 1Password vault %q...\n", srv.Name, srv.KeyPath, vault)
	if err := writeOnePasswordItem(ctx, cli, vault, title, onePasswordLoginCategory, func(doc []byte) ([]byte, error) {
		return secret.FillSSHKeyItem(doc, title, string(keyData))
	}); err != nil {
		return false, err
	}

	// The CLI exits 0 on a write it silently discarded, so "no error" says
	// nothing about whether the key is there. Read it back before touching
	// servers.yaml: rebinding on top of an empty item breaks a connection that
	// currently works, and leaves no local path to fall back to.
	if err := verifyPushedSSHKey(ctx, ref, string(keyData), expectedPublic); err != nil {
		return false, fmt.Errorf("pushed the key for %q but could not read it back, so servers.yaml was left unchanged: %w", srv.Name, err)
	}

	srv.KeyPath = ref
	if err := store.Save(*srv); err != nil {
		return false, fmt.Errorf("rebind server %q to 1Password: %w", srv.Name, err)
	}
	fmt.Printf("✅ %q now resolves its private key from %s\n", srv.Name, srv.KeyPath)

	if onePasswordDeleteLocal {
		if err := CleanupManagedKeyWithRefCheck(os.Stdout, store, srv.Name, expanded, false); err != nil {
			return true, err
		}
		if !isManagedKey(expanded) {
			fmt.Printf("   ⚠️  %s lives outside OpsPulse's managed key directory, so it was left on disk; remove it manually once you have verified the connection works.\n", localKeyPath)
		}
	}
	return true, nil
}

// pushPasswordToOnePassword uploads a server's plaintext password into a "Login"
// item, and rebinds the password field to it.
func pushPasswordToOnePassword(ctx context.Context, cli secret.CLI, store *server.Store, srv *server.Server, vault string) (bool, error) {
	switch {
	case secret.Is1PRef(srv.Password):
		fmt.Printf("⏭️  %q: the password already lives in 1Password (%s)\n", srv.Name, srv.Password)
		return false, nil
	case strings.TrimSpace(srv.Password) == "":
		return false, nil
	}

	title := secret.PasswordItemTitle(srv.Name)
	fmt.Printf("⬆️  Pushing password for %q (user %s) into 1Password vault %q...\n", srv.Name, srv.User, vault)
	if err := writeOnePasswordItem(ctx, cli, vault, title, onePasswordLoginCategory, func(doc []byte) ([]byte, error) {
		return secret.FillLoginItem(doc, title, srv.User, srv.Password)
	}); err != nil {
		return false, err
	}

	// Unlike a key file, the plaintext cannot stay behind: an op:// reference
	// sitting next to the password it points at protects nothing.
	srv.Password = secret.BuildPasswordRef(vault, title)
	if err := store.Save(*srv); err != nil {
		return false, fmt.Errorf("rebind server %q to 1Password: %w", srv.Name, err)
	}
	fmt.Printf("✅ %q now resolves its password from %s\n", srv.Name, srv.Password)
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
func writeOnePasswordItem(ctx context.Context, cli secret.CLI, vault, title, category string, build func(doc []byte) ([]byte, error)) error {
	existingID, err := findOnePasswordItemID(ctx, cli, vault, title)
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

// pullOutcome summarises what happened to one server, so that a batch can carry
// on past a failure and still print an honest summary at the end.
type pullOutcome struct {
	name       string
	keyPulled  bool
	passPulled bool
	// adopted marks a server that was rebound to 1Password without anything
	// being written to disk (--from-vault without --materialize). It is kept
	// apart from keyPulled/passPulled because "restored" would suggest a local
	// copy now exists, which is exactly what adoption avoids.
	adopted bool
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
func (o pullOutcome) restored() bool { return o.keyPulled || o.passPulled }

// pullPlan is what a pull will do for one server: the 1Password references to
// read, and implicitly whether the result lands on disk.
type pullPlan struct {
	server  *server.Server
	keyRef  string
	passRef string
}

// vaultDiscovery is what --from-vault found in one vault: the set of item
// titles, so a server can be matched by the deterministic name OpsPulse gives
// its items.
type vaultDiscovery struct {
	vault  string
	titles map[string]struct{}
}

// keyRef returns the op:// reference to a server's managed private key, or ""
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

// passwordRef returns the op:// reference to a server's managed password, or ""
// when the vault holds no such item.
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

func runPullFromOnePassword(ctx context.Context, args []string) error {
	if err := validateInventoryPullFlags(args); err != nil {
		return err
	}
	if err := validatePreferFlags(onePasswordPullInventory, onePasswordPullPreferLocal, onePasswordPullPreferRemote); err != nil {
		return err
	}

	cli, err := ensure1PCLI()
	if err != nil {
		return err
	}

	if onePasswordPullInventory {
		vault, err := resolveAndValidateVault(ctx, cli, onePasswordPullVault, false)
		if err != nil {
			return err
		}
		return pullInventoryFromOnePassword(ctx, cli, vault)
	}

	store := server.NewDefaultStore()

	var discovery *vaultDiscovery
	if onePasswordPullFromVault {
		vault, err := resolveAndValidateVault(ctx, cli, onePasswordPullVault, false)
		if err != nil {
			return err
		}
		if discovery, err = discoverVaultItems(ctx, cli, vault); err != nil {
			return err
		}
	}

	targets, explicit, skippedByBatch, err := selectOnePasswordPullTargets(store, args)
	if err != nil {
		return err
	}
	if len(skippedByBatch) > 0 {
		fmt.Printf("ℹ️  Skipped %d server(s) marked skip_batch: %s\n", len(skippedByBatch), strings.Join(skippedByBatch, ", "))
		fmt.Println("   Pass --include-skipped to pull them as well.")
	}
	if len(targets) == 0 {
		if filter := strings.TrimSpace(onePasswordPullFilter); filter != "" {
			fmt.Printf("No servers matched filter %q.\n", filter)
		} else {
			fmt.Println("Nothing to pull.")
		}
		return nil
	}

	plans := make([]pullPlan, 0, len(targets))
	for _, srv := range targets {
		plans = append(plans, planPull(srv, discovery))
	}

	// Ask up front rather than halfway through, so a declined confirmation
	// cannot leave the batch half-applied.
	if passwords := countPullablePasswords(plans); passwords > 0 {
		if err := confirmPlaintextPull(os.Stdin, os.Stdout, passwords, stdinIsInteractive(), onePasswordPullYes); err != nil {
			return err
		}
	}

	outcomes := make([]pullOutcome, 0, len(plans))
	for _, plan := range plans {
		outcomes = append(outcomes, pullOneServer(ctx, store, plan, explicit))
	}
	if err := reportPullOutcomes(os.Stdout, outcomes); err != nil {
		return err
	}
	if discovery != nil {
		if all, err := store.List(); err == nil {
			reportUnmatchedVaultItems(os.Stdout, discovery, all)
		}
	}
	return nil
}

// planPull decides where a server's credentials should be read from.
//
// Without --from-vault that is wherever servers.yaml already points. With it,
// the vault's item titles take precedence, because they are the only thing that
// survives a machine change: servers.yaml lives in the local config directory
// and is never synced. The references in servers.yaml are still used as a
// fallback, so --from-vault widens the search rather than replacing it.
func planPull(srv *server.Server, discovery *vaultDiscovery) pullPlan {
	plan := pullPlan{server: srv}
	if secret.Is1PRef(srv.KeyPath) {
		plan.keyRef = srv.KeyPath
	}
	if secret.Is1PRef(srv.Password) {
		plan.passRef = srv.Password
	}
	if ref := discovery.keyRef(srv.Name); ref != "" {
		plan.keyRef = ref
	}
	if ref := discovery.passwordRef(srv.Name); ref != "" {
		plan.passRef = ref
	}
	return plan
}

// discoverVaultItems lists the item titles in a vault once, so that matching a
// whole batch of servers costs a single call rather than one per server.
func discoverVaultItems(ctx context.Context, cli secret.CLI, vault string) (*vaultDiscovery, error) {
	out, err := cli.Run(ctx, "item", "list", "--vault", vault, "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("%s\n\nlist items in 1Password vault %q: %w", onePasswordAuthHint(cli), vault, err)
	}
	var items []struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(out, &items); err != nil {
		return nil, fmt.Errorf("parse 1Password item list: %w", err)
	}
	titles := make(map[string]struct{}, len(items))
	for _, item := range items {
		titles[item.Title] = struct{}{}
	}
	return &vaultDiscovery{vault: vault, titles: titles}, nil
}

// reportUnmatchedVaultItems tells the user about opspulse_* items that no server
// in servers.yaml claims.
//
// The comparison is against every configured server, not just this run's
// targets: an item belonging to a server that --filter or an explicit name left
// out is still matched, and reporting it as orphaned would send the user looking
// for a problem that does not exist.
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
	// name; without this it would be reported as orphaned on every pull.
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
	fmt.Fprintf(w, "\nℹ️  %d opspulse item(s) in vault %q match no server in servers.yaml: %s\n", len(unmatched), discovery.vault, strings.Join(unmatched, ", "))
	fmt.Fprintln(w, "   OpsPulse does not create servers from vault items, because an item carries no host or user.")
}

// countPullablePasswords counts the servers whose password would land in
// servers.yaml as plaintext, so the warning can name a number.
//
// Only a materializing pull writes plaintext: the default --from-vault mode
// stores an op:// reference instead, which is no more sensitive than the
// reference already in the file.
func countPullablePasswords(plans []pullPlan) int {
	if onePasswordPullFromVault && !onePasswordPullMaterialize {
		return 0
	}
	count := 0
	for _, plan := range plans {
		if plan.passRef != "" {
			count++
		}
	}
	return count
}

// selectOnePasswordPullTargets decides which servers a pull covers.
//
// Naming a server is an instruction, so an explicit name is honoured even when
// the server carries skip_batch: dropping it silently would look like the
// argument was ignored. The bulk form follows the project's convention for
// implicit batch operations (see 'ops doctor' and 'ops exec'), where servers are
// narrowed by --filter and skip_batch servers are left out unless
// --include-skipped asks for them.
//
// explicit reports which form was used, because the two treat "nothing is
// managed in 1Password" differently.
func selectOnePasswordPullTargets(store *server.Store, args []string) (targets []*server.Server, explicit bool, skipped []string, err error) {
	filter, err := resolveBatchFilter(onePasswordPullFilter, onePasswordPullAll, len(args) > 0)
	if err != nil {
		return nil, false, nil, err
	}
	if len(args) > 0 {
		targets = make([]*server.Server, 0, len(args))
		for _, name := range args {
			srv, getErr := store.Get(name)
			if getErr != nil {
				return nil, true, nil, getErr
			}
			targets = append(targets, srv)
		}
		return targets, true, nil, nil
	}

	all, err := store.List()
	if err != nil {
		return nil, false, nil, err
	}
	targets = make([]*server.Server, 0, len(all))
	for i := range all {
		if !all[i].MatchFilter(filter) {
			continue
		}
		if all[i].SkipBatch && !onePasswordPullInclSkip {
			skipped = append(skipped, all[i].Name)
			continue
		}
		targets = append(targets, &all[i])
	}
	return targets, false, skipped, nil
}

// confirmPlaintextPull gates the one irreversible step of a pull: a password
// can only come back as plaintext in servers.yaml.
//
// A non-interactive shell is refused rather than prompted at. Reading from a
// pipe that never closes would hang a script, and defaulting to "yes" would
// write a secret nobody agreed to. The I/O and the interactivity verdict are
// parameters so the policy can be tested without touching the real terminal.
func confirmPlaintextPull(in io.Reader, out io.Writer, count int, interactive, yes bool) error {
	if yes {
		return nil
	}
	if !interactive {
		return fmt.Errorf("refusing to write %d plaintext password(s) into servers.yaml without confirmation; re-run with --yes to accept this in a non-interactive shell", count)
	}
	prompt := fmt.Sprintf("⚠️  Warning: pulling will write %d plaintext password(s) into servers.yaml.\nAre you sure you want to proceed? [y/N]: ", count)
	if !promptConfirm(in, out, prompt, false) {
		return fmt.Errorf("pull cancelled by user")
	}
	return nil
}

// pullOneServer brings back every credential a single server keeps in
// 1Password, either as a local copy or as a reference.
//
// Key and password are checked independently on purpose. A server can hold both
// - 'ops server setup-key' deliberately leaves the password behind as a
// fallback - and restoring only one of them would strand an op:// reference
// once the 1Password account is gone.
func pullOneServer(ctx context.Context, store *server.Store, plan pullPlan, explicit bool) pullOutcome {
	srv := plan.server
	out := pullOutcome{name: srv.Name}

	if plan.keyRef == "" && plan.passRef == "" {
		if explicit {
			// The user asked for this server by name, so having nothing to pull
			// is a real failure rather than something to quietly skip past.
			out.err = fmt.Errorf("keeps its credentials in servers.yaml, not in 1Password; run 'ops 1p push %s' to upload them first", srv.Name)
			return out
		}
		out.reason = "no credential is managed in 1Password"
		return out
	}

	// --from-vault without --materialize is the adoption mode: point
	// servers.yaml at 1Password and leave the values there, so nothing new
	// lands on disk.
	if onePasswordPullFromVault && !onePasswordPullMaterialize {
		return adoptOneServer(ctx, store, plan)
	}

	if plan.keyRef != "" {
		written, err := pullKeyFromOnePassword(ctx, store, srv, plan.keyRef)
		switch {
		case err != nil:
			out.err = err
			return out
		case !written:
			out.blocked = true
			out.reason = "a different key already occupies the local path; re-run with --force to replace it"
		default:
			out.keyPulled = true
		}
	}
	if plan.passRef != "" {
		if err := pullPasswordFromOnePassword(ctx, store, srv, plan.passRef); err != nil {
			out.err = err
			return out
		}
		out.passPulled = true
	}
	return out
}

// adoptOneServer points a server at its 1Password items without writing
// anything to disk.
//
// Every reference is read and validated before servers.yaml is touched. A
// rebind is a promise that the connection still works, so a reference that
// turns out to be empty or unparsable would break a server that works today
// while leaving no local copy to fall back to. Nothing is written unless all of
// them resolved.
func adoptOneServer(ctx context.Context, store *server.Store, plan pullPlan) pullOutcome {
	srv := plan.server
	out := pullOutcome{name: srv.Name}

	if plan.keyRef != "" {
		key, err := onePasswordResolver().ResolveSSHKey(ctx, plan.keyRef)
		if err != nil {
			out.err = err
			return out
		}
		if err := validatePrivateKeyContent([]byte(key)); err != nil {
			out.err = fmt.Errorf("the 1Password item behind %s does not hold a usable SSH private key: %w", plan.keyRef, err)
			return out
		}
	}
	if plan.passRef != "" {
		password, err := onePasswordResolver().ResolvePassword(ctx, plan.passRef)
		if err != nil {
			out.err = err
			return out
		}
		if password == "" {
			out.err = fmt.Errorf("the 1Password item behind %s returned an empty password", plan.passRef)
			return out
		}
	}

	changed := false
	if plan.keyRef != "" && srv.KeyPath != plan.keyRef {
		srv.KeyPath = plan.keyRef
		changed = true
	}
	if plan.passRef != "" && srv.Password != plan.passRef {
		srv.Password = plan.passRef
		changed = true
	}
	if !changed {
		out.reason = "already references 1Password"
		return out
	}
	if err := store.Save(*srv); err != nil {
		out.err = fmt.Errorf("rebind server %q to 1Password: %w", srv.Name, err)
		return out
	}

	out.adopted = true
	fmt.Printf("🔗 %q now resolves its credentials from 1Password.\n", srv.Name)
	fmt.Printf("   Nothing was written to disk: 'ops ssh %s' materializes the key on demand.\n", srv.Name)
	fmt.Println("   This machine cannot connect while 1Password is unavailable; re-run with --materialize for a local copy.")
	return out
}

// reportPullOutcomes prints the per-server detail and the batch summary, and
// turns an incomplete restore into a non-zero exit.
//
// A blocked server counts towards the failure of the run even though nothing
// went wrong technically: its credential is still in 1Password, so a caller who
// asked for a complete off-ramp has not got one and must be told.
func reportPullOutcomes(w io.Writer, outcomes []pullOutcome) error {
	var restoredCount, adopted, skipped, blocked, failed, passwords int
	for _, out := range outcomes {
		if out.err != nil {
			failed++
			fmt.Fprintf(w, "❌ %q: %v\n", out.name, out.err)
			continue
		}
		// Counted before the switch so that a server whose key is blocked still
		// gets credit for the password that did come back.
		if out.passPulled {
			passwords++
		}

		switch {
		case out.blocked:
			blocked++
			fmt.Fprintf(w, "⚠️  %q: %s\n", out.name, out.reason)
		case out.adopted:
			adopted++
			fmt.Fprintf(w, "🔗 %q: bound to 1Password (no local copy)\n", out.name)
		case out.restored():
			restoredCount++
		default:
			skipped++
			fmt.Fprintf(w, "⏭️  %q: %s\n", out.name, out.reason)
		}
	}

	fmt.Fprintln(w)
	if passwords > 0 {
		fmt.Fprintf(w, "⚠️  Wrote %d plaintext password(s) into servers.yaml. Delete them (or re-run 'ops 1p push') once the local copy is no longer needed.\n", passwords)
	}
	fmt.Fprintf(w, "Pull finished: %d restored, %d adopted, %d skipped, %d blocked, %d failed.\n", restoredCount, adopted, skipped, blocked, failed)
	if skipped > 0 && restoredCount == 0 && adopted == 0 && blocked == 0 && failed == 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "💡 Every target keeps its credentials locally; none of them references 1Password.")
		fmt.Fprintln(w, "   servers.yaml is machine-local and does not sync, so credentials pushed on another")
		fmt.Fprintln(w, "   machine will not be referenced here. Run 'ops 1p pull --all --from-vault' to find")
		fmt.Fprintln(w, "   them by their item names in the vault instead.")
	}
	if failed > 0 {
		return fmt.Errorf("%d server(s) could not be pulled", failed)
	}
	if blocked > 0 {
		return fmt.Errorf("%d server(s) still need a decision before their credentials can be restored", blocked)
	}
	return nil
}

// pullKeyFromOnePassword writes a 1Password-hosted private key back onto local
// disk and rebinds the server to the file, leaving the item in 1Password alone.
//
// It reports whether the file was written: false with a nil error means the
// destination already holds a different key and was deliberately left alone.
func pullKeyFromOnePassword(ctx context.Context, store *server.Store, srv *server.Server, ref string) (bool, error) {
	vault, item, _, _ := secret.Parse1PRef(ref)
	fmt.Printf("⬇️  Pulling key for %q from 1Password (%s/%s)...\n", srv.Name, vault, item)

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

	srv.KeyPath = storedDest
	if err := store.Save(*srv); err != nil {
		return false, fmt.Errorf("rebind server %q to the local key: %w", srv.Name, err)
	}

	fmt.Printf("✅ %q: saved to %s and rebound to it.\n", srv.Name, storedDest)
	fmt.Printf("   The 1Password item %q was left untouched.\n", item)
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
	if onePasswordPullForce {
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

// pullPasswordFromOnePassword writes a 1Password-hosted password back into
// servers.yaml as plaintext, leaving the item in 1Password alone.
func pullPasswordFromOnePassword(ctx context.Context, store *server.Store, srv *server.Server, ref string) error {
	vault, item, _, _ := secret.Parse1PRef(ref)
	fmt.Printf("⬇️  Pulling password for %q from 1Password (%s/%s)...\n", srv.Name, vault, item)

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

	fmt.Printf("✅ %q: password written back into servers.yaml as plaintext.\n", srv.Name)
	fmt.Printf("   The 1Password item %q was left untouched.\n", item)
	return nil
}

// materializeOnePasswordKeys replaces op:// key references on the target server
// (and on its jump host) with concrete 0600 files, because the system ssh(1)
// client only understands file paths. The returned cleanup removes every file it
// created and must run once the session has ended.
//
// jumpKeyPath is the materialised identity file of the jump host, if any.
func materializeOnePasswordKeys(srv *server.Server, store *server.Store) (jumpKeyPath string, cleanup func(), err error) {
	var cleanups []func()
	restore := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}

	resolve := func(name, ref string) (string, error) {
		path, done, err := secret.NewResolver().MaterializeSSHKey(context.Background(), ref)
		if err != nil {
			return "", fmt.Errorf("resolve the 1Password key for %q: %w", name, err)
		}
		cleanups = append(cleanups, done)
		return path, nil
	}

	if secret.Is1PRef(srv.KeyPath) {
		path, err := resolve(srv.Name, srv.KeyPath)
		if err != nil {
			restore()
			return "", func() {}, err
		}
		srv.KeyPath = path
	}

	if srv.JumpHost != "" {
		if jumpSrv, err := store.Get(srv.JumpHost); err == nil && secret.Is1PRef(jumpSrv.KeyPath) {
			path, err := resolve(jumpSrv.Name, jumpSrv.KeyPath)
			if err != nil {
				restore()
				return "", func() {}, err
			}
			jumpKeyPath = path
		}
	}

	return jumpKeyPath, restore, nil
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
		fmt.Println("\n💡 Remember a default so plain 'ops 1p push --all' needs no flags:")
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

func runOnePasswordStatus() error {
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

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tKEY\tPASSWORD")
	managed := 0
	for _, s := range filtered {
		if secret.Is1PRef(s.KeyPath) || secret.Is1PRef(s.Password) {
			managed++
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", s.Name, describeKeySource(s), describePasswordSource(s))
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	fmt.Printf("\n%d/%d server(s) keep at least one credential in 1Password.\n", managed, len(filtered))
	fmt.Println("Tip: 'ops 1p pull <server>' writes a 1Password-hosted credential back into servers.yaml.")
	if settings, err := secret.LoadSettings(); err == nil && !settings.IsZero() {
		fmt.Printf("Remembered defaults: %s\n", settingsSummary(settings))
	}
	if cli := secret.Detect(); !cli.Available() {
		fmt.Println("⚠️  1Password CLI was not found on this host, so op:// references cannot be resolved right now.")
	}
	if details, err := sftp.ListMaterialized1PKeyDetails(); err == nil && len(details) > 0 {
		fmt.Printf("\n⚠️  Found %d materialized 1Password key(s) in ~/.ssh/opspulse-1p (created for GUI SFTP clients):\n", len(details))
		for _, d := range details {
			if d.IsStale {
				fmt.Printf("   - %s (stale: >24h old, please run ops 1p cleanup)\n", d.Name)
			} else {
				fmt.Printf("   - %s\n", d.Name)
			}
		}
		fmt.Println("   Run 'ops 1p cleanup' to purge them from local disk.")
	}
	return nil
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
func describeKeySource(s server.Server) string {
	switch {
	case secret.Is1PRef(s.KeyPath):
		return "1password (" + onePasswordRefDisplay(s.KeyPath) + ")"
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
		return "1password (" + onePasswordRefDisplay(s.Password) + ")"
	case strings.TrimSpace(s.Password) != "":
		return "plaintext (servers.yaml)"
	default:
		return "-"
	}
}

// selectOnePasswordTargets decides which servers a push covers.
//
// Naming a server is an instruction, so an explicit name is honoured even when
// the server carries skip_batch. The bulk form follows the project's convention
// for implicit batch operations (see 'ops doctor' and 'ops exec'): servers are
// narrowed by --filter, and skip_batch servers are left out unless
// --include-skipped asks for them. skipped reports the latter so the caller can
// say so rather than silently doing less than asked.
func selectOnePasswordTargets(store *server.Store, args []string) (targets []*server.Server, skipped []string, err error) {
	filter, err := resolveBatchFilter(onePasswordPushFilter, onePasswordAll, len(args) > 0)
	if err != nil {
		return nil, nil, err
	}
	if len(args) > 0 {
		targets = make([]*server.Server, 0, len(args))
		for _, name := range args {
			srv, getErr := store.Get(name)
			if getErr != nil {
				return nil, nil, getErr
			}
			targets = append(targets, srv)
		}
		return targets, nil, nil
	}

	all, err := store.List()
	if err != nil {
		return nil, nil, err
	}
	targets = make([]*server.Server, 0, len(all))
	for i := range all {
		if !all[i].MatchFilter(filter) {
			continue
		}
		if all[i].SkipBatch && !onePasswordPushInclSkip {
			skipped = append(skipped, all[i].Name)
			continue
		}
		targets = append(targets, &all[i])
	}
	return targets, skipped, nil
}

// resolveBatchFilter turns the positional-name / --filter / --all combination
// into the selector to apply, or into an error when the combination does not
// mean anything.
//
// Naming servers and filtering them are two different instructions, so asking
// for both is a mistake rather than a precedence puzzle: letting one silently
// win would target fewer or more servers than the user asked for. --all is
// spelled out as an alias of --filter all, so combining them is only an error
// when the two actually disagree.
//
// An empty filter counts as unset, matching how 'ops exec' treats it.
func resolveBatchFilter(filter string, all, named bool) (string, error) {
	trimmed := strings.TrimSpace(filter)
	if named {
		if trimmed != "" {
			return "", fmt.Errorf("--filter cannot be combined with explicit server names; drop one of them")
		}
		return "", nil
	}
	if trimmed == "" {
		if !all {
			return "", fmt.Errorf("specify at least one server name, or pass --all (or --filter <selector>) to target several")
		}
		return "all", nil
	}
	if all && !strings.EqualFold(trimmed, "all") {
		return "", fmt.Errorf("--all and --filter %q disagree: --all is equivalent to --filter all", trimmed)
	}
	return trimmed, nil
}

// listVaults returns the names of every vault the current account can see.
func listVaults(ctx context.Context, cli secret.CLI) ([]string, error) {
	out, err := cli.Run(ctx, "vault", "list", "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("%s\n\n%w", onePasswordAuthHint(cli), err)
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

func findOnePasswordItemID(ctx context.Context, cli secret.CLI, vault, title string) (string, error) {
	out, err := cli.Run(ctx, "item", "list", "--vault", vault, "--format", "json")
	if err != nil {
		return "", fmt.Errorf("list items in 1Password vault %q: %w", vault, err)
	}
	var items []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(out, &items); err != nil {
		return "", fmt.Errorf("parse 1Password item list: %w", err)
	}
	for _, item := range items {
		if item.Title == title {
			return item.ID, nil
		}
	}
	return "", nil
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

func init() {
	onePasswordCmd.PersistentFlags().StringVar(&onePasswordAccount, "account", "", "1Password account (sign-in address or ID); remembered for future runs")
	onePasswordPushCmd.Flags().StringVar(&onePasswordVault, "vault", "", "Vault for new items (default: remembered setting, then $OP_VAULT, then the only accessible vault)")
	onePasswordPushCmd.Flags().BoolVar(&onePasswordAll, "all", false, "Push every server that currently uses a local private key (equivalent to --filter all)")
	onePasswordPushCmd.Flags().StringVarP(&onePasswordPushFilter, "filter", "f", "", "Push servers matching a label (key=val), tag, or name")
	onePasswordPushCmd.Flags().BoolVar(&onePasswordPushInclSkip, "include-skipped", false, "Include servers configured with skip_batch when using --all/--filter")
	onePasswordPushCmd.Flags().BoolVar(&onePasswordDeleteLocal, "delete-local", false, "Delete the managed local key file after a successful push")
	onePasswordPushCmd.Flags().BoolVar(&onePasswordPushInventory, "inventory", false, "Back up the whole servers.yaml into one shared 1Password item (merges with what is already there)")
	onePasswordPushCmd.Flags().BoolVar(&onePasswordPushPreferLocal, "prefer-local", false, "With --inventory, resolve every conflict in favour of this machine's servers.yaml")
	onePasswordPushCmd.Flags().BoolVar(&onePasswordPushPreferRemote, "prefer-remote", false, "With --inventory, resolve every conflict in favour of the 1Password backup")
	onePasswordPushCmd.ValidArgsFunction = completeServerNames

	onePasswordConfigCmd.Flags().StringVar(&onePasswordConfigVault, "vault", "", "Remember this vault as the default target")
	onePasswordConfigCmd.Flags().StringVar(&onePasswordConfigAccount, "account", "", "Remember this account as the default")
	onePasswordConfigCmd.Flags().BoolVar(&onePasswordConfigUnset, "unset", false, "Forget the remembered vault and account")

	onePasswordPullCmd.Flags().BoolVar(&onePasswordPullAll, "all", false, "Pull every server that keeps a credential in 1Password (equivalent to --filter all)")
	onePasswordPullCmd.Flags().StringVarP(&onePasswordPullFilter, "filter", "f", "", "Pull servers matching a label (key=val), tag, or name")
	onePasswordPullCmd.Flags().BoolVarP(&onePasswordPullYes, "yes", "y", false, "Write plaintext passwords into servers.yaml without asking for confirmation")
	onePasswordPullCmd.Flags().BoolVar(&onePasswordPullForce, "force", false, "Overwrite a local key file even when it holds a different key")
	onePasswordPullCmd.Flags().BoolVar(&onePasswordPullInclSkip, "include-skipped", false, "Include servers configured with skip_batch when using --all/--filter")
	onePasswordPullCmd.Flags().BoolVar(&onePasswordPullFromVault, "from-vault", false, "Find credentials by their 1Password item name instead of by the references in servers.yaml")
	onePasswordPullCmd.Flags().BoolVar(&onePasswordPullMaterialize, "materialize", false, "With --from-vault, write credentials to local disk instead of just rebinding to op:// references")
	onePasswordPullCmd.Flags().StringVar(&onePasswordPullVault, "vault", "", "Vault to search with --from-vault (default: remembered setting, then $OP_VAULT, then the only accessible vault)")
	onePasswordPullCmd.Flags().BoolVar(&onePasswordPullInventory, "inventory", false, "Restore servers.yaml from the shared 1Password backup (merges with this machine's servers)")
	onePasswordPullCmd.Flags().BoolVar(&onePasswordPullPreferLocal, "prefer-local", false, "With --inventory, resolve every conflict in favour of this machine's servers.yaml")
	onePasswordPullCmd.Flags().BoolVar(&onePasswordPullPreferRemote, "prefer-remote", false, "With --inventory, resolve every conflict in favour of the 1Password backup")
	onePasswordPullCmd.ValidArgsFunction = completeServerNames
	onePasswordStatusCmd.Flags().StringVarP(&onePasswordFilter, "filter", "f", "", "Filter servers by label (key=val), tag, or name")

	onePasswordCleanupCmd.Flags().StringVarP(&onePasswordCleanupServer, "server", "s", "", "Only remove the materialized key for this specific server")
	onePasswordCleanupCmd.ValidArgsFunction = completeServerNames

	onePasswordCmd.AddCommand(onePasswordPushCmd, onePasswordPullCmd, onePasswordStatusCmd, onePasswordConfigCmd, onePasswordCleanupCmd)
	rootCmd.AddCommand(onePasswordCmd)
}
