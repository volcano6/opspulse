package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/server"
	"github.com/volcano6/opspulse/internal/storage"
)

const auditSummaryOutput = `{"message_type":"summary","files_new":3,"files_changed":1,"files_unmodified":10,"data_added":4096,"total_files_processed":14,"total_bytes_processed":8192,"total_duration":1.5,"snapshot_id":"snap-untracked-01"}`

// failingRecorder stands in for an audit store that cannot be written. SQLite
// lock contention, a read-only data directory and a disk error all surface here
// as a CreateRun error, so each path can be exercised deterministically instead
// of racing a real database.
type failingRecorder struct {
	mu        sync.Mutex
	failCount int // leading CreateRun calls that fail; -1 means every call fails
	err       error
	updateErr error // when set, every UpdateRun fails (finalize write failure)
	create    []*storage.BackupRun
	update    []*storage.BackupRun
}

func (f *failingRecorder) CreateRun(_ context.Context, run *storage.BackupRun) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.create = append(f.create, run)
	if f.failCount < 0 || len(f.create) <= f.failCount {
		if f.err != nil {
			return 0, f.err
		}
		return 0, errors.New("database is locked")
	}

	run.ID = int64(len(f.create))
	return run.ID, nil
}

func (f *failingRecorder) UpdateRun(_ context.Context, run *storage.BackupRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.update = append(f.update, run)
	return f.updateErr
}

func (f *failingRecorder) createAttempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.create)
}

func (f *failingRecorder) lastCreate() *storage.BackupRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.create) == 0 {
		return nil
	}
	return f.create[len(f.create)-1]
}

// countingExecutor counts real executions so a test can prove the runner
// refused before it touched the target.
type countingExecutor struct {
	calls  int
	output string
}

func (c *countingExecutor) Execute(_ context.Context, target executor.Target, taskName string, _ string, output io.Writer) (*executor.Result, error) {
	c.calls++
	if output != nil && c.output != "" {
		_, _ = io.WriteString(output, c.output)
	}

	return &executor.Result{
		ServerName: target.Name,
		Template:   taskName,
		Success:    true,
		ExitCode:   0,
		Duration:   time.Second,
	}, nil
}

func (c *countingExecutor) Test(_ context.Context, _ executor.Target) (time.Duration, string, error) {
	return time.Millisecond, "mock", nil
}

func newAuditRunner(t *testing.T, rec RunRecorder, exec executor.Executor) *Runner {
	t.Helper()

	dir := t.TempDir()
	t.Setenv("OPSPULSE_HOME", dir)

	serverStore := server.NewStore(filepath.Join(dir, "servers.yaml"))
	if err := serverStore.Save(server.Server{Name: "vps-01", Host: "203.0.113.10", User: "root"}); err != nil {
		t.Fatalf("Save(server) error: %v", err)
	}

	return NewRunner(exec, serverStore, rec)
}

func auditJob() Job {
	return Job{Name: "app-data", Server: "vps-01", Paths: []string{"/srv/app"}, Backend: "/srv/backups"}
}

