package main

import (
	"context"
	"fmt"
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
	"github.com/volcano6/opspulse/internal/format"
	"github.com/volcano6/opspulse/internal/server"
	"github.com/volcano6/opspulse/internal/storage"
)

var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "管理与执行备份作业",
	Long: `定义、查看、执行并监控跨服务器的 restic 备份作业。
每次备份的结构化指标、快照 ID 与历史日志持久化记录到 SQLite。`,
}

var backupListCmd = &cobra.Command{
	Use:   "list",
	Short: "列出全部已配置备份作业",
	RunE: func(_ *cobra.Command, _ []string) error {
		store := backup.NewDefaultStore()
		jobs, err := store.List()
		if err != nil {
			return fmt.Errorf("failed to list backup jobs: %w", err)
		}

		if len(jobs) == 0 {
			fmt.Println("No backup jobs configured. Configure jobs in $XDG_CONFIG_HOME/opspulse/backups.yaml.")
			return nil
		}

		tw := cliutil.NewTabWriter(os.Stdout)
		_, _ = fmt.Fprintln(tw, "NAME\tSERVER\tBACKEND\tPATHS\tSCHEDULE\tRETENTION\tTAGS")
		_, _ = fmt.Fprintln(tw, "----\t------\t-------\t-----\t--------\t---------\t----")

		for _, j := range jobs {
			pathsStr := strings.Join(j.Paths, ", ")
			retentionStr := "-"
			if j.Retention != nil {
				var parts []string
				if j.Retention.KeepDaily > 0 {
					parts = append(parts, fmt.Sprintf("daily:%d", j.Retention.KeepDaily))
				}
				if j.Retention.KeepWeekly > 0 {
					parts = append(parts, fmt.Sprintf("weekly:%d", j.Retention.KeepWeekly))
				}
				if j.Retention.KeepMonthly > 0 {
					parts = append(parts, fmt.Sprintf("monthly:%d", j.Retention.KeepMonthly))
				}
				if len(parts) > 0 {
					retentionStr = strings.Join(parts, " ")
				}
			}

			tagsStr := "-"
			if len(j.Tags) > 0 {
				tagsStr = strings.Join(j.Tags, ",")
			}

			scheduleStr := "-"
			if j.Schedule != "" {
				scheduleStr = j.Schedule
			}

			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				j.Name, j.Server, j.Backend, pathsStr, scheduleStr, retentionStr, tagsStr)
		}

		return tw.Flush()
	},
}

var (
	backupRunDryRun   bool
	backupRunParallel string
	backupRunAs       string
)

