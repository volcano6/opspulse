package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/asset"
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
	Short: "运行后台调度器守护进程，自动执行定时备份",
	Long: `启动 OpsPulse 调度器守护进程，按 $XDG_CONFIG_HOME/opspulse/backups.yaml 中配置的
cron 表达式执行备份作业。

作业执行完毕或出现故障时，按 $XDG_CONFIG_HOME/opspulse/notifications.yaml 的配置
自动分发告警通知。

收到 SIGINT 与 SIGTERM 信号时优雅退出，等待正在执行中的作业完成。

示例：
  ops daemon          # 运行调度器直到被中断
  ops daemon --once   # 按序执行全部定时作业一次后退出`,
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
		assetStore := asset.NewDefaultStore()
		exec := executor.NewSSHExecutor().WithServerResolver(serverStore.Get).WithWarnWriter(os.Stdout)
		runner := backup.NewRunnerWithStores(exec, serverStore, backupRepo, backupStore, assetStore)
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
	daemonCmd.Flags().BoolVar(&daemonRunOnce, "once", false, "按序执行全部定时备份作业一次后立即退出")
	rootCmd.AddCommand(daemonCmd)
}
