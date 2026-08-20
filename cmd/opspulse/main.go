// Package main is the entry point for the OpsPulse CLI.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/version"
)

var rootCmd = &cobra.Command{
	Use:   "opspulse",
	Short: "Personal infrastructure lifecycle management",
	Long:  "OpsPulse — Self-hosted server automation, backup orchestration, and secure operations.",
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
	rootCmd.AddCommand(versionCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
