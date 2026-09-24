package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/volcano6/opspulse/internal/secret"
)

// 'ops 1p config': showing the default vault and account, and remembering them.

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
