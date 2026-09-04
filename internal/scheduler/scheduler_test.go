package scheduler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/volcano6/opspulse/internal/backup"
	"github.com/volcano6/opspulse/internal/notify"
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

	registered, err := sched.RegisterJobs()
	if err != nil {
		t.Fatalf("RegisterJobs() failed: %v", err)
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

func TestScheduler_RegisterJobs_InvalidCron(t *testing.T) {
	tmpDir := t.TempDir()
	backupsFile := filepath.Join(tmpDir, "backups.yaml")
	backupStore := backup.NewStore(backupsFile)

	_ = backupStore.Save(backup.Job{
		Name:     "bad-cron-job",
		Server:   "local",
		Paths:    []string{"/tmp/data"},
		Backend:  "/tmp/repo",
		Schedule: "not-a-cron-expression",
	})

	var buf bytes.Buffer
	sched := New(backupStore, nil, nil, &buf)

	_, err := sched.RegisterJobs()
	if err == nil {
		t.Fatal("expected error for invalid schedule, got nil")
	}
	if !strings.Contains(err.Error(), "bad-cron-job") {
		t.Errorf("error message should mention job name, got: %v", err)
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
	if err != nil {
		t.Fatalf("RunOnce() failed: %v", err)
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

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