// TestRunner_Run_FailsClosedWhenAuditRecordCannotBeCreated pins the primary
// contract: when the run row cannot be written, no real backup happens. The
// three persistence failure shapes the ticket names (SQLite lock, read-only
// directory, disk error) must all behave identically.
func TestRunner_Run_FailsClosedWhenAuditRecordCannotBeCreated(t *testing.T) {
	causes := []struct {
		name string
		err  error
	}{
		{"sqlite lock", errors.New("database is locked (5) (SQLITE_BUSY)")},
		{"read-only directory", errors.New("attempt to write a readonly database (8)")},
		{"disk error", errors.New("disk I/O error (10)")},
	}

	for _, tc := range causes {
		t.Run(tc.name, func(t *testing.T) {
			rec := &failingRecorder{failCount: -1, err: tc.err}
			exec := &countingExecutor{output: auditSummaryOutput}
			runner := newAuditRunner(t, rec, exec)

			var out bytes.Buffer
			run, err := runner.Run(context.Background(), auditJob(), &out)

			if err == nil {
				t.Fatal("Run() succeeded; want a fail-closed refusal")
			}
			if !strings.Contains(err.Error(), tc.err.Error()) {
				t.Errorf("Run() error = %v; want it to carry the persistence error %q", err, tc.err)
			}
			if !strings.Contains(err.Error(), "--allow-untracked") {
				t.Errorf("Run() error = %v; want it to point at the --allow-untracked escape hatch", err)
			}
			if run != nil {
				t.Errorf("Run() returned record %+v; want nil on refusal", run)
			}
			if exec.calls != 0 {
				t.Errorf("executor ran %d time(s); want 0 — no backup may start without an audit record", exec.calls)
			}
		})
	}
}

// TestRunner_Run_FailsClosedOnUnusableDatabase covers the same refusal through
// the real SQLite repo instead of the injected fake: a closed database is the
// end state of a lock, read-only directory or disk failure.
func TestRunner_Run_FailsClosedOnUnusableDatabase(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("storage.Open() error: %v", err)
	}

	repo := storage.NewBackupRepo(db)
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close() error: %v", err)
	}

	exec := &countingExecutor{output: auditSummaryOutput}
	runner := newAuditRunner(t, repo, exec)

	var out bytes.Buffer
	if _, err := runner.Run(context.Background(), auditJob(), &out); err == nil {
		t.Fatal("Run() succeeded against a closed database; want a fail-closed refusal")
	}
	if exec.calls != 0 {
		t.Errorf("executor ran %d time(s); want 0", exec.calls)
	}
}

// TestRunner_Run_AllowUntrackedRunsAndMarks proves the emergency mode is
// explicit: the backup really runs, but both the record and the console say it
// was not audited.
func TestRunner_Run_AllowUntrackedRunsAndMarks(t *testing.T) {
	rec := &failingRecorder{failCount: -1, err: errors.New("database is locked (5)")}
	exec := &countingExecutor{output: auditSummaryOutput}
	runner := newAuditRunner(t, rec, exec)
	runner.SetAllowUntracked(true)

	var out bytes.Buffer
	run, err := runner.Run(context.Background(), auditJob(), &out)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if run == nil {
		t.Fatal("Run() returned a nil record")
	}
	if run.Status != "success" {
		t.Errorf("Status = %q; want success (the data really was backed up)", run.Status)
	}
	if !run.Untracked {
		t.Error("Untracked = false; want true in --allow-untracked mode")
	}
	if exec.calls != 1 {
		t.Errorf("executor ran %d time(s); want 1", exec.calls)
	}
	if !strings.Contains(out.String(), "UNTRACKED") {
		t.Errorf("console output = %q; want an UNTRACKED warning", out.String())
	}
	if rec.createAttempts() < 2 {
		t.Errorf("CreateRun attempts = %d; want the finalize step to retry persistence", rec.createAttempts())
	}
}

// TestRunner_Run_UntrackedRetryPersistsRecord covers a transient write failure:
// the run starts untracked, but the finalize retry still lands the row — and it
// must stay flagged so the audit gap is not silently closed.
func TestRunner_Run_UntrackedRetryPersistsRecord(t *testing.T) {
	rec := &failingRecorder{failCount: 1, err: errors.New("database is locked (5)")}
	exec := &countingExecutor{output: auditSummaryOutput}
	runner := newAuditRunner(t, rec, exec)
	runner.SetAllowUntracked(true)

	var out bytes.Buffer
	run, err := runner.Run(context.Background(), auditJob(), &out)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if run.ID == 0 {
		t.Fatal("record was not persisted by the finalize retry (ID == 0)")
	}
	if !run.Untracked {
		t.Error("Untracked = false; a late-created record must stay flagged")
	}

	last := rec.lastCreate()
	if last == nil || last.Status != "success" || !last.Untracked {
		t.Errorf("persisted record = %+v; want status success with untracked true", last)
	}
}

