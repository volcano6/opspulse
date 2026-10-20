// Package executor handles SSH connection, authentication, and remote command execution.
package executor

import (
	"errors"
	"fmt"
)

// ErrBashUnavailable indicates that an execution target has no bash binary.
// OpsPulse-generated scripts use bash-only syntax, so falling back to sh would
// either fail halfway through or mis-execute the script.
var ErrBashUnavailable = errors.New("bash is required but not available")

// BashUnavailableError identifies the target whose bash preflight failed.
type BashUnavailableError struct {
	Target string
}

func (e *BashUnavailableError) Error() string {
	return fmt.Sprintf("bash is required on %s, but it is not installed or not in PATH", e.Target)
}

func (e *BashUnavailableError) Unwrap() error {
	return ErrBashUnavailable
}

// AuthError indicates an SSH authentication failure.
type AuthError struct {
	User   string
	Host   string
	Reason error
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("authentication failed for %s@%s: %v", e.User, e.Host, e.Reason)
}

func (e *AuthError) Unwrap() error {
	return e.Reason
}

// NetworkError indicates a connection or network-level error.
type NetworkError struct {
	Host   string
	Reason error
}

func (e *NetworkError) Error() string {
	return fmt.Sprintf("network connection error to %s: %v", e.Host, e.Reason)
}

func (e *NetworkError) Unwrap() error {
	return e.Reason
}

// ExecutionError indicates that a remote command or script exited with a non-zero status.
type ExecutionError struct {
	ExitCode int
	Server   string
	Message  string
}

func (e *ExecutionError) Error() string {
	return fmt.Sprintf("execution failed on %s (exit code %d): %s", e.Server, e.ExitCode, e.Message)
}
