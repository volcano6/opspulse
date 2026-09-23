// Package main is the entry point for the OpsPulse CLI.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/logger"
	"github.com/volcano6/opspulse/internal/version"
)

var debugFlag bool

var rootCmd = &cobra.Command{
	Use:     "ops",
	Aliases: []string{"opspulse"},
	Short:   "Personal infrastructure lifecycle management",
	Long: `Ops — Self-hosted server automation, backup orchestration, and secure operations.

Environment variables:
  OPSPULSE_HOME                Override the OpsPulse home directory (config and data).
  OPSPULSE_OP_PATH             Force the 1Password CLI binary that 'ops 1p' drives.
  OPSPULSE_KNOWN_HOSTS         Use a different known_hosts file for the built-in SSH client.
  OPSPULSE_TRUST_NEW_HOST_KEY  Set to 1 to accept an unknown host key on first use.
  OP_VAULT                     Default 1Password vault, overriding the remembered one.
  OP_ACCOUNT                   Default 1Password account, overriding the remembered one.`,
	Version:      version.Version,
	SilenceUsage: true,
	PersistentPreRun: func(_ *cobra.Command, _ []string) {
		logger.Setup(debugFlag)
	},
}

// versionLine is the one place the version is rendered, so that 'ops version'
// and 'ops --version' cannot drift apart.
func versionLine() string {
	return fmt.Sprintf("ops %s (commit: %s, built: %s)", version.Version, version.Commit, version.Date)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(_ *cobra.Command, _ []string) {
		fmt.Println(versionLine())
	},
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&debugFlag, "debug", false, "Enable verbose debug logging")
	rootCmd.SetVersionTemplate(versionLine() + "\n")
	rootCmd.AddCommand(versionCmd)
}

func main() {
	if os.Getenv(askpassHelperFlag) == "1" {
		prompt := ""
		if len(os.Args) > 1 {
			prompt = os.Args[1]
		}
		password, err := readSSHAskpassPassword(prompt)
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		_, _ = fmt.Fprint(os.Stdout, password)
		return
	}
	if err := rootCmd.Execute(); err != nil {
		os.Exit(commandExitCode(err))
	}
}

func commandExitCode(err error) int {
	var executionErr *executor.ExecutionError
	if errors.As(err, &executionErr) && executionErr.ExitCode > 0 {
		return executionErr.ExitCode
	}
	// A child process that fails reaches here as a bare *exec.ExitError, for
	// example 'ops ssh <name> --exec ...'. Report its status instead of a
	// generic 1 so callers can branch on the remote command's exit code.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() > 0 {
		return exitErr.ExitCode()
	}
	return 1
}