// TestPool_RunAll_CountsUntrackedSuccesses pins the batch accounting: an
// untracked success is still a success for the data, but it is counted and
// displayed separately so a summary can never hide the audit gap.
func TestPool_RunAll_CountsUntrackedSuccesses(t *testing.T) {
	rec := &failingRecorder{failCount: -1, err: errors.New("database is locked (5)")}
	exec := &countingExecutor{output: auditSummaryOutput}
	runner := newAuditRunner(t, rec, exec)
	runner.SetAllowUntracked(true)

	pool := NewPool(runner, 1)
	var console bytes.Buffer
	res, err := pool.RunAll(context.Background(), []Job{auditJob()}, false, &console)
	if err != nil {
		t.Fatalf("RunAll() error: %v", err)
	}
	if res.SuccessCount != 1 || res.UntrackedCount != 1 || res.FailureCount != 0 {
		t.Errorf("counts = success %d / untracked %d / failure %d; want 1/1/0",
			res.SuccessCount, res.UntrackedCount, res.FailureCount)
	}

	var summary bytes.Buffer
	res.PrintSummary(&summary)
	if !strings.Contains(summary.String(), "SUCCESS (UNTRACKED)") {
		t.Errorf("summary = %q; want the row status to read SUCCESS (UNTRACKED)", summary.String())
	}
	if !strings.Contains(summary.String(), "Untracked: 1") {
		t.Errorf("summary = %q; want an Untracked total", summary.String())
	}
}

// TestPool_RunAll_TrackedSuccessIsNotMarkedUntracked is the negative control:
// the flag must never leak onto ordinary runs.
func TestPool_RunAll_TrackedSuccessIsNotMarkedUntracked(t *testing.T) {
	rec := &failingRecorder{}
	exec := &countingExecutor{output: auditSummaryOutput}
	runner := newAuditRunner(t, rec, exec)

	pool := NewPool(runner, 1)
	var console bytes.Buffer
	res, err := pool.RunAll(context.Background(), []Job{auditJob()}, false, &console)
	if err != nil {
		t.Fatalf("RunAll() error: %v", err)
	}
	if res.SuccessCount != 1 || res.UntrackedCount != 0 {
		t.Errorf("counts = success %d / untracked %d; want 1/0", res.SuccessCount, res.UntrackedCount)
	}

	var summary bytes.Buffer
	res.PrintSummary(&summary)
	if strings.Contains(summary.String(), "UNTRACKED") {
		t.Errorf("summary = %q; must not mark an audited run as untracked", summary.String())
	}
}

// TestRunner_Run_UntrackedWhenFinalizeCannotPersist covers the audit gap that
// opens after the run row was created: the row can no longer be written, so it
// would keep its stale 'running' status while the caller reported a normal
// success. The run must come back flagged untracked and as an error, so the CLI
// exits 3 and the console says so.
func TestRunner_Run_UntrackedWhenFinalizeCannotPersist(t *testing.T) {
	rec := &failingRecorder{updateErr: errors.New("attempt to write a readonly database (8)")}
	exec := &countingExecutor{output: auditSummaryOutput}
	runner := newAuditRunner(t, rec, exec)

	var out bytes.Buffer
	run, err := runner.Run(context.Background(), auditJob(), &out)

	if err == nil {
		t.Fatal("Run() returned nil error; an unaudited success must be reported as such")
	}
	if !errors.Is(err, ErrUntracked) {
		t.Errorf("Run() error = %v; want it to wrap ErrUntracked", err)
	}
	if run == nil {
		t.Fatal("Run() returned a nil record")
	}
	if !run.Untracked {
		t.Error("Untracked = false; want true when the finalize write failed")
	}
	if run.Status != "success" {
		t.Errorf("Status = %q; want success - the backup itself worked", run.Status)
	}
	if exec.calls != 1 {
		t.Errorf("executor ran %d time(s); want 1", exec.calls)
	}
	if !strings.Contains(out.String(), "UNTRACKED") {
		t.Errorf("console output = %q; want an UNTRACKED warning", out.String())
	}
}

