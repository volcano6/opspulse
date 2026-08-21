package executor

import (
	"context"
	"io"
	"time"
)

// Executor defines the common interface for script and command execution across targets.
type Executor interface {
	// Execute runs a script on the given target, streaming stdout/stderr to outputWriter.
	Execute(ctx context.Context, target Target, taskName string, scriptContent string, outputWriter io.Writer) (*Result, error)
	// Test checks connectivity to the target and returns latency and system banner.
	Test(ctx context.Context, target Target) (time.Duration, string, error)
}