var backupRunCmd = &cobra.Command{
	Use:   "run <job1,job2... | all | server:container>",
	Short: "执行备份作业或直接备份容器",
	Long: `执行一个或多个已配置的备份作业，或直接备份单个容器。

示例：
  ops backup run blog-backup                # 执行单个作业
  ops backup run blog-backup,db-backup -j 2 # 并发执行多个作业，每次两个
  ops backup run all -j unlimited           # 取消默认并发上限
  ops backup run vps-1:blog-db --as blog    # 备份 vps-1 上的容器 blog-db，作业名为 "blog"`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Resolve --parallel up front so a typo fails before any connection,
		// storage access or container inspection.
		parallel, err := cliutil.ParseParallelism(backupRunParallel, cmd.Flags().Changed("parallel"), os.Stderr)
		if err != nil {
			return err
		}

		// Check for container target syntax: ops backup run <server>:<container> [--as <alias>]
		if len(args) == 1 {
			if srv, ctr, isContainer := backup.ParseContainerTarget(args[0]); isContainer {
				if backupRunDryRun {
					// This branch generates a Compose file, hot-dumps databases
					// and writes to the restic repository, none of which has a
					// preview mode. Refusing is the only honest answer: silently
					// running the real backup behind --dry-run would be a lie.
					return fmt.Errorf("--dry-run is not supported for %s:%s backups", srv, ctr)
				}
				db, err := storage.OpenDefault()
				if err != nil {
					return fmt.Errorf("failed to open database: %w", err)
				}
				defer func() { _ = db.Close() }()

				store := backup.NewDefaultStore()
				backupRepo := storage.NewBackupRepo(db)
				serverStore := server.NewDefaultStore()
				assetStore := asset.NewDefaultStore()
				exec := executor.NewSSHExecutor().WithServerResolver(serverStore.Get).WithWarnWriter(os.Stderr)
				runner := backup.NewRunnerWithStores(exec, serverStore, backupRepo, store, assetStore)

				ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
				defer cancel()

				opts := backup.ContainerBackupOptions{
					Server:        srv,
					ContainerName: ctr,
					AliasName:     backupRunAs,
				}

				res, err := runner.RunContainerBackup(ctx, opts, os.Stdout)
				if err != nil {
					return err
				}

				fmt.Printf("\n✅ Container backup completed successfully!\n")
				fmt.Printf("   Job Name:     %s\n", res.JobName)
				fmt.Printf("   Server:       %s\n", res.ServerName)
				if res.SnapshotID != "" {
					fmt.Printf("   Snapshot ID:  %s\n", res.SnapshotID)
				}
				if res.ComposePath != "" {
					fmt.Printf("   Compose File: %s\n", res.ComposePath)
				}
				if res.IsDatabase {
					fmt.Printf("   Database:     Online hot dump created & archived\n")
				}
				fmt.Printf("\nTo restore on another VPS and auto-start:\n")
				fmt.Printf("   ops restore run %s --target-server <target-vps>\n\n", res.JobName)
				return nil
			}
		}

		store := backup.NewDefaultStore()
		allJobs, err := store.List()
		if err != nil {
			return fmt.Errorf("failed to read backup jobs: %w", err)
		}

		if len(allJobs) == 0 {
			return fmt.Errorf("no backup jobs configured in %s", store.FilePath())
		}

		// Resolve target jobs
		var targetJobs []backup.Job
		isAll := false
		for _, arg := range args {
			if strings.ToLower(arg) == "all" {
				isAll = true
				break
			}
		}

		if isAll {
			targetJobs = allJobs
		} else {
			jobMap := make(map[string]backup.Job)
			for _, j := range allJobs {
				jobMap[j.Name] = j
			}

			for _, arg := range args {
				for _, name := range strings.Split(arg, ",") {
					trimmed := strings.TrimSpace(name)
					if trimmed == "" {
						continue
					}
					j, ok := jobMap[trimmed]
					if !ok {
						return fmt.Errorf("backup job %q not found in %s", trimmed, store.FilePath())
					}
					targetJobs = append(targetJobs, j)
				}
			}
		}

		// Initialize storage, executor and runner
		db, err := storage.OpenDefault()
		if err != nil {
			return fmt.Errorf("failed to open database: %w", err)
		}
		defer func() { _ = db.Close() }()

		backupRepo := storage.NewBackupRepo(db)
		serverStore := server.NewDefaultStore()
		assetStore := asset.NewDefaultStore()
		exec := executor.NewSSHExecutor().WithServerResolver(serverStore.Get).WithWarnWriter(os.Stderr) // SSH executor handles remote servers
		// Wrap with multi-target capability: if target is local, runner uses LocalExecutor
		runner := backup.NewRunnerWithStores(exec, serverStore, backupRepo, store, assetStore)

		pool := backup.NewPool(runner, parallel)
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		res, err := pool.RunAll(ctx, targetJobs, backupRunDryRun, os.Stdout)
		if res != nil {
			res.PrintSummary(os.Stdout)
		}
		if err != nil {
			return err
		}

		if res != nil && res.FailureCount > 0 {
			return fmt.Errorf("%d backup job(s) failed", res.FailureCount)
		}

		return nil
	},
}

var backupStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "查看全部备份作业的最新状态",
	RunE: func(_ *cobra.Command, _ []string) error {
		store := backup.NewDefaultStore()
		jobs, err := store.List()
		if err != nil {
			return err
		}

		db, err := storage.OpenDefault()
		if err != nil {
			return fmt.Errorf("failed to open database: %w", err)
		}
		defer func() { _ = db.Close() }()

		repo := storage.NewBackupRepo(db)
		latestRuns, err := repo.GetAllLatestRuns(context.Background())
		if err != nil {
			return err
		}

		runMap := make(map[string]storage.BackupRun)
		for _, r := range latestRuns {
			runMap[r.JobName] = r
		}

		tw := cliutil.NewTabWriter(os.Stdout)
		_, _ = fmt.Fprintln(tw, "JOB\tSERVER\tSTATUS\tSNAPSHOT\tDATA ADDED\tTOTAL SIZE\tDURATION\tLAST RUN")
		_, _ = fmt.Fprintln(tw, "---\t------\t------\t--------\t----------\t----------\t--------\t--------")

		for _, j := range jobs {
			r, hasRun := runMap[j.Name]
			if !hasRun {
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					j.Name, j.Server, "NEVER RUN", "-", "-", "-", "-", "-")
				continue
			}

			status := strings.ToUpper(r.Status)
			snapID := r.SnapshotID
			if snapID == "" {
				snapID = "-"
			} else if len(snapID) > 8 {
				snapID = snapID[:8]
			}

			addedStr := format.Bytes(r.DataAddedBytes)
			totalStr := format.Bytes(r.TotalBytes)
			durationStr := fmt.Sprintf("%.2fs", r.DurationSeconds)
			lastRunStr := r.StartedAt.Format("2006-01-02 15:04:05")

			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				j.Name, r.ServerName, status, snapID, addedStr, totalStr, durationStr, lastRunStr)
		}

		return tw.Flush()
	},
}

var historyLimit int

var backupHistoryCmd = &cobra.Command{
	Use:   "history <job-name>",
	Short: "查看指定备份作业的历史执行记录",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		jobName := args[0]

		db, err := storage.OpenDefault()
		if err != nil {
			return fmt.Errorf("failed to open database: %w", err)
		}
		defer func() { _ = db.Close() }()

		repo := storage.NewBackupRepo(db)
		runs, err := repo.ListRuns(context.Background(), jobName, historyLimit)
		if err != nil {
			return err
		}

		if len(runs) == 0 {
			fmt.Printf("No execution history found for job %q.\n", jobName)
			return nil
		}

		tw := cliutil.NewTabWriter(os.Stdout)
		_, _ = fmt.Fprintln(tw, "ID\tSTATUS\tSNAPSHOT\tFILES (NEW/CHG)\tADDED\tTOTAL\tDURATION\tSTARTED AT")
		_, _ = fmt.Fprintln(tw, "--\t------\t--------\t---------------\t-----\t-----\t--------\t----------")

		for _, r := range runs {
			status := strings.ToUpper(r.Status)
			snapID := r.SnapshotID
			if snapID == "" {
				snapID = "-"
			} else if len(snapID) > 8 {
				snapID = snapID[:8]
			}

			filesStr := fmt.Sprintf("%d / %d", r.FilesNew, r.FilesChanged)
			addedStr := format.Bytes(r.DataAddedBytes)
			totalStr := format.Bytes(r.TotalBytes)
			durationStr := fmt.Sprintf("%.2fs", r.DurationSeconds)
			startedStr := r.StartedAt.Format("2006-01-02 15:04:05")

			_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				r.ID, status, snapID, filesStr, addedStr, totalStr, durationStr, startedStr)
		}

		return tw.Flush()
	},
}

