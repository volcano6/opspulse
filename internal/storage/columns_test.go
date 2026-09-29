package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestColumnConstantsByteEquivalence guards the refactor against drift: the
// generated SELECT column lists must remain byte-identical to the
// pre-refactor literals (column order, newlines, indentation all preserved).
func TestColumnConstantsByteEquivalence(t *testing.T) {
	wantBackup := "id, job_name, server_name, snapshot_id, status,\n" +
		"\t\tfiles_new, files_changed, files_unmodified,\n" +
		"\t\tdata_added_bytes, total_bytes, duration_seconds,\n" +
		"\t\terror_message, log_path, started_at, finished_at"
	if backupRunColumns != wantBackup {
		t.Errorf("backupRunColumns drifted:\n got %q\nwant %q", backupRunColumns, wantBackup)
	}

	wantRestore := "id, job_name, asset_id, snapshot_id, source_server, target_server, target_path,\n" +
		"\t\t       status, files_restored, total_bytes_restored, duration_seconds, error_message,\n" +
		"\t\t       log_path, started_at, finished_at"
	if restoreRunColumns != wantRestore {
		t.Errorf("restoreRunColumns drifted:\n got %q\nwant %q", restoreRunColumns, wantRestore)
	}
}

// TestRestoreRepo_ListRunsUnfiltered exercises the jobName=="" branch of
// RestoreRepo.ListRuns against a real SQLite database.
func TestRestoreRepo_ListRunsUnfiltered(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := Open(filepath.Join(tmpDir, "test_restore_unfiltered.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer func() { _ = db.Close() }()

	repo := NewRestoreRepo(db)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)

	for _, job := range []string{"job-a", "job-b"} {
		run := &RestoreRun{
			JobName:      job,
			SnapshotID:   "snap-" + job,
			SourceServer: "vps-old",
			TargetServer: "vps-new",
			Status:       "success",
			StartedAt:    now,
		}
		if _, err := repo.CreateRun(ctx, run); err != nil {
			t.Fatalf("CreateRun(%s) error: %v", job, err)
		}
	}

	runs, err := repo.ListRuns(ctx, "", 10)
	if err != nil {
		t.Fatalf("ListRuns('') error: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected 2 unfiltered runs, got %d", len(runs))
	}

	// Both branches share restoreRunColumns; verify field mapping end-to-end.
	filtered, err := repo.ListRuns(ctx, "job-a", 10)
	if err != nil {
		t.Fatalf("ListRuns('job-a') error: %v", err)
	}
	if len(filtered) != 1 || filtered[0].JobName != "job-a" || filtered[0].SnapshotID != "snap-job-a" {
		t.Errorf("unexpected filtered result: %+v", filtered)
	}
}
