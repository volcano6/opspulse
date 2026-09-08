package backup

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/volcano6/opspulse/internal/asset"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/server"
	"github.com/volcano6/opspulse/internal/storage"
)

type restoreMockExecutor struct {
	tasksExecuted []string
}

func (m *restoreMockExecutor) Execute(_ context.Context, target executor.Target, taskName string, _ string, output io.Writer) (*executor.Result, error) {
	m.tasksExecuted = append(m.tasksExecuted, taskName)

	if strings.HasPrefix(taskName, "snapshots-") {
		snapshotJSON := `[{"id":"snap-abcdef1234567890","short_id":"snap-abc1","time":"2026-09-01T10:00:00Z"}]`
		if output != nil {
			_, _ = io.WriteString(output, snapshotJSON)
		}
	}

	return &executor.Result{
		ServerName: target.Name,
		Template:   taskName,
		Success:    true,
		ExitCode:   0,
		Duration:   500 * time.Millisecond,
	}, nil
}

func (m *restoreMockExecutor) Test(_ context.Context, _ executor.Target) (time.Duration, string, error) {
	return 10 * time.Millisecond, "Linux", nil
}

func TestRestoreRunner_Run_AutoStartByDefault(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := storage.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("storage.Open() error: %v", err)
	}
	defer func() { _ = db.Close() }()

	restoreRepo := storage.NewRestoreRepo(db)
	serverStore := server.NewStore(filepath.Join(tmpDir, "servers.yaml"))
	backupStore := NewStore(filepath.Join(tmpDir, "backups.yaml"))
	assetStore := asset.NewStore(filepath.Join(tmpDir, "assets.yaml"))

	_ = serverStore.Save(server.Server{Name: "vps-01", Host: "10.0.0.1", User: "root"})
	_ = serverStore.Save(server.Server{Name: "vps-02", Host: "10.0.0.2", User: "root"})

	job := Job{
		Name:    "my-app",
		Server:  "vps-01",
		Paths:   []string{"/var/lib/opspulse/containers/my-app"},
		Backend: "/mnt/repo",
	}
	_ = backupStore.Save(job)

	mockExec := &restoreMockExecutor{}
	runner := NewRestoreRunner(mockExec, serverStore, restoreRepo, backupStore, assetStore)

	opts := RestoreOptions{
		SnapshotID:   "snap-abcdef1234567890",
		TargetServer: "vps-02",
		AliasName:    "my-renamed-app",
		NoStart:      false, // default behavior
	}

	var buf bytes.Buffer
	record, err := runner.Run(context.Background(), job, opts, &buf)
	if err != nil {
		t.Fatalf("RestoreRunner.Run() error = %v", err)
	}

	if record.Status != "success" {
		t.Errorf("record.Status = %q, want success", record.Status)
	}

	// Verify autostart or manifest check was invoked
	foundAutostart := false
	for _, task := range mockExec.tasksExecuted {
		if strings.HasPrefix(task, "autostart-") || strings.HasPrefix(task, "read-manifest-") {
			foundAutostart = true
			break
		}
	}
	if !foundAutostart {
		t.Errorf("expected autostart or read-manifest task to be executed, executed tasks: %v", mockExec.tasksExecuted)
	}
}

func TestRestoreRunner_Run_NoStartSuppressed(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := storage.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("storage.Open() error: %v", err)
	}
	defer func() { _ = db.Close() }()

	restoreRepo := storage.NewRestoreRepo(db)
	serverStore := server.NewStore(filepath.Join(tmpDir, "servers.yaml"))
	backupStore := NewStore(filepath.Join(tmpDir, "backups.yaml"))
	assetStore := asset.NewStore(filepath.Join(tmpDir, "assets.yaml"))

	_ = serverStore.Save(server.Server{Name: "vps-01", Host: "10.0.0.1", User: "root"})
	_ = serverStore.Save(server.Server{Name: "vps-02", Host: "10.0.0.2", User: "root"})

	job := Job{
		Name:    "my-app",
		Server:  "vps-01",
		Paths:   []string{"/data"},
		Backend: "/mnt/repo",
	}

	mockExec := &restoreMockExecutor{}
	runner := NewRestoreRunner(mockExec, serverStore, restoreRepo, backupStore, assetStore)

	opts := RestoreOptions{
		SnapshotID:   "snap-abcdef1234567890",
		TargetServer: "vps-02",
		NoStart:      true, // suppress auto start
	}

	var buf bytes.Buffer
	record, err := runner.Run(context.Background(), job, opts, &buf)
	if err != nil {
		t.Fatalf("RestoreRunner.Run() error = %v", err)
	}

	if record.Status != "success" {
		t.Errorf("record.Status = %q, want success", record.Status)
	}

	// Verify autostart was NOT invoked
	for _, task := range mockExec.tasksExecuted {
		if strings.HasPrefix(task, "autostart-") || strings.HasPrefix(task, "read-manifest-") {
			t.Errorf("autostart task should NOT be executed when NoStart is true, got: %v", mockExec.tasksExecuted)
		}
	}
}

func TestRestoredPath(t *testing.T) {
	tests := []struct {
		target   string
		original string
		expected string
	}{
		{"/", "/var/lib/app", "/var/lib/app"},
		{"", "/var/lib/app", "/var/lib/app"},
		{"/mnt/restore", "/var/lib/app", "/mnt/restore/var/lib/app"},
		{"/mnt/restore/", "/var/lib/app", "/mnt/restore/var/lib/app"},
		{"/opt/dest", "/tmp/dump.sql.gz", "/opt/dest/tmp/dump.sql.gz"},
	}

	for _, tt := range tests {
		got := RestoredPath(tt.target, tt.original)
		if got != tt.expected {
			t.Errorf("RestoredPath(%q, %q) = %q, want %q", tt.target, tt.original, got, tt.expected)
		}
	}
}

func TestRestoreRunner_Run_WrongSnapshotRejected(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := storage.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("storage.Open() error: %v", err)
	}
	defer func() { _ = db.Close() }()

	restoreRepo := storage.NewRestoreRepo(db)
	serverStore := server.NewStore(filepath.Join(tmpDir, "servers.yaml"))
	backupStore := NewStore(filepath.Join(tmpDir, "backups.yaml"))
	assetStore := asset.NewStore(filepath.Join(tmpDir, "assets.yaml"))

	_ = serverStore.Save(server.Server{Name: "vps-01", Host: "10.0.0.1", User: "root"})

	job := Job{
		Name:    "my-app",
		Server:  "vps-01",
		Paths:   []string{"/var/lib/opspulse/containers/my-app"},
		Backend: "/mnt/repo",
	}

	mockExec := &restoreMockExecutor{}
	runner := NewRestoreRunner(mockExec, serverStore, restoreRepo, backupStore, assetStore)

	opts := RestoreOptions{
		SnapshotID: "wrong-snapshot-from-other-job",
	}

	var buf bytes.Buffer
	_, err = runner.Run(context.Background(), job, opts, &buf)
	if err == nil {
		t.Fatal("expected error when restoring snapshot belonging to another job, got nil")
	}
	if !strings.Contains(err.Error(), "does not belong to job") {
		t.Errorf("error message should mention snapshot mismatch: %v", err)
	}
}
