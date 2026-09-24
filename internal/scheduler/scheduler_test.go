package scheduler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/volcano6/opspulse/internal/backup"
	"github.com/volcano6/opspulse/internal/notify"
	"github.com/volcano6/opspulse/internal/storage"
)

func TestValidateSchedule(t *testing.T) {
	validSpecs := []string{
		"0 2 * * *",
		"@daily",
		"@hourly",
		"@weekly",
		"@monthly",
		"*/15 * * * *",
		"@every 1h30m",
	}

	for _, spec := range validSpecs {
		if err := ValidateSchedule(spec); err != nil {
			t.Errorf("ValidateSchedule(%q) unexpected error: %v", spec, err)
		}
	}

	invalidSpecs := []string{
		"",
		"   ",
		"invalid cron",
		"99 99 99 99 99",
		"* * *",
	}

	for _, spec := range invalidSpecs {
		if err := ValidateSchedule(spec); err == nil {
			t.Errorf("ValidateSchedule(%q) expected error, got nil", spec)
		}
	}
}

func TestScheduler_RegisterJobs(t *testing.T) {
	tmpDir := t.TempDir()
	backupsFile := filepath.Join(tmpDir, "backups.yaml")

	backupStore := backup.NewStore(backupsFile)

	// Save test jobs
	_ = backupStore.Save(backup.Job{
		Name:     "daily-job",
		Server:   "local",
		Paths:    []string{"/tmp/data"},
		Backend:  "/tmp/repo",
		Schedule: "@daily",
	})
	_ = backupStore.Save(backup.Job{
		Name:     "manual-job",
		Server:   "local",
		Paths:    []string{"/tmp/data2"},
		Backend:  "/tmp/repo",
		Schedule: "", // No schedule
	})
	_ = backupStore.Save(backup.Job{
		Name:     "hourly-job",
		Server:   "local",
		Paths:    []string{"/tmp/data3"},
		Backend:  "/tmp/repo",
		Schedule: "@hourly",
	})

	var buf bytes.Buffer
	sched := New(backupStore, nil, nil, &buf)

	registered, warnings, err := sched.RegisterJobs()
	if err != nil {
		t.Fatalf("RegisterJobs() failed: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("RegisterJobs() warnings = %v, want none", warnings)
	}

	if len(registered) != 2 {
		t.Fatalf("registered count = %d, want 2", len(registered))
	}

	list := sched.ListRegistered()
	if len(list) != 2 {
		t.Fatalf("ListRegistered count = %d, want 2", len(list))
	}

	names := make(map[string]bool)
	for _, r := range registered {
		names[r.JobName] = true
		if r.Next.IsZero() {
			t.Errorf("job %s has zero Next time", r.JobName)
		}
	}
	if !names["daily-job"] || !names["hourly-job"] {
		t.Errorf("registered jobs missing expected names: %+v", names)
	}
}

func TestScheduler_RegisterJobs_SkipsInvalidCron(t *testing.T) {
	tmpDir := t.TempDir()
	backupsFile := filepath.Join(tmpDir, "backups.yaml")
	backupStore := backup.NewStore(backupsFile)

	_ = backupStore.Save(backup.Job{
		Name:     "good-job",
		Server:   "local",
		Paths:    []string{"/tmp/data"},
		Backend:  "/tmp/repo",
		Schedule: "@daily",
	})
	_ = backupStore.Save(backup.Job{
		Name:     "bad-cron-job",
		Server:   "local",
		Paths:    []string{"/tmp/data"},
		Backend:  "/tmp/repo",
		Schedule: "not-a-cron-expression",
	})

	var buf bytes.Buffer
	sched := New(backupStore, nil, nil, &buf)

	registered, warnings, err := sched.RegisterJobs()
	if err != nil {
		t.Fatalf("RegisterJobs() returned error for a single bad job, want nil: %v", err)
	}
	if len(registered) != 1 || registered[0].JobName != "good-job" {
		t.Fatalf("registered = %+v, want only good-job", registered)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Error(), "bad-cron-job") {
		t.Fatalf("warnings = %v, want one mentioning bad-cron-job", warnings)
	}
	if len(sched.ListRegistered()) != 1 {
		t.Fatalf("ListRegistered() = %d, want 1", len(sched.ListRegistered()))
	}
}

func TestScheduler_RegisterJobs_AllInvalidCron(t *testing.T) {
	tmpDir := t.TempDir()
	backupsFile := filepath.Join(tmpDir, "backups.yaml")
	backupStore := backup.NewStore(backupsFile)

	for _, name := range []string{"bad-one", "bad-two"} {
		_ = backupStore.Save(backup.Job{
			Name:     name,
			Server:   "local",
			Paths:    []string{"/tmp/data"},
			Backend:  "/tmp/repo",
			Schedule: "not-a-cron-expression",
		})
	}

	var buf bytes.Buffer
	sched := New(backupStore, nil, nil, &buf)

	registered, warnings, err := sched.RegisterJobs()
	if err == nil {
		t.Fatal("RegisterJobs() = nil error, want error when every scheduled job is invalid")
	}
	if len(registered) != 0 {
		t.Fatalf("registered = %+v, want none", registered)
	}
	if len(warnings) != 2 {
		t.Fatalf("warnings = %v, want 2", warnings)
	}
	if !strings.Contains(err.Error(), "bad-one") || !strings.Contains(err.Error(), "bad-two") {
		t.Fatalf("aggregated error should mention both jobs, got: %v", err)
	}
}

func TestScheduler_RunOnce_WithNotification(t *testing.T) {
	var notifyCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&notifyCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	backupsFile := filepath.Join(tmpDir, "backups.yaml")
	notifyFile := filepath.Join(tmpDir, "notifications.yaml")

	backupStore := backup.NewStore(backupsFile)
	_ = backupStore.Save(backup.Job{
		Name:     "sched-job",
		Server:   "local",
		Paths:    []string{"/tmp/data"},
		Backend:  "/tmp/repo",
		Schedule: "@daily",
	})

	notifyStore := notify.NewStore(notifyFile)
	_ = notifyStore.Save(notify.Channel{
		Name: "alert-ch",
		Type: "webhook",
		URL:  server.URL,
		On:   "always",
	})

	dispatcher := notify.NewDispatcherWithClient(notifyStore, server.Client())

	var buf bytes.Buffer
	// Runner is nil in this test so executeJob will record failure and trigger notification
	sched := New(backupStore, nil, dispatcher, &buf)

	err := sched.RunOnce(context.Background())
	if err == nil {
		t.Fatalf("expected RunOnce() to fail when runner is nil, got nil")
	}

	if atomic.LoadInt32(&notifyCount) != 1 {
		t.Errorf("notifyCount = %d, want 1", notifyCount)
	}
}

func TestScheduler_GracefulShutdown(t *testing.T) {
	tmpDir := t.TempDir()
	backupsFile := filepath.Join(tmpDir, "backups.yaml")
	backupStore := backup.NewStore(backupsFile)

	_ = backupStore.Save(backup.Job{
		Name:     "test-job",
		Server:   "local",
		Paths:    []string{"/tmp/data"},
		Backend:  "/tmp/repo",
		Schedule: "@daily",
	})

	var buf bytes.Buffer
	sched := New(backupStore, nil, nil, &buf)

	ctx, cancel := context.WithCancel(context.Background())

	runErrChan := make(chan error, 1)
	go func() {
		runErrChan <- sched.Run(ctx)
	}()

	// Allow scheduler to register and start
	time.Sleep(100 * time.Millisecond)

	// Cancel context to initiate graceful shutdown
	cancel()

	select {
	case err := <-runErrChan:
		if err != nil {
			t.Errorf("sched.Run() returned error on shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler did not shut down within timeout")
	}

	output := buf.String()
	if !strings.Contains(output, "Registered 1 scheduled backup job") {
		t.Errorf("output missing registration message: %s", output)
	}
	if !strings.Contains(output, "stopped gracefully") {
		t.Errorf("output missing stopped gracefully message: %s", output)
	}
}

func TestScheduler_RunOnce_ContextCancellation(t *testing.T) {
	tmpDir := t.TempDir()
	backupsFile := filepath.Join(tmpDir, "backups.yaml")
	backupStore := backup.NewStore(backupsFile)

	_ = backupStore.Save(backup.Job{
		Name:     "job-1",
		Server:   "local",
		Paths:    []string{"/tmp/data1"},
		Backend:  "/tmp/repo",
		Schedule: "@daily",
	})
	_ = backupStore.Save(backup.Job{
		Name:     "job-2",
		Server:   "local",
		Paths:    []string{"/tmp/data2"},
		Backend:  "/tmp/repo",
		Schedule: "@daily",
	})

	var buf bytes.Buffer
	sched := New(backupStore, nil, nil, &buf)

	// Create an already-canceled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := sched.RunOnce(ctx)
	if err == nil {
		t.Fatal("expected RunOnce() to return error on cancelled context, got nil")
	}

	output := buf.String()
	if !strings.Contains(output, "Execution cancelled") {
		t.Errorf("output missing cancellation notification: %s", output)
	}
}

// fakeRunner is a Runner that blocks until released, letting tests observe the
// shutdown path without a live remote executor.
type fakeRunner struct {
	started   chan struct{}
	release   chan struct{}
	startOnce sync.Once
	mu        sync.Mutex
	canceled  bool
	record    *storage.BackupRun
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{started: make(chan struct{}), release: make(chan struct{})}
}

func (f *fakeRunner) Run(ctx context.Context, job backup.Job, _ io.Writer) (*storage.BackupRun, error) {
	f.startOnce.Do(func() { close(f.started) })

	select {
	case <-f.release:
	case <-ctx.Done():
		f.mu.Lock()
		f.canceled = true
		f.mu.Unlock()
		return &storage.BackupRun{JobName: job.Name, ServerName: job.Server, Status: "canceled"}, ctx.Err()
	}

	rec := &storage.BackupRun{JobName: job.Name, ServerName: job.Server, Status: "success"}
	f.mu.Lock()
	f.record = rec
	f.mu.Unlock()
	return rec, nil
}

func (f *fakeRunner) wasCanceled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.canceled
}

func (f *fakeRunner) recordedRun() *storage.BackupRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.record
}

