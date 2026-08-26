package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// LocalExecutor executes scripts and commands on the local machine.
type LocalExecutor struct {
	ExecuteTimeout time.Duration
}

// NewLocalExecutor creates a new LocalExecutor with sensible default timeout.
func NewLocalExecutor() *LocalExecutor {
	return &LocalExecutor{
		ExecuteTimeout: 15 * time.Minute,
	}
}

// Test checks local shell availability and returns execution latency and system info.
func (e *LocalExecutor) Test(ctx context.Context, _ Target) (time.Duration, string, error) {
	start := time.Now()
	cmd := exec.CommandContext(ctx, "sh", "-c", "uname -srm 2>/dev/null || ver 2>/dev/null || echo 'local system'") //nolint:gosec // G204: Test command on local machine
	output, err := cmd.CombinedOutput()
	duration := time.Since(start)
	if err != nil {
		return duration, "", fmt.Errorf("local execution test failed: %w", err)
	}
	return duration, strings.TrimSpace(string(output)), nil
}

// Execute runs a script on the local machine, streaming stdout/stderr to outputWriter.
func (e *LocalExecutor) Execute(ctx context.Context, target Target, taskName string, scriptContent string, outputWriter io.Writer) (*Result, error) {
	startTime := time.Now()
	name := target.Name
	if name == "" {
		name = "local"
	}

	res := &Result{
		ServerName: name,
		Template:   taskName,
		StartTime:  startTime,
	}

	// Normalize script line endings to standard LF
	scriptContent = strings.ReplaceAll(scriptContent, "\r\n", "\n")
	scriptContent = strings.ReplaceAll(scriptContent, "\r", "\n")

	// Use bash if available, fallback to sh
	shell := "bash"
	if _, err := exec.LookPath("bash"); err != nil {
		shell = "sh"
	}

	cmd := exec.CommandContext(ctx, shell, "-s") //nolint:gosec // G204: Subprocess launched to execute script on local machine
	if outputWriter != nil {
		safeWriter := NewSyncWriter(outputWriter)
		cmd.Stdout = safeWriter
		cmd.Stderr = safeWriter
	}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		res.Error = err
		res.EndTime = time.Now()
		res.Duration = res.EndTime.Sub(startTime)
		return res, err
	}

	if err := cmd.Start(); err != nil {
		_ = stdinPipe.Close()
		res.Error = err
		res.EndTime = time.Now()
		res.Duration = res.EndTime.Sub(startTime)
		return res, err
	}

	go func() {
		defer func() { _ = stdinPipe.Close() }()
		_, _ = io.WriteString(stdinPipe, scriptContent)
	}()

	runErr := cmd.Wait()
	res.EndTime = time.Now()
	res.Duration = res.EndTime.Sub(startTime)

	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
			execError := &ExecutionError{
				ExitCode: res.ExitCode,
				Server:   name,
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
