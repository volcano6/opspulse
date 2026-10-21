package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/asset"
	"github.com/volcano6/opspulse/internal/backup"
	"github.com/volcano6/opspulse/internal/cliutil"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/notify"
	"github.com/volcano6/opspulse/internal/server"
	"github.com/volcano6/opspulse/internal/storage"
)

var restoreCmd = &cobra.Command{
	Use:   "restore",
	Short: "从 restic 备份快照还原数据",
	Long: `执行基于 restic 备份快照的还原操作，支持跨机迁移、路径重映射、
单资产精准还原与 dry-run 预演。`,
}

// restoreNotifyTimeout bounds one notification delivery so a wedged webhook
// cannot hang the CLI after the restore itself has finished.
const restoreNotifyTimeout = 30 * time.Second

// notifyRestoreResult delivers one restore outcome to the configured channels.
// It is a package-level variable so a test can substitute a capture, while
// production goes through the shared notify.Dispatcher.
var notifyRestoreResult = func(ctx context.Context, event notify.Event) []error {
	return notify.NewDispatcher(notify.NewDefaultStore()).Dispatch(ctx, event)
}

// newRestoreExecutor builds the executor the restore command runs against. It
// is a package-level variable so a CLI test can drive the whole command against
// a fake target instead of a real host.
var newRestoreExecutor = func(serverStore *server.Store) executor.Executor {
	return executor.NewSSHExecutor().WithServerResolver(serverStore.Get).WithWarnWriter(os.Stdout)
}

var (
	restoreRunSnapshot     string
	restoreRunTargetServer string
	restoreRunTargetPath   string
	restoreRunAssetID      string
	restoreRunDryRun       bool
	restoreRunStart        bool
	restoreRunNoStart      bool
	restoreRunAs           string
	restoreRunYes          bool
	restoreRunAllowUntracked bool
)

var restoreRunCmd = &cobra.Command{
	Use:   "run <job-name>",
	Short: "从备份快照执行还原操作",
	Long: `将 restic 备份快照中的文件还原到原始服务器或另一台服务器。

还原结束后会逐步骤报告结果（文件恢复、volume 恢复、数据库导入、Compose 启动），
每一步各自是 success / failed / skipped / blocked / manual，总状态是
success / partial / failed / canceled / dry-run。

是否启动服务按场景决定：普通文件恢复与同机恢复默认只写回文件、不启动服务；
跨机恢复在找到与本任务匹配的容器 manifest 时才会自动启动。需要强制启动用
--start，需要只解压文件用 --no-start（两者互斥）。

示例：
  # 还原最新快照到原始服务器和原始路径
  ops restore run blog-backup

  # 还原指定快照
  ops restore run blog-backup --snapshot abc12345

  # 跨机迁移：还原到新 VPS（有容器 manifest 时自动启动服务）
  ops restore run blog-backup --target-server new-vps --target-path /data/blog

  # 普通文件恢复但需要启动目标机上已还原的服务
  ops restore run blog-backup --start

  # 单资产精准还原
  ops restore run blog-backup --asset blog-mysql

  # 预演文件列表，不真正还原
  ops restore run blog-backup --dry-run`,
	Args: cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		jobName := args[0]

		backupStore := backup.NewDefaultStore()
		job, err := backupStore.Get(jobName)
		if err != nil {
			return fmt.Errorf("backup job %q not found: %w", jobName, err)
		}

		db, err := storage.OpenDefault()
		if err != nil {
			return fmt.Errorf("failed to open database: %w", err)
		}
		defer func() { _ = db.Close() }()

		restoreRepo := storage.NewRestoreRepo(db)
		serverStore := server.NewDefaultStore()
		assetStore := asset.NewDefaultStore()
		exec := newRestoreExecutor(serverStore)

		runner := backup.NewRestoreRunner(exec, serverStore, restoreRepo, backupStore, assetStore)

		startPolicy, err := restoreStartPolicy(restoreRunStart, restoreRunNoStart)
		if err != nil {
			return err
		}

		opts := backup.RestoreOptions{
			SnapshotID:     restoreRunSnapshot,
			TargetServer:   restoreRunTargetServer,
			TargetPath:     restoreRunTargetPath,
			AssetID:        restoreRunAssetID,
			DryRun:         restoreRunDryRun,
			StartPolicy:    startPolicy,
			AliasName:      restoreRunAs,
			AllowUntracked: restoreRunAllowUntracked,
		}

		targetHost := job.Server
		if restoreRunTargetServer != "" {
			targetHost = restoreRunTargetServer
		}
		if err := checkRestoreConfirmation(os.Stdin, os.Stdout, jobName, targetHost, restoreRunTargetPath, restoreRunDryRun, restoreRunYes); err != nil {
			return err
		}

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		result, runErr := runner.Run(ctx, *job, opts, os.Stdout)

		// The outcome is notified whether it succeeded or not: a failed or
		// partial restore is exactly what a failure-triggered channel exists
		// for. Delivery gets its own bounded context so an interrupted restore
		// still reports what happened.
		if result != nil {
			notifyCtx, cancelNotify := context.WithTimeout(context.Background(), restoreNotifyTimeout)
			defer cancelNotify()
			if errs := notifyRestoreResult(notifyCtx, restoreNotificationEvent(result)); len(errs) > 0 {
				for _, notifyErr := range errs {
					_, _ = fmt.Fprintf(os.Stderr, "Warning: restore notification failed: %v\n", notifyErr)
				}
			}
		}

		if runErr != nil {
			return runErr
		}

		if result != nil && (result.Status == "failed" || result.Status == "partial") {
			return fmt.Errorf("restore finished with status %q: %s", result.Status, result.ErrorMessage)
		}

		if result != nil && result.Untracked {
			return fmt.Errorf("restore %q completed but was not audited: %w", jobName, backup.ErrRestoreUntracked)
		}

		return nil
	},
}

