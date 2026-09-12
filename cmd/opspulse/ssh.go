package main

import (
	"bufio"
	"bytes"
	"encoding/json"
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
	"github.com/volcano6/opspulse/internal/secret"
	"github.com/volcano6/opspulse/internal/server"
	"golang.org/x/term"
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

		// The system ssh(1) binary only understands file paths, so any op://
		// references are materialised into 0600 temp files for the duration of
		// the session and removed on the way out.
		jumpKeyPath, cleanupKeys, err := materializeOnePasswordKeys(srv, store)
		if err != nil {
			return err
		}
		defer cleanupKeys()

		sshArgs := buildSSHArgs(sshPath, *srv, extraArgs, jumpKeyPath)

		if srv.JumpHost != "" {
			fmt.Printf("--> Connecting to %s (%s) via jump host %s...\n", srv.Name, srv.Address(), srv.JumpHost)
		} else {
			fmt.Printf("--> Connecting to %s (%s)...\n", srv.Name, srv.Address())
		}

		shouldFilter := len(extraArgs) == 0 && !sshNoTitle && term.IsTerminal(int(os.Stdin.Fd()))
		if len(extraArgs) == 0 && !sshNoTitle {
			setTerminalTitle(srv.Name)
			defer resetTerminalTitle()
		}

		needsPassword := (srv.Password != "" && srv.KeyPath == "")
		if srv.JumpHost != "" {
			if jumpSrv, err := store.Get(srv.JumpHost); err == nil && jumpSrv.Password != "" && jumpSrv.KeyPath == "" {
				needsPassword = true
			}
		}
		if needsPassword {
			return runPasswordSSH(sshPath, sshArgs, *srv, store, shouldFilter)
		}
		return runInteractiveSSH(sshPath, sshArgs, shouldFilter)
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
			user := s.User
			if user == "" {
				user = "root"
			}
			target := fmt.Sprintf("%s@%s", user, s.Host)
			if s.Port > 0 && s.Port != 22 {
				target = fmt.Sprintf("%s:%d", target, s.Port)
			}
			details = append(details, target)
			if s.JumpHost != "" {
				details = append(details, fmt.Sprintf("(via %s)", s.JumpHost))
			}
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

// buildSSHArgs builds the argv for the system ssh client. jumpKeyPath, when
// non-empty, overrides the jump host's identity file; it is how a 1Password
// op:// key of the jump host gets injected after being materialised on disk.
func buildSSHArgs(binary string, srv server.Server, extraArgs []string, jumpKeyPath string) []string {
	args := []string{binary}

	// Jump Host handling
	if srv.JumpHost != "" {
		store := server.NewDefaultStore()
		jumpSrv, err := store.Get(srv.JumpHost)

		var proxyParts []string
		proxyParts = append(proxyParts,
			"ssh",
			"-W", "%h:%p",
		)

		if err == nil {
			identity := jumpSrv.KeyPath
			if jumpKeyPath != "" {
				identity = jumpKeyPath
			}
			if identity != "" && !secret.Is1PRef(identity) {
				expandedJumpKey := filepath.ToSlash(expandHome(identity))
				if strings.Contains(expandedJumpKey, " ") {
					expandedJumpKey = fmt.Sprintf(`"%s"`, expandedJumpKey)
				}
				proxyParts = append(proxyParts, "-o", "IdentitiesOnly=yes", "-i", expandedJumpKey)
			}
			if jumpSrv.Port > 0 && jumpSrv.Port != 22 {
				proxyParts = append(proxyParts, "-p", strconv.Itoa(jumpSrv.Port))
			}
			user := jumpSrv.User
			if user == "" {
				user = "root"
			}
			proxyParts = append(proxyParts, fmt.Sprintf("%s@%s", user, jumpSrv.Host))
		} else {
			proxyParts = append(proxyParts, srv.JumpHost)
		}

		args = append(args, "-o", fmt.Sprintf("ProxyCommand=%s", strings.Join(proxyParts, " ")))
	}

	// Port
	if srv.Port > 0 && srv.Port != 22 {
		args = append(args, "-p", strconv.Itoa(srv.Port))
	}

	// A configured identity must be the only public key offered. This avoids
	// exhausting the remote server's authentication attempts via ssh-agent.
	if srv.KeyPath != "" && !secret.Is1PRef(srv.KeyPath) {
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

func setTerminalTitle(title string) {
	fmt.Printf("\033]0;%s\007", title)
}

func resetTerminalTitle() {
	fmt.Print("\033]0;\007")
}

type filterState int

const (
	stateNormal filterState = iota
	stateEscape
	stateOSCHeader
	stateInTitle
	stateTitleEsc
)

type titleFilterWriter struct {
	w        io.Writer
	state    filterState
	pending  []byte
	lastByte byte
}

func newTitleFilterWriter(w io.Writer) *titleFilterWriter {
	return &titleFilterWriter{w: w, state: stateNormal}
}

func (f *titleFilterWriter) Write(p []byte) (int, error) {
	totalLen := len(p)
	var out []byte

	emitByte := func(b byte) {
		if b == '\n' && f.lastByte != '\r' {
			out = append(out, '\r')
		}
		out = append(out, b)
		f.lastByte = b
	}

	emitBytes := func(bs []byte) {
		for _, b := range bs {
			emitByte(b)
		}
	}

	for len(p) > 0 {
		switch f.state {
		case stateNormal:
			idx := bytes.IndexByte(p, 0x1b)
			if idx == -1 {
				emitBytes(p)
				p = nil
			} else {
				emitBytes(p[:idx])
				f.pending = append(f.pending[:0], 0x1b)
				f.state = stateEscape
				p = p[idx+1:]
			}

		case stateEscape:
			b := p[0]
			p = p[1:]
			if b == ']' {
				f.pending = append(f.pending, ']')
				f.state = stateOSCHeader
			} else {
				emitBytes(f.pending)
				f.pending = f.pending[:0]
				emitByte(b)
				f.state = stateNormal
			}

		case stateOSCHeader:
			b := p[0]
			p = p[1:]
			f.pending = append(f.pending, b)
			s := string(f.pending)
			if s == "\x1b]0;" || s == "\x1b]2;" {
				f.pending = f.pending[:0]
				f.state = stateInTitle
			} else if len(s) >= 4 || (len(s) == 3 && b != '0' && b != '2') {
				emitBytes(f.pending)
				f.pending = f.pending[:0]
				f.state = stateNormal
			}

		case stateInTitle:
			idx := bytes.IndexAny(p, "\x07\x1b")
			if idx == -1 {
				p = nil
			} else {
				termByte := p[idx]
				p = p[idx+1:]
				if termByte == 0x07 {
					f.state = stateNormal
				} else {
					f.state = stateTitleEsc
				}
			}

		case stateTitleEsc:
			b := p[0]
			p = p[1:]
			if b == '\\' || b == 0x07 {
				f.state = stateNormal
			} else {
				f.state = stateInTitle
			}
		}
	}

	if len(out) > 0 {
		if _, err := f.w.Write(out); err != nil {
			return 0, err
		}
	}
	return totalLen, nil
}

func (f *titleFilterWriter) Flush() error {
	if len(f.pending) > 0 {
		var out []byte
		for _, b := range f.pending {
			if b == '\n' && f.lastByte != '\r' {
				out = append(out, '\r')
			}
			out = append(out, b)
			f.lastByte = b
		}
		f.pending = f.pending[:0]
		if len(out) > 0 {
			_, err := f.w.Write(out)
			return err
		}
	}
	return nil
}

func runInteractiveSSH(binary string, args []string, shouldFilter bool) error {
	if !shouldFilter {
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

	cmdArgs := append([]string{"-tt"}, args[1:]...)
	cmd := exec.Command(binary, cmdArgs...)
	cmd.Stdin = os.Stdin
	filterOut := newTitleFilterWriter(os.Stdout)
	filterErr := newTitleFilterWriter(os.Stderr)
	defer func() {
		_ = filterOut.Flush()
		_ = filterErr.Flush()
	}()
	cmd.Stdout = filterOut
	cmd.Stderr = filterErr

	cleanupResize := setupTerminalResizeNotify(cmd)
	defer cleanupResize()

	return cmd.Run()
}

const (
	askpassHelperFlag = "OPSPULSE_ASKPASS_HELPER"
	askpassDataFile   = "OPSPULSE_ASKPASS_DATA_FILE"
)

type askpassConfig struct {
	DefaultPass string            `json:"default_pass"`
	HostPass    map[string]string `json:"host_pass"`
}

func runPasswordSSH(binary string, args []string, srv server.Server, store *server.Store, shouldFilter bool) error {
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

	cfg := askpassConfig{
		DefaultPass: srv.Password,
		HostPass:    make(map[string]string),
	}
	if srv.Password != "" {
		cfg.HostPass[srv.Host] = srv.Password
		cfg.HostPass[srv.Name] = srv.Password
	}
	if srv.JumpHost != "" {
		if jumpSrv, err := store.Get(srv.JumpHost); err == nil && jumpSrv.Password != "" {
			cfg.HostPass[jumpSrv.Host] = jumpSrv.Password
			cfg.HostPass[jumpSrv.Name] = jumpSrv.Password
		}
	}

	payload, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("serialize SSH password helper file: %w", err)
	}

	if err := os.WriteFile(passwordPath, payload, 0o600); err != nil {
		return fmt.Errorf("write SSH password helper file: %w", err)
	}

	cmdArgs := args[1:]
	if shouldFilter {
		cmdArgs = append([]string{"-tt"}, cmdArgs...)
	}

	cmd := exec.Command(binary, cmdArgs...)
	cmd.Stdin = os.Stdin
	if shouldFilter {
		filterOut := newTitleFilterWriter(os.Stdout)
		filterErr := newTitleFilterWriter(os.Stderr)
		defer func() {
			_ = filterOut.Flush()
			_ = filterErr.Flush()
		}()
		cmd.Stdout = filterOut
		cmd.Stderr = filterErr
		cleanupResize := setupTerminalResizeNotify(cmd)
		defer cleanupResize()
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	envMap := map[string]string{
		"SSH_ASKPASS":         askpassPath,
		"SSH_ASKPASS_REQUIRE": "force",
		askpassHelperFlag:     "1",
		askpassDataFile:       passwordPath,
	}
	cmd.Env = overrideEnv(os.Environ(), envMap)
	return cmd.Run()
}

func readSSHAskpassPassword(prompt string) (string, error) {
	data, err := os.ReadFile(os.Getenv(askpassDataFile)) // #nosec G703 -- parent creates and owns the 0600 file in a 0700 temporary directory
	if err != nil {
		return "", fmt.Errorf("read SSH password helper file: %w", err)
	}

	trimmed := bytes.TrimSpace(data)
	if bytes.HasPrefix(trimmed, []byte("{")) {
		var cfg askpassConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return "", fmt.Errorf("parse SSH password helper file: %w", err)
		}

		if prompt != "" && len(cfg.HostPass) > 0 {
			lowerPrompt := strings.ToLower(prompt)
			for hostOrName, pass := range cfg.HostPass {
				if strings.Contains(lowerPrompt, strings.ToLower(hostOrName)) {
					return pass, nil
				}
			}
		}
		if cfg.DefaultPass != "" {
			return cfg.DefaultPass, nil
		}
		return "", fmt.Errorf("no matching password for SSH prompt %q", prompt)
	}

	// Legacy or direct raw password string (when not JSON formatted)
	return string(data), nil
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

var sshNoTitle bool

func init() {
	sshCmd.Flags().BoolVar(&sshNoTitle, "no-title", false, "Do not set terminal title during SSH session")
	sshCmd.ValidArgsFunction = completeServerNames
	rootCmd.AddCommand(sshCmd)
}
