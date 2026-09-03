package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/backup"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/server"
	"github.com/volcano6/opspulse/internal/storage"
)

var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Manage and execute backup jobs",
	Long: `Define, inspect, execute, and monitor restic backup jobs across servers.
Records structured metrics, snapshot IDs, and historical logs in SQLite.`,
}

var backupListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all configured backup jobs",
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

		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
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
	backupRunParallel int
)

var backupRunCmd = &cobra.Command{
	Use:   "run <job1,job2... | all>",
	Short: "Execute one or more backup jobs",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
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
		exec := executor.NewSSHExecutor() // SSH executor handles remote servers
		// Wrap with multi-target capability: if target is local, runner uses LocalExecutor
		runner := backup.NewRunner(exec, serverStore, backupRepo)

		pool := backup.NewPool(runner, backupRunParallel)
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
	Short: "Show the latest status of all configured backup jobs",
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

		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
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

			addedStr := backup.FormatBytes(r.DataAddedBytes)
			totalStr := backup.FormatBytes(r.TotalBytes)
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
	Short: "Show historical execution runs for a specific backup job",
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

		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
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
			addedStr := backup.FormatBytes(r.DataAddedBytes)
			totalStr := backup.FormatBytes(r.TotalBytes)
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
	Short: "Query and list remote snapshots for a backup job",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		jobName := args[0]
		store := backup.NewDefaultStore()
		job, err := store.Get(jobName)
		if err != nil {
			return err
		}

		exec := executor.NewSSHExecutor()
		serverStore := server.NewDefaultStore()
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

		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
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
		comps = append(comps, "all\tExecute all configured backup jobs")
	}
	for _, j := range jobs {
		if !selected[j.Name] {
			comps = append(comps, fmt.Sprintf("%s\t%s (%s)", j.Name, j.Server, j.Backend))
		}
	}
	return comps, cobra.ShellCompDirectiveNoFileComp
}

func init() {
	backupRunCmd.Flags().BoolVar(&backupRunDryRun, "dry-run", false, "Simulate execution without running restic")
	backupRunCmd.Flags().IntVarP(&backupRunParallel, "parallel", "p", 0, "Maximum concurrent jobs (0 = unlimited)")

	backupHistoryCmd.Flags().IntVarP(&historyLimit, "limit", "n", 20, "Maximum number of history records to show")

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