// TestPool_RunAll_FinalizeGapIsUntrackedNotFailure keeps the batch accounting
// honest for the same gap: the data really was backed up, so the run is reported
// as an unaudited success - which is what turns into exit code 3 - rather than
// as a failed backup.
func TestPool_RunAll_FinalizeGapIsUntrackedNotFailure(t *testing.T) {
	rec := &failingRecorder{updateErr: errors.New("database is locked (5)")}
	exec := &countingExecutor{output: auditSummaryOutput}
	runner := newAuditRunner(t, rec, exec)

	pool := NewPool(runner, 1)
	var console bytes.Buffer
	res, err := pool.RunAll(context.Background(), []Job{auditJob()}, false, &console)
	if err != nil {
		t.Fatalf("RunAll() error: %v", err)
	}
	if res.SuccessCount != 1 || res.UntrackedCount != 1 || res.FailureCount != 0 {
		t.Errorf("counts = success %d / untracked %d / failure %d; want 1/1/0",
			res.SuccessCount, res.UntrackedCount, res.FailureCount)
	}

	var summary bytes.Buffer
	res.PrintSummary(&summary)
	if !strings.Contains(summary.String(), "SUCCESS (UNTRACKED)") {
		t.Errorf("summary = %q; want the row status to read SUCCESS (UNTRACKED)", summary.String())
	}
}

// failingExecutor fails every execution so a run ends as a failure.
type failingExecutor struct{ calls int }

func (f *failingExecutor) Execute(_ context.Context, target executor.Target, taskName string, _ string, _ io.Writer) (*executor.Result, error) {
	f.calls++
	return &executor.Result{
		ServerName: target.Name,
		Template:   taskName,
		Success:    false,
		ExitCode:   1,
		Duration:   time.Second,
	}, errors.New("restic exited with status 1")
}

func (f *failingExecutor) Test(_ context.Context, _ executor.Target) (time.Duration, string, error) {
	return time.Millisecond, "mock", nil
}

// TestPool_RunAll_UntrackedFailureIsFlaggedAndCounted covers the mirror case:
// when the backup itself failed and the audit record is missing too, the summary
// must show the gap instead of a plain failure, and the run counts as a failure
// - never as an untracked success.
func TestPool_RunAll_UntrackedFailureIsFlaggedAndCounted(t *testing.T) {
	rec := &failingRecorder{failCount: -1, err: errors.New("database is locked (5)")}
	exec := &failingExecutor{}
	runner := newAuditRunner(t, rec, exec)
	runner.SetAllowUntracked(true)

	pool := NewPool(runner, 1)
	var console bytes.Buffer
	res, err := pool.RunAll(context.Background(), []Job{auditJob()}, false, &console)
	if err != nil {
		t.Fatalf("RunAll() error: %v", err)
	}
	if res.SuccessCount != 0 || res.UntrackedCount != 1 || res.FailureCount != 1 {
		t.Errorf("counts = success %d / untracked %d / failure %d; want 0/1/1",
			res.SuccessCount, res.UntrackedCount, res.FailureCount)
	}

	var summary bytes.Buffer
	res.PrintSummary(&summary)
	if !strings.Contains(summary.String(), "FAILED (UNTRACKED)") {
		t.Errorf("summary = %q; want the row status to read FAILED (UNTRACKED)", summary.String())
	}
	if !strings.Contains(summary.String(), "Untracked: 1") {
		t.Errorf("summary = %q; want the untracked failure in the Untracked total", summary.String())
	}
}
