package executor

import (
	"context"
	"io"
	"sync"
	"time"
)

// Executor defines the common interface for script and command execution across targets.
type Executor interface {
	// Execute runs a script on the given target, streaming stdout/stderr to outputWriter.
	Execute(ctx context.Context, target Target, taskName string, scriptContent string, outputWriter io.Writer) (*Result, error)
	// Test checks connectivity to the target and returns latency and system banner.
	Test(ctx context.Context, target Target) (time.Duration, string, error)
}

// SyncWriter wraps an io.Writer with a mutex to make it safe for concurrent writes from stdout/stderr.
type SyncWriter struct {
	w  io.Writer
	mu sync.Mutex
}

// NewSyncWriter wraps an io.Writer with mutex synchronization.
func NewSyncWriter(w io.Writer) io.Writer {
	if w == nil {
		return nil
	}
	return &SyncWriter{w: w}
}

// Write safely writes to the underlying writer with mutex locking.
func (s *SyncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
