package backup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/volcano6/opspulse/internal/asset"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/server"
	"github.com/volcano6/opspulse/internal/storage"
)

// Runner coordinates the execution of restic backup jobs and records their status in SQLite.
type Runner struct {
	executor      executor.Executor
	localExecutor *executor.LocalExecutor
	serverStore   *server.Store
	backupRepo    *storage.BackupRepo
	backupStore   *Store
	assetStore    *asset.Store
}

// NewRunner creates a new backup Runner.
func NewRunner(exec executor.Executor, serverStore *server.Store, backupRepo *storage.BackupRepo) *Runner {
	return NewRunnerWithStores(exec, serverStore, backupRepo, nil, nil)
}

// NewRunnerWithStores creates a backup Runner with all persistence stores configured.
func NewRunnerWithStores(
	exec executor.Executor,
	serverStore *server.Store,
	backupRepo *storage.BackupRepo,
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

// ResolveTarget determines whether the target is local or a remote server from inventory.
func (r *Runner) ResolveTarget(serverName string) (executor.Target, error) {
	if serverName == "local" || serverName == "" {
		return executor.NewLocalTarget(), nil
	}

	if r.serverStore == nil {
		return executor.Target{}, fmt.Errorf("serverStore is nil, cannot resolve %q", serverName)
	}

	srv, err := r.serverStore.Get(serverName)
	if err != nil {
		return executor.Target{}, fmt.Errorf("server %q not found in inventory: %w", serverName, err)
	}

	return executor.NewServerTarget(*srv), nil
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
	logFilePath, _ := executor.LogPathFor("backup-"+job.Name, startTime)

	// 1. Record initial 'running' state in SQLite
	runRecord := &storage.BackupRun{
		JobName:    job.Name,
		ServerName: job.Server,
		Status:     "running",
		LogPath:    logFilePath,
		StartedAt:  startTime,
	}

	if r.backupRepo != nil {
		if _, err := r.backupRepo.CreateRun(ctx, runRecord); err != nil {
			_, _ = fmt.Fprintf(consoleOut, "Warning: failed to record initial backup run in database: %v\n", err)
		}
	}

	// 2. Set up logging (Console with prefix + Log File + In-Memory Buffer for JSON parsing)
	var logFile *os.File
	if logFilePath != "" {
		logFile, _ = os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if logFile != nil {
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

	// 3. Build script and execute via appropriate executor
	script, err := BuildBackupScript(job)
	if err != nil {
		return r.finalizeRun(ctx, runRecord, false, 0, err, startTime, logFile, prefixedConsole)
	}

	execToUse := r.executor
	if target.IsLocal {
		execToUse = r.localExecutor
	}

	execRes, execErr := execToUse.Execute(ctx, target, "backup-"+job.Name, script, multiWriter)
	_ = prefixedConsole.Flush()

	// 4. Parse restic summary from execution output
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
	} else {
		runRecord.Status = "failed"
		if err != nil {
			runRecord.ErrorMessage = err.Error()
		}
	}

	// Update SQLite record
	if r.backupRepo != nil && runRecord.ID > 0 {
		if updateErr := r.backupRepo.UpdateRun(ctx, runRecord); updateErr != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Warning: failed to update backup run record in database: %v\n", updateErr)
		}
	}

	if logFile != nil {
		_, _ = fmt.Fprintf(logFile, "\n=== Finished Backup Job at %s (Status: %s) ===\n",
			finishedAt.Format(time.RFC3339), runRecord.Status)
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

	script := BuildSnapshotsScript(job)
	var outputBuf bytes.Buffer

	execToUse := r.executor
	if target.IsLocal {
		execToUse = r.localExecutor
	}

	res, err := execToUse.Execute(ctx, target, "snapshots-"+job.Name, script, &outputBuf)
	if err != nil {
		return nil, fmt.Errorf("failed to query snapshots: %w", err)
	}
	if !res.Success {
		return nil, fmt.Errorf("snapshots command failed: %v", res.Error)
	}

	return ParseSnapshotsJSON(outputBuf.String())
}