// TestScheduler_ShutdownWaitsForInFlightJob pins the graceful-shutdown order:
// stop accepting triggers, wait for in-flight jobs, then cancel the daemon
// context. A job must complete (and be accounted for) instead of being aborted
// the moment shutdown starts (audit B1).
func TestScheduler_ShutdownWaitsForInFlightJob(t *testing.T) {
	tmpDir := t.TempDir()
	backupsFile := filepath.Join(tmpDir, "backups.yaml")
	backupStore := backup.NewStore(backupsFile)

	if err := backupStore.Save(backup.Job{
		Name:     "inflight-job",
		Server:   "local",
		Paths:    []string{"/tmp/data"},
		Backend:  "/tmp/repo",
		Schedule: "@every 1s",
	}); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	runner := newFakeRunner()
	var buf bytes.Buffer
	sched := New(backupStore, runner, nil, &buf)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- sched.Run(ctx) }()

	select {
	case <-runner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduled job did not start")
	}

	cancel()

	// Shutdown is now in progress, but the job is still running: it must not be
	// canceled, and its account (run record) must survive.
	time.Sleep(300 * time.Millisecond)
	if runner.wasCanceled() {
		t.Fatal("shutdown canceled an in-flight job before it completed (B1 regression)")
	}

	close(runner.release)

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run() = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("scheduler did not shut down after the in-flight job completed")
	}

	rec := runner.recordedRun()
	if rec == nil {
		t.Fatal("in-flight job run record was lost during shutdown")
	}
	if rec.JobName != "inflight-job" || rec.Status != "success" {
		t.Fatalf("recorded run = %+v, want inflight-job with status success", rec)
	}
	if !strings.Contains(buf.String(), "finished successfully") {
		t.Errorf("output missing successful completion log: %s", buf.String())
	}
}

