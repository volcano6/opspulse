package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/server"
)

var sshCmd = &cobra.Command{
	Use:   "ssh <name> [flags] [-- <ssh_args...>]",
	Short: "Establish an interactive SSH terminal session to a server",
	Long: `Directly opens a native interactive SSH session to the specified server.
Reads connection parameters (host, port, user, key_path) automatically from servers.yaml.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		serverName := args[0]
		extraArgs := args[1:]

		store := server.NewDefaultStore()
		srv, err := store.Get(serverName)
		if err != nil {
			return err
		}

		sshPath, err := exec.LookPath("ssh")
		if err != nil {
			return fmt.Errorf("system 'ssh' client not found in PATH: %w", err)
		}

		sshArgs := buildSSHArgs(sshPath, *srv, extraArgs)

		fmt.Printf("--> Connecting to %s (%s)...\n", srv.Name, srv.Address())
		return runInteractiveSSH(sshPath, sshArgs)
	},
}

func buildSSHArgs(binary string, srv server.Server, extraArgs []string) []string {
	args := []string{binary}

	// Port
	if srv.Port > 0 && srv.Port != 22 {
		args = append(args, "-p", strconv.Itoa(srv.Port))
	}

	// Key Path
	if srv.KeyPath != "" {
		expandedKey := expandHome(srv.KeyPath)
		args = append(args, "-i", expandedKey)
	}

	// Extra args
	args = append(args, extraArgs...)

	// Destination: user@host
	user := srv.User
	if user == "" {
		user = "root"
	}
	args = append(args, fmt.Sprintf("%s@%s", user, srv.Host))

	return args
}

func expandHome(path string) string {
	if path == "" {
		return ""
	}
	if path == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			return home
		}
	} else if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func runInteractiveSSH(binary string, args []string) error {
	// On Unix-like systems, replace current process with exec
	if runtime.GOOS != "windows" {
		return syscall.Exec(binary, args, os.Environ())
	}

	// On Windows, run child process with stdin/stdout/stderr attached
	cmd := exec.Command(binary, args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func init() {
	sshCmd.ValidArgsFunction = completeServerNames
	rootCmd.AddCommand(sshCmd)
}
