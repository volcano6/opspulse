package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/server"
)

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export configurations and tool integrations",
	Long:  "Export Ops configurations, inventory, and tool integrations such as OpenSSH config for VS Code and Cursor.",
}

var (
	exportWritePath string
	exportWrite     bool
	exportFilter    string
)

var exportSSHConfigCmd = &cobra.Command{
	Use:   "ssh-config",
	Short: "Export managed servers as OpenSSH config (for VS Code, Cursor, and native ssh)",
	Long: `Render managed servers into OpenSSH config format for seamless integration with
VS Code Remote-SSH, Cursor, GoLand, and the native 'ssh' terminal command.

By default, the rendered configuration is printed to stdout.
Use --write to automatically and idempotently update ~/.ssh/config.

Examples:
  # Print SSH config to stdout
  ops export ssh-config

  # Write directly to ~/.ssh/config (idempotent, preserves custom hosts)
  ops export ssh-config --write

  # Write filtered servers to custom path
  ops export ssh-config --write --file ~/.ssh/config.opspulse --filter env=prod`,
	RunE: func(_ *cobra.Command, _ []string) error {
		store := server.NewDefaultStore()
		servers, err := store.List()
		if err != nil {
			return fmt.Errorf("list servers: %w", err)
		}

		if exportFilter != "" {
			var filtered []server.Server
			for _, s := range servers {
				if s.MatchFilter(exportFilter) {
					filtered = append(filtered, s)
				}
			}
			servers = filtered
		}

		if !exportWrite {
			rendered := server.RenderSSHConfig(servers)
			_, _ = fmt.Fprint(os.Stdout, rendered)
			return nil
		}

		targetPath := exportWritePath
		if targetPath == "" {
			var pathErr error
			targetPath, pathErr = server.DefaultSSHConfigPath()
			if pathErr != nil {
				return pathErr
			}
		}

		_, count, err := server.UpdateSSHConfigFile(targetPath, servers)
		if err != nil {
			return fmt.Errorf("write ssh config: %w", err)
		}

		_, _ = fmt.Fprintf(os.Stdout, "✅ Successfully wrote %d managed hosts to %s\n", count, targetPath)
		_, _ = fmt.Fprintln(os.Stdout, "   VS Code Remote-SSH, Cursor, and 'ssh <name>' are now ready!")
		return nil
	},
}

func init() {
	exportSSHConfigCmd.Flags().BoolVarP(&exportWrite, "write", "w", false, "Write directly to SSH config file (idempotent)")
	exportSSHConfigCmd.Flags().StringVar(&exportWritePath, "file", "", "Target SSH config file path (defaults to ~/.ssh/config)")
	exportSSHConfigCmd.Flags().StringVarP(&exportFilter, "filter", "f", "", "Filter servers by label (key=val), tag, or name")

	exportCmd.AddCommand(exportSSHConfigCmd)
	rootCmd.AddCommand(exportCmd)
}