// TestScheduler_NotificationDoesNotBlockJob proves a wedged webhook cannot hold
// the cron task body: executeJob must return while delivery is still blocked
// (audit N5).
func TestScheduler_NotificationDoesNotBlockJob(t *testing.T) {
	blocked := make(chan struct{})
	entered := make(chan struct{})
	var enterOnce sync.Once
	var notifyCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		enterOnce.Do(func() { close(entered) })
		<-blocked
		atomic.AddInt32(&notifyCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	notifyStore := notify.NewStore(filepath.Join(tmpDir, "notifications.yaml"))
	if err := notifyStore.Save(notify.Channel{
		Name: "alert-ch",
		Type: "webhook",
		URL:  server.URL,
		On:   "always",
	}); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	backupStore := backup.NewStore(filepath.Join(tmpDir, "backups.yaml"))
	dispatcher := notify.NewDispatcherWithClient(notifyStore, server.Client())

	var buf bytes.Buffer
	sched := New(backupStore, nil, dispatcher, &buf)

	job := backup.Job{Name: "sched-job", Server: "local", Backend: "/tmp/repo", Paths: []string{"/tmp/data"}}
	jobDone := make(chan error, 1)
	go func() { jobDone <- sched.executeJob(context.Background(), job) }()

	// The webhook handler is now blocked, yet the task body must already have
	// returned instead of waiting for delivery.
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("notification delivery never started")
	}
	select {
	case <-jobDone:
	case <-time.After(3 * time.Second):
		t.Fatal("executeJob blocked on notification delivery (N5 regression)")
	}

	close(blocked)
	sched.shutdownDispatcher(5 * time.Second)

	if got := atomic.LoadInt32(&notifyCount); got != 1 {
		t.Fatalf("notifyCount = %d, want 1 (notification must still be delivered)", got)
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
