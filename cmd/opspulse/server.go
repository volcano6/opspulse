package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
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

		return renderServerTable(os.Stdout, filtered)
	},
}

func renderServerTable(w io.Writer, servers []server.Server) error {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tTARGET\tVIA JUMP\tAUTH\tTAGS\tDESCRIPTION")

	for _, s := range servers {
		user := s.User
		if user == "" {
			user = "root"
		}
		target := fmt.Sprintf("%s@%s", user, s.Host)
		if s.Port > 0 && s.Port != 22 {
			target = fmt.Sprintf("%s:%d", target, s.Port)
		}

		var authMethod string
		if s.KeyPath != "" {
			if isManagedKey(s.KeyPath) {
				authMethod = "key (managed)"
			} else {
				authMethod = fmt.Sprintf("key (%s)", formatKeyDisplay(s.KeyPath))
			}
		} else if s.Password != "" {
			authMethod = "password"
		} else {
			authMethod = "default key"
		}

		jumpStr := "-"
		if s.JumpHost != "" {
			jumpStr = s.JumpHost
		}

		tagsStr := formatTagsAndLabels(s)

		descStr := s.Description
		if descStr == "" {
			descStr = "-"
		}

		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			s.Name, target, jumpStr, authMethod, tagsStr, descStr)
	}
	return tw.Flush()
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

		if srv.JumpHost != "" {
			fmt.Printf("🔍 Probing system information for %s (%s via %s)...\n", srv.Name, srv.Address(), srv.JumpHost)
		} else {
			fmt.Printf("🔍 Probing system information for %s (%s)...\n", srv.Name, srv.Address())
		}
		exec := executor.NewSSHExecutor().WithServerResolver(store.Get)

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

		if srv.JumpHost != "" {
			fmt.Printf("Connecting to %s (%s via jump host %s)...\n", srv.Name, srv.Address(), srv.JumpHost)
		} else {
			fmt.Printf("Connecting to %s (%s)...\n", srv.Name, srv.Address())
		}
		exec := executor.NewSSHExecutor().WithServerResolver(store.Get)

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

		dependents, depErr := store.GetDependents(name)
		if depErr == nil && len(dependents) > 0 {
			var depNames []string
			for _, d := range dependents {
				depNames = append(depNames, d.Name)
			}
			return fmt.Errorf("cannot remove server %q: %d server(s) depend on it as a jump host (%s). Please remove or reconfigure them first",
				name, len(dependents), strings.Join(depNames, ", "))
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
		var descParts []string
		user := s.User
		if user == "" {
			user = "root"
		}
		target := fmt.Sprintf("%s@%s", user, s.Host)
		if s.Port > 0 && s.Port != 22 {
			target = fmt.Sprintf("%s:%d", target, s.Port)
		}
		descParts = append(descParts, target)

		if s.JumpHost != "" {
			descParts = append(descParts, fmt.Sprintf("via %s", s.JumpHost))
		}
		if s.Description != "" {
			descParts = append(descParts, s.Description)
		}
		comps = append(comps, fmt.Sprintf("%s\t%s", s.Name, strings.Join(descParts, ", ")))
	}
	return comps, cobra.ShellCompDirectiveNoFileComp
}

func formatKeyDisplay(keyPath string) string {
	if strings.HasPrefix(keyPath, "~") {
		return keyPath
	}
	home, err := os.UserHomeDir()
	if err == nil && home != "" && strings.HasPrefix(keyPath, home) {
		rel := strings.TrimPrefix(keyPath, home)
		return "~" + filepath.ToSlash(rel)
	}
	return filepath.Base(keyPath)
}

func formatTagsAndLabels(s server.Server) string {
	var parts []string
	if len(s.Tags) > 0 {
		parts = append(parts, strings.Join(s.Tags, ","))
	}
	lbls := s.FormatLabels()
	if lbls != "-" && lbls != "" {
		parts = append(parts, lbls)
	}
	if s.SkipBatch {
		hasTag := false
		for _, t := range s.Tags {
			if strings.EqualFold(t, "skip-batch") || strings.EqualFold(t, "[skip-batch]") {
				hasTag = true
				break
			}
		}
		if !hasTag {
			parts = append(parts, "[skip-batch]")
		}
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " ")
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

	testCmd.ValidArgsFunction = completeServerNames
	infoCmd.ValidArgsFunction = completeServerNames

	rootCmd.AddCommand(serverCmd)
	rootCmd.AddCommand(lsCmd)
	rootCmd.AddCommand(testCmd)
	rootCmd.AddCommand(infoCmd)
}

var lsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List all configured servers (shortcut for 'ops server list')",
	RunE: func(cmd *cobra.Command, args []string) error {
		return serverListCmd.RunE(cmd, args)
	},
}

var testCmd = &cobra.Command{
	Use:   "test <name>",
	Short: "Test SSH connectivity to a server (shortcut for 'ops server test')",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return serverTestCmd.RunE(cmd, args)
	},
}

var infoCmd = &cobra.Command{
	Use:   "info <name>",
	Short: "Inspect system OS, hardware resources, and Docker status of a server (shortcut for 'ops server info')",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return serverInfoCmd.RunE(cmd, args)
	},
}

func completePrivateKeyPath(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	var dir, prefix, displayDir string
	isSlash := strings.Contains(toComplete, "/") || !strings.Contains(toComplete, `\`)

	if toComplete == "" {
		dir = "."
		prefix = ""
		displayDir = ""
	} else {
		expanded := expandHome(toComplete)
		if strings.HasSuffix(toComplete, "/") || strings.HasSuffix(toComplete, `\`) {
			dir = strings.TrimRight(expanded, `/\`)
			if dir == "" && (strings.HasPrefix(expanded, "/") || strings.HasPrefix(expanded, `\`)) {
				dir = "/"
			}
			if len(dir) == 2 && dir[1] == ':' {
				dir += `\`
			}
			prefix = ""
			displayDir = strings.TrimRight(toComplete, `/\`)
			if displayDir == "" && (strings.HasPrefix(toComplete, "/") || strings.HasPrefix(toComplete, `\`)) {
				displayDir = "/"
			}
		} else {
			dir = filepath.Dir(expanded)
			if dir == "" {
				dir = "."
			}
			prefix = filepath.Base(expanded)
			if isSlash {
				displayDir = path.Dir(toComplete)
			} else {
				displayDir = filepath.Dir(toComplete)
			}
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	if displayDir == "." && !strings.ContainsAny(toComplete, `/\`) {
		displayDir = ""
	}

	completions := make([]string, 0, len(entries))
	hasDir := false

	for _, entry := range entries {
		name := entry.Name()
		if prefix != "" && !strings.HasPrefix(name, prefix) {
			continue
		}

		// Skip hidden files/directories unless prefix explicitly starts with a dot
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(prefix, ".") {
			continue
		}

		if entry.IsDir() {
			hasDir = true
			candidate := name + "/"
			if displayDir != "" {
				if displayDir == "/" {
					candidate = "/" + name + "/"
				} else if isSlash {
					candidate = path.Join(displayDir, name) + "/"
				} else {
					candidate = filepath.Join(displayDir, name) + `\`
				}
			}
			completions = append(completions, candidate)
			continue
		}

		if !isPrivateKeyName(name) {
			continue
		}

		candidate := name
		if displayDir != "" {
			if displayDir == "/" {
				candidate = "/" + name
			} else if isSlash {
				candidate = path.Join(displayDir, name)
			} else {
				candidate = filepath.Join(displayDir, name)
			}
		}
		completions = append(completions, candidate)
	}

	// When user starts from empty, also suggest ~/.ssh/ as a convenient shortcut
	if toComplete == "" {
		completions = append(completions, "~/.ssh/")
		hasDir = true
	}

	directive := cobra.ShellCompDirectiveNoFileComp
	if hasDir {
		directive = cobra.ShellCompDirectiveNoSpace | cobra.ShellCompDirectiveNoFileComp
	}

	return completions, directive
}

func isPrivateKeyName(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".pub") {
		return false
	}
	if lower == "known_hosts" || lower == "known_hosts.old" || lower == "authorized_keys" || lower == "config" {
		return false
	}
	ext := filepath.Ext(lower)
	if ext == ".pem" || ext == ".key" || ext == ".rsa" || ext == ".pkcs8" {
		return true
	}
	if strings.HasPrefix(lower, "id_") {
		return true
	}
	if ext == ".txt" || ext == ".json" || ext == ".yaml" || ext == ".yml" || ext == ".md" || ext == ".sh" || ext == ".exe" || ext == ".log" {
		return false
	}
	return true
}
