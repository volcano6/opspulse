package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/volcano6/opspulse/internal/secret"
	"github.com/volcano6/opspulse/internal/server"
	"github.com/volcano6/opspulse/internal/sftp"
)

// 'ops 1p status': offline by default, showing where each server's credentials
// come from and whether the files they name are still on this machine.

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
//
// The per-machine backup documents are read as well, because that is where a
// server backed up since the switch actually lives. It costs one op call per
// machine that has ever backed up, which --remote has already opted into by
// being the flag that contacts 1Password at all.
func statusVaultDiscovery(ctx context.Context) (*vaultDiscovery, error) {
	cli, err := ensure1PCLI()
	if err != nil {
		return nil, err
	}
	vault, err := resolveAndValidateVault(ctx, cli, onePasswordVault, false)
	if err != nil {
		return nil, err
	}
	index := newItemIndex(cli, vault)
	discovery, err := discoverVaultItems(ctx, index)
	if err != nil {
		return nil, err
	}
	blobs, err := readBackupBlobs(ctx, cli, vault, index)
	if err != nil {
		return nil, err
	}
	discovery.machineBackups = blobs
	return discovery, nil
}

// describeBackupState renders the --remote column for one server.
func describeBackupState(discovery *vaultDiscovery, s server.Server) string {
	if ref := discovery.keyRef(s.Name); ref != "" {
		return "✅ " + onePasswordRefDisplay(ref)
	}
	if titles := discovery.backedUpIn(s.Name); len(titles) > 0 {
		return "✅ " + strings.Join(titles, ", ")
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
