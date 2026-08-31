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

// RestoreOptions configures how a restore operation should be executed.
type RestoreOptions struct {
	SnapshotID   string // Snapshot ID to restore from ("latest" or specific ID)
	TargetServer string // Target server name (empty = same as job's source server)
	TargetPath   string // Override restore target path (empty = original paths)
	AssetID      string // Optional asset ID for targeted single-asset restore
	DryRun       bool   // Only preview files without writing
}

// RestoreRunner coordinates the execution of restic restore operations and records metrics in SQLite.
type RestoreRunner struct {
	executor      executor.Executor
	localExecutor *executor.LocalExecutor
	serverStore   *server.Store
	restoreRepo   *storage.RestoreRepo
	backupStore   *Store
	assetStore    *asset.Store
}

// NewRestoreRunner creates a new RestoreRunner with the required dependencies.
func NewRestoreRunner(
	exec executor.Executor,
	serverStore *server.Store,
	restoreRepo *storage.RestoreRepo,
	backupStore *Store,
	assetStore *asset.Store,
) *RestoreRunner {
	return &RestoreRunner{
		executor:      exec,
		localExecutor: executor.NewLocalExecutor(),
		serverStore:   serverStore,
		restoreRepo:   restoreRepo,
		backupStore:   backupStore,
		assetStore:    assetStore,
	}
}

// Run executes a restore operation for the given backup job according to the provided options.
func (r *RestoreRunner) Run(ctx context.Context, job Job, opts RestoreOptions, consoleOut io.Writer) (*storage.RestoreRun, error) {
	if err := job.Validate(); err != nil {
		return nil, err
	}

	if consoleOut == nil {
		consoleOut = io.Discard
	}

	// 1. Resolve snapshot ID: if "latest", query the repository for the most recent snapshot
	snapshotID := opts.SnapshotID
	if snapshotID == "" || snapshotID == "latest" {
		resolvedID, err := r.resolveLatestSnapshot(ctx, job, consoleOut)
		if err != nil {
			return nil, err
		}
		snapshotID = resolvedID
	}

	// 2. Determine target server (defaults to the job's source server)
	targetServerName := opts.TargetServer
	if targetServerName == "" {
		targetServerName = job.Server
	}

	// 3. Resolve executor target
	target, err := r.resolveTarget(targetServerName)
	if err != nil {
		return nil, err
	}

	// 4. Determine target path (defaults to "/")
	targetPath := opts.TargetPath
	if targetPath == "" {
		targetPath = "/"
	}

	// 5. Resolve asset include patterns for single-asset restore
	var includePatterns []string
	if opts.AssetID != "" && r.assetStore != nil {
		a, err := r.assetStore.Get(opts.AssetID)
		if err != nil {
			return nil, fmt.Errorf("asset %q not found: %w", opts.AssetID, err)
		}
		includePatterns = append(includePatterns, a.Source)
	}

	startTime := time.Now()
	logFilePath, _ := executor.LogPathFor("restore-"+job.Name, startTime)

	// 6. Record initial 'running' state in SQLite
	runRecord := &storage.RestoreRun{
		JobName:      job.Name,
		SnapshotID:   snapshotID,
		SourceServer: job.Server,
		TargetServer: targetServerName,
		TargetPath:   targetPath,
		Status:       "running",
		LogPath:      logFilePath,
		StartedAt:    startTime,
	}
	if opts.AssetID != "" {
		runRecord.AssetID = opts.AssetID
	}

	if r.restoreRepo != nil {
		if _, err := r.restoreRepo.CreateRun(ctx, runRecord); err != nil {
			_, _ = fmt.Fprintf(consoleOut, "Warning: failed to record initial restore run in database: %v\n", err)
		}
	}

	// 7. Set up logging
	var logFile *os.File
	if logFilePath != "" {
		logFile, _ = os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if logFile != nil {
			defer func() { _ = logFile.Close() }()
			_, _ = fmt.Fprintf(logFile, "=== Restore Log for Job %q (Snapshot: %s, Target: %s) at %s ===\n\n",
				job.Name, snapshotID, targetServerName, startTime.Format(time.RFC3339))
		}
	}

	prefix := fmt.Sprintf("[restore:%s] ", job.Name)
	prefixedConsole := executor.NewPrefixedWriter(prefix, consoleOut)

	var outputBuf bytes.Buffer
	writers := []io.Writer{prefixedConsole, &outputBuf}
	if logFile != nil {
		writers = append(writers, logFile)
	}
	multiWriter := io.MultiWriter(writers...)

	// 8. Handle dry-run mode
	if opts.DryRun {
		_, _ = fmt.Fprintf(consoleOut, "--> [DRY-RUN] Previewing restore for job %q (snapshot: %s)...\n",
			job.Name, snapshotID)

		script := BuildRestoreDryRunScript(job, snapshotID, includePatterns)
		execToUse := r.executor
		if target.IsLocal {
			execToUse = r.localExecutor
		}

		_, execErr := execToUse.Execute(ctx, target, "restore-dryrun-"+job.Name, script, multiWriter)
		_ = prefixedConsole.Flush()

		finishedAt := time.Now()
		runRecord.FinishedAt = &finishedAt
		runRecord.DurationSeconds = time.Since(startTime).Seconds()

		if execErr != nil {
			runRecord.Status = "failed"
			runRecord.ErrorMessage = execErr.Error()
		} else {
			runRecord.Status = "dry-run"
		}

		r.updateRunRecord(ctx, runRecord)
		return runRecord, execErr
	}

	// 9. Execute actual restore
	_, _ = fmt.Fprintf(consoleOut, "--> Starting restore for job %q (snapshot: %s) on %s (target: %s)...\n",
		job.Name, snapshotID, targetServerName, targetPath)

	script := BuildRestoreScript(job, snapshotID, targetPath, includePatterns)
	execToUse := r.executor
	if target.IsLocal {
		execToUse = r.localExecutor
	}

	execRes, execErr := execToUse.Execute(ctx, target, "restore-"+job.Name, script, multiWriter)
	_ = prefixedConsole.Flush()

	// 10. Finalize run record
	finishedAt := time.Now()
	runRecord.FinishedAt = &finishedAt

	if execRes != nil {
		runRecord.DurationSeconds = execRes.Duration.Seconds()
	} else {
		runRecord.DurationSeconds = time.Since(startTime).Seconds()
	}

	if execErr == nil && execRes != nil && execRes.Success {
		runRecord.Status = "success"
		_, _ = fmt.Fprintf(consoleOut, "✅ Restore completed successfully (Duration: %.2fs)\n", runRecord.DurationSeconds)
	} else {
		runRecord.Status = "failed"
		if execErr != nil {
			runRecord.ErrorMessage = execErr.Error()
		} else if execRes != nil && execRes.Error != nil {
			runRecord.ErrorMessage = execRes.Error.Error()
		}
		_, _ = fmt.Fprintf(consoleOut, "❌ Restore failed: %s\n", runRecord.ErrorMessage)
	}

	r.updateRunRecord(ctx, runRecord)

	if logFile != nil {
		_, _ = fmt.Fprintf(logFile, "\n=== Finished Restore Job at %s (Status: %s) ===\n",
			finishedAt.Format(time.RFC3339), runRecord.Status)
	}

	return runRecord, execErr
}

