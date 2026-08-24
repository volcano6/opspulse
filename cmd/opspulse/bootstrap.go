package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/bootstrap"
	"github.com/volcano6/opspulse/internal/server"
	"github.com/volcano6/opspulse/internal/template"
)

var (
	bootstrapTemplates string
	bootstrapDryRun    bool
	bootstrapContinue  bool
)

var bootstrapCmd = &cobra.Command{
	Use:   "bootstrap <server1,server2...>",
	Short: "Bootstrap servers with specified script templates",
	Long: `Initialize and configure one or more servers sequentially by executing
a series of script templates over SSH. Logs are streamed to the console
and saved locally under $XDG_DATA_HOME/opspulse/logs/.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		if bootstrapTemplates == "" {
			return fmt.Errorf("at least one template must be specified using --templates or -t (e.g. -t docker,base)")
		}

		// Split servers by comma or space
		var serverNames []string
		for _, arg := range args {
			for _, s := range strings.Split(arg, ",") {
				if trimmed := strings.TrimSpace(s); trimmed != "" {
					serverNames = append(serverNames, trimmed)
				}
			}
		}

		// Split templates by comma
		var templateNames []string
		for _, t := range strings.Split(bootstrapTemplates, ",") {
			if trimmed := strings.TrimSpace(t); trimmed != "" {
				templateNames = append(templateNames, trimmed)
			}
		}

		svc := bootstrap.NewDefaultService()
		opts := bootstrap.RunOptions{
			ServerNames:   serverNames,
			TemplateNames: templateNames,
			DryRun:        bootstrapDryRun,
			StopOnError:   !bootstrapContinue,
		}

		summary, err := svc.Run(context.Background(), opts, os.Stdout)
		if summary != nil {
			summary.PrintTable(os.Stdout)
		}
		if err != nil {
			return err
		}

		if summary != nil && summary.FailureCount > 0 {
			return fmt.Errorf("%d template(s) failed during bootstrap", summary.FailureCount)
		}

		return nil
	},
}

func completeBootstrapServerArgs(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	store := server.NewDefaultStore()
	servers, err := store.List()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}

	selected := make(map[string]bool)
	for _, arg := range args {
		for _, part := range strings.Split(arg, ",") {
			if p := strings.TrimSpace(part); p != "" {
				selected[p] = true
			}
		}
	}

	if idx := strings.LastIndex(toComplete, ","); idx != -1 {
		prefix := toComplete[:idx+1]
		currentParts := strings.Split(toComplete[:idx], ",")
		for _, p := range currentParts {
			if p = strings.TrimSpace(p); p != "" {
				selected[p] = true
			}
		}

		var comps []string
		for _, s := range servers {
			if !selected[s.Name] {
				if s.Description != "" {
					comps = append(comps, fmt.Sprintf("%s%s\t%s (%s)", prefix, s.Name, s.Host, s.Description))
				} else {
					comps = append(comps, fmt.Sprintf("%s%s\t%s", prefix, s.Name, s.Host))
				}
			}
		}
		return comps, cobra.ShellCompDirectiveNoFileComp
	}

	var comps []string
	for _, s := range servers {
		if !selected[s.Name] {
			if s.Description != "" {
				comps = append(comps, fmt.Sprintf("%s\t%s (%s)", s.Name, s.Host, s.Description))
			} else {
				comps = append(comps, fmt.Sprintf("%s\t%s", s.Name, s.Host))
			}
		}
	}
	return comps, cobra.ShellCompDirectiveNoFileComp
}

func completeBootstrapTemplateFlag(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	loader := template.NewDefaultLoader()
	templates, err := loader.List()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}

	selected := make(map[string]bool)
	if idx := strings.LastIndex(toComplete, ","); idx != -1 {
		prefix := toComplete[:idx+1]
		currentParts := strings.Split(toComplete[:idx], ",")
		for _, p := range currentParts {
			if p = strings.TrimSpace(p); p != "" {
				selected[p] = true
			}
		}

		var comps []string
		for _, t := range templates {
			if !selected[t.Metadata.Name] {
				if t.Metadata.Description != "" {
					comps = append(comps, fmt.Sprintf("%s%s\t%s", prefix, t.Metadata.Name, t.Metadata.Description))
				} else {
					comps = append(comps, prefix+t.Metadata.Name)
				}
			}
		}
		return comps, cobra.ShellCompDirectiveNoFileComp
	}

	var comps []string
	for _, t := range templates {
		if t.Metadata.Description != "" {
			comps = append(comps, fmt.Sprintf("%s\t%s", t.Metadata.Name, t.Metadata.Description))
		} else {
			comps = append(comps, t.Metadata.Name)
		}
	}
	return comps, cobra.ShellCompDirectiveNoFileComp
}

func init() {
	bootstrapCmd.Flags().StringVarP(&bootstrapTemplates, "templates", "t", "", "Comma-separated list of templates to execute (e.g. base,security,docker)")
	bootstrapCmd.Flags().BoolVar(&bootstrapDryRun, "dry-run", false, "Simulate execution without establishing SSH connections")
	bootstrapCmd.Flags().BoolVar(&bootstrapContinue, "continue-on-error", false, "Continue executing remaining templates/servers if an error occurs")

	bootstrapCmd.ValidArgsFunction = completeBootstrapServerArgs
	_ = bootstrapCmd.RegisterFlagCompletionFunc("templates", completeBootstrapTemplateFlag)

	rootCmd.AddCommand(bootstrapCmd)
}
