package storage

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func faultRun() *BackupRun {
	return &BackupRun{JobName: "app-data", ServerName: "vps-01", Status: "running", StartedAt: time.Now()}
}

// TestBackupRepo_CreateRunFailsUnderRealSQLiteLock drives the lock failure mode
// against a real database instead of an injected error: a second connection
// holds BEGIN IMMEDIATE while another handle with no busy timeout tries to
// insert. The fail-closed refusal the runner depends on must see this error
// rather than proceed as if the run were recorded.
func TestBackupRepo_CreateRunFailsUnderRealSQLiteLock(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "locked.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	holder, err := db.Conn().Conn(ctx)
	if err != nil {
		t.Fatalf("Conn() error: %v", err)
	}
	t.Cleanup(func() { _ = holder.Close() })

	if _, err := holder.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("BEGIN IMMEDIATE error: %v", err)
	}
	released := false
	t.Cleanup(func() {
		if !released {
			_, _ = holder.ExecContext(ctx, "ROLLBACK")
		}
	})

	// No busy timeout, so the lock surfaces at once instead of retrying for the
	// production 5s window.
	locked, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	t.Cleanup(func() { _ = locked.Close() })

	repo := NewBackupRepo(&DB{conn: locked})
	if _, err := repo.CreateRun(ctx, faultRun()); err == nil {
		t.Fatal("CreateRun() succeeded while another connection held the write lock; want a busy error")
	}

	if _, err := holder.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatalf("ROLLBACK error: %v", err)
	}
	released = true

	if _, err := repo.CreateRun(ctx, faultRun()); err != nil {
		t.Fatalf("CreateRun() after the lock was released error: %v; the fixture is broken, not the refusal", err)
	}
}

// TestOpenFailsOnReadOnlyDirectory covers the second named failure mode: the
// audit store sits in a directory the process cannot write, so not even the
// database file can be created. The runner then refuses before touching the
// target. The third mode - a disk error - ends in the same unusable-database
// state covered by TestRunner_Run_FailsClosedOnUnusableDatabase.
func TestOpenFailsOnReadOnlyDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions are not enforced")
	}

	dir := filepath.Join(t.TempDir(), "readonly")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("Mkdir() error: %v", err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("Chmod() error: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	db, err := Open(filepath.Join(dir, "opspulse.db"))
	if err == nil {
		_ = db.Close()
		t.Fatal("Open() succeeded in a read-only directory; want a write failure")
	}
}
