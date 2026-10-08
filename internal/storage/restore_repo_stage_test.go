package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestRestoreRepo_FailureStageRoundTrip pins the failure_stage column and the
// snapshot_id update that a restore depends on: the row is created before the
// snapshot is resolved, so the final write has to replace "latest" with the
// real ID and carry the stage the attempt failed in.
func TestRestoreRepo_FailureStageRoundTrip(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "stage.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer func() { _ = db.Close() }()

	repo := NewRestoreRepo(db)
	ctx := context.Background()
	started := time.Now().UTC().Truncate(time.Second)

	run := &RestoreRun{
		JobName:      "web-data",
		SnapshotID:   "latest",
		SourceServer: "vps-01",
		TargetServer: "vps-01",
		TargetPath:   "/",
		Status:       "running",
		StartedAt:    started,
	}
	if _, err := repo.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun() error: %v", err)
	}

	runs, err := repo.ListRuns(ctx, "web-data", 1)
	if err != nil {
		t.Fatalf("ListRuns() error: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	if runs[0].FailureStage != "" {
		t.Errorf("running row failure stage = %q, want empty", runs[0].FailureStage)
	}
	if runs[0].SnapshotID != "latest" {
		t.Errorf("running row snapshot = %q, want latest", runs[0].SnapshotID)
	}

	finished := started.Add(2 * time.Second)
	run.SnapshotID = "snap-abcdef1234567890"
	run.Status = "failed"
	run.FailureStage = RestoreStageValidation
	run.ErrorMessage = `snapshot "snap-abcdef1234567890" does not belong to job "web-data"`
	run.DurationSeconds = 2
	run.FinishedAt = &finished
	if err := repo.UpdateRun(ctx, run); err != nil {
		t.Fatalf("UpdateRun() error: %v", err)
	}

	runs, err = repo.ListRuns(ctx, "web-data", 1)
	if err != nil {
		t.Fatalf("ListRuns() error: %v", err)
	}
	got := runs[0]
	if got.Status != "failed" || got.FailureStage != RestoreStageValidation {
		t.Errorf("status/stage = %q/%q, want failed/validation", got.Status, got.FailureStage)
	}
	if got.SnapshotID != "snap-abcdef1234567890" {
		t.Errorf("snapshot = %q, want the resolved ID written by UpdateRun", got.SnapshotID)
	}
	if got.ErrorMessage != run.ErrorMessage {
		t.Errorf("error message = %q, want %q", got.ErrorMessage, run.ErrorMessage)
	}
	if got.FinishedAt == nil {
		t.Error("finished_at is nil after UpdateRun")
	}
}
