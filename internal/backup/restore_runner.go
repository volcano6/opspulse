package backup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/volcano6/opspulse/internal/asset"
	"github.com/volcano6/opspulse/internal/docker"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/secret"
	"github.com/volcano6/opspulse/internal/server"
	"github.com/volcano6/opspulse/internal/shellquote"
	"github.com/volcano6/opspulse/internal/storage"
)

// RestoreOptions configures how a restore operation should be executed.
type RestoreOptions struct {
	SnapshotID   string // Snapshot ID to restore from ("latest" or specific ID)
	TargetServer string // Target server name (empty = same as job's source server)
	TargetPath   string // Override restore target path (empty = original paths)
	AssetID      string // Optional asset ID for targeted single-asset restore
	DryRun       bool   // Only preview files without writing
	NoStart      bool   // If true, do not automatically start containers or import database
	AliasName    string // Optional rename alias for container/service
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

	// 0. Resolve op:// secrets in environment variables
	resolvedEnv, err := secret.NewResolver().ResolveMap(ctx, job.Env)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve secrets: %w", err)
	}
	job.Env = resolvedEnv

	// 1. Resolve snapshot ID: if "latest", query the repository for the most recent snapshot
	snapshotID := opts.SnapshotID
	if snapshotID == "" || snapshotID == "latest" {
		resolvedID, err := r.resolveLatestSnapshot(ctx, job, consoleOut)
		if err != nil {
			return nil, err
		}
		snapshotID = resolvedID
	} else {
		if err := r.verifySnapshotBelongsToJob(ctx, job, snapshotID); err != nil {
			return nil, err
		}
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
	logFilePath, logErr := executor.LogPathFor("restore-"+job.Name, startTime)
	if logErr != nil {
		_, _ = fmt.Fprintf(consoleOut, "Warning: failed to initialize log file path: %v (running without log file)\n", logErr)
	}

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
		var openErr error
		logFile, openErr = os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if openErr != nil {
			_, _ = fmt.Fprintf(consoleOut, "Warning: failed to open log file %s: %v (running without log file)\n", logFilePath, openErr)
		} else {
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
		} else if sshExec, ok := r.executor.(*executor.SSHExecutor); ok {
			scopedExec := *sshExec
			scopedExec.WarnWriter = multiWriter
			execToUse = &scopedExec
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
	} else if sshExec, ok := r.executor.(*executor.SSHExecutor); ok {
		scopedExec := *sshExec
		scopedExec.WarnWriter = multiWriter
		execToUse = &scopedExec
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

		// Auto-start containers by default unless explicitly suppressed via --no-start
		if !opts.NoStart && !opts.DryRun {
			if startErr := r.autoStartContainers(ctx, target, job, opts, consoleOut); startErr != nil {
				runRecord.Status = "partial"
				runRecord.ErrorMessage = fmt.Sprintf("restore completed but container auto-start failed: %v", startErr)
				_, _ = fmt.Fprintf(consoleOut, "⚠️ Restore completed with partial status: %s\n", runRecord.ErrorMessage)
				execErr = startErr
			}
		}
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

	// Sort snapshots strictly ascending by Time
	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].Time.Before(snapshots[j].Time)
	})

	latest := snapshots[len(snapshots)-1]
	_, _ = fmt.Fprintf(consoleOut, "   Using snapshot: %s (%s)\n", latest.ShortID, latest.Time.Local().Format("2006-01-02 15:04:05"))
	return latest.ID, nil
}

