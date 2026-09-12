package executor

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/volcano6/opspulse/internal/server"
	"github.com/volcano6/opspulse/internal/shellquote"
	"golang.org/x/crypto/ssh"
)

// ErrInvalidTarget is returned when a target does not match the executor's requirements.
var ErrInvalidTarget = errors.New("invalid target for executor")

// SSHExecutor handles executing scripts and commands over SSH.
type SSHExecutor struct {
	ConnectTimeout time.Duration
	ExecuteTimeout time.Duration
	ServerResolver func(name string) (*server.Server, error)
	WarnWriter     io.Writer
}

// NewSSHExecutor creates a new SSHExecutor with sensible default timeouts.
func NewSSHExecutor() *SSHExecutor {
	return &SSHExecutor{
		ConnectTimeout: 15 * time.Second,
		ExecuteTimeout: 0,
	}
}

// WithServerResolver sets the server resolver callback and returns the executor.
func (e *SSHExecutor) WithServerResolver(resolver func(name string) (*server.Server, error)) *SSHExecutor {
	e.ServerResolver = resolver
	return e
}

// WithWarnWriter sets the warn writer for host key warnings and audit notices.
func (e *SSHExecutor) WithWarnWriter(w io.Writer) *SSHExecutor {
	e.WarnWriter = w
	return e
}

// DialTarget establishes an SSH client connection to the target server, optionally
// tunneling through target.JumpServer via SSH direct-tcpip port forwarding.
// The caller must invoke the returned cleanup function when finished.
func (e *SSHExecutor) DialTarget(ctx context.Context, target Target) (*ssh.Client, func(), error) {
	var safeWarnWriter io.Writer
	if e.WarnWriter != nil {
		safeWarnWriter = NewSyncWriter(e.WarnWriter)
	}
	return e.DialTargetWithWriter(ctx, target, safeWarnWriter)
}

// DialTargetWithWriter establishes an SSH client connection to the target server,
// routing any host key warnings to the provided warnWriter.
func (e *SSHExecutor) DialTargetWithWriter(ctx context.Context, target Target, warnWriter io.Writer) (*ssh.Client, func(), error) {
	if target.Server == nil {
		return nil, nil, fmt.Errorf("%w: SSHExecutor requires a server target", ErrInvalidTarget)
	}
	srv := *target.Server

	targetConfig, err := BuildClientConfigWithWriter(srv, e.ConnectTimeout, warnWriter)
	if err != nil {
		return nil, nil, err
	}

	jumpSrv := target.JumpServer
	if jumpSrv == nil && srv.JumpHost != "" && e.ServerResolver != nil {
		resolved, err := e.ServerResolver(srv.JumpHost)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to resolve jump host %q for server %q: %w", srv.JumpHost, srv.Name, err)
		}
		jumpSrv = resolved
	}

	dialer := net.Dialer{Timeout: e.ConnectTimeout}

	// 1. If Jump Host is configured, establish direct-tcpip tunnel through it
	if jumpSrv != nil {
		jumpConfig, err := BuildClientConfigWithWriter(*jumpSrv, e.ConnectTimeout, warnWriter)
		if err != nil {
			return nil, nil, fmt.Errorf("jump host %s config error: %w", jumpSrv.Name, err)
		}

		jumpConn, err := dialer.DialContext(ctx, "tcp", jumpSrv.Address())
		if err != nil {
			return nil, nil, &NetworkError{Host: jumpSrv.Host, Reason: fmt.Errorf("connect to jump host: %w", err)}
		}

		jumpSSHConn, chans, reqs, err := ssh.NewClientConn(jumpConn, jumpSrv.Address(), jumpConfig)
		if err != nil {
			_ = jumpConn.Close()
			return nil, nil, &AuthError{User: jumpSrv.User, Host: jumpSrv.Host, Reason: fmt.Errorf("authenticate with jump host: %w", err)}
		}
		jumpClient := ssh.NewClient(jumpSSHConn, chans, reqs)

		tunnelConn, err := jumpClient.Dial("tcp", srv.Address())
		if err != nil {
			_ = jumpClient.Close()
			return nil, nil, &NetworkError{Host: srv.Host, Reason: fmt.Errorf("open tunnel through jump host %s: %w", jumpSrv.Name, err)}
		}

		targetSSHConn, tChans, tReqs, err := ssh.NewClientConn(tunnelConn, srv.Address(), targetConfig)
		if err != nil {
			_ = tunnelConn.Close()
			_ = jumpClient.Close()
			return nil, nil, &AuthError{User: srv.User, Host: srv.Host, Reason: err}
		}
		targetClient := ssh.NewClient(targetSSHConn, tChans, tReqs)

		cleanup := func() {
			_ = targetClient.Close()
			_ = tunnelConn.Close()
			_ = jumpClient.Close()
		}
		return targetClient, cleanup, nil
	}

	// 2. Direct connection
	addr := srv.Address()
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, nil, &NetworkError{Host: srv.Host, Reason: err}
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, targetConfig)
	if err != nil {
		_ = conn.Close()
		return nil, nil, &AuthError{User: srv.User, Host: srv.Host, Reason: err}
	}
	client := ssh.NewClient(sshConn, chans, reqs)
	cleanup := func() {
		_ = client.Close()
	}
	return client, cleanup, nil
}

// Test checks SSH connectivity and returns latency and system info.
func (e *SSHExecutor) Test(ctx context.Context, target Target) (time.Duration, string, error) {
	return e.TestWithWriter(ctx, target, e.WarnWriter)
}

