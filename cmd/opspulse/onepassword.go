package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/secret"
)

// The 'ops 1p' command surface: the subcommands, their flags, and the vault and
// account resolution that backup, restore and status all share.

// Flags shared by 'ops 1p backup' and 'ops 1p restore'. The two commands never
// run together, and both mean the same thing by every one of these, so sharing
// them keeps the two flag surfaces from drifting apart.
var (
	onePasswordVault        string
	onePasswordAccount      string
	onePasswordPreferLocal  bool
	onePasswordPreferRemote bool
)

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
  ops 1p doctor             Check the whole path end to end, changing nothing

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
	Long: `Back up this machine's server list and every private key it holds to 1Password.

The whole thing travels in one Secure Note named after this machine
(opspulse_inventory_<hostname>), which is what keeps a backup down to three op
calls rather than one per server. It is deliberately unconditional: no server
selection, no skip list.

The item is read before it is written, and a private key that can no longer be
read on this machine is carried over from the previous backup instead of being
dropped - that copy is the only one left once the local file is gone. A read that
fails for any other reason stops the backup rather than overwriting blind.

servers.yaml is NOT rewritten. Local disk stays the source of truth, so a backup
never changes how 'ops ssh' connects and never turns a working server into one
that depends on 1Password being unlocked.

  ops 1p backup
  ops 1p backup --vault Private

A server still holding an 'op://' reference is refused outright: uploading it
would push a stale reference into the backup. Run 'ops 1p restore' to migrate it
to a local credential first.

Each machine backs up into its own item, so two machines never overwrite each
other, and a restore unions them. Removing a server from a backup is therefore
done by hand: delete it locally, then back up again - the other machines keep it
until they back up too.`,
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
per-machine backup items first, then every server's key is written to
~/.ssh/opspulse_<server> and every password into servers.yaml. That is all a new
machine needs after installing the 1Password CLI.

  ops 1p restore               # the server list and every credential
  ops 1p restore web db-01     # only these servers' credentials
  ops 1p restore --yes         # unattended

With arguments only the named servers' credentials are restored, and servers.yaml
is left alone. A name that is not in servers.yaml is an error rather than a
silent skip, since the usual cause is restoring credentials before the list.

Because a password can only come back as plaintext, OpsPulse asks for
confirmation before it writes anything, whenever the restore would write one. In
a non-interactive shell the command refuses instead of hanging, unless --yes says
the answer up front.

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

// resolveBackupVault picks the vault to write into, listing the vaults only when
// there is nothing recorded to go on.
//
// `op vault list` is a full round trip through the Desktop App, and the whole
// point of the backup is to spend as few of those as possible. A recorded
// preference is therefore taken at face value: if it has gone stale the write
// fails and names the vault, which is a clearer report than a list would have
// produced anyway.
//
// The vault that had to be discovered is remembered, so that only the first
// backup ever pays for the listing. Without this every run would cost three
// calls instead of two, which is exactly the cost this design exists to remove.
func resolveBackupVault(ctx context.Context, cli secret.CLI, explicitVault string) (string, error) {
	if vault := strings.TrimSpace(explicitVault); vault != "" {
		rememberOnePasswordSetting("vault", vault)
		return vault, nil
	}
	if candidates := rememberedVaultCandidates(); len(candidates) > 0 {
		return candidates[0], nil
	}
	vault, err := resolveAndValidateVault(ctx, cli, "", true)
	if err != nil {
		return "", err
	}
	rememberOnePasswordSetting("vault", vault)
	return vault, nil
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
// remember records an explicit choice as the new default. Backup wants that,
// since the vault is where items are created; restore's --vault only scopes a
// search, and remembering a read-only archive vault would silently move the
// next backup's target.
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
// must not break an otherwise successful backup.
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

func init() {
	onePasswordCmd.PersistentFlags().StringVar(&onePasswordAccount, "account", "", "1Password account (sign-in address or ID); remembered for future runs")

	onePasswordBackupCmd.Flags().StringVar(&onePasswordVault, "vault", "", "Vault to back up into (default: remembered setting, then $OP_VAULT, then the only accessible vault)")

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

	onePasswordCmd.AddCommand(
		onePasswordBackupCmd,
		onePasswordRestoreCmd,
		onePasswordStatusCmd,
		onePasswordConfigCmd,
		onePasswordDoctorCmd,
	)
	rootCmd.AddCommand(onePasswordCmd)
}