func (r *RestoreRunner) verifySnapshotBelongsToJob(ctx context.Context, job Job, snapshotID string) error {
	tmpRunner := &Runner{
		executor:      r.executor,
		localExecutor: r.localExecutor,
		serverStore:   r.serverStore,
	}
	snapshots, err := tmpRunner.ListSnapshots(ctx, job)
	if err != nil {
		return fmt.Errorf("failed to query snapshots to verify snapshot %q: %w", snapshotID, err)
	}
	for _, s := range snapshots {
		if s.ID == snapshotID || s.ShortID == snapshotID || strings.HasPrefix(s.ID, snapshotID) {
			return nil
		}
	}
	return fmt.Errorf("snapshot %q does not belong to job %q", snapshotID, job.Name)
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

// RestoredPath maps an original absolute path to its location under targetRoot when restic restores with --target.
func RestoredPath(targetRoot, originalAbsolutePath string) string {
	if targetRoot == "" || targetRoot == "/" {
		return path.Clean(originalAbsolutePath)
	}
	targetClean := path.Clean(targetRoot)
	origClean := path.Clean(originalAbsolutePath)
	trimmed := strings.TrimPrefix(origClean, "/")
	return path.Join(targetClean, trimmed)
}

func (r *RestoreRunner) autoStartContainers(
	ctx context.Context,
	target executor.Target,
	job Job,
	opts RestoreOptions,
	consoleOut io.Writer,
) error {
	_, _ = fmt.Fprintf(consoleOut, "--> Inspecting restored files for container services (auto-start enabled)...\n")

	execToUse := r.executor
	if target.IsLocal {
		execToUse = r.localExecutor
	}

	candidateDirs := []string{fmt.Sprintf("/var/lib/opspulse/containers/%s", job.Name)}
	for _, p := range job.Paths {
		candidateDirs = append(candidateDirs, p)
	}

	var (
		manifest           *docker.ContainerManifest
		restoredProjectDir string
	)

	for _, cand := range candidateDirs {
		rDir := RestoredPath(opts.TargetPath, cand)
		mPath := path.Join(rDir, docker.ManifestFileName)
		readManifestScript := fmt.Sprintf("cat %s 2>/dev/null || true", shellquote.Quote(mPath))
		var mBuf bytes.Buffer
		res, err := execToUse.Execute(ctx, target, "read-manifest-"+job.Name, readManifestScript, &mBuf)
		if err == nil && res != nil && res.Success && len(bytes.TrimSpace(mBuf.Bytes())) > 0 {
			m, parseErr := docker.UnmarshalManifest(mBuf.Bytes())
			if parseErr == nil && m != nil {
				manifest = m
				restoredProjectDir = rDir
				if strings.EqualFold(m.App, job.Name) || (opts.AliasName != "" && strings.EqualFold(m.App, opts.AliasName)) {
					break
				}
			}
		}
	}

	if manifest != nil {
		_, _ = fmt.Fprintf(consoleOut, "  -> Detected package manifest for app %q. Running manifest-driven restore...\n", manifest.App)

		// A. Check external mounts
		for _, em := range manifest.ExternalMounts {
			if em.Reason == "system_mount" || strings.HasPrefix(em.Source, "/var/run") || strings.HasPrefix(em.Source, "/dev") {
				checkScript := fmt.Sprintf("test -e %s || echo 'MISSING'", shellquote.Quote(em.Source))
				var checkBuf bytes.Buffer
				_, _ = execToUse.Execute(ctx, target, "check-mount", checkScript, &checkBuf)
				if strings.Contains(checkBuf.String(), "MISSING") {
					_, _ = fmt.Fprintf(consoleOut, "  ⚠️ Warning: external host mount %s is missing on target %s\n", em.Source, target.Name)
				}
			}
		}

		// B. Restore named volumes
		for _, v := range manifest.Volumes {
			if v.Archive != "" {
				archivePath := path.Join(restoredProjectDir, v.Archive)
				_, _ = fmt.Fprintf(consoleOut, "  -> Restoring volume %q from %s...\n", v.OriginalName, v.Archive)
				volScript := docker.BuildVolumeImportScript(v.OriginalName, archivePath)
				vRes, vErr := execToUse.Execute(ctx, target, "restore-vol-"+v.OriginalName, volScript, consoleOut)
				if vErr != nil || (vRes != nil && !vRes.Success) {
					return fmt.Errorf("failed to restore volume %q: %v", v.OriginalName, vErr)
				}
			}
		}

		// C. Start Compose
		projectName := manifest.App
		if opts.AliasName != "" {
			projectName = opts.AliasName
		}
		composeFile := manifest.ComposeFile
		if composeFile == "" {
			composeFile = "compose.yaml"
		}
		startComposeScript := fmt.Sprintf(`if docker compose version >/dev/null 2>&1; then
  COMPOSE="docker compose"
elif command -v docker-compose >/dev/null 2>&1; then
  COMPOSE="docker-compose"
else
  echo "Error: docker compose not found" >&2
  exit 127
fi
cd %s
export COMPOSE_PROJECT_NAME=%s
if [ -f %s ]; then
  $COMPOSE -f %s up -d
else
  $COMPOSE up -d
fi
`, shellquote.Quote(restoredProjectDir), shellquote.Quote(projectName), shellquote.Quote(composeFile), shellquote.Quote(composeFile))

		cRes, cErr := execToUse.Execute(ctx, target, "compose-up-"+job.Name, startComposeScript, consoleOut)
		if cErr != nil || (cRes != nil && !cRes.Success) {
			return fmt.Errorf("compose up failed for %q: %v", projectName, cErr)
		}
		// D. Import Database
		if manifest.Database != nil && manifest.Database.Dump != "" {
			dumpPath := path.Join(restoredProjectDir, manifest.Database.Dump)
			targetContainer := manifest.Database.Container
			if opts.AliasName != "" && targetContainer == manifest.App {
				targetContainer = opts.AliasName
			}
			_, _ = fmt.Fprintf(consoleOut, "  -> Importing database dump from %s into %s (%s)...\n",
				dumpPath, targetContainer, manifest.Database.Engine)
			importScript, impErr := docker.BuildImportScript(manifest.Database.Engine, targetContainer, dumpPath)
			if impErr != nil {
				return fmt.Errorf("failed to build import script: %w", impErr)
			}
			impRes, impExecErr := execToUse.Execute(ctx, target, "import-db-"+job.Name, importScript, consoleOut)
			if impExecErr != nil {
				return fmt.Errorf("database import failed for container %q: %w", targetContainer, impExecErr)
			}
			if impRes != nil && !impRes.Success {
				if impRes.Error != nil {
					return fmt.Errorf("database import failed for container %q: %w", targetContainer, impRes.Error)
				}
				return fmt.Errorf("database import failed for container %q: command exited with code %d", targetContainer, impRes.ExitCode)
			}
		}

		_, _ = fmt.Fprintf(consoleOut, "🚀 Container app %q successfully restored and running on %s!\n", projectName, target.Name)
		return nil
	}

	// Fallback to auto-start script for non-manifest backups
	var fallbackCandidateDirs []string
	if opts.TargetPath != "" && opts.TargetPath != "/" {
		fallbackCandidateDirs = append(fallbackCandidateDirs, opts.TargetPath)
	}
	fallbackCandidateDirs = append(fallbackCandidateDirs, restoredProjectDir)
	for _, p := range job.Paths {
		fallbackCandidateDirs = append(fallbackCandidateDirs, RestoredPath(opts.TargetPath, p))
	}

	autoOpts := docker.AutoStartOptions{
		ComposeDirs: dedupPaths(fallbackCandidateDirs),
		AliasName:   opts.AliasName,
	}

	if r.assetStore != nil {
		targetAssetID := opts.AssetID
		if targetAssetID == "" {
			targetAssetID = job.Name
		}
		if a, err := r.assetStore.Get(targetAssetID); err == nil && a.Type == asset.TypeDatabase {
			autoOpts.DatabaseEngine = a.Engine
			autoOpts.DatabaseContainer = a.Container
			if autoOpts.DatabaseContainer == "" {
				autoOpts.DatabaseContainer = job.Name
			}
			autoOpts.DatabaseDump = RestoredPath(opts.TargetPath, fmt.Sprintf("/tmp/opspulse-dumps/%s", docker.DumpFileName(job.Name)))
		}
	}

	script := docker.BuildAutoStartScript(autoOpts)
	startRes, startErr := execToUse.Execute(ctx, target, "autostart-"+job.Name, script, consoleOut)
	if startErr != nil {
		return fmt.Errorf("container auto-start failed: %w", startErr)
	}
	if startRes != nil && !startRes.Success {
		if startRes.Error != nil {
			return fmt.Errorf("container auto-start failed: %w", startRes.Error)
		}
		return fmt.Errorf("container auto-start failed: command exited with code %d", startRes.ExitCode)
	}
	_, _ = fmt.Fprintf(consoleOut, "🚀 Container services are up and running on %s!\n", target.Name)
	return nil
}
