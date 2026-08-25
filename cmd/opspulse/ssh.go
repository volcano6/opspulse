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
		if srv.Password != "" && srv.KeyPath == "" {
			return runPasswordSSH(sshPath, sshArgs, srv.Password)
		}
		return runInteractiveSSH(sshPath, sshArgs)
	},
}

func buildSSHArgs(binary string, srv server.Server, extraArgs []string) []string {
	args := []string{binary}

	// Port
	if srv.Port > 0 && srv.Port != 22 {
		args = append(args, "-p", strconv.Itoa(srv.Port))
	}

	// A configured identity must be the only public key offered. This avoids
	// exhausting the remote server's authentication attempts via ssh-agent.
	if srv.KeyPath != "" {
		expandedKey := expandHome(srv.KeyPath)
		args = append(args, "-o", "IdentitiesOnly=yes", "-i", expandedKey)
	} else if srv.Password != "" {
		args = append(args,
			"-o", "PubkeyAuthentication=no",
			"-o", "PreferredAuthentications=password,keyboard-interactive")
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

const (
	sshAskpassModeEnv  = "OPSPULSE_SSH_ASKPASS"
	sshPasswordFileEnv = "OPSPULSE_SSH_PASSWORD_FILE"
)

func runPasswordSSH(binary string, args []string, password string) error {
	askpassPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve SSH password helper: %w", err)
	}
	passwordDir, err := os.MkdirTemp("", "opspulse-askpass-")
	if err != nil {
		return fmt.Errorf("create SSH password directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(passwordDir) }()
	passwordPath := filepath.Join(passwordDir, "password")
	if err := os.WriteFile(passwordPath, []byte(password), 0o600); err != nil {
		return fmt.Errorf("write SSH password helper file: %w", err)
	}

	cmd := exec.Command(binary, args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = overrideEnv(os.Environ(), map[string]string{
		"SSH_ASKPASS":         askpassPath,
		"SSH_ASKPASS_REQUIRE": "force",
		sshAskpassModeEnv:     "1",
		sshPasswordFileEnv:    passwordPath,
	})
	return cmd.Run()
}

func readSSHAskpassPassword() (string, error) {
	password, err := os.ReadFile(os.Getenv(sshPasswordFileEnv))
	if err != nil {
		return "", fmt.Errorf("read SSH password helper file: %w", err)
	}
	return string(password), nil
}

func overrideEnv(environ []string, values map[string]string) []string {
	result := make([]string, 0, len(environ)+len(values))
	for _, entry := range environ {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := values[key]; !replaced {
			result = append(result, entry)
		}
	}
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	return result
}

func init() {
	sshCmd.ValidArgsFunction = completeServerNames
	rootCmd.AddCommand(sshCmd)
}
