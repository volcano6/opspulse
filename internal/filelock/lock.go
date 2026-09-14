// Package filelock provides cross-process exclusive file locking.
package filelock

import (
	"fmt"
	"os"
	"path/filepath"
)

// UnlockFunc releases the acquired file lock and cleans up associated resources.
type UnlockFunc func()

// Lock acquires an exclusive, cross-process lock for the given target path.
// It uses a dedicated lock file (<path>.lock) to avoid interfering with
// atomic rename operations on the target file itself.
// The returned UnlockFunc must be called when the critical section is complete.
func Lock(targetPath string) (UnlockFunc, error) {
	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create lock file directory: %w", err)
	}

	lockPath := targetPath + ".lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("failed to open lock file %q: %w", lockPath, err)
	}

	if err := lockFile(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("failed to acquire lock on %q: %w", lockPath, err)
	}

	return func() {
		_ = unlockFile(f)
		_ = f.Close()
	}, nil
}
