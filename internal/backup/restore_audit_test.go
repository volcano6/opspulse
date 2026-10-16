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

	"github.com/volcano6/opspulse/internal/asset"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/server"
	"github.com/volcano6/opspulse/internal/storage"
)

// auditSnapshotID is the single snapshot served by restoreAuditExecutor.
const auditSnapshotID = "snap-abcdef1234567890"

// restoreAuditExecutor answers snapshot queries with one known snapshot and can
// be told to fail every task whose name starts with failPrefix.
type restoreAuditExecutor struct {
	failPrefix string
}

func (m *restoreAuditExecutor) Execute(_ context.Context, target executor.Target, taskName, _ string, output io.Writer) (*executor.Result, error) {
	if strings.HasPrefix(taskName, "snapshots-") {
		_, _ = io.WriteString(output, `[{"id":"`+auditSnapshotID+`","short_id":"snap-abc1","time":"2026-09-01T10:00:00Z"}]`)
	}
	if m.failPrefix != "" && strings.HasPrefix(taskName, m.failPrefix) {
		return &executor.Result{
			ServerName: target.Name,
			Template:   taskName,
			Success:    false,
			ExitCode:   1,
			Duration:   time.Millisecond,
		}, fmt.Errorf("exit status 1: %s failed", taskName)
	}
	return &executor.Result{
		ServerName: target.Name,
		Template:   taskName,
		Success:    true,
		ExitCode:   0,
		Duration:   time.Millisecond,
	}, nil
}

func (m *restoreAuditExecutor) Test(_ context.Context, _ executor.Target) (time.Duration, string, error) {
	return time.Millisecond, "Linux", nil
}

type restoreAuditFixture struct {
	runner *RestoreRunner
	repo   *storage.RestoreRepo
	job    Job
}

