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
	"golang.org/x/crypto/ssh"
)

// ErrInvalidTarget is returned when a target does not match the executor's requirements.
var ErrInvalidTarget = errors.New("invalid target for executor")

// SSHExecutor handles executing scripts and commands over SSH.
type SSHExecutor struct {
	ConnectTimeout time.Duration
	ExecuteTimeout time.Duration
	ServerResolver func(name string) (*server.Server, error)
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

// DialTarget establishes an SSH client connection to the target server, optionally
// tunneling through target.JumpServer via SSH direct-tcpip port forwarding.
// The caller must invoke the returned cleanup function when finished.
func (e *SSHExecutor) DialTarget(ctx context.Context, target Target) (*ssh.Client, func(), error) {
	if target.Server == nil {
		return nil, nil, fmt.Errorf("%w: SSHExecutor requires a server target", ErrInvalidTarget)
	}
	srv := *target.Server

	targetConfig, err := BuildClientConfig(srv, e.ConnectTimeout)
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

	var dialer net.Dialer

	// 1. If Jump Host is configured, establish direct-tcpip tunnel through it
	if jumpSrv != nil {
		jumpConfig, err := BuildClientConfig(*jumpSrv, e.ConnectTimeout)
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
	start := time.Now()
	client, cleanup, err := e.DialTarget(ctx, target)
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

	client, cleanup, err := e.DialTarget(ctx, target)
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
		safeWriter := NewSyncWriter(outputWriter)
		session.Stdout = safeWriter
		session.Stderr = safeWriter
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
	// Select one available shell before execution. Using "bash || sh" would
	// rerun an already-consumed script with sh and mask bash's exit status.
	if len(scriptContent) <= 64*1024 {
		encoded := base64.StdEncoding.EncodeToString([]byte(scriptContent))
		return fmt.Sprintf("if command -v bash >/dev/null 2>&1; then shell=bash; else shell=sh; fi; printf '%%s' '%s' | base64 -d | \"$shell\"", encoded), nil
	}
	return "if command -v bash >/dev/null 2>&1; then bash -s; else sh -s; fi", strings.NewReader(scriptContent)
}