var backupSnapshotsCmd = &cobra.Command{
	Use:   "snapshots <job-name>",
	Short: "查询并列出备份作业的远端快照",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		jobName := args[0]
		store := backup.NewDefaultStore()
		job, err := store.Get(jobName)
		if err != nil {
			return err
		}

		serverStore := server.NewDefaultStore()
		exec := executor.NewSSHExecutor().WithServerResolver(serverStore.Get).WithWarnWriter(os.Stderr)
		runner := backup.NewRunner(exec, serverStore, nil)

		fmt.Printf("Querying snapshots for job %q from %s (%s)...\n",
			job.Name, job.Server, job.Backend)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		snapshots, err := runner.ListSnapshots(ctx, *job)
		if err != nil {
			return fmt.Errorf("failed to list snapshots: %w", err)
		}

		if len(snapshots) == 0 {
			fmt.Println("No snapshots found in repository.")
			return nil
		}

		tw := cliutil.NewTabWriter(os.Stdout)
		_, _ = fmt.Fprintln(tw, "ID\tDATE / TIME\tHOSTNAME\tPATHS\tTAGS")
		_, _ = fmt.Fprintln(tw, "--\t-----------\t--------\t-----\t----")

		for _, s := range snapshots {
			timeStr := s.Time.Local().Format("2006-01-02 15:04:05")
			pathsStr := strings.Join(s.Paths, ", ")
			tagsStr := "-"
			if len(s.Tags) > 0 {
				tagsStr = strings.Join(s.Tags, ",")
			}

			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
				s.ShortID, timeStr, s.Hostname, pathsStr, tagsStr)
		}

		return tw.Flush()
	},
}

func completeBackupJobNames(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	store := backup.NewDefaultStore()
	jobs, err := store.List()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	var comps []string
	for _, j := range jobs {
		comps = append(comps, fmt.Sprintf("%s\t%s (%s)", j.Name, j.Server, j.Backend))
	}
	return comps, cobra.ShellCompDirectiveNoFileComp
}

func completeBackupRunArgs(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	store := backup.NewDefaultStore()
	jobs, err := store.List()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}

	selected := make(map[string]bool)
	for _, arg := range args {
		for _, part := range strings.Split(arg, ",") {
			if p := strings.TrimSpace(part); p != "" {
				selected[p] = true
			}
		}
	}

	if idx := strings.LastIndex(toComplete, ","); idx != -1 {
		prefix := toComplete[:idx+1]
		currentParts := strings.Split(toComplete[:idx], ",")
		for _, p := range currentParts {
			if p = strings.TrimSpace(p); p != "" {
				selected[p] = true
			}
		}

		var comps []string
		for _, j := range jobs {
			if !selected[j.Name] {
				comps = append(comps, fmt.Sprintf("%s%s\t%s (%s)", prefix, j.Name, j.Server, j.Backend))
			}
		}
		return comps, cobra.ShellCompDirectiveNoFileComp
	}

	var comps []string
	if len(args) == 0 && len(selected) == 0 {
		comps = append(comps, "all\t执行全部已配置的备份作业")
	}
	for _, j := range jobs {
		if !selected[j.Name] {
			comps = append(comps, fmt.Sprintf("%s\t%s (%s)", j.Name, j.Server, j.Backend))
		}
	}
	return comps, cobra.ShellCompDirectiveNoFileComp
}

func init() {
	backupRunCmd.Flags().BoolVar(&backupRunDryRun, "dry-run", false, "预演执行，不真正运行 restic（不支持 <server>:<container> 目标）")
	backupRunCmd.Flags().StringVarP(&backupRunParallel, "parallel", "j", "", "最大并发作业数（默认 5，'unlimited' 表示不设上限）")
	backupRunCmd.Flags().StringVar(&backupRunAs, "as", "", "重命名生成的 Compose 与备份作业中的容器（使用 <server>:<container> 时）")

	backupHistoryCmd.Flags().IntVarP(&historyLimit, "limit", "n", 20, "最多显示的历史记录条数")

	backupRunCmd.ValidArgsFunction = completeBackupRunArgs
	backupHistoryCmd.ValidArgsFunction = completeBackupJobNames
	backupSnapshotsCmd.ValidArgsFunction = completeBackupJobNames

	backupCmd.AddCommand(backupListCmd)
	backupCmd.AddCommand(backupRunCmd)
	backupCmd.AddCommand(backupStatusCmd)
	backupCmd.AddCommand(backupHistoryCmd)
	backupCmd.AddCommand(backupSnapshotsCmd)

	rootCmd.AddCommand(backupCmd)
}
