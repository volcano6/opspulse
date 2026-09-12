package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/platform"
	"github.com/volcano6/opspulse/internal/secret"
	"github.com/volcano6/opspulse/internal/server"
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
	onePasswordVault       string
	onePasswordAll         bool
	onePasswordFilter      string
	onePasswordDeleteLocal bool
	onePasswordAccount     string
)

var onePasswordCmd = &cobra.Command{
	Use:     "1p",
	Aliases: []string{"1password", "onepassword"},
	Short:   "Move SSH private keys and passwords between local disk and 1Password",
	Long: `Push local credentials into 1Password, or pull them back onto local disk.

  ops 1p push <server>...   Upload local keys/passwords and rebind the servers to op:// references
  ops 1p pull <server>      Write a 1Password-hosted credential back into servers.yaml
  ops 1p status             Show where each server's credentials currently live
  ops 1p config             Show or change the remembered vault and account

Keys are stored as 1Password "SSH Key" items titled opspulse_<server>, and
passwords as "Login" items titled opspulse_<server>_password. Once a credential
has been pushed, servers.yaml keeps only an op:// reference and the value is
resolved on demand at connection time, so it never has to stay on disk.

You normally do not have to name a vault at all: OpsPulse uses the one you
remembered with 'ops 1p config --vault <name>', then $OP_VAULT, and otherwise the
only vault the account can see. The account works the same way, with $OP_ACCOUNT
taking precedence over the remembered value.`,
}

var onePasswordPushCmd = &cobra.Command{
	Use:   "push [server...]",
	Short: "Upload local credentials to 1Password and rebind servers to op:// references",
	Long: `Upload locally stored credentials into 1Password.

A server's private key goes into an "SSH Key" item and its password into a
"Login" item, and the server is rebound so that key_path/password become op://
references. Local key files are left in place unless --delete-local is given; a
pushed password always stops being stored in servers.yaml, since keeping the
plaintext next to an op:// reference would defeat the point.`,
	Args: cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPushToOnePassword(cmd.Context(), args)
	},
}

var onePasswordPullCmd = &cobra.Command{
	Use:   "pull <server>",
	Short: "Copy a 1Password-hosted key back onto local disk and rebind the server",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPullFromOnePassword(cmd.Context(), args[0])
	},
}

var onePasswordStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show where each server's private key currently lives",
	RunE: func(_ *cobra.Command, _ []string) error {
		return runOnePasswordStatus()
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
	cli, err := ensure1PCLI()
	if err != nil {
		return err
	}

	store := server.NewDefaultStore()
	targets, err := selectOnePasswordTargets(store, args)
	if err != nil {
		return err
	}

	vault, err := resolveAndValidateVault(ctx, cli)
	if err != nil {
		return err
	}

	pushed := 0
	for _, srv := range targets {
		changed, err := pushServerToOnePassword(ctx, cli, store, srv, vault)
		if err != nil {
			return err
		}
		if changed {
			pushed++
		}
	}

	fmt.Println()
	if pushed == 0 {
		fmt.Println("Nothing to push: no target server had a local private key or password.")
		return nil
	}
	fmt.Printf("🎉 Pushed credentials for %d server(s) into 1Password vault %q.\n", pushed, vault)
	fmt.Println("💡 These servers now resolve their credentials through op://, so the values no longer live in servers.yaml.")
	if !onePasswordDeleteLocal {
		fmt.Println("   Run 'ops 1p push <server> --delete-local' if you want OpsPulse to remove the managed local key copies for you.")
	}
	fmt.Println("   Verify with: ops 1p status")
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

// pushPrivateKeyToOnePassword uploads a server's local key file into an
// "SSH Key" item, and rebinds key_path to it.
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
	publicKey := authorizedKeyFor(expanded, keyData)

	fmt.Printf("⬆️  Pushing key for %q (%s) into 1Password vault %q...\n", srv.Name, srv.KeyPath, vault)
	if err := writeOnePasswordItem(ctx, cli, vault, title, onePasswordSSHKeyCategory, func(doc []byte) ([]byte, error) {
		return secret.FillSSHKeyItem(doc, title, string(keyData), publicKey)
	}); err != nil {
		return false, err
	}

	srv.KeyPath = secret.BuildSSHKeyRef(vault, title)
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
const (
	onePasswordSSHKeyCategory = "SSH Key"
	onePasswordLoginCategory  = "Login"
)

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

func runPullFromOnePassword(ctx context.Context, name string) error {
	if _, err := ensure1PCLI(); err != nil {
		return err
	}

	store := server.NewDefaultStore()
	srv, err := store.Get(name)
	if err != nil {
		return err
	}
	switch {
	case secret.Is1PRef(srv.KeyPath):
		return pullKeyFromOnePassword(ctx, store, srv)
	case secret.Is1PRef(srv.Password):
		return pullPasswordFromOnePassword(ctx, store, srv)
	case strings.TrimSpace(srv.KeyPath) == "" && strings.TrimSpace(srv.Password) == "":
		return fmt.Errorf("server %q has no credential configured; nothing to pull", name)
	default:
		return fmt.Errorf("server %q keeps its credentials in servers.yaml, not in 1Password; run 'ops 1p push %s' to upload them first", name, name)
	}
}

// pullKeyFromOnePassword writes a 1Password-hosted private key back onto local
// disk and rebinds the server to the file, leaving the item in 1Password alone.
func pullKeyFromOnePassword(ctx context.Context, store *server.Store, srv *server.Server) error {
	ref := srv.KeyPath
	vault, item, _, _ := secret.Parse1PRef(ref)
	fmt.Printf("⬇️  Pulling key for %q from 1Password (%s/%s)...\n", srv.Name, vault, item)

	keyData, err := onePasswordResolver().ResolveSSHKey(ctx, ref)
	if err != nil {
		return err
	}
	if err := validatePrivateKeyContent([]byte(keyData)); err != nil {
		return fmt.Errorf("the 1Password item %q does not contain a usable SSH private key: %w", item, err)
	}

	storedDest, expandedDest, err := setupKeyPath(srv.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(expandedDest), 0o700); err != nil {
		return fmt.Errorf("create ~/.ssh directory: %w", err)
	}
	body := []byte(keyData)
	if !strings.HasSuffix(keyData, "\n") {
		body = append(body, '\n')
	}
	if err := os.WriteFile(expandedDest, body, 0o600); err != nil {
		return fmt.Errorf("write private key to %s: %w", expandedDest, err)
	}
	writePublicKeyFile(expandedDest, body)

	srv.KeyPath = storedDest
	if err := store.Save(*srv); err != nil {
		return fmt.Errorf("rebind server %q to the local key: %w", srv.Name, err)
	}

	fmt.Printf("✅ Saved to %s and rebound %q to it.\n", storedDest, srv.Name)
	fmt.Printf("   The 1Password item %q was left untouched.\n", secret.SSHKeyItemTitle(srv.Name))
	return nil
}

// pullPasswordFromOnePassword writes a 1Password-hosted password back into
// servers.yaml as plaintext, leaving the item in 1Password alone.
func pullPasswordFromOnePassword(ctx context.Context, store *server.Store, srv *server.Server) error {
	ref := srv.Password
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

	fmt.Printf("✅ Wrote the password for %q back into servers.yaml as plaintext.\n", srv.Name)
	fmt.Printf("   The 1Password item %q was left untouched.\n", secret.PasswordItemTitle(srv.Name))
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

	if accounts, err := listOnePasswordAccounts(ctx, cli); err == nil && len(accounts) > 0 {
		fmt.Println("\nAccounts visible to the CLI:")
		for _, a := range accounts {
			fmt.Printf("  %-30s %s\n", a.URL, a.Email)
		}
	}

	names, err := listVaults(ctx, cli)
	if err != nil {
		fmt.Printf("\n⚠️  Could not list vaults: %v\n", err)
		return nil
	}
	fmt.Printf("\nVaults visible to the current account: %s\n", strings.Join(names, ", "))

	if settings.IsZero() {
		fmt.Println("\n💡 Remember a default so plain 'ops 1p push --all' needs no flags:")
		fmt.Println("   ops 1p config --vault <name> [--account <sign-in-address>]")
	}
	return nil
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

func selectOnePasswordTargets(store *server.Store, args []string) ([]*server.Server, error) {
	if len(args) > 0 {
		targets := make([]*server.Server, 0, len(args))
		for _, name := range args {
			srv, err := store.Get(name)
			if err != nil {
				return nil, err
			}
			targets = append(targets, srv)
		}
		return targets, nil
	}
	if !onePasswordAll {
		return nil, fmt.Errorf("specify at least one server name, or pass --all to target every server")
	}

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

// resolveAndValidateVault decides which vault new items go into.
//
// Precedence, from strongest to weakest:
//  1. --vault: an explicit instruction, so an unknown name is a hard error.
//  2. $OP_VAULT, then the remembered setting: a name that no longer exists warns
//     and falls through rather than wedging every future command.
//  3. the only vault the account can see: no point demanding a choice.
func resolveAndValidateVault(ctx context.Context, cli secret.CLI) (string, error) {
	names, err := listVaults(ctx, cli)
	if err != nil {
		return "", err
	}

	explicit := strings.TrimSpace(onePasswordVault)
	vault, warnings, err := chooseVault(names, explicit, rememberedVaultCandidates())
	for _, warning := range warnings {
		fmt.Printf("⚠️  %s\n", warning)
	}
	if err != nil {
		return "", err
	}
	if explicit != "" {
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
	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
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
	if winget == "" {
		fmt.Println("OpsPulse could not reach winget.exe from WSL.")
		fmt.Println("Install the Windows build from Windows PowerShell instead:")
		fmt.Println("  winget install AgileBits.1Password.CLI")
		return fmt.Errorf("winget.exe is not reachable from WSL")
	}

	fmt.Println("Running (Windows build): winget.exe install AgileBits.1Password.CLI")
	cmd := exec.Command(winget, // #nosec G204 -- fixed command, no user input
		"install", "--id", "AgileBits.1Password.CLI", "-e",
		"--accept-source-agreements", "--accept-package-agreements")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func init() {
	onePasswordCmd.PersistentFlags().StringVar(&onePasswordAccount, "account", "", "1Password account (sign-in address or ID); remembered for future runs")
	onePasswordPushCmd.Flags().StringVar(&onePasswordVault, "vault", "", "Vault for new items (default: remembered setting, then $OP_VAULT, then the only accessible vault)")
	onePasswordPushCmd.Flags().BoolVar(&onePasswordAll, "all", false, "Push every server that currently uses a local private key")
	onePasswordPushCmd.Flags().BoolVar(&onePasswordDeleteLocal, "delete-local", false, "Delete the managed local key file after a successful push")
	onePasswordPushCmd.ValidArgsFunction = completeServerNames

	onePasswordConfigCmd.Flags().StringVar(&onePasswordConfigVault, "vault", "", "Remember this vault as the default target")
	onePasswordConfigCmd.Flags().StringVar(&onePasswordConfigAccount, "account", "", "Remember this account as the default")
	onePasswordConfigCmd.Flags().BoolVar(&onePasswordConfigUnset, "unset", false, "Forget the remembered vault and account")

	onePasswordPullCmd.ValidArgsFunction = completeServerNames
	onePasswordStatusCmd.Flags().StringVarP(&onePasswordFilter, "filter", "f", "", "Filter servers by label (key=val), tag, or name")

	onePasswordCmd.AddCommand(onePasswordPushCmd, onePasswordPullCmd, onePasswordStatusCmd, onePasswordConfigCmd)
	rootCmd.AddCommand(onePasswordCmd)
}