var restoreHistoryLimit int

var restoreHistoryCmd = &cobra.Command{
	Use:   "history [job-name]",
	Short: "查看还原执行历史记录",
	Long: `显示还原操作的历史记录，可按备份作业名筛选。
展示状态、快照 ID、源/目标服务器、耗时与时间戳。`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		var jobName string
		if len(args) > 0 {
			jobName = args[0]
		}

		db, err := storage.OpenDefault()
		if err != nil {
			return fmt.Errorf("failed to open database: %w", err)
		}
		defer func() { _ = db.Close() }()

		repo := storage.NewRestoreRepo(db)
		runs, err := repo.ListRuns(context.Background(), jobName, restoreHistoryLimit)
		if err != nil {
			return err
		}

		if len(runs) == 0 {
			if jobName != "" {
				fmt.Printf("No restore history found for job %q.\n", jobName)
			} else {
				fmt.Println("No restore history found.")
			}
			return nil
		}

		tw := cliutil.NewTabWriter(os.Stdout)
		_, _ = fmt.Fprintln(tw, "ID\tJOB\tSTATUS\tSNAPSHOT\tSOURCE\tTARGET\tDURATION\tSTARTED AT")
		_, _ = fmt.Fprintln(tw, "--\t---\t------\t--------\t------\t------\t--------\t----------")

		for _, r := range runs {
			status := restoreStatusLabel(r)
			snapID := r.SnapshotID
			if len(snapID) > 8 {
				snapID = snapID[:8]
			}
			durationStr := fmt.Sprintf("%.2fs", r.DurationSeconds)
			startedStr := r.StartedAt.Format("2006-01-02 15:04:05")

			targetStr := r.TargetServer
			if r.TargetPath != "" && r.TargetPath != "/" {
				targetStr = fmt.Sprintf("%s:%s", r.TargetServer, r.TargetPath)
			}

			_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				r.ID, r.JobName, status, snapID, r.SourceServer, targetStr, durationStr, startedStr)
		}

		return tw.Flush()
	},
}

