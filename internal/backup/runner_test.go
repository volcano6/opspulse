package backup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/server"
	"github.com/volcano6/opspulse/internal/storage"
)

func TestBuildBackupScript(t *testing.T) {
	job := Job{
		Name:    "site-backup",
		Server:  "vps-01",
		Paths:   []string{"/var/www", "/etc/nginx"},
		Backend: "s3:s3.amazonaws.com/backup-bucket",
		Env: map[string]string{
			"AWS_ACCESS_KEY_ID": "mock-key",
			"RESTIC_PASSWORD":   "secret-pw",
		},
		Retention: &RetentionPolicy{
			KeepDaily:   7,
			KeepWeekly:  4,
			KeepMonthly: 6,
		},
		Excludes:    []string{"*.log", ".cache"},
		Tags:        []string{"prod"},
		Description: "Production backup",
	}

	script, err := BuildBackupScript(job)
	if err != nil {
		t.Fatalf("BuildBackupScript() error: %v", err)
	}

	// Verify environment variables
	if !strings.Contains(script, `export AWS_ACCESS_KEY_ID="mock-key"`) {
		t.Error("script missing AWS_ACCESS_KEY_ID")
	}
	if !strings.Contains(script, `export RESTIC_PASSWORD="secret-pw"`) {
		t.Error("script missing RESTIC_PASSWORD")
	}
	if !strings.Contains(script, `export RESTIC_REPOSITORY="s3:s3.amazonaws.com/backup-bucket"`) {
		t.Error("script missing RESTIC_REPOSITORY")
	}

	// Verify restic backup command with tags, excludes, and paths
	if !strings.Contains(script, `restic backup --json`) {
		t.Error("script missing restic backup --json")
	}
	if !strings.Contains(script, `--tag "prod"`) || !strings.Contains(script, `--tag "job:site-backup"`) {
		t.Error("script missing expected tags")
	}
	if !strings.Contains(script, `--exclude "*.log"`) || !strings.Contains(script, `--exclude ".cache"`) {
		t.Error("script missing expected excludes")
	}
	if !strings.Contains(script, `"/var/www"`) || !strings.Contains(script, `"/etc/nginx"`) {
		t.Error("script missing expected backup paths")
	}

	// Verify retention policy
	if !strings.Contains(script, `restic forget --prune --keep-daily 7 --keep-weekly 4 --keep-monthly 6`) {
		t.Errorf("script missing retention forget command, got:\n%s", script)
	}
}

func TestBuildSnapshotsScript(t *testing.T) {
	job := Job{
		Name:    "site-backup",
		Server:  "vps-01",
		Backend: "/mnt/backup",
	}

	script := BuildSnapshotsScript(job)
	if !strings.Contains(script, `export RESTIC_REPOSITORY="/mnt/backup"`) {
		t.Error("snapshots script missing RESTIC_REPOSITORY")
	}
	if !strings.Contains(script, `restic snapshots --json`) {
		t.Error("snapshots script missing restic snapshots --json")
	}
}

// mockExecutor implements executor.Executor for testing Runner
type mockExecutor struct {
	outputToReturn string
	shouldFail     bool
}

func (m *mockExecutor) Execute(_ context.Context, target executor.Target, taskName string, _ string, output io.Writer) (*executor.Result, error) {
	if output != nil && m.outputToReturn != "" {
		_, _ = io.WriteString(output, m.outputToReturn)
	}

	if m.shouldFail {
		return &executor.Result{
			ServerName: target.Name,
			Template:   taskName,
			Success:    false,
			ExitCode:   1,
			Error:      fmt.Errorf("simulated execution failure"),
		}, fmt.Errorf("simulated execution failure")
	}

	return &executor.Result{
		ServerName: target.Name,
		Template:   taskName,
		Success:    true,
		ExitCode:   0,
		Duration:   2 * time.Second,
	}, nil
}

func (m *mockExecutor) Test(_ context.Context, _ executor.Target) (time.Duration, string, error) {
	return 10 * time.Millisecond, "Linux mock", nil
}

