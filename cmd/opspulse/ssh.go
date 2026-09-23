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
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/server"
	"golang.org/x/term"
)

var sshCmd = &cobra.Command{
	Use:   "ssh [name] [flags] [-- <ssh_args...>]",
	Short: "Establish an interactive SSH terminal session to a server",
	Long: `Directly opens a native interactive SSH session to the specified server.
Reads connection parameters (host, port, user, key_path) automatically from servers.yaml.

Arguments after '--' are handed to ssh(1) as options (-o, -L, -v, ...). They are
placed ahead of the destination because that is where ssh(1) requires options to
be, so a remote command cannot be passed this way — ssh(1) would read it as the
host name. Use --exec to run a command instead.

With --exec the command runs non-interactively: no pseudo-terminal is allocated,
stdout carries the command's output and nothing else, and the command's exit
status becomes the exit status of ops.

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

		prepareControlMaster()

		// This path drives the system ssh(1) binary and never reaches
		// executor.BuildClientConfig, so the op:// guard has to be applied here.
		// Without it the literal "op://..." string would be handed to ssh as a
		// key path.
		if err := srv.RejectLegacy1PRefs(); err != nil {
			return err
		}
		if srv.JumpHost != "" {
			if jumpSrv, err := store.Get(srv.JumpHost); err == nil {
				if err := jumpSrv.RejectLegacy1PRefs(); err != nil {
					return err
				}
			}
		}

		sshArgs := buildSSHArgs(sshPath, *srv, extraArgs, sshExec, store)

		// A remote command is not an interactive session: no pseudo-terminal and
		// no terminal-title rewriting, and the banner goes to stderr so that
		// stdout carries the command's output and nothing else.
		execMode := sshExec != ""

		banner := fmt.Sprintf("--> Connecting to %s (%s)...\n", srv.Name, srv.Address())
		if srv.JumpHost != "" {
			banner = fmt.Sprintf("--> Connecting to %s (%s) via jump host %s...\n", srv.Name, srv.Address(), srv.JumpHost)
		}
		if execMode {
			fmt.Fprint(os.Stderr, banner)
		} else {
			fmt.Print(banner)
		}

		interactive := !execMode && len(extraArgs) == 0 && !sshNoTitle
		shouldFilter := interactive && term.IsTerminal(int(os.Stdin.Fd()))
		if interactive {
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
	for i := range servers {
		if strings.EqualFold(servers[i].Name, choice) {
			return &servers[i], nil
		}
	}
	for i := range servers {
		if strings.HasPrefix(strings.ToLower(servers[i].Name), strings.ToLower(choice)) {
			return &servers[i], nil
		}
	}

	return nil, fmt.Errorf("server %q not found in inventory", choice)
}

// prepareControlMaster makes the multiplexing socket directory, so that
// buildSSHArgs can hand ssh a ControlPath it is able to bind.
//
// Failing is not fatal and not silent: without the directory ssh still
// connects, it just cannot reuse a session, and the user should know why the
// second 'ops ssh' to the same box authenticated again.
func prepareControlMaster() {
	if !server.ControlMasterEnabled() {
		return
	}
	if err := server.EnsureControlMasterDir(); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  SSH session reuse is off: %v\n", err)
	}
}

// buildSSHArgs builds the argv for the system ssh client.
//
// remoteCmd, when non-empty, is the command ssh(1) runs on the far side.
func buildSSHArgs(binary string, srv server.Server, extraArgs []string, remoteCmd string, store *server.Store) []string {
	args := []string{binary}

	// Session reuse comes first so a user-supplied '-o ControlMaster=...' in
	// extraArgs still wins: later options override earlier ones in ssh(1).
	args = append(args, server.ControlMasterArgs()...)

	// Compatibility with legacy RSA/DSA host keys and public keys when explicitly tagged.
	// SECURITY NOTE: This is intentional, opt-in backwards compatibility for legacy hosts
	// (such as older CentOS/Debian distributions where sshd only offers ssh-rsa/ssh-dss).
	// Modern hosts do NOT get weak algorithms injected. Do NOT remove this without consulting
	// the legacy host compatibility requirements.
	if srv.IsLegacySSH() {
		args = append(args,
			"-o", "HostKeyAlgorithms=+ssh-rsa,ssh-dss",
			"-o", "PubkeyAcceptedKeyTypes=+ssh-rsa",
		)
	}

	// Jump Host handling
	if srv.JumpHost != "" {
		var jumpSrv *server.Server
		var err error
		if store != nil {
			jumpSrv, err = store.Get(srv.JumpHost)
		} else {
			jumpStore := server.NewDefaultStore()
			jumpSrv, err = jumpStore.Get(srv.JumpHost)
		}

		var proxyParts []string
		proxyParts = append(proxyParts,
			"ssh",
			"-W", "%h:%p",
		)

		if err == nil {
			if jumpSrv.IsLegacySSH() {
				proxyParts = append(proxyParts,
					"-o", "HostKeyAlgorithms=+ssh-rsa,ssh-dss",
					"-o", "PubkeyAcceptedKeyTypes=+ssh-rsa",
				)
			}
			identity := jumpSrv.KeyPath
			if identity != "" {
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

	// ssh(1) takes the first non-option argument as the destination and treats
	// everything after it as the remote command, so the command can only be
	// appended once the destination is in place. Placing it any earlier — as an
	// extraArg would — makes ssh(1) read the command as the host name.
	if remoteCmd != "" {
		args = append(args, remoteCmd)
	}

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

func buildAskpassConfig(srv server.Server, jumpSrv *server.Server, targetPassword, jumpPassword string) askpassConfig {
	cfg := askpassConfig{
		DefaultPass: targetPassword, // DefaultPass is ONLY targetPassword; NEVER fallback to jumpPassword to prevent cross-host leakage
		HostPass:    make(map[string]string),
	}
	if targetPassword != "" {
		cfg.HostPass[srv.Host] = targetPassword
		cfg.HostPass[srv.Name] = targetPassword
	}
	if jumpPassword != "" && jumpSrv != nil {
		cfg.HostPass[jumpSrv.Host] = jumpPassword
		cfg.HostPass[jumpSrv.Name] = jumpPassword
	}
	return cfg
}

// writeAskpassPayload stores the password payload as a 0600 file, then wipes
// the serialized bytes from memory so the plaintext does not linger while a
// long session runs.
func writeAskpassPayload(path string, cfg askpassConfig) error {
	payload, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("serialize SSH password helper file: %w", err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return fmt.Errorf("write SSH password helper file: %w", err)
	}
	for i := range payload {
		payload[i] = 0
	}
	return nil
}

// newAskpassFile creates a private directory holding the password payload and
// returns its path plus a cleanup that zeroes the file before removing it.
//
// The zeroing is not ceremony: the payload is the plaintext password, and a
// bare unlink would leave the bytes recoverable on the underlying device for
// as long as the blocks are not reused.
func newAskpassFile(cfg askpassConfig) (string, func(), error) {
	dir, err := os.MkdirTemp("", "opspulse-askpass-")
	if err != nil {
		return "", nil, fmt.Errorf("create SSH password directory: %w", err)
	}
	path := filepath.Join(dir, "password")

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			if fi, err := os.Stat(path); err == nil {
				zeroes := make([]byte, fi.Size())
				_ = os.WriteFile(path, zeroes, 0o600)
			}
			_ = os.RemoveAll(dir)
		})
	}

	if err := writeAskpassPayload(path, cfg); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}

// askpassEnv points a child ssh or sftp process at this binary as its
// SSH_ASKPASS helper. REQUIRE=force is what makes the client consult the helper
// instead of prompting on the terminal it inherited.
func askpassEnv(selfPath, dataFile string) map[string]string {
	return map[string]string{
		"SSH_ASKPASS":         selfPath,
		"SSH_ASKPASS_REQUIRE": "force",
		askpassHelperFlag:     "1",
		askpassDataFile:       dataFile,
	}
}

func matchHostPassword(hostPass map[string]string, prompt string) string {
	if len(hostPass) == 0 || prompt == "" {
		return ""
	}
	lowerPrompt := strings.ToLower(prompt)
	// Sort keys by length descending (longest first) to ensure deterministic matching without prefix collision
	keys := make([]string, 0, len(hostPass))
	for k := range hostPass {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return len(keys[i]) > len(keys[j])
	})

	for _, k := range keys {
		if strings.Contains(lowerPrompt, strings.ToLower(k)) {
			return hostPass[k]
		}
	}
	return ""
}

// resolveTargetPassword returns the password to hand to ssh(1).
//
// This used to resolve op:// references through 1Password, downgrading a
// failure to a warning when a key was also configured. References are rejected
// up front by Server.RejectLegacy1PRefs, so the stored value is already final.
func resolveTargetPassword(srv server.Server) string {
	return srv.Password
}

func runPasswordSSH(binary string, args []string, srv server.Server, store *server.Store, shouldFilter bool) error {
	askpassPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve SSH password helper: %w", err)
	}

	targetPassword := resolveTargetPassword(srv)

	var jumpPassword string
	var jumpSrv *server.Server
	if srv.JumpHost != "" {
		if js, err := store.Get(srv.JumpHost); err == nil {
			jumpSrv = js
			jumpPassword = js.Password
		}
	}

	cfg := buildAskpassConfig(srv, jumpSrv, targetPassword, jumpPassword)

	passwordPath, cleanup, err := newAskpassFile(cfg)
	if err != nil {
		return err
	}
	defer cleanup()

	cmdArgs := args[1:]
	if shouldFilter {
		cmdArgs = append([]string{"-tt"}, cmdArgs...)
	}

	// Run child ssh process without cancelling on parent SIGINT,
	// allowing child ssh to gracefully capture interrupt and restore terminal raw mode
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

	cmd.Env = overrideEnv(os.Environ(), askpassEnv(askpassPath, passwordPath))
	return cmd.Run()
}

func readSSHAskpassPassword(prompt string) (string, error) {
	// ssh(1) sends its host-key confirmation through SSH_ASKPASS as well once
	// the helper is forced. That prompt expects "yes" or "no"; answering it with
	// a password makes ssh's confirm loop reject the answer and ask again
	// forever, so the connection hangs instead of failing. Refusing the prompt
	// turns it back into a closed failure.
	if isHostKeyConfirmation(prompt) {
		return "", fmt.Errorf("refusing to answer the host key prompt %q", prompt)
	}

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

		if pass := matchHostPassword(cfg.HostPass, prompt); pass != "" {
			return pass, nil
		}
		if cfg.DefaultPass != "" {
			return cfg.DefaultPass, nil
		}
		return "", fmt.Errorf("no matching password for SSH prompt %q", prompt)
	}

	// Legacy or direct raw password string (when not JSON formatted)
	return string(data), nil
}

// isHostKeyConfirmation reports whether the prompt is ssh's unknown-host
// question rather than a credential prompt.
//
// Matched on wording rather than position because the question is the only
// prompt whose valid answers are "yes" and "no"; a credential prompt never
// reads like this.
func isHostKeyConfirmation(prompt string) bool {
	lower := strings.ToLower(prompt)
	return strings.Contains(lower, "are you sure you want to continue connecting") ||
		strings.Contains(lower, "(yes/no")
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

var (
	sshNoTitle bool
	sshExec    string
)

func init() {
	sshCmd.Flags().BoolVar(&sshNoTitle, "no-title", false, "Do not set terminal title during SSH session")
	sshCmd.Flags().StringVar(&sshExec, "exec", "", "Run a command on the server non-interactively and exit with its status")
	sshCmd.ValidArgsFunction = completeServerNames
	rootCmd.AddCommand(sshCmd)
}
