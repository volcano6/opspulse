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

func TestRunBoundedDoesNotStartWorkAfterFailure(t *testing.T) {
	wantErr := errors.New("first task failed")
	items := make([]int, 20)
	for i := range items {
		items[i] = i
	}
	var started atomic.Int32
	release := make(chan struct{})
	time.AfterFunc(20*time.Millisecond, func() { close(release) })

	err := runBounded(items, 5, func(item int) error {
		if item == 0 {
			return wantErr
		}
		started.Add(1)
		<-release
		return nil
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("runBounded() error = %v, want %v", err, wantErr)
	}
	if got := started.Load(); got > 4 {
		t.Fatalf("started tasks after failure = %d, want at most 4 in-flight workers", got)
	}
}
