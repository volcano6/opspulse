package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/config"
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
--file only applies together with --write.

Examples:
  # Print SSH config to stdout
  ops export ssh-config

  # Write directly to ~/.ssh/config (idempotent, preserves custom hosts)
  ops export ssh-config --write

  # Write filtered servers to custom path
  ops export ssh-config --write --file ~/.ssh/config.opspulse --filter env=prod`,
	RunE: func(_ *cobra.Command, _ []string) error {
		if exportWritePath != "" && !exportWrite {
			return errors.New("--file requires --write")
		}

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
		} else {
			targetPath = config.ExpandPath(targetPath)
		}

		if len(servers) == 0 {
			// An empty inventory is not something to report as a success: the
			// block would list no host, and "wrote 0 managed hosts" reads as if
			// the export had done something. A file that never carried the
			// block is left alone, so an export cannot conjure one out of an
			// empty inventory; a file that still carries it is rewritten, which
			// is what clears the hosts left over from a previous export.
			hasBlock, err := hasManagedSSHBlock(targetPath)
			if err != nil {
				return err
			}
			if !hasBlock {
				fmt.Println("No managed hosts configured; nothing to write. Add one with 'ops add <name> <host>'.")
				return nil
			}
			if _, _, err := server.UpdateSSHConfigFile(targetPath, servers); err != nil {
				return fmt.Errorf("write ssh config: %w", err)
			}
			_, _ = fmt.Fprintf(os.Stdout, "Emptied the OpsPulse-managed block in %s (no managed hosts remain).\n", targetPath)
			return nil
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

// hasManagedSSHBlock reports whether the file at path already carries an
// OpsPulse-managed block.
//
// The markers are the same rule MergeSSHConfigContent applies when it decides
// between replacing an existing block and appending a new one, so a file this
// reports true for is exactly one that export would rewrite.
func hasManagedSSHBlock(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read ssh config %s: %w", path, err)
	}
	content := string(data)
	return strings.Contains(content, server.MarkerBegin) && strings.Contains(content, server.MarkerEnd), nil
}

func init() {
	exportSSHConfigCmd.Flags().BoolVarP(&exportWrite, "write", "w", false, "Write directly to SSH config file (idempotent)")
	exportSSHConfigCmd.Flags().StringVar(&exportWritePath, "file", "", "Target SSH config file path (defaults to ~/.ssh/config; requires --write)")
	exportSSHConfigCmd.Flags().StringVarP(&exportFilter, "filter", "f", "", "Filter servers by label (key=val), tag, or name")

	exportCmd.AddCommand(exportSSHConfigCmd)
	rootCmd.AddCommand(exportCmd)
}
