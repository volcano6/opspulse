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

	// Verify autostart was invoked
	foundAutostart := false
	for _, task := range mockExec.tasksExecuted {
		if strings.HasPrefix(task, "autostart-") {
			foundAutostart = true
			break
		}
	}
	if !foundAutostart {
		t.Errorf("expected autostart task to be executed, executed tasks: %v", mockExec.tasksExecuted)
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
		if strings.HasPrefix(task, "autostart-") {
			t.Errorf("autostart task should NOT be executed when NoStart is true, got: %v", mockExec.tasksExecuted)
		}
	}
}
