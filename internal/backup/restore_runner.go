package backup

import (
	"bytes"
	"context"
	"errors"
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

// StartPolicy decides whether a restore starts container services after the
// files are in place.
type StartPolicy string

const (
	// StartPolicyAuto is the default and is scenario-aware: a cross-machine
	// restore driven by a matching container manifest starts the services,
	// while a plain file restore and a same-host restore only put the files
	// back. Auto-starting a service nobody asked for is the mistake this
	// policy exists to prevent.
	StartPolicyAuto StartPolicy = "auto"
	// StartPolicyStart forces the container steps even for a scenario that
	// would not start them by default (plain file restore, same-host restore).
	StartPolicyStart StartPolicy = "start"
	// StartPolicyNoStart restores files only: no named volume import, no
	// database import and no Compose start.
	StartPolicyNoStart StartPolicy = "no-start"
)

// RestoreOptions configures how a restore operation should be executed.
type RestoreOptions struct {
	SnapshotID   string // Snapshot ID to restore from ("latest" or specific ID)
	TargetServer string // Target server name (empty = same as job's source server)
	TargetPath   string // Override restore target path (empty = original paths)
	AssetID      string // Optional asset ID for targeted single-asset restore
	DryRun       bool   // Only preview files without writing
	StartPolicy  StartPolicy
	AliasName    string // Optional rename alias for container/service
}

// effectiveStartPolicy resolves the zero value to the scenario-aware default.
func (o RestoreOptions) effectiveStartPolicy() StartPolicy {
	if o.StartPolicy == "" {
		return StartPolicyAuto
	}
	return o.StartPolicy
}

// RestoreRunner coordinates the execution of restic restore operations and records metrics in SQLite.
type RestoreRunner struct {
	executor      executor.Executor
	localExecutor *executor.LocalExecutor
	serverStore   *server.Store
	restoreRepo   RestoreRecorder
	backupStore   *Store
	assetStore    *asset.Store
}

// RestoreRecorder persists restore audit records. *storage.RestoreRepo is the
// production implementation; the interface lets tests exercise persistence
// failures deterministically.
type RestoreRecorder interface {
	CreateRun(ctx context.Context, run *storage.RestoreRun) (int64, error)
	UpdateRun(ctx context.Context, run *storage.RestoreRun) error
}

// NewRestoreRunner creates a new RestoreRunner with the required dependencies.
func NewRestoreRunner(
	exec executor.Executor,
	serverStore *server.Store,
	restoreRepo RestoreRecorder,
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
func (r *RestoreRunner) Run(ctx context.Context, job Job, opts RestoreOptions, consoleOut io.Writer) (*RestoreResult, error) {
	if consoleOut == nil {
		consoleOut = io.Discard
	}

	startTime := time.Now()
	logFilePath, logErr := executor.LogPathFor("restore-"+job.Name, startTime)
	if logErr != nil {
		_, _ = fmt.Fprintf(consoleOut, "Warning: failed to initialize log file path: %v (running without log file)\n", logErr)
	}

	// Target server (defaults to the job's source server) and target path
	// (defaults to "/") are resolved before recording: the row has to describe
	// the attempt even when resolving the target server itself fails.
	targetServerName := opts.TargetServer
	if targetServerName == "" {
		targetServerName = job.Server
	}
	targetPath := opts.TargetPath
	if targetPath == "" {
		targetPath = "/"
	}
	requestedSnapshot := opts.SnapshotID
	if requestedSnapshot == "" {
		requestedSnapshot = "latest"
	}

	// Record the attempt before validating anything: a snapshot, target, asset
	// or secret failure must still show up in `ops restore history`.
	runRecord := &storage.RestoreRun{
		JobName:      job.Name,
		AssetID:      opts.AssetID,
		SnapshotID:   requestedSnapshot,
		SourceServer: job.Server,
		TargetServer: targetServerName,
		TargetPath:   targetPath,
		Status:       "running",
		StartedAt:    startTime,
	}
	if r.restoreRepo != nil {
		if _, err := r.restoreRepo.CreateRun(ctx, runRecord); err != nil {
			return nil, fmt.Errorf("refusing to run restore %q: its audit record could not be created in SQLite: %w", job.Name, err)
		}
	}

	// The per-step report is built alongside the run record: the record keeps
	// the backward-compatible aggregate status, the report carries the detail.
	result := newRestoreReport(runRecord)

	// 0. Validate the job and the container alias. Both reach generated shell
	// scripts, so they are checked before anything else runs.
	if err := job.Validate(); err != nil {
		return r.failRun(ctx, result, storage.RestoreStageValidation, err, consoleOut)
	}
	if opts.AliasName != "" {
		alias := strings.TrimSpace(opts.AliasName)
		if err := validateContainerAlias(alias); err != nil {
			return r.failRun(ctx, result, storage.RestoreStageValidation, err, consoleOut)
		}
		opts.AliasName = alias
	}

	// 1. Resolve op:// secrets in environment variables
	resolvedEnv, err := secret.NewResolver().ResolveMap(ctx, job.Env)
	if err != nil {
		return r.failRun(ctx, result, storage.RestoreStageValidation, fmt.Errorf("failed to resolve secrets: %w", err), consoleOut)
	}
	job.Env = resolvedEnv

	// 2. Resolve snapshot ID: if "latest", query the repository for the most recent snapshot
	snapshotID := opts.SnapshotID
	if snapshotID == "" || snapshotID == "latest" {
		resolvedID, err := r.resolveLatestSnapshot(ctx, job, consoleOut)
		if err != nil {
			return r.failRun(ctx, result, storage.RestoreStageValidation, err, consoleOut)
		}
		snapshotID = resolvedID
	} else {
		if err := r.verifySnapshotBelongsToJob(ctx, job, snapshotID); err != nil {
			return r.failRun(ctx, result, storage.RestoreStageValidation, err, consoleOut)
		}
	}
	runRecord.SnapshotID = snapshotID

	// 3. Resolve executor target
	target, err := r.resolveTarget(targetServerName)
	if err != nil {
		return r.failRun(ctx, result, storage.RestoreStageValidation, err, consoleOut)
	}

	// 4. Resolve asset include patterns for single-asset restore
	var includePatterns []string
	if opts.AssetID != "" && r.assetStore != nil {
		a, err := r.assetStore.Get(opts.AssetID)
		if err != nil {
			return r.failRun(ctx, result, storage.RestoreStageValidation, fmt.Errorf("asset %q not found: %w", opts.AssetID, err), consoleOut)
		}
		includePatterns = append(includePatterns, a.Source)
	}

	// 5. Set up logging
	var logFile *os.File
	if logFilePath != "" {
		var openErr error
		logFile, openErr = os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if openErr != nil {
			_, _ = fmt.Fprintf(consoleOut, "Warning: failed to open log file %s: %v (running without log file)\n", logFilePath, openErr)
		} else {
			defer func() { _ = logFile.Close() }()
			runRecord.LogPath = logFilePath
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

	// 6. Handle dry-run mode
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
			runRecord.FailureStage = storage.RestoreStageExecution
			runRecord.ErrorMessage = execErr.Error()
			result.setStep(RestoreStepFiles, RestoreStepFailed, execErr.Error(), "")
			result.blockSteps("预演失败，未执行",
				RestoreStepVolumes, RestoreStepDatabase, RestoreStepCompose)
		} else {
			runRecord.Status = "dry-run"
			result.setStep(RestoreStepFiles, RestoreStepSuccess,
				fmt.Sprintf("预演完成：列出快照 %s 中将被还原的文件，未写入任何数据", shortSnapshotID(snapshotID)), "")
			result.setStep(RestoreStepVolumes, RestoreStepSkipped, "预演模式不导入命名卷", "")
			result.setStep(RestoreStepDatabase, RestoreStepSkipped, "预演模式不导入数据库", "")
			result.setStep(RestoreStepCompose, RestoreStepSkipped, "预演模式不启动服务", "")
		}

		writeRestoreResult(consoleOut, result, runRecord.DurationSeconds)
		auditErr := r.updateRunRecord(ctx, runRecord, consoleOut)
		if execErr != nil {
			if auditErr != nil {
				return result, errors.Join(
					execErr,
					fmt.Errorf("%w: %v", ErrRestoreAuditFailed, auditErr),
				)
			}
			return result, execErr
		}
		if auditErr != nil {
			return result, fmt.Errorf(
				"restore dry-run for %q completed but its audit record could not be persisted: %w",
				runRecord.JobName,
				errors.Join(ErrRestoreUntracked, auditErr),
			)
		}
		return result, nil
	}

	// 7. Execute actual restore
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

	// 8. Finalize run record
	finishedAt := time.Now()
	runRecord.FinishedAt = &finishedAt

	if execRes != nil {
		runRecord.DurationSeconds = execRes.Duration.Seconds()
	} else {
		runRecord.DurationSeconds = time.Since(startTime).Seconds()
	}

	if execErr == nil && execRes != nil && execRes.Success {
		result.setStep(RestoreStepFiles, RestoreStepSuccess,
			fmt.Sprintf("快照 %s 已还原到 %s:%s", shortSnapshotID(snapshotID), targetServerName, targetPath), "")
		_, _ = fmt.Fprintf(consoleOut, "✅ Restore completed successfully (Duration: %.2fs)\n", runRecord.DurationSeconds)

		// The container steps are scenario-aware: see runPostRestoreSteps.
		if startErr := r.runPostRestoreSteps(ctx, target, job, opts, targetServerName, result, consoleOut); startErr != nil {
			runRecord.FailureStage = storage.RestoreStagePostRestore
			runRecord.ErrorMessage = fmt.Sprintf("restore completed but container auto-start failed: %v", startErr)
			_, _ = fmt.Fprintf(consoleOut, "⚠️ Restore completed with partial status (%s): %s\n", storage.RestoreStagePostRestore, runRecord.ErrorMessage)
			if hint := restoreStageHint(storage.RestoreStagePostRestore); hint != "" {
				_, _ = fmt.Fprintf(consoleOut, "   Next: %s\n", hint)
			}
			execErr = startErr
		}
		runRecord.Status = result.aggregateStatus(false)
	} else {
		runRecord.Status = "failed"
		runRecord.FailureStage = storage.RestoreStageExecution
		if execErr != nil {
			runRecord.ErrorMessage = execErr.Error()
		} else if execRes != nil && execRes.Error != nil {
			runRecord.ErrorMessage = execRes.Error.Error()
		}
		result.setStep(RestoreStepFiles, RestoreStepFailed, runRecord.ErrorMessage,
			"检查日志文件里失败的 restic 命令；`--dry-run` 可列出将被还原的文件")
		result.blockSteps("文件恢复失败，未执行",
			RestoreStepVolumes, RestoreStepDatabase, RestoreStepCompose)
		_, _ = fmt.Fprintf(consoleOut, "❌ Restore failed during %s: %s\n", storage.RestoreStageExecution, runRecord.ErrorMessage)
		if hint := restoreStageHint(storage.RestoreStageExecution); hint != "" {
			_, _ = fmt.Fprintf(consoleOut, "   Next: %s\n", hint)
		}
	}

	writeRestoreResult(consoleOut, result, runRecord.DurationSeconds)
	auditErr := r.updateRunRecord(ctx, runRecord, consoleOut)

	if logFile != nil {
		_, _ = fmt.Fprintf(logFile, "\n=== Finished Restore Job at %s (Status: %s) ===\n",
			finishedAt.Format(time.RFC3339), runRecord.Status)
	}

	if auditErr != nil {
		if execErr == nil {
			return result, fmt.Errorf(
				"restore run %q completed but its audit record could not be persisted: %w",
				runRecord.JobName,
				errors.Join(ErrRestoreUntracked, auditErr),
			)
		}
		return result, errors.Join(
			execErr,
			fmt.Errorf(
				"restore run %q failed and its audit record could not be persisted: %w: %v",
				runRecord.JobName,
				ErrRestoreAuditFailed,
				auditErr,
			),
		)
	}

	return result, execErr
}

// failRun finalizes a restore attempt that failed before producing a restic
// result. It records the phase the attempt died in (so `ops restore history`
// can tell validation apart from execution and post-restore failures) and
// returns the record together with a wrapped error that keeps the original
// cause intact.
func (r *RestoreRunner) failRun(
	ctx context.Context,
	result *RestoreResult,
	stage string,
	err error,
	consoleOut io.Writer,
) (*RestoreResult, error) {
	runRecord := result.RestoreRun
	finishedAt := time.Now()
	runRecord.Status = "failed"
	runRecord.FailureStage = stage
	runRecord.ErrorMessage = err.Error()
	runRecord.FinishedAt = &finishedAt
	runRecord.DurationSeconds = finishedAt.Sub(runRecord.StartedAt).Seconds()
	result.setStep(RestoreStepFiles, RestoreStepFailed, err.Error(), restoreStageHint(stage))
	result.blockSteps("未执行：前置校验失败", RestoreStepVolumes, RestoreStepDatabase, RestoreStepCompose)
	auditErr := r.updateRunRecord(ctx, runRecord, consoleOut)

	_, _ = fmt.Fprintf(consoleOut, "❌ Restore failed during %s: %s\n", stage, err)
	if hint := restoreStageHint(stage); hint != "" {
		_, _ = fmt.Fprintf(consoleOut, "   Next: %s\n", hint)
	}

	if auditErr != nil {
		_, _ = fmt.Fprintf(consoleOut, "Warning: restore audit record could not be persisted: %v\n", auditErr)
	}

	runErr := fmt.Errorf("restore failed during %s: %w", stage, err)
	if auditErr != nil {
		return result, errors.Join(
			runErr,
			fmt.Errorf("%w: %v", ErrRestoreAuditFailed, auditErr),
		)
	}
	return result, runErr
}

// restoreStageHint returns the next step to suggest for a failed restore stage.
func restoreStageHint(stage string) string {
	switch stage {
	case storage.RestoreStageValidation:
		return "nothing was restored; fix the reported input (see `ops backup snapshots <job>`, `ops asset list`, `ops server list`) and re-run"
	case storage.RestoreStageExecution:
		return "inspect the log file for the failing restic command; `--dry-run` lists the files a restore would write"
	case storage.RestoreStagePostRestore:
		return "the files are restored; start the containers manually or re-run with --no-start to skip auto-start"
	default:
		return ""
	}
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
	return executor.ResolveTarget(r.serverStore, serverName)
}

func (r *RestoreRunner) updateRunRecord(ctx context.Context, runRecord *storage.RestoreRun, consoleOut io.Writer) error {
	if r.restoreRepo != nil && runRecord.ID > 0 {
		if err := r.restoreRepo.UpdateRun(ctx, runRecord); err != nil {
			if consoleOut != nil {
				_, _ = fmt.Fprintf(consoleOut, "Warning: failed to update restore run record in database: %v\n", err)
			}
			return err
		}
	}
	return nil
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

// runPostRestoreSteps decides whether the container steps run at all. The
// decision is scenario-aware: an ordinary file restore and a same-host restore
// leave the services alone (auto-starting them is the mistake this policy
// exists to prevent), while a cross-machine restore driven by a matching
// container manifest starts them. --no-start and --start override the scenario.
func (r *RestoreRunner) runPostRestoreSteps(
	ctx context.Context,
	target executor.Target,
	job Job,
	opts RestoreOptions,
	targetServerName string,
	result *RestoreResult,
	consoleOut io.Writer,
) error {
	switch opts.effectiveStartPolicy() {
	case StartPolicyNoStart:
		result.skipContainerSteps("已按 --no-start 跳过：只解压文件，不导入命名卷、不灌库、不启动服务")
		result.addPending(fmt.Sprintf("需要启动服务时重新执行：%s", restoreStartHint(job.Name, opts)))
		_, _ = fmt.Fprintln(consoleOut, "--> --no-start: files restored only, container services left untouched.")
		return nil
	case StartPolicyAuto:
		if targetServerName == job.Server {
			result.skipContainerSteps("同机恢复默认不启动服务：文件按原路径写回，避免误启动线上服务")
			result.addPending(fmt.Sprintf("需要启动服务时显式执行：%s", restoreStartHint(job.Name, opts)))
			_, _ = fmt.Fprintln(consoleOut, "--> Same-host restore: container services left untouched (pass --start to start them).")
			return nil
		}
	}
	return r.startRestoredServices(ctx, target, job, opts, result, consoleOut)
}

// startRestoredServices runs the post-file steps (named volumes, Compose start,
// database import) and records each one in the report. A step that fails stops
// the steps that depend on it and marks them blocked, so the result shows both
// what was done and what still has to happen.
func (r *RestoreRunner) startRestoredServices(
	ctx context.Context,
	target executor.Target,
	job Job,
	opts RestoreOptions,
	result *RestoreResult,
	consoleOut io.Writer,
) error {
	_, _ = fmt.Fprintf(consoleOut, "--> Inspecting restored files for container services...\n")

	execToUse := r.executor
	if target.IsLocal {
		execToUse = r.localExecutor
	}

	candidateDirs := dedupPaths(append([]string{fmt.Sprintf("/var/lib/opspulse/containers/%s", job.Name)}, job.Paths...))

	// A located container package: the parsed manifest plus the directory its
	// relative archive/dump paths are resolved against and the project
	// directory compose is started from.
	type manifestCandidate struct {
		manifest    *docker.ContainerManifest
		packageRoot string
		dir         string
	}

	var candidates []manifestCandidate
	seenManifestPaths := make(map[string]bool)
	for _, cand := range candidateDirs {
		rDir := RestoredPath(opts.TargetPath, cand)
		// Probe the namespaced location first, then the legacy one, so that
		// snapshots taken before artifacts moved under ".opspulse/" remain
		// restorable.
		for _, mPath := range []string{docker.ManifestPath(rDir), docker.LegacyManifestPath(rDir)} {
			if seenManifestPaths[mPath] {
				continue
			}
			seenManifestPaths[mPath] = true

			readManifestScript := fmt.Sprintf("cat %s 2>/dev/null || true", shellquote.Quote(mPath))
			var mBuf bytes.Buffer
			res, err := execToUse.Execute(ctx, target, "read-manifest-"+job.Name, readManifestScript, &mBuf)
			if err != nil || res == nil || !res.Success || len(bytes.TrimSpace(mBuf.Bytes())) == 0 {
				continue
			}
			m, parseErr := docker.UnmarshalManifest(mBuf.Bytes())
			if parseErr != nil || m == nil {
				result.warnf(consoleOut, "  ⚠️ Warning: found container manifest %s but could not parse it: %v", mPath, parseErr)
				continue
			}
			candidates = append(candidates, manifestCandidate{
				manifest:    m,
				packageRoot: path.Dir(mPath),
				dir:         rDir,
			})
		}
	}

	// Only a manifest whose app names this job (or the requested alias) may
	// drive the restore. Silently using "the last manifest we found" started a
	// different application (C72).
	var matched []manifestCandidate
	for _, c := range candidates {
		if strings.EqualFold(c.manifest.App, job.Name) || (opts.AliasName != "" && strings.EqualFold(c.manifest.App, opts.AliasName)) {
			matched = append(matched, c)
		}
	}

	var (
		manifest           *docker.ContainerManifest
		restoredProjectDir string
		packageRoot        string
	)
	switch {
	case len(matched) > 0:
		manifest = matched[0].manifest
		restoredProjectDir = matched[0].dir
		packageRoot = matched[0].packageRoot
		if len(matched) > 1 {
			locations := make([]string, 0, len(matched))
			for _, c := range matched {
				locations = append(locations, path.Join(c.packageRoot, docker.ManifestFileName))
			}
			result.warnf(consoleOut, "  ⚠️ Warning: %d manifests match app %q (%s); using %s.",
				len(matched), matched[0].manifest.App, strings.Join(locations, ", "), packageRoot)
		}
	case len(candidates) > 0:
		found := make([]string, 0, len(candidates))
		for _, c := range candidates {
			found = append(found, fmt.Sprintf("%q at %s", c.manifest.App, path.Join(c.packageRoot, docker.ManifestFileName)))
		}
		aliasNote := ""
		if opts.AliasName != "" {
			aliasNote = fmt.Sprintf(" or alias %q", opts.AliasName)
		}
		if opts.effectiveStartPolicy() == StartPolicyStart {
			result.warnf(consoleOut, "  ⚠️ Warning: found container manifest(s) %s, but none matches job %q%s. Skipping manifest-driven restore and falling back to auto-start.",
				strings.Join(found, "; "), job.Name, aliasNote)
		} else {
			result.warnf(consoleOut, "  ⚠️ Warning: found container manifest(s) %s, but none matches job %q%s. Not starting any service.",
				strings.Join(found, "; "), job.Name, aliasNote)
		}
	}

	if manifest == nil {
		if opts.effectiveStartPolicy() != StartPolicyStart {
			result.skipContainerSteps("未发现与本任务匹配的容器 manifest：普通文件恢复默认不启动服务")
			result.addPending(fmt.Sprintf("需要启动服务时重新执行：%s", restoreStartHint(job.Name, opts)))
			_, _ = fmt.Fprintf(consoleOut, "--> No container manifest for job %q: leaving container services stopped.\n", job.Name)
			return nil
		}
		return r.startWithoutManifest(ctx, execToUse, target, job, opts, candidateDirs, result, consoleOut)
	}

	_, _ = fmt.Fprintf(consoleOut, "  -> Detected package manifest for app %q. Running manifest-driven restore...\n", manifest.App)

	// A. Check external mounts. They are host resources OpsPulse never
	// migrates, so a missing one is a warning plus a manual follow-up.
	for _, em := range manifest.ExternalMounts {
		// docker.IsSystemMount is the single source of truth for "this is a
		// host runtime dependency, not operator data" and matches on path
		// boundaries, so "/development" is never mistaken for "/dev".
		if em.Reason == "system_mount" || docker.IsSystemMount(em.Source) {
			checkScript := fmt.Sprintf("test -e %s || echo 'MISSING'", shellquote.Quote(em.Source))
			var checkBuf bytes.Buffer
			_, _ = execToUse.Execute(ctx, target, "check-mount", checkScript, &checkBuf)
			if strings.Contains(checkBuf.String(), "MISSING") {
				result.warnf(consoleOut, "  ⚠️ Warning: external host mount %s is missing on target %s", em.Source, target.Name)
				result.addPending(fmt.Sprintf("在目标机 %s 上准备宿主机挂载 %s（外部依赖不会随快照迁移）", target.Name, em.Source))
			}
		}
	}

	// B. Restore named volumes
	if len(manifest.Volumes) == 0 {
		result.setStep(RestoreStepVolumes, RestoreStepSkipped, "快照中没有命名卷需要恢复", "")
	}
	restoredVolumes := 0
	for _, v := range manifest.Volumes {
		if v.Archive == "" {
			continue
		}
		archivePath := path.Join(packageRoot, v.Archive)
		_, _ = fmt.Fprintf(consoleOut, "  -> Restoring volume %q from %s...\n", v.OriginalName, v.Archive)
		volScript := docker.BuildVolumeImportScript(v.OriginalName, archivePath)
		vRes, vErr := execToUse.Execute(ctx, target, "restore-vol-"+v.OriginalName, volScript, consoleOut)
		if vErr != nil || (vRes != nil && !vRes.Success) {
			stepErr := fmt.Errorf("failed to restore volume %q: %v", v.OriginalName, vErr)
			result.setStep(RestoreStepVolumes, classifyStepStatus(vRes), stepErr.Error(),
				fmt.Sprintf("在目标机手工导入该卷归档（快照内路径 %s），或确认 helper 镜像可用后重试", archivePath))
			result.blockSteps("volume 恢复失败，未执行", RestoreStepDatabase, RestoreStepCompose)
			return stepErr
		}
		restoredVolumes++
	}
	if restoredVolumes > 0 {
		result.setStep(RestoreStepVolumes, RestoreStepSuccess, fmt.Sprintf("%d 个命名卷已导入", restoredVolumes), "")
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
		stepErr := fmt.Errorf("compose up failed for %q: %v", projectName, cErr)
		result.setStep(RestoreStepCompose, classifyStepStatus(cRes), stepErr.Error(),
			fmt.Sprintf("在目标机手工执行：cd %s && docker compose -f %s up -d", restoredProjectDir, composeFile))
		result.blockSteps("Compose 启动失败，未导入数据库", RestoreStepDatabase)
		return stepErr
	}
	result.setStep(RestoreStepCompose, RestoreStepSuccess,
		fmt.Sprintf("项目 %q 已启动（cd %s && docker compose up -d）", projectName, restoredProjectDir), "")

	// D. Import Database
	if err := r.importRestoredDatabase(ctx, execToUse, target, job, manifest, opts, packageRoot, result, consoleOut); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(consoleOut, "🚀 Container app %q successfully restored and running on %s!\n", projectName, target.Name)
	return nil
}

// importRestoredDatabase performs the database step of a manifest-driven
// restore. A manifest that names a database without a logical dump is recorded
// as manual: copying a live data directory is not a consistent import, and
// saying so is the point of reporting the step at all.
func (r *RestoreRunner) importRestoredDatabase(
	ctx context.Context,
	execToUse executor.Executor,
	target executor.Target,
	job Job,
	manifest *docker.ContainerManifest,
	opts RestoreOptions,
	packageRoot string,
	result *RestoreResult,
	consoleOut io.Writer,
) error {
	if manifest.Database == nil {
		result.setStep(RestoreStepDatabase, RestoreStepSkipped, "快照中没有数据库导出需要导入", "")
		return nil
	}
	if manifest.Database.Dump == "" {
		result.setStep(RestoreStepDatabase, RestoreStepManual,
			fmt.Sprintf("manifest 声明了 %s 数据库但没有逻辑导出，数据需要人工处理", manifest.Database.Engine),
			"按该数据库自身的一致性流程手工恢复数据，或重新备份该容器以生成逻辑导出")
		result.addPending(fmt.Sprintf("%s 数据库没有逻辑导出，需要人工恢复数据", manifest.Database.Engine))
		return nil
	}

	dumpPath := path.Join(packageRoot, manifest.Database.Dump)
	targetContainer := manifest.Database.Container
	if opts.AliasName != "" && targetContainer == manifest.App {
		targetContainer = opts.AliasName
	}
	_, _ = fmt.Fprintf(consoleOut, "  -> Importing database dump from %s into %s (%s)...\n",
		dumpPath, targetContainer, manifest.Database.Engine)

	importScript, impErr := docker.BuildImportScript(manifest.Database.Engine, targetContainer, dumpPath)
	if impErr != nil {
		stepErr := fmt.Errorf("failed to build import script: %w", impErr)
		result.setStep(RestoreStepDatabase, RestoreStepFailed, stepErr.Error(), "")
		return stepErr
	}

	impRes, impExecErr := execToUse.Execute(ctx, target, "import-db-"+job.Name, importScript, consoleOut)
	if impExecErr != nil || (impRes != nil && !impRes.Success) {
		var stepErr error
		switch {
		case impExecErr != nil:
			stepErr = fmt.Errorf("database import failed for container %q: %w", targetContainer, impExecErr)
		case impRes.Error != nil:
			stepErr = fmt.Errorf("database import failed for container %q: %w", targetContainer, impRes.Error)
		default:
			stepErr = fmt.Errorf("database import failed for container %q: command exited with code %d", targetContainer, impRes.ExitCode)
		}
		result.setStep(RestoreStepDatabase, classifyStepStatus(impRes), stepErr.Error(),
			fmt.Sprintf("在目标机确认容器 %q 就绪后，手工导入快照内的 %s（%s）", targetContainer, dumpPath, manifest.Database.Engine))
		result.addPending(fmt.Sprintf("数据库 %s 的导入未完成，快照内的导出文件在 %s", manifest.Database.Engine, dumpPath))
		return stepErr
	}

	result.setStep(RestoreStepDatabase, RestoreStepSuccess,
		fmt.Sprintf("%s 导出已导入容器 %q", manifest.Database.Engine, targetContainer), "")
	return nil
}

// startWithoutManifest is the generic auto-start fallback: it scans the
// restored directories for compose files. It only runs when the operator asked
// for a start explicitly, because it can start services the restore was not
// about.
func (r *RestoreRunner) startWithoutManifest(
	ctx context.Context,
	execToUse executor.Executor,
	target executor.Target,
	job Job,
	opts RestoreOptions,
	candidateDirs []string,
	result *RestoreResult,
	consoleOut io.Writer,
) error {
	var fallbackCandidateDirs []string
	if opts.TargetPath != "" && opts.TargetPath != "/" {
		fallbackCandidateDirs = append(fallbackCandidateDirs, opts.TargetPath)
	}
	for _, cand := range candidateDirs {
		fallbackCandidateDirs = append(fallbackCandidateDirs, RestoredPath(opts.TargetPath, cand))
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

	const manualStartHint = "在目标机手工执行 docker compose up -d 启动服务"
	script := docker.BuildAutoStartScript(autoOpts)
	startRes, startErr := execToUse.Execute(ctx, target, "autostart-"+job.Name, script, consoleOut)
	if startErr != nil {
		stepErr := fmt.Errorf("container auto-start failed: %w", startErr)
		result.setStep(RestoreStepCompose, classifyStepStatus(startRes), stepErr.Error(), manualStartHint)
		return stepErr
	}
	if startRes != nil && !startRes.Success {
		var stepErr error
		if startRes.Error != nil {
			stepErr = fmt.Errorf("container auto-start failed: %w", startRes.Error)
		} else {
			stepErr = fmt.Errorf("container auto-start failed: command exited with code %d", startRes.ExitCode)
		}
		result.setStep(RestoreStepCompose, classifyStepStatus(startRes), stepErr.Error(), manualStartHint)
		return stepErr
	}

	result.setStep(RestoreStepVolumes, RestoreStepSkipped, "未发现容器 manifest，未导入命名卷", "")
	result.setStep(RestoreStepDatabase, RestoreStepSkipped, "未发现容器 manifest，未导入数据库", "")
	result.setStep(RestoreStepCompose, RestoreStepSuccess, "通用 auto-start 已启动还原目录中检测到的服务", "")
	_, _ = fmt.Fprintf(consoleOut, "🚀 Container services are up and running on %s!\n", target.Name)
	return nil
}
