package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/volcano6/opspulse/internal/asset"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/secret"
	"github.com/volcano6/opspulse/internal/server"
	"github.com/volcano6/opspulse/internal/storage"
)

// ErrUntracked reports that a backup ran without an audit record because its
// SQLite run row could not be written in --allow-untracked emergency mode.
var ErrUntracked = errors.New("backup completed but was not audited")

// ErrRestoreUntracked reports that a restore ran but its SQLite audit row could
// not be persisted. It is separate from ErrUntracked so callers can distinguish
// a backup audit gap from a restore audit gap.
var ErrRestoreUntracked = errors.New("restore completed but was not audited")

// ErrRestoreAuditFailed reports that a restore failed and its failure could not
// be persisted to SQLite. It is intentionally distinct from
// ErrRestoreUntracked: the restore did not complete successfully, so callers
// must not map it to the "unaudited success" exit code.
var ErrRestoreAuditFailed = errors.New("restore failed and its audit record could not be persisted")

// ExitUntracked is the process exit code the CLI returns for ErrUntracked, so
// scripts can tell an unaudited success apart from a normal one.
const ExitUntracked = 3

// auditWriteTimeout bounds the final audit write after the operation context
// has been canceled. The write must not inherit cancellation, otherwise an
// interrupted backup/restore could never persist its terminal status, but it
// still needs a ceiling so a wedged database cannot block process shutdown.
const auditWriteTimeout = 5 * time.Second

// RunRecorder persists backup run audit records. *storage.BackupRepo is the
// production implementation; the interface exists so the fail-closed and
// untracked contracts can be exercised against deterministic persistence
// failures (SQLite lock, read-only directory, disk error) without racing a real
// database.
type RunRecorder interface {
	CreateRun(ctx context.Context, run *storage.BackupRun) (int64, error)
	UpdateRun(ctx context.Context, run *storage.BackupRun) error
}

// Runner coordinates the execution of restic backup jobs and records their status in SQLite.
type Runner struct {
	executor       executor.Executor
	localExecutor  *executor.LocalExecutor
	serverStore    *server.Store
	backupRepo     RunRecorder
	backupStore    *Store
	assetStore     *asset.Store
	allowUntracked bool
}

// auditWriteContext detaches an audit write from the operation cancellation
// while preserving context values. It is used only for terminal status writes:
// the work itself must stop when the caller cancels, but recording that stop
// must still succeed.
func auditWriteContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), auditWriteTimeout)
}

// NewRunner creates a new backup Runner.
func NewRunner(exec executor.Executor, serverStore *server.Store, backupRepo RunRecorder) *Runner {
	return NewRunnerWithStores(exec, serverStore, backupRepo, nil, nil)
}

// NewRunnerWithStores creates a backup Runner with all persistence stores configured.
func NewRunnerWithStores(
	exec executor.Executor,
	serverStore *server.Store,
	backupRepo RunRecorder,
	backupStore *Store,
	assetStore *asset.Store,
) *Runner {
	return &Runner{
		executor:      exec,
		localExecutor: executor.NewLocalExecutor(),
		serverStore:   serverStore,
		backupRepo:    backupRepo,
		backupStore:   backupStore,
		assetStore:    assetStore,
	}
}

// SetStores configures optional backup and asset stores on the Runner.
func (r *Runner) SetStores(backupStore *Store, assetStore *asset.Store) {
	r.backupStore = backupStore
	r.assetStore = assetStore
}

// SetAllowUntracked toggles the emergency mode in which a backup still runs when
// its audit record cannot be created. Every such run is marked untracked in the
// database, the terminal and every notification, and exits with ExitUntracked.
func (r *Runner) SetAllowUntracked(allow bool) {
	r.allowUntracked = allow
}

// ResolveTarget determines whether the target is local or a remote server from inventory.
func (r *Runner) ResolveTarget(serverName string) (executor.Target, error) {
	return executor.ResolveTarget(r.serverStore, serverName)
}

// Run executes a backup job on the resolved target, streams logs, and records structured metrics in SQLite.
func (r *Runner) Run(ctx context.Context, job Job, consoleOut io.Writer) (*storage.BackupRun, error) {
	// Dynamically resolve paths from referenced assets if specified
	if len(job.Assets) > 0 && r.assetStore != nil {
		resolved, err := job.ResolveAllPaths(r.assetStore)
		if err != nil {
			return nil, err
		}
		job.Paths = resolved
	}

	if err := job.Validate(); err != nil {
		return nil, err
	}

	target, err := r.ResolveTarget(job.Server)
	if err != nil {
		return nil, err
	}

	if consoleOut == nil {
		consoleOut = io.Discard
	}

	startTime := time.Now()
	runRecord, err := r.beginRun(ctx, job, consoleOut, startTime)
	if err != nil {
		return nil, err
	}

	return r.executeRun(ctx, job, target, consoleOut, runRecord, startTime)
}

