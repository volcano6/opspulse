package storage

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestDB_LifecycleAndMigration(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() unexpected error: %v", err)
	}
	defer func() { _ = db.Close() }()

	if db.Path() != dbPath {
		t.Errorf("expected path %q, got %q", dbPath, db.Path())
	}
	if db.Conn() == nil {
		t.Fatal("expected non-nil Conn()")
	}

	// Test idempotency of Migrate
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("second Migrate() should be idempotent, got error: %v", err)
	}
}

func TestBackupRepo_CRUD(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := Open(filepath.Join(tmpDir, "test_backup.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer func() { _ = db.Close() }()

	repo := NewBackupRepo(db)
	ctx := context.Background()

	// 1. GetLatestRun on empty table
	latestEmpty, err := repo.GetLatestRun(ctx, "job-web")
	if err != nil {
		t.Fatalf("GetLatestRun on empty DB error: %v", err)
	}
	if latestEmpty != nil {
		t.Errorf("expected nil for empty DB, got %+v", latestEmpty)
	}

	// 2. CreateRun
	now := time.Now().Truncate(time.Second)
	run := &BackupRun{
		JobName:    "job-web",
		ServerName: "vps-01",
		Status:     "running",
		StartedAt:  now,
	}

	id, err := repo.CreateRun(ctx, run)
	if err != nil {
		t.Fatalf("CreateRun() error: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive id, got %d", id)
	}

	// 3. UpdateRun
	finished := now.Add(45 * time.Second)
	run.SnapshotID = "snap-12345"
	run.Status = "success"
	run.FilesNew = 10
	run.FilesChanged = 2
	run.FilesUnmodified = 100
	run.DataAddedBytes = 5242880 // 5MB
	run.TotalBytes = 104857600   // 100MB
	run.DurationSeconds = 45.2
	run.LogPath = "/tmp/logs/backup.log"
	run.FinishedAt = &finished

	if err := repo.UpdateRun(ctx, run); err != nil {
		t.Fatalf("UpdateRun() error: %v", err)
	}

	// 4. GetLatestRun
	latest, err := repo.GetLatestRun(ctx, "job-web")
	if err != nil {
		t.Fatalf("GetLatestRun() error: %v", err)
	}
	if latest == nil {
		t.Fatal("expected non-nil latest run")
	}
	if latest.SnapshotID != "snap-12345" {
		t.Errorf("expected snapshot_id 'snap-12345', got %q", latest.SnapshotID)
	}
	if latest.Status != "success" {
		t.Errorf("expected status 'success', got %q", latest.Status)
	}
	if latest.FilesNew != 10 || latest.FilesChanged != 2 {
		t.Errorf("expected files_new=10, files_changed=2, got new=%d, changed=%d", latest.FilesNew, latest.FilesChanged)
	}
	if latest.DataAddedBytes != 5242880 || latest.TotalBytes != 104857600 {
		t.Errorf("expected data_added=5242880, total=104857600, got added=%d, total=%d", latest.DataAddedBytes, latest.TotalBytes)
	}

	// 5. Create a second run for another job
	jobDBRun := &BackupRun{
		JobName:         "job-db",
		ServerName:      "vps-02",
		SnapshotID:      "snap-67890",
		Status:          "success",
		DurationSeconds: 12.0,
		StartedAt:       now.Add(time.Minute),
	}
	_, err = repo.CreateRun(ctx, jobDBRun)
	if err != nil {
		t.Fatalf("CreateRun(job-db) error: %v", err)
	}

	// 6. GetAllLatestRuns
	allLatest, err := repo.GetAllLatestRuns(ctx)
	if err != nil {
		t.Fatalf("GetAllLatestRuns() error: %v", err)
	}
	if len(allLatest) != 2 {
		t.Fatalf("expected 2 latest runs, got %d", len(allLatest))
	}

	// 7. ListRuns with filter
	webRuns, err := repo.ListRuns(ctx, "job-web", 10)
	if err != nil {
		t.Fatalf("ListRuns(job-web) error: %v", err)
	}
	if len(webRuns) != 1 {
		t.Errorf("expected 1 run for job-web, got %d", len(webRuns))
	}

	allRuns, err := repo.ListRuns(ctx, "", 10)
	if err != nil {
		t.Fatalf("ListRuns('') error: %v", err)
	}
	if len(allRuns) != 2 {
		t.Errorf("expected 2 runs in total, got %d", len(allRuns))
	}
}

func TestBackupRepo_Concurrency(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := Open(filepath.Join(tmpDir, "test_concurrent.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer func() { _ = db.Close() }()

	repo := NewBackupRepo(db)
	ctx := context.Background()

	var wg sync.WaitGroup
	workers := 10
	iterations := 5

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				run := &BackupRun{
					JobName:    "concurrent-job",
					ServerName: "vps-concurrent",
					Status:     "success",
					StartedAt:  time.Now(),
				}
				_, err := repo.CreateRun(ctx, run)
				if err != nil {
					t.Errorf("worker %d CreateRun error: %v", workerID, err)
				}
			}
		}(w)
	}

	wg.Wait()

	runs, err := repo.ListRuns(ctx, "concurrent-job", 100)
	if err != nil {
		t.Fatalf("ListRuns error: %v", err)
	}
	expectedTotal := workers * iterations
	if len(runs) != expectedTotal {
		t.Errorf("expected %d runs, got %d", expectedTotal, len(runs))
	}
}