func newRestoreAuditFixture(t *testing.T, exec executor.Executor) restoreAuditFixture {
	t.Helper()

	tmpDir := t.TempDir()
	db, err := storage.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("storage.Open() error: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	serverStore := server.NewStore(filepath.Join(tmpDir, "servers.yaml"))
	if err := serverStore.Save(server.Server{Name: "vps-01", Host: "203.0.113.10", User: "root"}); err != nil {
		t.Fatalf("serverStore.Save() error: %v", err)
	}
	backupStore := NewStore(filepath.Join(tmpDir, "backups.yaml"))
	job := Job{
		Name:    "web-data",
		Server:  "vps-01",
		Paths:   []string{"/var/www"},
		Backend: "/mnt/repo",
	}
	if err := backupStore.Save(job); err != nil {
		t.Fatalf("backupStore.Save() error: %v", err)
	}

	repo := storage.NewRestoreRepo(db)
	return restoreAuditFixture{
		runner: NewRestoreRunner(exec, serverStore, repo, backupStore, asset.NewStore(filepath.Join(tmpDir, "assets.yaml"))),
		repo:   repo,
		job:    job,
	}
}

// onlyRun asserts exactly one attempt was recorded and returns it.
func (f restoreAuditFixture) onlyRun(t *testing.T) storage.RestoreRun {
	t.Helper()

	runs, err := f.repo.ListRuns(context.Background(), f.job.Name, 10)
	if err != nil {
		t.Fatalf("ListRuns() error: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 recorded restore attempt, got %d", len(runs))
	}
	return runs[0]
}

// TestRestoreRunner_RecordsValidationFailure verifies that a snapshot that does
// not belong to the job is still visible in the history, with every input the
// operator supplied and the stage it failed in.
func TestRestoreRunner_RecordsValidationFailure(t *testing.T) {
	f := newRestoreAuditFixture(t, &restoreAuditExecutor{})

	var buf bytes.Buffer
	record, err := f.runner.Run(context.Background(), f.job, RestoreOptions{
		SnapshotID: "snapshot-of-another-job",
		AssetID:    "web-data-files",
	}, &buf)
	if err == nil {
		t.Fatal("expected validation failure, got nil error")
	}
	if !strings.Contains(err.Error(), storage.RestoreStageValidation) {
		t.Errorf("error should name the failing stage, got: %v", err)
	}
	if record == nil {
		t.Fatal("expected a run record for a validation failure")
	}
	if record.Status != "failed" || record.FailureStage != storage.RestoreStageValidation {
		t.Errorf("record status/stage = %q/%q, want failed/validation", record.Status, record.FailureStage)
	}

	got := f.onlyRun(t)
	if got.Status != "failed" {
		t.Errorf("history status = %q, want failed", got.Status)
	}
	if got.FailureStage != storage.RestoreStageValidation {
		t.Errorf("history failure stage = %q, want validation", got.FailureStage)
	}
	if got.SnapshotID != "snapshot-of-another-job" {
		t.Errorf("history snapshot = %q, want the requested parameter", got.SnapshotID)
	}
	if got.AssetID != "web-data-files" {
		t.Errorf("history asset = %q, want web-data-files", got.AssetID)
	}
	if got.SourceServer != "vps-01" || got.TargetServer != "vps-01" {
		t.Errorf("history source/target = %q/%q, want vps-01/vps-01", got.SourceServer, got.TargetServer)
	}
	if got.TargetPath != "/" {
		t.Errorf("history target path = %q, want /", got.TargetPath)
	}
	if !strings.Contains(got.ErrorMessage, "does not belong to job") {
		t.Errorf("history error message lost the cause: %q", got.ErrorMessage)
	}
	if got.FinishedAt == nil {
		t.Error("history record has no finished_at for a validation failure")
	}
	if !strings.Contains(buf.String(), "Next:") {
		t.Errorf("console output should suggest a next step, got:\n%s", buf.String())
	}
}

func TestRestoreRunner_RecordsAssetValidationFailure(t *testing.T) {
	f := newRestoreAuditFixture(t, &restoreAuditExecutor{})

	var buf bytes.Buffer
	if _, err := f.runner.Run(context.Background(), f.job, RestoreOptions{
		SnapshotID: auditSnapshotID,
		AssetID:    "missing-asset",
	}, &buf); err == nil {
		t.Fatal("expected asset validation failure, got nil error")
	}

	got := f.onlyRun(t)
	if got.FailureStage != storage.RestoreStageValidation {
		t.Errorf("history failure stage = %q, want validation", got.FailureStage)
	}
	if got.AssetID != "missing-asset" {
		t.Errorf("history asset = %q, want missing-asset", got.AssetID)
	}
	if !strings.Contains(got.ErrorMessage, `asset "missing-asset" not found`) {
		t.Errorf("history error message = %q, want the asset lookup failure", got.ErrorMessage)
	}
}

// TestRestoreRunner_RecordsSecretValidationFailure covers the secret-resolution
// call site: an unresolvable op:// reference aborts the attempt before any
// remote work, so it must leave the same validation-stage row as the other
// pre-flight failures. OPSPULSE_OP_PATH is pinned to a path that cannot exist
// so the test never talks to a real 1Password installation.
func TestRestoreRunner_RecordsSecretValidationFailure(t *testing.T) {
	t.Setenv("OPSPULSE_OP_PATH", filepath.Join(t.TempDir(), "op-not-installed"))

	f := newRestoreAuditFixture(t, &restoreAuditExecutor{})
	job := f.job
	job.Env = map[string]string{"DB_PASSWORD": "op://Private/opspulse_web_data/password"}

	var buf bytes.Buffer
	if _, err := f.runner.Run(context.Background(), job, RestoreOptions{SnapshotID: auditSnapshotID}, &buf); err == nil {
		t.Fatal("expected secret resolution failure, got nil error")
	}

	got := f.onlyRun(t)
	if got.Status != "failed" || got.FailureStage != storage.RestoreStageValidation {
		t.Errorf("history status/stage = %q/%q, want failed/validation", got.Status, got.FailureStage)
	}
	if !strings.Contains(got.ErrorMessage, "failed to resolve secrets") {
		t.Errorf("history error message = %q, want the secret resolution failure", got.ErrorMessage)
	}
	if got.SnapshotID != auditSnapshotID {
		t.Errorf("history snapshot = %q, want the requested %q", got.SnapshotID, auditSnapshotID)
	}
}

func TestRestoreRunner_RecordsUnknownTargetServer(t *testing.T) {
	f := newRestoreAuditFixture(t, &restoreAuditExecutor{})

	var buf bytes.Buffer
	if _, err := f.runner.Run(context.Background(), f.job, RestoreOptions{
		SnapshotID:   auditSnapshotID,
		TargetServer: "ghost-vps",
		TargetPath:   "/data/web",
	}, &buf); err == nil {
		t.Fatal("expected unknown target server failure, got nil error")
	}

	got := f.onlyRun(t)
	if got.FailureStage != storage.RestoreStageValidation {
		t.Errorf("history failure stage = %q, want validation", got.FailureStage)
	}
	if got.TargetServer != "ghost-vps" {
		t.Errorf("history target server = %q, want the requested ghost-vps", got.TargetServer)
	}
	if got.TargetPath != "/data/web" {
		t.Errorf("history target path = %q, want /data/web", got.TargetPath)
	}
	if !strings.Contains(got.ErrorMessage, "not found in inventory") {
		t.Errorf("history error message = %q, want the inventory lookup failure", got.ErrorMessage)
	}
}

func TestRestoreRunner_RecordsExecutionFailure(t *testing.T) {
	f := newRestoreAuditFixture(t, &restoreAuditExecutor{failPrefix: "restore-"})

	var buf bytes.Buffer
	record, err := f.runner.Run(context.Background(), f.job, RestoreOptions{
		SnapshotID:  auditSnapshotID,
		StartPolicy: StartPolicyNoStart,
	}, &buf)
	if err == nil {
		t.Fatal("expected execution failure, got nil error")
	}
	if record == nil || record.FailureStage != storage.RestoreStageExecution {
		t.Fatalf("record failure stage = %v, want execution", record)
	}
	// A failed file restore blocks everything downstream instead of leaving the
	// steps silently unreported.
	if got := stepStatus(t, record, RestoreStepFiles); got.Status != RestoreStepFailed {
		t.Errorf("files step = %+v, want failed", got)
	}
	for _, kind := range []string{RestoreStepVolumes, RestoreStepDatabase, RestoreStepCompose} {
		if got := stepStatus(t, record, kind); got.Status != RestoreStepBlocked {
			t.Errorf("%s step = %+v, want blocked", kind, got)
		}
	}

	got := f.onlyRun(t)
	if got.Status != "failed" || got.FailureStage != storage.RestoreStageExecution {
		t.Errorf("history status/stage = %q/%q, want failed/execution", got.Status, got.FailureStage)
	}
	// The snapshot was resolved before the failure, so the history carries the
	// real ID rather than the "latest" parameter.
	if got.SnapshotID != auditSnapshotID {
		t.Errorf("history snapshot = %q, want the resolved %q", got.SnapshotID, auditSnapshotID)
	}
}

func TestRestoreRunner_RecordsPostRestoreFailure(t *testing.T) {
	f := newRestoreAuditFixture(t, &restoreAuditExecutor{failPrefix: "autostart-"})

	var buf bytes.Buffer
	record, err := f.runner.Run(context.Background(), f.job, RestoreOptions{
		SnapshotID:  auditSnapshotID,
		StartPolicy: StartPolicyStart,
	}, &buf)
	if err == nil {
		t.Fatal("expected post-restore failure, got nil error")
	}
	if record == nil || record.Status != "partial" || record.FailureStage != storage.RestoreStagePostRestore {
		t.Fatalf("record = %+v, want partial/post_restore", record)
	}
	// The file restore itself succeeded; the Compose step is the one that did
	// not, and the result has to say which.
	if got := stepStatus(t, record, RestoreStepFiles); got.Status != RestoreStepSuccess {
		t.Errorf("files step = %+v, want success", got)
	}
	if got := stepStatus(t, record, RestoreStepCompose); got.Status != RestoreStepFailed {
		t.Errorf("compose step = %+v, want failed", got)
	}
	if got := stepStatus(t, record, RestoreStepCompose); got.NextStep == "" {
		t.Error("a failed step must carry the operator's next step")
	}

	got := f.onlyRun(t)
	if got.Status != "partial" || got.FailureStage != storage.RestoreStagePostRestore {
		t.Errorf("history status/stage = %q/%q, want partial/post_restore", got.Status, got.FailureStage)
	}
}

func TestRestoreRunner_SuccessKeepsFailureStageEmpty(t *testing.T) {
	f := newRestoreAuditFixture(t, &restoreAuditExecutor{})

	var buf bytes.Buffer
	record, err := f.runner.Run(context.Background(), f.job, RestoreOptions{
		SnapshotID:  auditSnapshotID,
		StartPolicy: StartPolicyNoStart,
	}, &buf)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if record.FailureStage != "" {
		t.Errorf("record.FailureStage = %q, want empty on success", record.FailureStage)
	}

	got := f.onlyRun(t)
	if got.Status != "success" || got.FailureStage != "" {
		t.Errorf("history status/stage = %q/%q, want success/empty", got.Status, got.FailureStage)
	}
}

// TestRestoreRunner_DryRunKeepsFailureStageEmpty guards the dry-run contract: a
// successful preview records status dry-run and no failure stage.
func TestRestoreRunner_DryRunKeepsFailureStageEmpty(t *testing.T) {
	f := newRestoreAuditFixture(t, &restoreAuditExecutor{})

	var buf bytes.Buffer
	record, err := f.runner.Run(context.Background(), f.job, RestoreOptions{
		SnapshotID: auditSnapshotID,
		DryRun:     true,
	}, &buf)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if record.Status != "dry-run" || record.FailureStage != "" {
		t.Errorf("record status/stage = %q/%q, want dry-run/empty", record.Status, record.FailureStage)
	}
}
