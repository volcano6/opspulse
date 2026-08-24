package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestRestoreRepo_CRUD(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := Open(filepath.Join(tmpDir, "test_restore.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer func() { _ = db.Close() }()

	repo := NewRestoreRepo(db)
	ctx := context.Background()

	// 1. GetLatestRun on empty table
	latestEmpty, err := repo.GetLatestRun(ctx, "job-blog")
	if err != nil {
		t.Fatalf("GetLatestRun on empty DB error: %v", err)
	}
	if latestEmpty != nil {
		t.Errorf("expected nil for empty DB, got %+v", latestEmpty)
	}

	// 2. CreateRun
	now := time.Now().Truncate(time.Second)
	run := &RestoreRun{
		JobName:      "job-blog",
		AssetID:      "blog-compose",
		SnapshotID:   "snap-12345678",
		SourceServer: "vps-old",
		TargetServer: "vps-new",
		TargetPath:   "/var/data/blog",
		Status:       "running",
		StartedAt:    now,
	}

	id, err := repo.CreateRun(ctx, run)
	if err != nil {
		t.Fatalf("CreateRun() error: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected positive id, got %d", id)
	}

	// 3. UpdateRun
	finished := now.Add(15 * time.Second)
	run.Status = "success"
	run.FilesRestored = 25
	run.TotalBytesRestored = 10485760
	run.DurationSeconds = 15.3
	run.LogPath = "/tmp/logs/restore.log"
	run.FinishedAt = &finished

	if err := repo.UpdateRun(ctx, run); err != nil {
		t.Fatalf("UpdateRun() error: %v", err)
	}

	// 4. GetLatestRun
	latest, err := repo.GetLatestRun(ctx, "job-blog")
	if err != nil {
		t.Fatalf("GetLatestRun() error: %v", err)
	}
	if latest == nil {
		t.Fatal("expected non-nil latest run")
	}
	if latest.SnapshotID != "snap-12345678" || latest.Status != "success" {
		t.Errorf("unexpected latest run: %+v", latest)
	}
	if latest.FilesRestored != 25 || latest.TotalBytesRestored != 10485760 {
		t.Errorf("unexpected metrics: files=%d, bytes=%d", latest.FilesRestored, latest.TotalBytesRestored)
	}
	if latest.TargetPath != "/var/data/blog" || latest.AssetID != "blog-compose" {
		t.Errorf("unexpected paths/assets: target=%s, asset=%s", latest.TargetPath, latest.AssetID)
	}

	// 5. ListRuns
	runs, err := repo.ListRuns(ctx, "job-blog", 10)
	if err != nil {
		t.Fatalf("ListRuns error: %v", err)
	}
	if len(runs) != 1 {
		t.Errorf("expected 1 run, got %d", len(runs))
	}
}
