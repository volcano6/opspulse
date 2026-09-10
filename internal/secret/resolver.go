package secret

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Resolver handles dynamic secret resolution (e.g., 1Password op:// URIs).
type Resolver struct{}

// NewResolver creates a new secret resolver.
func NewResolver() *Resolver {
	return &Resolver{}
}

// IsAvailable checks if the 1Password CLI (op) is available in PATH.
func (r *Resolver) IsAvailable() bool {
	_, err := exec.LookPath("op")
	return err == nil
}

// Resolve checks if the value is an op:// URI and resolves it.
// If it is not an op:// URI, it returns the value as-is.
func (r *Resolver) Resolve(ctx context.Context, value string) (string, error) {
	if !strings.HasPrefix(value, "op://") {
		return value, nil
	}

	if !r.IsAvailable() {
		return "", fmt.Errorf("secret starts with op:// but 1Password CLI (op) is not installed or not in PATH")
	}

	cmd := exec.CommandContext(ctx, "op", "read", value, "--no-newline")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		return "", fmt.Errorf("failed to read 1Password secret %q: %v (stderr: %s)", value, err, errMsg)
	}

	return stdout.String(), nil
}

// ResolveMap iterates over a map and resolves any op:// values.
// Returns a new map with resolved values, leaving non-op:// values untouched.
func (r *Resolver) ResolveMap(ctx context.Context, env map[string]string) (map[string]string, error) {
	if len(env) == 0 {
		return env, nil
	}

	resolved := make(map[string]string, len(env))
	for k, v := range env {
		resValue, err := r.Resolve(ctx, v)
		if err != nil {
			return nil, fmt.Errorf("resolving env %s: %w", k, err)
		}
		resolved[k] = resValue
	}
	return resolved, nil
}
