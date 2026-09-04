package main

import (
	"context"
	"fmt"
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
	Short: "Restore data from restic backup snapshots",
	Long: `Execute restore operations from restic backup snapshots with support for
cross-server migration, path remapping, single-asset targeted restore, and dry-run preview.`,
}

var (
	restoreRunSnapshot     string
	restoreRunTargetServer string
	restoreRunTargetPath   string
	restoreRunAssetID      string
	restoreRunDryRun       bool
	restoreRunNoStart      bool
	restoreRunAs           string
)

var restoreRunCmd = &cobra.Command{
	Use:   "run <job-name>",
	Short: "Execute a restore operation from a backup snapshot",
	Long: `Restore files from a restic backup snapshot to the original or a different server.

Examples:
  # Restore latest snapshot to original server and paths
  opspulse restore run blog-backup

  # Restore a specific snapshot
  opspulse restore run blog-backup --snapshot abc12345

  # Cross-server migration: restore to a new VPS
  opspulse restore run blog-backup --target-server new-vps --target-path /data/blog

  # Targeted single-asset restore
  opspulse restore run blog-backup --asset blog-mysql

  # Preview files without actually restoring
  opspulse restore run blog-backup --dry-run`,
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
		exec := executor.NewSSHExecutor()

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

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		runRecord, err := runner.Run(ctx, *job, opts, os.Stdout)
		if err != nil {
			return err
		}

		if runRecord != nil && runRecord.Status == "failed" {
			return fmt.Errorf("restore failed: %s", runRecord.ErrorMessage)
		}

		return nil
	},
}

var restoreHistoryLimit int

var restoreHistoryCmd = &cobra.Command{
	Use:   "history [job-name]",
	Short: "Show historical restore execution records",
	Long: `Display the history of restore operations, optionally filtered by backup job name.
Shows status, snapshot ID, source/target servers, duration, and timestamps.`,
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
	restoreRunCmd.Flags().StringVar(&restoreRunSnapshot, "snapshot", "latest", "Snapshot ID to restore from ('latest' for most recent)")
	restoreRunCmd.Flags().StringVar(&restoreRunTargetServer, "target-server", "", "Target server for cross-server migration (default: same as source)")
	restoreRunCmd.Flags().StringVar(&restoreRunTargetPath, "target-path", "", "Override restore target path for path remapping (default: original paths)")
	restoreRunCmd.Flags().StringVar(&restoreRunAssetID, "asset", "", "Restore a specific asset only (by asset ID)")
	restoreRunCmd.Flags().BoolVar(&restoreRunDryRun, "dry-run", false, "Preview files without actually restoring")
	restoreRunCmd.Flags().BoolVar(&restoreRunNoStart, "no-start", false, "Do not automatically start containers or import database after restore")
	restoreRunCmd.Flags().StringVar(&restoreRunAs, "as", "", "Rename container/service project name on target server")

	restoreHistoryCmd.Flags().IntVarP(&restoreHistoryLimit, "limit", "n", 20, "Maximum number of history records to show")

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
