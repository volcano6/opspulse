package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/asset"
	"github.com/volcano6/opspulse/internal/backup"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/server"
	"github.com/volcano6/opspulse/internal/storage"
)

var restoreCmd = &cobra.Command{
	Use:   "restore",
	Short: "从 restic 备份快照还原数据",
	Long: `执行基于 restic 备份快照的还原操作，支持跨机迁移、路径重映射、
单资产精准还原与 dry-run 预演。`,
}

var (
	restoreRunSnapshot     string
	restoreRunTargetServer string
	restoreRunTargetPath   string
	restoreRunAssetID      string
	restoreRunDryRun       bool
	restoreRunNoStart      bool
	restoreRunAs           string
	restoreRunYes          bool
)

var restoreRunCmd = &cobra.Command{
	Use:   "run <job-name>",
	Short: "从备份快照执行还原操作",
	Long: `将 restic 备份快照中的文件还原到原始服务器或另一台服务器。

示例：
  # 还原最新快照到原始服务器和原始路径
  ops restore run blog-backup

  # 还原指定快照
  ops restore run blog-backup --snapshot abc12345

  # 跨机迁移：还原到新 VPS
  ops restore run blog-backup --target-server new-vps --target-path /data/blog

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
		exec := executor.NewSSHExecutor().WithServerResolver(serverStore.Get).WithWarnWriter(os.Stdout)

		runner := backup.NewRestoreRunner(exec, serverStore, restoreRepo, backupStore, assetStore)

		opts := backup.RestoreOptions{
			SnapshotID:   restoreRunSnapshot,
			TargetServer: restoreRunTargetServer,
			TargetPath:   restoreRunTargetPath,
			AssetID:      restoreRunAssetID,
			DryRun:       restoreRunDryRun,
			NoStart:      restoreRunNoStart,
			AliasName:    restoreRunAs,
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

		runRecord, err := runner.Run(ctx, *job, opts, os.Stdout)
		if err != nil {
			return err
		}

		if runRecord != nil && (runRecord.Status == "failed" || runRecord.Status == "partial") {
			return fmt.Errorf("restore finished with status %q: %s", runRecord.Status, runRecord.ErrorMessage)
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

		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		_, _ = fmt.Fprintln(tw, "ID\tJOB\tSTATUS\tSNAPSHOT\tSOURCE\tTARGET\tDURATION\tSTARTED AT")
		_, _ = fmt.Fprintln(tw, "--\t---\t------\t--------\t------\t------\t--------\t----------")

		for _, r := range runs {
			status := strings.ToUpper(r.Status)
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
	restoreRunCmd.Flags().BoolVar(&restoreRunNoStart, "no-start", false, "还原后不自动启动：仅解压文件，不拉起容器也不灌库")
	restoreRunCmd.Flags().StringVar(&restoreRunAs, "as", "", "在目标服务器上重命名容器/服务项目名")
	restoreRunCmd.Flags().BoolVarP(&restoreRunYes, "yes", "y", false, "跳过执行前的交互式确认")

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
