// Package executor handles SSH connection, authentication, and remote command execution.
package executor

import (
	"fmt"
)

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
