package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/server"
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Manage server inventory",
	Long:  "Add, list, inspect, test connectivity, and remove managed servers from servers.yaml.",
}


var listFilter string

var serverListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all configured servers",
	RunE: func(_ *cobra.Command, _ []string) error {
		store := server.NewDefaultStore()
		servers, err := store.List()
		if err != nil {
			return fmt.Errorf("failed to list servers: %w", err)
		}

		if len(servers) == 0 {
			fmt.Printf("No servers found. Add one using:\n  opspulse server add <name> --host <host>\n")
			return nil
		}

		var filtered []server.Server
		for _, s := range servers {
			if s.MatchFilter(listFilter) {
				filtered = append(filtered, s)
			}
		}

		if len(filtered) == 0 {
			fmt.Printf("No servers matched filter %q.\n", listFilter)
			return nil
		}

		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		_, _ = fmt.Fprintln(tw, "NAME\tHOST\tPORT\tUSER\tAUTH\tLABELS\tTAGS\tDESCRIPTION")
		_, _ = fmt.Fprintln(tw, "----\t----\t----\t----\t----\t------\t----\t-----------")

		for _, s := range filtered {
			var authMethod string
			if s.KeyPath != "" {
				authMethod = fmt.Sprintf("key (%s)", s.KeyPath)
			} else if s.Password != "" {
				authMethod = "password"
			} else {
				authMethod = "default key"
			}

			tagsStr := "-"
			if len(s.Tags) > 0 {
				tagsStr = strings.Join(s.Tags, ",")
			}

			labelsStr := s.FormatLabels()

			descStr := s.Description
			if descStr == "" {
				descStr = "-"
			}

			_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n",
				s.Name, s.Host, s.Port, s.User, authMethod, labelsStr, tagsStr, descStr)
		}
		return tw.Flush()
	},
}

var serverInfoCmd = &cobra.Command{
	Use:   "info <name>",
	Short: "Inspect system OS, hardware resources, and Docker status of a server",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		name := args[0]
		store := server.NewDefaultStore()
		srv, err := store.Get(name)
		if err != nil {
			return err
		}

		fmt.Printf("🔍 Probing system information for %s (%s)...\n", srv.Name, srv.Address())
		exec := executor.NewSSHExecutor()

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		var outBuf bytes.Buffer
		script := server.BuildInfoProbeScript()
		res, err := exec.Execute(ctx, executor.NewServerTarget(*srv), "probe-info", script, &outBuf)
		if err != nil {
			return fmt.Errorf("❌ Failed to inspect server %q: %w", name, err)
		}
		if !res.Success {
			return fmt.Errorf("❌ Probe script failed on %q: %v", name, res.Error)
		}

		outputStr := outBuf.String()
		if strings.TrimSpace(outputStr) == "" || !strings.Contains(outputStr, "---OS_RELEASE---") {
			trimmed := strings.TrimSpace(outputStr)
			if trimmed == "" {
				return fmt.Errorf("❌ Probe script returned empty output for %q. Please check server status and retry", name)
			}
			return fmt.Errorf("❌ Probe script returned unexpected output for %q:\n%s", name, trimmed)
		}

		info := server.ParseInfo(srv.Name, srv.Address(), outputStr)
		info.FormatBox(os.Stdout)
		return nil
	},
}

var serverTestCmd = &cobra.Command{
	Use:   "test <name>",
	Short: "Test SSH connectivity to a server",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		name := args[0]
		store := server.NewDefaultStore()
		srv, err := store.Get(name)
		if err != nil {
			return err
		}

		fmt.Printf("Connecting to %s (%s)...\n", srv.Name, srv.Address())
		exec := executor.NewSSHExecutor()

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		rtt, banner, err := exec.Test(ctx, executor.NewServerTarget(*srv))
		if err != nil {
			if isManagedKey(srv.KeyPath) {
				fmt.Printf("💡 Note: This server uses an OpsPulse-managed key (%s).\n", srv.KeyPath)
				fmt.Printf("   If the key is invalid, run 'opspulse server set %s --key <new_path>' to replace, or 'opspulse server remove %s' to clean up.\n", srv.Name, srv.Name)
			}
			return fmt.Errorf("❌ Connection failed: %w", err)
		}

		fmt.Printf("✅ Connection successful!\n")
		fmt.Printf("   Latency : %.2f ms\n", float64(rtt.Microseconds())/1000.0)
		fmt.Printf("   System  : %s\n", banner)
		return nil
	},
}

