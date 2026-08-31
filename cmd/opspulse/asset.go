package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/asset"
)

var assetCmd = &cobra.Command{
	Use:   "asset",
	Short: "Manage structured business assets",
	Long: `Define, inspect, and manage stateful infrastructure assets (Docker Compose projects,
Volumes, Databases, Directories, Files) with stable IDs for backup and restore operations.

Assets are stored in $XDG_CONFIG_HOME/opspulse/assets.yaml.`,
}

var assetListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all configured assets",
	RunE: func(_ *cobra.Command, _ []string) error {
		store := asset.NewDefaultStore()
		assets, err := store.List()
		if err != nil {
			return fmt.Errorf("failed to list assets: %w", err)
		}

		if len(assets) == 0 {
			fmt.Printf("No assets configured. Add assets via:\n  opspulse asset add <id> --type <type> --source <path>\n\nConfig: %s\n", store.FilePath())
			return nil
		}

		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		_, _ = fmt.Fprintln(tw, "ID\tTYPE\tSOURCE\tENGINE\tDESCRIPTION")
		_, _ = fmt.Fprintln(tw, "--\t----\t------\t------\t-----------")

		for _, a := range assets {
			engine := "-"
			if a.Engine != "" {
				engine = a.Engine
			}
			desc := "-"
			if a.Description != "" {
				desc = a.Description
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
				a.ID, string(a.Type), a.Source, engine, desc)
		}

		return tw.Flush()
	},
}

var (
	assetAddType      string
	assetAddSource    string
	assetAddEngine    string
	assetAddContainer string
	assetAddExcludes  string
	assetAddDesc      string
)

var assetAddCmd = &cobra.Command{
	Use:   "add <id>",
	Short: "Add or update a business asset",
	Long: `Register a new stateful asset with a stable ID for backup and restore operations.

Supported types: docker_compose, volume, database, directory, file

Examples:
  opspulse asset add blog-compose --type docker_compose --source /opt/blog --desc "Ghost blog"
  opspulse asset add blog-mysql --type database --source /var/lib/mysql --engine mysql --container blog-db`,
	Args: cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		id := args[0]

		if assetAddType == "" {
			return fmt.Errorf("--type is required (docker_compose, volume, database, directory, file)")
		}
		if assetAddSource == "" {
			return fmt.Errorf("--source is required")
		}

		a := asset.Asset{
			ID:          id,
			Type:        asset.Type(assetAddType),
			Source:      assetAddSource,
			Engine:      assetAddEngine,
			Container:   assetAddContainer,
			Description: assetAddDesc,
		}

		if assetAddExcludes != "" {
			for _, e := range strings.Split(assetAddExcludes, ",") {
				if trimmed := strings.TrimSpace(e); trimmed != "" {
					a.Excludes = append(a.Excludes, trimmed)
				}
			}
		}

		store := asset.NewDefaultStore()

		// Check if this is an update
		existing, _ := store.Get(id)
		if err := store.Save(a); err != nil {
			return fmt.Errorf("failed to save asset: %w", err)
		}

		if existing != nil {
			fmt.Printf("✅ Asset %q updated successfully in %s\n", id, store.FilePath())
		} else {
			fmt.Printf("✅ Asset %q (%s) saved successfully to %s\n", id, assetAddType, store.FilePath())
		}

		return nil
	},
}

var assetShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Show detailed information for a specific asset",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		id := args[0]
		store := asset.NewDefaultStore()
		a, err := store.Get(id)
		if err != nil {
			return err
		}

		fmt.Printf("Asset: %s\n", a.ID)
		fmt.Printf("  Type        : %s\n", a.Type)
		fmt.Printf("  Source      : %s\n", a.Source)
		if a.Engine != "" {
			fmt.Printf("  Engine      : %s\n", a.Engine)
		}
		if a.Container != "" {
			fmt.Printf("  Container   : %s\n", a.Container)
		}
		if len(a.Excludes) > 0 {
			fmt.Printf("  Excludes    : %s\n", strings.Join(a.Excludes, ", "))
		}
		if a.Description != "" {
			fmt.Printf("  Description : %s\n", a.Description)
		}
		fmt.Printf("  Config      : %s\n", store.FilePath())

		return nil
	},
}

var assetRemoveCmd = &cobra.Command{
	Use:     "remove <id>",
	Aliases: []string{"rm", "delete"},
	Short:   "Remove an asset from configuration",
	Args:    cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		id := args[0]
		store := asset.NewDefaultStore()

		if err := store.Delete(id); err != nil {
			return err
		}

		fmt.Printf("✅ Asset %q removed successfully from %s\n", id, store.FilePath())
		return nil
	},
}

func completeAssetIDs(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	store := asset.NewDefaultStore()
	assets, err := store.List()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	var comps []string
	for _, a := range assets {
		if a.Description != "" {
			comps = append(comps, fmt.Sprintf("%s\t%s (%s)", a.ID, a.Type, a.Description))
		} else {
			comps = append(comps, fmt.Sprintf("%s\t%s @ %s", a.ID, a.Type, a.Source))
		}
	}
	return comps, cobra.ShellCompDirectiveNoFileComp
}

func completeAssetTypes(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	return []string{
		"docker_compose\tDocker Compose project directory",
		"volume\tDocker named volume or storage mount",
		"database\tDatabase logical backup (MySQL, PostgreSQL)",
		"directory\tGeneric filesystem directory",
		"file\tIndividual file or file group",
	}, cobra.ShellCompDirectiveNoFileComp
}

func init() {
	assetAddCmd.Flags().StringVar(&assetAddType, "type", "", "Asset type (docker_compose, volume, database, directory, file)")
	assetAddCmd.Flags().StringVar(&assetAddSource, "source", "", "Source path on the server (required)")
	assetAddCmd.Flags().StringVar(&assetAddEngine, "engine", "", "Database engine (mysql, postgres) — only for 'database' type")
	assetAddCmd.Flags().StringVar(&assetAddContainer, "container", "", "Docker container name — only for 'database' type")
	assetAddCmd.Flags().StringVar(&assetAddExcludes, "excludes", "", "Comma-separated glob exclude patterns")
	assetAddCmd.Flags().StringVarP(&assetAddDesc, "desc", "d", "", "Asset description")

	_ = assetAddCmd.RegisterFlagCompletionFunc("type", completeAssetTypes)

	assetShowCmd.ValidArgsFunction = completeAssetIDs
	assetRemoveCmd.ValidArgsFunction = completeAssetIDs

	assetCmd.AddCommand(assetListCmd)
	assetCmd.AddCommand(assetAddCmd)
	assetCmd.AddCommand(assetShowCmd)
	assetCmd.AddCommand(assetRemoveCmd)

	rootCmd.AddCommand(assetCmd)
}