// beginRun writes the initial 'running' audit row for a job and returns it.
//
// It fails closed: when the row cannot be created the job is refused before any
// remote work happens, because a backup that cannot be recorded cannot be
// trusted. In --allow-untracked emergency mode the run proceeds instead and the
// returned record is flagged Untracked so the caller, the persisted history and
// every notification can mark the gap.
func (r *Runner) beginRun(ctx context.Context, job Job, consoleOut io.Writer, startTime time.Time) (*storage.BackupRun, error) {
	logFilePath, logErr := executor.LogPathFor("backup-"+job.Name, startTime)
	if logErr != nil {
		_, _ = fmt.Fprintf(consoleOut, "Warning: failed to initialize log file path: %v (running without log file)\n", logErr)
	}

	runRecord := &storage.BackupRun{
		JobName:    job.Name,
		ServerName: job.Server,
		Status:     "running",
		LogPath:    logFilePath,
		StartedAt:  startTime,
	}

	if r.backupRepo == nil {
		return runRecord, nil
	}

	if _, err := r.backupRepo.CreateRun(ctx, runRecord); err != nil {
		if !r.allowUntracked {
			return nil, fmt.Errorf("refusing to run backup %q: its audit record could not be created in SQLite (%w); fix the database or re-run with --allow-untracked to proceed and mark the run untracked", job.Name, err)
		}
		runRecord.Untracked = true
		_, _ = fmt.Fprintf(consoleOut, "Warning: cannot create the audit record for backup %q: %v\n", job.Name, err)
		_, _ = fmt.Fprintf(consoleOut, "Warning: --allow-untracked is set; running anyway. This run is UNTRACKED (exit code %d).\n", ExitUntracked)
	}

	return runRecord, nil
}

// executeRun performs the actual backup against an already resolved target,
// using the audit record created by beginRun.
func (r *Runner) executeRun(
	ctx context.Context,
	job Job,
	target executor.Target,
	consoleOut io.Writer,
	runRecord *storage.BackupRun,
	startTime time.Time,
) (*storage.BackupRun, error) {
	logFilePath := runRecord.LogPath

	// Set up logging (Console with prefix + Log File + In-Memory Buffer for JSON parsing)
	var logFile *os.File
	if logFilePath != "" {
		var openErr error
		logFile, openErr = os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if openErr != nil {
			_, _ = fmt.Fprintf(consoleOut, "Warning: failed to open log file %s: %v (running without log file)\n", logFilePath, openErr)
		} else {
			defer func() { _ = logFile.Close() }()
			_, _ = fmt.Fprintf(logFile, "=== Backup Log for Job %q (Server: %s) at %s ===\n\n",
				job.Name, job.Server, startTime.Format(time.RFC3339))
		}
	}

	prefix := fmt.Sprintf("[%s:%s] ", job.Server, job.Name)
	prefixedConsole := executor.NewPrefixedWriter(prefix, consoleOut)

	var outputBuf bytes.Buffer
	writers := []io.Writer{prefixedConsole, &outputBuf}
	if logFile != nil {
		writers = append(writers, logFile)
	}
	multiWriter := io.MultiWriter(writers...)

	_, _ = fmt.Fprintf(consoleOut, "--> Starting backup job %q on %s (Backend: %s)...\n",
		job.Name, job.Server, job.Backend)

	// Resolve op:// secrets in environment variables
	resolvedEnv, err := secret.NewResolver().ResolveMap(ctx, job.Env)
	if err != nil {
		return r.finalizeRun(ctx, runRecord, false, 0, fmt.Errorf("failed to resolve secrets: %w", err), startTime, logFile, prefixedConsole)
	}
	job.Env = resolvedEnv

	// Build script and execute via appropriate executor
	script, err := BuildBackupScript(job)
	if err != nil {
		return r.finalizeRun(ctx, runRecord, false, 0, err, startTime, logFile, prefixedConsole)
	}

	execToUse := r.executor
	if target.IsLocal {
		execToUse = r.localExecutor
	} else if sshExec, ok := r.executor.(*executor.SSHExecutor); ok {
		var jobWarnWriter io.Writer = prefixedConsole
		if logFile != nil {
			jobWarnWriter = io.MultiWriter(prefixedConsole, logFile)
		}
		scopedExec := *sshExec
		scopedExec.WarnWriter = jobWarnWriter
		execToUse = &scopedExec
	}

	execRes, execErr := execToUse.Execute(ctx, target, "backup-"+job.Name, script, multiWriter)
	_ = prefixedConsole.Flush()

	// Parse restic summary from execution output
	outputStr := outputBuf.String()
	summary := ParseResticSummary(outputStr)

	if summary != nil {
		runRecord.SnapshotID = summary.SnapshotID
		runRecord.FilesNew = summary.FilesNew
		runRecord.FilesChanged = summary.FilesChanged
		runRecord.FilesUnmodified = summary.FilesUnmodified
		runRecord.DataAddedBytes = summary.DataAdded
		runRecord.TotalBytes = summary.TotalBytes
		if summary.TotalDuration > 0 {
			runRecord.DurationSeconds = summary.TotalDuration
		}
	}

	success := (execErr == nil && execRes != nil && execRes.Success)
	var finalErr error
	if execErr != nil {
		finalErr = execErr
	} else if execRes != nil && execRes.Error != nil {
		finalErr = execRes.Error
	}

	var duration time.Duration
	if execRes != nil {
		duration = execRes.Duration
	} else {
		duration = time.Since(startTime)
	}

	return r.finalizeRun(ctx, runRecord, success, duration, finalErr, startTime, logFile, prefixedConsole)
}