var removeKeepKey bool

var serverRemoveCmd = &cobra.Command{
	Use:     "remove <name>",
	Aliases: []string{"rm", "delete"},
	Short:   "Remove a server from the inventory",
	Args:    cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		name := args[0]
		store := server.NewDefaultStore()
		srv, err := store.Get(name)
		if err != nil {
			return err
		}
		if err := CleanupManagedKeyWithRefCheck(os.Stdout, store, srv.Name, srv.KeyPath, removeKeepKey); err != nil {
			return err
		}
		if err := store.Delete(name); err != nil {
			return err
		}
		fmt.Printf("✅ Server %q removed successfully from inventory.\n", name)
		return nil
	},
}

func completeServerNames(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	store := server.NewDefaultStore()
	servers, err := store.List()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	var comps []string
	for _, s := range servers {
		if s.Description != "" {
			comps = append(comps, fmt.Sprintf("%s\t%s (%s)", s.Name, s.Host, s.Description))
		} else {
			comps = append(comps, fmt.Sprintf("%s\t%s", s.Name, s.Host))
		}
	}
	return comps, cobra.ShellCompDirectiveNoFileComp
}

func init() {
	serverRemoveCmd.Flags().BoolVar(&removeKeepKey, "keep-key", false, "Do not delete the managed private key file from disk")

	serverListCmd.Flags().StringVarP(&listFilter, "filter", "f", "", "Filter servers by label (key=val), tag, or name")

	serverInfoCmd.ValidArgsFunction = completeServerNames
	serverTestCmd.ValidArgsFunction = completeServerNames
	serverRemoveCmd.ValidArgsFunction = completeServerNames

	serverCmd.AddCommand(serverListCmd)
	serverCmd.AddCommand(serverInfoCmd)
	serverCmd.AddCommand(serverTestCmd)
	serverCmd.AddCommand(serverRemoveCmd)

	lsCmd.Flags().StringVarP(&listFilter, "filter", "f", "", "Filter servers by key=value, tag, or name")

	rootCmd.AddCommand(serverCmd)
	rootCmd.AddCommand(lsCmd)
}

var lsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List all configured servers (shortcut for 'ops server list')",
	RunE: func(cmd *cobra.Command, args []string) error {
		return serverListCmd.RunE(cmd, args)
	},
}

func completePrivateKeyPath(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	expanded := expandHome(toComplete)
	dir := filepath.Dir(expanded)
	prefix := filepath.Base(expanded)
	displayDir := filepath.Dir(toComplete)
	if strings.HasSuffix(toComplete, "/") || strings.HasSuffix(toComplete, `\`) {
		dir = strings.TrimRight(expanded, `/\`)
		prefix = ""
		displayDir = strings.TrimRight(toComplete, `/\`)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	if displayDir == "." && !strings.ContainsAny(toComplete, `/\`) {
		displayDir = ""
	}
	completions := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) || !isPrivateKeyName(entry.Name()) {
			continue
		}
		candidate := entry.Name()
		if displayDir != "" {
			if strings.Contains(toComplete, "/") && !strings.Contains(toComplete, `\`) {
				candidate = path.Join(displayDir, entry.Name())
			} else {
				candidate = filepath.Join(displayDir, entry.Name())
			}
		}
		completions = append(completions, candidate)
	}
	return completions, cobra.ShellCompDirectiveNoFileComp
}

func isPrivateKeyName(name string) bool {
	return (strings.HasPrefix(name, "id_") && !strings.HasSuffix(name, ".pub")) ||
		strings.EqualFold(filepath.Ext(name), ".pem")
}
