package main

import (
	"context"
	"fmt"
	"os"
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
	Long:  "Add, list, test connectivity, and remove managed servers from servers.yaml.",
}

var (
	addHost     string
	addPort     int
	addUser     string
	addKey      string
	addPassword string
	addTags     string
	addLabels   string
	addDesc     string
)

var serverAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Add or update a server in the inventory",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		name := args[0]
		if addHost == "" {
			return fmt.Errorf("--host is required")
		}

		var tags []string
		if addTags != "" {
			for _, t := range strings.Split(addTags, ",") {
				if trimmed := strings.TrimSpace(t); trimmed != "" {
					tags = append(tags, trimmed)
				}
			}
		}

		labels := make(map[string]string)
		if addLabels != "" {
			for _, pair := range strings.Split(addLabels, ",") {
				trimmed := strings.TrimSpace(pair)
				if trimmed == "" {
					continue
				}
				if strings.Contains(trimmed, "=") {
					kv := strings.SplitN(trimmed, "=", 2)
					labels[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
				} else {
					labels[trimmed] = "true"
				}
			}
		}

		srv := server.Server{
			Name:        name,
			Host:        addHost,
			Port:        addPort,
			User:        addUser,
			KeyPath:     addKey,
			Password:    addPassword,
			Tags:        tags,
			Labels:      labels,
			Description: addDesc,
		}

		store := server.NewDefaultStore()
		if err := store.Save(srv); err != nil {
			return fmt.Errorf("failed to save server: %w", err)
		}

		fmt.Printf("✅ Server %q (%s) saved successfully to %s\n", srv.Name, srv.Address(), store.FilePath())
		return nil
	},
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
			return fmt.Errorf("❌ Connection failed: %w", err)
		}

		fmt.Printf("✅ Connection successful!\n")
		fmt.Printf("   Latency : %.2f ms\n", float64(rtt.Microseconds())/1000.0)
		fmt.Printf("   System  : %s\n", banner)
		return nil
	},
}

var serverRemoveCmd = &cobra.Command{
	Use:     "remove <name>",
	Aliases: []string{"rm", "delete"},
	Short:   "Remove a server from the inventory",
	Args:    cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		name := args[0]
		store := server.NewDefaultStore()
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
	serverAddCmd.Flags().StringVar(&addHost, "host", "", "Server IP or hostname (required)")
	serverAddCmd.Flags().IntVarP(&addPort, "port", "p", 22, "SSH port")
	serverAddCmd.Flags().StringVarP(&addUser, "user", "u", "root", "SSH username")
	serverAddCmd.Flags().StringVarP(&addKey, "key", "k", "", "Path to private key file")
	serverAddCmd.Flags().StringVar(&addPassword, "password", "", "SSH password (optional)")
	serverAddCmd.Flags().StringVarP(&addTags, "tags", "t", "", "Comma-separated tags (e.g. prod,web)")
	serverAddCmd.Flags().StringVarP(&addLabels, "labels", "l", "", "Comma-separated key=value labels (e.g. provider=oracle,region=sg)")
	serverAddCmd.Flags().StringVarP(&addDesc, "desc", "d", "", "Server description")

	serverListCmd.Flags().StringVarP(&listFilter, "filter", "f", "", "Filter servers by label (key=val), tag, or name")

	serverTestCmd.ValidArgsFunction = completeServerNames
	serverRemoveCmd.ValidArgsFunction = completeServerNames

	serverCmd.AddCommand(serverAddCmd)
	serverCmd.AddCommand(serverListCmd)
	serverCmd.AddCommand(serverTestCmd)
	serverCmd.AddCommand(serverRemoveCmd)

	rootCmd.AddCommand(serverCmd)
}