func (r *RestoreRunner) resolveLatestSnapshot(ctx context.Context, job Job, consoleOut io.Writer) (string, error) {
	_, _ = fmt.Fprintf(consoleOut, "--> Querying latest snapshot for job %q from %s...\n", job.Name, job.Server)

	// Build a temporary runner for snapshot listing
	tmpRunner := &Runner{
		executor:      r.executor,
		localExecutor: r.localExecutor,
		serverStore:   r.serverStore,
	}

	snapshots, err := tmpRunner.ListSnapshots(ctx, job)
	if err != nil {
		return "", fmt.Errorf("failed to query snapshots for job %q: %w", job.Name, err)
	}
	if len(snapshots) == 0 {
		return "", fmt.Errorf("no snapshots found for job %q — run a backup first", job.Name)
	}

	// Snapshots are typically returned in chronological order; take the last one
	latest := snapshots[len(snapshots)-1]
	_, _ = fmt.Fprintf(consoleOut, "   Using snapshot: %s (%s)\n", latest.ShortID, latest.Time.Local().Format("2006-01-02 15:04:05"))
	return latest.ID, nil
}

func (r *RestoreRunner) resolveTarget(serverName string) (executor.Target, error) {
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

func (r *RestoreRunner) updateRunRecord(ctx context.Context, runRecord *storage.RestoreRun) {
	if r.restoreRepo != nil && runRecord.ID > 0 {
		if err := r.restoreRepo.UpdateRun(ctx, runRecord); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Warning: failed to update restore run record in database: %v\n", err)
		}
	}
}