// TestWithWriter checks SSH connectivity and returns latency and system info,
// routing any host key warnings to the provided warnWriter.
func (e *SSHExecutor) TestWithWriter(ctx context.Context, target Target, warnWriter io.Writer) (time.Duration, string, error) {
	start := time.Now()
	var safeWarnWriter io.Writer
	if warnWriter != nil {
		safeWarnWriter = NewSyncWriter(warnWriter)
	} else if e.WarnWriter != nil {
		safeWarnWriter = NewSyncWriter(e.WarnWriter)
	}

	client, cleanup, err := e.DialTargetWithWriter(ctx, target, safeWarnWriter)
	if err != nil {
		return 0, "", err
	}
	defer cleanup()

	session, err := client.NewSession()
	if err != nil {
		return 0, "", fmt.Errorf("failed to open SSH session: %w", err)
	}
	defer func() { _ = session.Close() }()

	type testOutput struct {
		output []byte
		err    error
	}
	outChan := make(chan testOutput, 1)
	go func() {
		out, runErr := session.Output("uname -srm || ver")
		outChan <- testOutput{output: out, err: runErr}
	}()

	select {
	case <-ctx.Done():
		_ = session.Close()
		return time.Since(start), "", ctx.Err()
	case res := <-outChan:
		duration := time.Since(start)
		if res.err != nil {
			return duration, "", fmt.Errorf("failed to execute test command: %w", res.err)
		}
		return duration, strings.TrimSpace(string(res.output)), nil
	}
}

// Execute runs a script on the remote server, streaming stdout/stderr to outputWriter.
func (e *SSHExecutor) Execute(ctx context.Context, target Target, taskName string, scriptContent string, outputWriter io.Writer) (*Result, error) {
	if e.ExecuteTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.ExecuteTimeout)
		defer cancel()
	}

	startTime := time.Now()
	res := &Result{
		ServerName: target.Name,
		Template:   taskName,
		StartTime:  startTime,
	}

	if target.Server == nil {
		res.Error = fmt.Errorf("%w: SSHExecutor requires a server target", ErrInvalidTarget)
		res.EndTime = time.Now()
		res.Duration = res.EndTime.Sub(startTime)
		return res, res.Error
	}
	srv := *target.Server

	var safeWarnWriter io.Writer
	if e.WarnWriter != nil {
		safeWarnWriter = NewSyncWriter(e.WarnWriter)
	} else if outputWriter != nil {
		safeWarnWriter = NewSyncWriter(outputWriter)
	}

	client, cleanup, err := e.DialTargetWithWriter(ctx, target, safeWarnWriter)
	if err != nil {
		res.Error = err
		res.EndTime = time.Now()
		res.Duration = res.EndTime.Sub(startTime)
		return res, err
	}
	defer cleanup()

	session, err := client.NewSession()
	if err != nil {
		sessErr := fmt.Errorf("failed to open session: %w", err)
		res.Error = sessErr
		res.EndTime = time.Now()
		res.Duration = res.EndTime.Sub(startTime)
		return res, sessErr
	}
	defer func() { _ = session.Close() }()

	// Stream stdout and stderr thread-safely (prevent data race on shared bytes.Buffer)
	if outputWriter != nil {
		safeOutWriter := NewSyncWriter(outputWriter)
		session.Stdout = safeOutWriter
		session.Stderr = safeOutWriter
	}

	// Normalize script line endings to standard LF before executing remotely
	scriptContent = strings.ReplaceAll(scriptContent, "\r\n", "\n")
	scriptContent = strings.ReplaceAll(scriptContent, "\r", "\n")

	execCmd, stdin := remoteShellCommand(scriptContent)
	if stdin != nil {
		session.Stdin = stdin
	}

	execErrChan := make(chan error, 1)
	go func() {
		execErrChan <- session.Run(execCmd)
	}()

	// Wait for execution or context cancellation
	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGTERM)
		_ = session.Close()
		res.EndTime = time.Now()
		res.Duration = res.EndTime.Sub(startTime)
		res.Error = ctx.Err()
		return res, ctx.Err()
	case runErr := <-execErrChan:
		res.EndTime = time.Now()
		res.Duration = res.EndTime.Sub(startTime)

		if runErr != nil {
			var exitErr *ssh.ExitError
			if errors.As(runErr, &exitErr) {
				res.ExitCode = exitErr.ExitStatus()
				execError := &ExecutionError{
					ExitCode: res.ExitCode,
					Server:   srv.Name,
					Message:  fmt.Sprintf("script '%s' exited with code %d", taskName, res.ExitCode),
				}
				res.Error = execError
				return res, execError
			}
			res.Error = runErr
			return res, runErr
		}

		res.Success = true
		res.ExitCode = 0
		return res, nil
	}
}

func remoteShellCommand(scriptContent string) (string, io.Reader) {
	// Select one available shell before execution and automatically elevate
	// with passwordless sudo if the remote session user is non-root.
	if len(scriptContent) <= 64*1024 {
		encoded := base64.StdEncoding.EncodeToString([]byte(scriptContent))
		return fmt.Sprintf("if command -v bash >/dev/null 2>&1; then shell=bash; else shell=sh; fi; if [ \"$(id -u)\" -ne 0 ] && command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then runner=\"sudo -E $shell\"; else runner=\"$shell\"; fi; printf '%%s' %s | base64 -d | $runner", shellquote.Quote(encoded)), nil
	}
	return "if command -v bash >/dev/null 2>&1; then shell=bash; else shell=sh; fi; if [ \"$(id -u)\" -ne 0 ] && command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then sudo -E \"$shell\" -s; else \"$shell\" -s; fi", strings.NewReader(scriptContent)
}