func (r *Runner) finalizeRun(
	ctx context.Context,
	runRecord *storage.BackupRun,
	success bool,
	duration time.Duration,
	err error,
	_ time.Time,
	logFile *os.File,
	console *executor.PrefixedWriter,
) (*storage.BackupRun, error) {
	finishedAt := time.Now()
	runRecord.FinishedAt = &finishedAt

	if runRecord.DurationSeconds <= 0 {
		runRecord.DurationSeconds = duration.Seconds()
	}

	if success {
		runRecord.Status = "success"
		runRecord.ErrorMessage = ""
	} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		runRecord.Status = "canceled"
		if err != nil {
			runRecord.ErrorMessage = err.Error()
		}
	} else {
		runRecord.Status = "failed"
		if err != nil {
			runRecord.ErrorMessage = err.Error()
		}
	}

	// Persist the outcome. A run whose initial row was never created (untracked
	// emergency mode) gets one last CreateRun attempt: a transient failure such
	// as a SQLite lock may have cleared, and recording the audit gap is exactly
	// what this mode must not silently drop.
	//
	// A failure here is itself an audit gap: the row would keep its stale
	// 'running' status while the caller reports a normal success. Mark the run
	// untracked so the terminal and every notification say so, and surface
	// ErrUntracked when the backup itself succeeded - that is the case where the
	// caller would otherwise exit 0 on an unaudited success.
	var persistErr error
	persistCtx, cancelPersist := auditWriteContext(ctx)
	defer cancelPersist()
	switch {
	case r.backupRepo == nil:
	case runRecord.ID > 0:
		persistErr = r.backupRepo.UpdateRun(persistCtx, runRecord)
	default:
		_, persistErr = r.backupRepo.CreateRun(persistCtx, runRecord)
	}
	if persistErr != nil {
		// In --allow-untracked mode the caller already accepted that this run
		// would not be audited, so the flag alone carries the gap. Only a run that
		// was audited until now turns an ordinary success into an unaudited one.
		newlyUntracked := !runRecord.Untracked
		runRecord.Untracked = true
		_, _ = fmt.Fprintf(os.Stderr, "Warning: backup run %q could not be recorded in SQLite: %v\n", runRecord.JobName, persistErr)
		if newlyUntracked && (err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			err = fmt.Errorf("backup run %q completed but its audit record could not be persisted: %w", runRecord.JobName, ErrUntracked)
		}
	}

	if logFile != nil {
		_, _ = fmt.Fprintf(logFile, "\n=== Finished Backup Job at %s (Status: %s) ===\n",
			finishedAt.Format(time.RFC3339), runRecord.Status)
	}

	if runRecord.Untracked {
		_, _ = fmt.Fprintf(console, "\n!!! UNTRACKED: this run has no audit record in SQLite; `ops backup status`/`history` will not list it as a normal run. !!!\n")
	}

	_ = console.Flush()
	return runRecord, err
}

// ListSnapshots queries and parses all snapshots for a given backup job.
func (r *Runner) ListSnapshots(ctx context.Context, job Job) ([]Snapshot, error) {
	target, err := r.ResolveTarget(job.Server)
	if err != nil {
		return nil, err
	}

	resolvedEnv, err := secret.NewResolver().ResolveMap(ctx, job.Env)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve secrets: %w", err)
	}
	job.Env = resolvedEnv

	script := BuildSnapshotsScript(job)
	var outputBuf bytes.Buffer

	execToUse := r.executor
	if target.IsLocal {
		execToUse = r.localExecutor
	} else if sshExec, ok := r.executor.(*executor.SSHExecutor); ok {
		scopedExec := *sshExec
		if scopedExec.WarnWriter == nil {
			scopedExec.WarnWriter = os.Stderr
		}
		execToUse = &scopedExec
	}

	res, err := execToUse.Execute(ctx, target, "snapshots-"+job.Name, script, &outputBuf)
	if err != nil {
		return nil, fmt.Errorf("failed to query snapshots: %w", err)
	}
	if !res.Success {
		if res.Error != nil {
			return nil, fmt.Errorf("snapshots command failed: %w", res.Error)
		}
		return nil, fmt.Errorf("snapshots command failed: command exited with code %d", res.ExitCode)
	}

	return ParseSnapshotsJSON(outputBuf.String())
}