func TestRunner_Run_Success(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := storage.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("storage.Open() error: %v", err)
	}
	defer func() { _ = db.Close() }()

	backupRepo := storage.NewBackupRepo(db)
	serverStore := server.NewStore(filepath.Join(tmpDir, "servers.yaml"))
	_ = serverStore.Save(server.Server{
		Name: "vps-01",
		Host: "192.168.1.100",
		User: "root",
	})

	mockOutput := `
{"message_type":"status","percent_done":1.0}
{"message_type":"summary","files_new":12,"files_changed":3,"files_unmodified":50,"data_added":10485760,"total_files_processed":65,"total_bytes_processed":52428800,"total_duration":2.5,"snapshot_id":"snap-abc12345"}
`
	exec := &mockExecutor{outputToReturn: mockOutput}
	runner := NewRunner(exec, serverStore, backupRepo)

	job := Job{
		Name:    "app-data",
		Server:  "vps-01",
		Paths:   []string{"/var/app"},
		Backend: "/mnt/backup",
	}

	var console bytes.Buffer
	runRecord, err := runner.Run(context.Background(), job, &console)
	if err != nil {
		t.Fatalf("runner.Run() unexpected error: %v", err)
	}

	if runRecord.Status != "success" {
		t.Errorf("expected status 'success', got %q", runRecord.Status)
	}
	if runRecord.SnapshotID != "snap-abc12345" {
		t.Errorf("expected snapshot_id 'snap-abc12345', got %q", runRecord.SnapshotID)
	}
	if runRecord.FilesNew != 12 || runRecord.DataAddedBytes != 10485760 {
		t.Errorf("unexpected metrics: new=%d, added=%d", runRecord.FilesNew, runRecord.DataAddedBytes)
	}

	// Verify persistence in SQLite
	latest, err := backupRepo.GetLatestRun(context.Background(), "app-data")
	if err != nil {
		t.Fatalf("backupRepo.GetLatestRun() error: %v", err)
	}
	if latest == nil || latest.SnapshotID != "snap-abc12345" {
		t.Errorf("expected persisted snapshot_id 'snap-abc12345', got %+v", latest)
	}
}

func TestRunner_Run_Failure(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := storage.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("storage.Open() error: %v", err)
	}
	defer func() { _ = db.Close() }()

	backupRepo := storage.NewBackupRepo(db)
	serverStore := server.NewStore(filepath.Join(tmpDir, "servers.yaml"))

	exec := &mockExecutor{shouldFail: true}
	runner := NewRunner(exec, serverStore, backupRepo)

	job := Job{
		Name:    "local-test",
		Server:  "local",
		Paths:   []string{"/tmp/data"},
		Backend: "/tmp/backup",
	}

	runRecord, err := runner.Run(context.Background(), job, nil)
	if err == nil {
		t.Error("expected error from failed run, got nil")
	}
	if runRecord.Status != "failed" {
		t.Errorf("expected status 'failed', got %q", runRecord.Status)
	}
	if runRecord.ErrorMessage == "" {
		t.Error("expected non-empty error message")
	}

	// Verify failure recorded in SQLite
	latest, err := backupRepo.GetLatestRun(context.Background(), "local-test")
	if err != nil {
		t.Fatalf("backupRepo.GetLatestRun() error: %v", err)
	}
	if latest == nil || latest.Status != "failed" {
		t.Errorf("expected persisted status 'failed', got %+v", latest)
	}
}

func TestRunner_ListSnapshots(t *testing.T) {
	mockSnapshots := `[
		{"id":"snap-1111","time":"2026-08-20T10:00:00Z","paths":["/data"],"hostname":"vps-01","username":"root"}
	]`
	exec := &mockExecutor{outputToReturn: mockSnapshots}
	runner := NewRunner(exec, nil, nil)

	job := Job{
		Name:    "test-job",
		Server:  "local",
		Paths:   []string{"/data"},
		Backend: "/backup",
	}

	snapshots, err := runner.ListSnapshots(context.Background(), job)
	if err != nil {
		t.Fatalf("ListSnapshots() error: %v", err)
	}
	if len(snapshots) != 1 || snapshots[0].ID != "snap-1111" {
		t.Errorf("unexpected snapshots: %+v", snapshots)
	}
}
