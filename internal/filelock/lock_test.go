package filelock

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLock_Sequential(t *testing.T) {
	tmpDir := t.TempDir()
	targetPath := filepath.Join(tmpDir, "config.yaml")

	unlock1, err := Lock(targetPath)
	if err != nil {
		t.Fatalf("first Lock() failed: %v", err)
	}
	unlock1()

	unlock2, err := Lock(targetPath)
	if err != nil {
		t.Fatalf("second Lock() failed: %v", err)
	}
	unlock2()
}

func TestLock_DirCreation(t *testing.T) {
	tmpDir := t.TempDir()
	targetPath := filepath.Join(tmpDir, "nested", "sub", "config.yaml")

	unlock, err := Lock(targetPath)
	if err != nil {
		t.Fatalf("Lock() on non-existent directory failed: %v", err)
	}
	unlock()
}

func TestLock_Concurrency(t *testing.T) {
	tmpDir := t.TempDir()
	targetPath := filepath.Join(tmpDir, "shared.yaml")

	var activeLocks int32
	var maxConcurrency int32
	var wg sync.WaitGroup

	numWorkers := 10
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				unlock, err := Lock(targetPath)
				if err != nil {
					t.Errorf("Lock() failed: %v", err)
					return
				}

				current := atomic.AddInt32(&activeLocks, 1)
				if current > 1 {
					atomic.StoreInt32(&maxConcurrency, current)
				}
				time.Sleep(2 * time.Millisecond)
				atomic.AddInt32(&activeLocks, -1)

				unlock()
				time.Sleep(time.Millisecond)
			}
		}()
	}

	wg.Wait()

	if maxConc := atomic.LoadInt32(&maxConcurrency); maxConc > 1 {
		t.Errorf("expected max concurrency 1, got %d", maxConc)
	}
}
