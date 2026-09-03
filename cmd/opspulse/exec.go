package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/server"
)

var execTimeout time.Duration

var execCmd = &cobra.Command{
	Use:   "exec <server> <command...>",
	Short: "Execute a command on a remote server",
	Long: `Execute arbitrary shell commands on a specified remote server using OpsPulse's executor engine.
Streams stdout and stderr in real-time and preserves command exit status for scripting and pipelines.`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		serverName := args[0]
		commandStr := strings.Join(args[1:], " ")

		store := server.NewDefaultStore()
		srv, err := store.Get(serverName)
		if err != nil {
			return err
		}

		ctx := context.Background()
		var cancel context.CancelFunc
		if execTimeout > 0 {
			ctx, cancel = context.WithTimeout(ctx, execTimeout)
			defer cancel()
		}

		exec := executor.NewSSHExecutor()
		target := executor.NewServerTarget(*srv)

		res, err := exec.Execute(ctx, target, "exec", commandStr, os.Stdout)
		if err != nil {
			return fmt.Errorf("execution error on %s: %w", serverName, err)
		}
		if !res.Success {
			return fmt.Errorf("command failed on %s (exit code %d): %v", serverName, res.ExitCode, res.Error)
		}

		return nil
	},
}

func init() {
	execCmd.Flags().DurationVarP(&execTimeout, "timeout", "T", 60*time.Second, "Command execution timeout (0 to disable)")
	execCmd.ValidArgsFunction = completeServerNames
	rootCmd.AddCommand(execCmd)
}
