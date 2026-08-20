package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/bootstrap"
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

func init() {
	bootstrapCmd.Flags().StringVarP(&bootstrapTemplates, "templates", "t", "", "Comma-separated list of templates to execute (e.g. base,security,docker)")
	bootstrapCmd.Flags().BoolVar(&bootstrapDryRun, "dry-run", false, "Simulate execution without establishing SSH connections")
	bootstrapCmd.Flags().BoolVar(&bootstrapContinue, "continue-on-error", false, "Continue executing remaining templates/servers if an error occurs")

	rootCmd.AddCommand(bootstrapCmd)
}
