package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/server"
)

var sshCmd = &cobra.Command{
	Use:   "ssh [name] [flags] [-- <ssh_args...>]",
	Short: "Establish an interactive SSH terminal session to a server",
	Long: `Directly opens a native interactive SSH session to the specified server.
Reads connection parameters (host, port, user, key_path) automatically from servers.yaml.

If no server name is provided, an interactive menu allows selecting a server to connect.`,
	Args: cobra.ArbitraryArgs,
	RunE: func(_ *cobra.Command, args []string) error {
		var serverName string
		var extraArgs []string

		store := server.NewDefaultStore()

		if len(args) == 0 {
			servers, err := store.List()
			if err != nil {
				return err
			}
			selected, err := selectServerInteractively(os.Stdin, os.Stdout, servers)
			if err != nil {
				return err
			}
			serverName = selected.Name
		} else {
			serverName = args[0]
			extraArgs = args[1:]
		}

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

func selectServerInteractively(in io.Reader, out io.Writer, servers []server.Server) (*server.Server, error) {
	if len(servers) == 0 {
		return nil, fmt.Errorf("no servers configured. Add one using 'ops add <name> <host>'")
	}

	if len(servers) == 1 {
		if out != nil {
			_, _ = fmt.Fprintf(out, "--> Only 1 server configured: connecting to %q (%s)...\n", servers[0].Name, servers[0].Address())
		}
		return &servers[0], nil
	}

	if out != nil {
		_, _ = fmt.Fprintln(out, "📋 Select a server to connect:")
		for i, s := range servers {
			var details []string
			details = append(details, s.Address())
			user := s.User
			if user == "" {
				user = "root"
			}
			details = append(details, user)
			if len(s.Tags) > 0 {
				details = append(details, fmt.Sprintf("[%s]", strings.Join(s.Tags, ",")))
			}
			if s.Description != "" {
				details = append(details, fmt.Sprintf("- %s", s.Description))
			}
			_, _ = fmt.Fprintf(out, "  [%d] %-14s %s\n", i+1, s.Name, strings.Join(details, " "))
		}
		_, _ = fmt.Fprintf(out, "Enter number [1-%d] or name (or 'q' to cancel) [1]: ", len(servers))
	}

	scanner := bufio.NewScanner(in)
	if !scanner.Scan() {
		return nil, fmt.Errorf("no input provided; connection canceled")
	}

	choice := strings.TrimSpace(scanner.Text())
	if strings.EqualFold(choice, "q") || strings.EqualFold(choice, "quit") {
		return nil, fmt.Errorf("connection canceled by user")
	}

	// Default to 1 on empty Enter
	if choice == "" {
		return &servers[0], nil
	}

	// Try numeric choice
	if num, err := strconv.Atoi(choice); err == nil {
		if num >= 1 && num <= len(servers) {
			return &servers[num-1], nil
		}
		return nil, fmt.Errorf("invalid server number %d (choose 1-%d)", num, len(servers))
	}

	// Try matching server name or prefix
	for _, s := range servers {
		if strings.EqualFold(s.Name, choice) {
			return &s, nil
		}
	}
	for _, s := range servers {
		if strings.HasPrefix(strings.ToLower(s.Name), strings.ToLower(choice)) {
			return &s, nil
		}
	}

	return nil, fmt.Errorf("server %q not found in inventory", choice)
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
	return executor.ExpandPath(path)
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
	askpassHelperFlag = "OPSPULSE_ASKPASS_HELPER"
	askpassDataFile   = "OPSPULSE_ASKPASS_DATA_FILE"
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
		askpassHelperFlag:     "1",
		askpassDataFile:       passwordPath,
	})
	return cmd.Run()
}

func readSSHAskpassPassword() (string, error) {
	password, err := os.ReadFile(os.Getenv(askpassDataFile)) // #nosec G703 -- parent creates and owns the 0600 file in a 0700 temporary directory
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
