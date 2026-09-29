package sftp

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunBoundedUsesExactlyWorkerLimit(t *testing.T) {
	var active, peak, executed atomic.Int32
	items := make([]int, 50)

	err := runBounded(items, 5, func(int) error {
		executed.Add(1)
		current := active.Add(1)
		for {
			old := peak.Load()
			if current <= old || peak.CompareAndSwap(old, current) {
				break
			}
		}
		time.Sleep(2 * time.Millisecond)
		active.Add(-1)
		return nil
	})
	if err != nil {
		t.Fatalf("runBounded() error = %v", err)
	}
	if got := peak.Load(); got != 5 {
		t.Errorf("peak concurrency = %d, want 5", got)
	}
	if got := peak.Load(); got > 5 {
		t.Errorf("peak concurrency = %d, want no more than 5", got)
	}
	if got := executed.Load(); got != 50 {
		t.Errorf("executed tasks = %d, want 50", got)
	}
}

func TestRunBoundedStopsDispatchingAndReturnsFirstError(t *testing.T) {
	wantErr := errors.New("task 25 failed")
	var executed atomic.Int32
	items := make([]int, 50)
	for i := range items {
		items[i] = i + 1
	}

	err := runBounded(items, 5, func(item int) error {
		executed.Add(1)
		if item == 25 {
			return wantErr
		}
		time.Sleep(time.Millisecond)
		return nil
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("runBounded() error = %v, want %v", err, wantErr)
	}
}
