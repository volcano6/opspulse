package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestBackupRepo_UntrackedRoundTrip pins the audit-gap flag end to end: a run
// stored as untracked must read back as untracked, a normal run must not, and
// the flag must survive the finalize UpdateRun.
func TestBackupRepo_UntrackedRoundTrip(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer func() { _ = db.Close() }()

	repo := NewBackupRepo(db)
	ctx := context.Background()
	started := time.Now().Truncate(time.Second)

	untracked := &BackupRun{
		JobName:    "web-data",
		ServerName: "vps-01",
		Status:     "success",
		Untracked:  true,
		StartedAt:  started,
	}
	if _, err := repo.CreateRun(ctx, untracked); err != nil {
		t.Fatalf("CreateRun(untracked) error: %v", err)
	}

	tracked := &BackupRun{
		JobName:    "db-data",
		ServerName: "vps-01",
		Status:     "success",
		StartedAt:  started.Add(time.Second),
	}
	if _, err := repo.CreateRun(ctx, tracked); err != nil {
		t.Fatalf("CreateRun(tracked) error: %v", err)
	}

	got, err := repo.GetLatestRun(ctx, "web-data")
	if err != nil {
		t.Fatalf("GetLatestRun(web-data) error: %v", err)
	}
	if got == nil || !got.Untracked {
		t.Errorf("untracked run read back as %+v; want Untracked=true", got)
	}

	// The flag must also survive the finalize path, which rewrites the row.
	untracked.Status = "failed"
	untracked.ErrorMessage = "restic exited with status 1"
	if err := repo.UpdateRun(ctx, untracked); err != nil {
		t.Fatalf("UpdateRun() error: %v", err)
	}
	got, err = repo.GetLatestRun(ctx, "web-data")
	if err != nil {
		t.Fatalf("GetLatestRun(web-data) after update error: %v", err)
	}
	if got == nil || !got.Untracked || got.Status != "failed" {
		t.Errorf("after UpdateRun: %+v; want Untracked=true with status failed", got)
	}

	gotTracked, err := repo.GetLatestRun(ctx, "db-data")
	if err != nil {
		t.Fatalf("GetLatestRun(db-data) error: %v", err)
	}
	if gotTracked == nil || gotTracked.Untracked {
		t.Errorf("tracked run read back as %+v; want Untracked=false", gotTracked)
	}
}