func init() {
	restoreRunCmd.Flags().StringVar(&restoreRunSnapshot, "snapshot", "latest", "用于还原的快照 ID（'latest' 表示最新快照）")
	restoreRunCmd.Flags().StringVar(&restoreRunTargetServer, "target-server", "", "跨机迁移的目标服务器（默认与源相同）")
	restoreRunCmd.Flags().StringVar(&restoreRunTargetPath, "target-path", "", "覆盖还原目标路径以实现路径重映射（默认使用快照中的原始路径）")
	restoreRunCmd.Flags().StringVar(&restoreRunAssetID, "asset", "", "仅还原指定资产（按资产 ID）")
	restoreRunCmd.Flags().BoolVar(&restoreRunDryRun, "dry-run", false, "预演模式：仅列出文件，不执行实际还原")
	restoreRunCmd.Flags().BoolVar(&restoreRunStart, "start", false, "还原后显式启动容器服务与数据库导入（普通文件恢复与同机恢复默认不启动）")
	restoreRunCmd.Flags().BoolVar(&restoreRunNoStart, "no-start", false, "还原后不启动：仅解压文件，不拉起容器也不灌库（与 --start 互斥）")
	restoreRunCmd.Flags().StringVar(&restoreRunAs, "as", "", "在目标服务器上重命名容器/服务项目名")
	restoreRunCmd.Flags().BoolVarP(&restoreRunYes, "yes", "y", false, "跳过执行前的交互式确认")
	restoreRunCmd.Flags().BoolVar(&restoreRunAllowUntracked, "allow-untracked", false, "SQLite 写入失败时仍继续还原（记录标记为 UNTRACKED，进程以状态码 3 退出）")

	restoreHistoryCmd.Flags().IntVarP(&restoreHistoryLimit, "limit", "n", 20, "最多显示的历史记录条数")

	restoreRunCmd.ValidArgsFunction = completeBackupJobNames
	restoreHistoryCmd.ValidArgsFunction = completeBackupJobNames

	_ = restoreRunCmd.RegisterFlagCompletionFunc("asset", completeRestoreAssetIDs)
	_ = restoreRunCmd.RegisterFlagCompletionFunc("target-server", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		store := server.NewDefaultStore()
		servers, err := store.List()
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		var comps []string
		for _, s := range servers {
			comps = append(comps, fmt.Sprintf("%s\t%s", s.Name, s.Host))
		}
		return comps, cobra.ShellCompDirectiveNoFileComp
	})

	restoreCmd.AddCommand(restoreRunCmd)
	restoreCmd.AddCommand(restoreHistoryCmd)

	rootCmd.AddCommand(restoreCmd)
}

func completeRestoreAssetIDs(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	store := asset.NewDefaultStore()
	assets, err := store.List()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	var comps []string
	for _, a := range assets {
		comps = append(comps, fmt.Sprintf("%s\t%s @ %s", a.ID, a.Type, a.Source))
	}
	return comps, cobra.ShellCompDirectiveNoFileComp
}

// restoreStartPolicy maps the two start flags onto the engine's policy. They
// are mutually exclusive: "start this" and "do not start this" cannot both be
// true, and silently picking one would hide a scripted mistake.
func restoreStartPolicy(start, noStart bool) (backup.StartPolicy, error) {
	switch {
	case start && noStart:
		return "", errors.New("--start 与 --no-start 互斥，请只保留一个")
	case start:
		return backup.StartPolicyStart, nil
	case noStart:
		return backup.StartPolicyNoStart, nil
	default:
		return backup.StartPolicyAuto, nil
	}
}

// restoreNotificationEvent turns a restore result into the notification event:
// snapshot, duration, per-step status, warnings and the work that is still
// outstanding all travel with it, so an alert is actionable on its own.
func restoreNotificationEvent(result *backup.RestoreResult) notify.Event {
	event := notify.Event{
		Kind:            notify.EventKindRestore,
		JobName:         result.JobName,
		Status:          result.Status,
		Server:          result.TargetServer,
		Snapshot:        result.SnapshotID,
		DurationSeconds: result.DurationSeconds,
		Error:           result.ErrorMessage,
		Untracked:       result.Untracked,
		Timestamp:       time.Now(),
		Warnings:        result.Warnings,
		Pending:         result.Pending,
	}
	for _, step := range result.Steps {
		event.Steps = append(event.Steps, notify.StepStatus{
			Kind:     step.Kind,
			Label:    step.Label,
			Status:   step.Status,
			Detail:   step.Detail,
			NextStep: step.NextStep,
		})
	}
	return event
}

// restoreStatusLabel renders a restore run's status for the history table. A
// failed run also shows the stage it failed in, so validation failures (nothing
// was touched) are not confused with execution or post-restore failures.
func restoreStatusLabel(r storage.RestoreRun) string {
	label := strings.ToUpper(r.Status)
	if r.FailureStage != "" {
		label += " (" + strings.ToUpper(r.FailureStage) + ")"
	}
	return label
}

// checkRestoreConfirmation keeps its own shape instead of going through
// confirmGate: --dry-run has to skip it as well, and the overwrite warning is
// worth printing even when stdin is a pipe (the run still aborts when nothing
// answers the prompt, so a script cannot silently accept it).
func checkRestoreConfirmation(in io.Reader, out io.Writer, jobName, targetServer, targetPath string, dryRun, yes bool) error {
	if dryRun || yes {
		return nil
	}
	targetDir := targetPath
	if targetDir == "" {
		targetDir = "original backup paths"
	}
	prompt := fmt.Sprintf("⚠️  Warning: Restoring job %q to %s (%s) may overwrite existing files.\nAre you sure you want to proceed? [y/N]: ", jobName, targetServer, targetDir)
	if !promptConfirm(in, out, prompt, false) {
		return fmt.Errorf("restore cancelled by user; re-run with --yes to skip the confirmation prompt")
	}
	return nil
}
