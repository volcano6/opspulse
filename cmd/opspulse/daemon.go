package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/backup"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/notify"
	"github.com/volcano6/opspulse/internal/scheduler"
	"github.com/volcano6/opspulse/internal/server"
	"github.com/volcano6/opspulse/internal/storage"
)

var daemonRunOnce bool

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run background scheduler daemon for automated backups",
	Long: `Start the OpsPulse scheduler daemon to execute backup jobs according to
their cron expressions configured in $XDG_CONFIG_HOME/opspulse/backups.yaml.

Alert notifications will be dispatched upon completion or failure according to
$XDG_CONFIG_HOME/opspulse/notifications.yaml.

Signals SIGINT and SIGTERM trigger a graceful shutdown, waiting for in-flight jobs to complete.`,
	RunE: func(_ *cobra.Command, _ []string) error {
		backupStore := backup.NewDefaultStore()
		serverStore := server.NewDefaultStore()
		notifyStore := notify.NewDefaultStore()

		db, err := storage.OpenDefault()
		if err != nil {
			return fmt.Errorf("failed to open database: %w", err)
		}
		defer func() { _ = db.Close() }()

		backupRepo := storage.NewBackupRepo(db)
		exec := executor.NewSSHExecutor()
		runner := backup.NewRunner(exec, serverStore, backupRepo)
		dispatcher := notify.NewDispatcher(notifyStore)

		sched := scheduler.New(backupStore, runner, dispatcher, os.Stdout)

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		if daemonRunOnce {
			return sched.RunOnce(ctx)
		}

		return sched.Run(ctx)
	},
}

func init() {
	daemonCmd.Flags().BoolVar(&daemonRunOnce, "once", false, "Execute all scheduled backup jobs once sequentially and exit immediately")
	rootCmd.AddCommand(daemonCmd)
}
