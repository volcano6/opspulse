// Package main is the entry point for the OpsPulse CLI.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/logger"
	"github.com/volcano6/opspulse/internal/version"
)

var debugFlag bool

var rootCmd = &cobra.Command{
	Use:   "opspulse",
	Short: "Personal infrastructure lifecycle management",
	Long:  "OpsPulse — Self-hosted server automation, backup orchestration, and secure operations.",
	PersistentPreRun: func(_ *cobra.Command, _ []string) {
		logger.Setup(debugFlag)
	},
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(_ *cobra.Command, _ []string) {
		fmt.Printf("opspulse %s (commit: %s, built: %s)\n",
			version.Version, version.Commit, version.Date)
	},
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&debugFlag, "debug", false, "Enable verbose debug logging")
	rootCmd.AddCommand(versionCmd)
}

func main() {
	if os.Getenv(askpassHelperFlag) == "1" {
		password, err := readSSHAskpassPassword()
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		_, _ = fmt.Fprint(os.Stdout, password)
		return
	}
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
