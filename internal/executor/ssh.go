package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/volcano6/opspulse/internal/server"
	"golang.org/x/crypto/ssh"
)

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
func (e *SSHExecutor) Test(ctx context.Context, srv server.Server) (time.Duration, string, error) {
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

	output, err := session.Output("uname -srm || ver")
	duration := time.Since(start)
	if err != nil {
		return duration, "", fmt.Errorf("failed to execute test command: %w", err)
	}

	return duration, strings.TrimSpace(string(output)), nil
}

// Execute runs a script on the remote server, streaming stdout/stderr to outputWriter.
func (e *SSHExecutor) Execute(ctx context.Context, srv server.Server, templateName string, scriptContent string, outputWriter io.Writer) (*Result, error) {
	startTime := time.Now()
	res := &Result{
		ServerName: srv.Name,
		Template:   templateName,
		StartTime:  startTime,
	}

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

	// Stream stdout and stderr
	if outputWriter != nil {
		session.Stdout = outputWriter
		session.Stderr = outputWriter
	}

	stdinPipe, err := session.StdinPipe()
	if err != nil {
		res.Error = err
		res.EndTime = time.Now()
		res.Duration = res.EndTime.Sub(startTime)
		return res, err
	}

	// Run bash -s with the script piped into stdin
	execErrChan := make(chan error, 1)
	go func() {
		execErrChan <- session.Run("bash -s")
	}()

	// Write script to stdin and close pipe
	go func() {
		defer func() { _ = stdinPipe.Close() }()
		_, _ = io.WriteString(stdinPipe, scriptContent)
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
					Message:  fmt.Sprintf("script '%s' exited with code %d", templateName, res.ExitCode),
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
