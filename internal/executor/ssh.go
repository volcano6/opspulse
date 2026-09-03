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

	"golang.org/x/crypto/ssh"
)

// ErrInvalidTarget is returned when a target does not match the executor's requirements.
var ErrInvalidTarget = errors.New("invalid target for executor")

// SSHExecutor handles executing scripts and commands over SSH.
type SSHExecutor struct {
	ConnectTimeout time.Duration
	ExecuteTimeout time.Duration
}

// NewSSHExecutor creates a new SSHExecutor with sensible default timeouts.
func NewSSHExecutor() *SSHExecutor {
	return &SSHExecutor{
		ConnectTimeout: 15 * time.Second,
		ExecuteTimeout: 15 * time.Minute,
	}
}

// Test checks SSH connectivity and returns latency and system info.
func (e *SSHExecutor) Test(ctx context.Context, target Target) (time.Duration, string, error) {
	if target.Server == nil {
		return 0, "", fmt.Errorf("%w: SSHExecutor requires a server target", ErrInvalidTarget)
	}
	srv := *target.Server

	config, err := BuildClientConfig(srv, e.ConnectTimeout)
	if err != nil {
		return 0, "", err
	}

	start := time.Now()
	addr := srv.Address()

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return 0, "", &NetworkError{Host: srv.Host, Reason: err}
	}
	defer func() { _ = conn.Close() }()

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		return 0, "", &AuthError{User: srv.User, Host: srv.Host, Reason: err}
	}
	client := ssh.NewClient(sshConn, chans, reqs)
	defer func() { _ = client.Close() }()

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

	config, err := BuildClientConfig(srv, e.ConnectTimeout)
	if err != nil {
		res.Error = err
		res.EndTime = time.Now()
		res.Duration = res.EndTime.Sub(startTime)
		return res, err
	}

	addr := srv.Address()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		netErr := &NetworkError{Host: srv.Host, Reason: err}
		res.Error = netErr
		res.EndTime = time.Now()
		res.Duration = res.EndTime.Sub(startTime)
		return res, netErr
	}
	defer func() { _ = conn.Close() }()

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		authErr := &AuthError{User: srv.User, Host: srv.Host, Reason: err}
		res.Error = authErr
		res.EndTime = time.Now()
		res.Duration = res.EndTime.Sub(startTime)
		return res, authErr
	}
	client := ssh.NewClient(sshConn, chans, reqs)
	defer func() { _ = client.Close() }()

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
