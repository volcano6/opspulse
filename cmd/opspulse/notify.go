package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/notify"
)

var notifyCmd = &cobra.Command{
	Use:   "notify",
	Short: "管理与测试告警通知渠道",
	Long: `查看已配置的 Webhook 通知渠道并验证告警投递。

配置文件：$XDG_CONFIG_HOME/opspulse/notifications.yaml

示例：
  ops notify list                 # 列出全部已配置渠道
  ops notify test                 # 向全部渠道发送测试事件
  ops notify test ops-alerts      # 仅向指定渠道发送测试事件`,
}

var notifyListCmd = &cobra.Command{
	Use:   "list",
	Short: "列出全部已配置通知渠道",
	RunE: func(_ *cobra.Command, _ []string) error {
		store := notify.NewDefaultStore()
		channels, err := store.List()
		if err != nil {
			return fmt.Errorf("failed to list notification channels: %w", err)
		}

		if len(channels) == 0 {
			fmt.Printf("No notification channels configured.\nAdd channels to %s\n", store.FilePath())
			return nil
		}

		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		_, _ = fmt.Fprintln(tw, "NAME\tTYPE\tTRIGGER\tURL")
		_, _ = fmt.Fprintln(tw, "----\t----\t-------\t---")

		for _, ch := range channels {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
				ch.Name, ch.Type, ch.EffectiveTrigger(), ch.URL)
		}

		return tw.Flush()
	},
}

var notifyTestCmd = &cobra.Command{
	Use:   "test [channel-name]",
	Short: "发送测试通知验证 Webhook 投递",
	Long: `向已配置的通知渠道发送一条测试事件载荷，验证投递是否正常。
提供 [channel-name] 时只测试该渠道，否则测试全部渠道。`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		store := notify.NewDefaultStore()
		dispatcher := notify.NewDispatcher(store)

		var targetChannel string
		if len(args) > 0 {
			targetChannel = args[0]
			fmt.Printf("Testing notification channel %q...\n", targetChannel)
		} else {
			// Probe first: announcing "testing all channels" and then failing
			// because there are none reads as a delivery problem when the real
			// cause is an empty configuration file.
			channels, err := store.List()
			if err != nil {
				return fmt.Errorf("failed to list notification channels: %w", err)
			}
			if len(channels) == 0 {
				return fmt.Errorf("no notification channels configured in %s", store.FilePath())
			}
			fmt.Println("Testing all configured notification channels...")
		}

		if err := dispatcher.SendTest(context.Background(), targetChannel); err != nil {
			return fmt.Errorf("notification test failed: %w", err)
		}

		if targetChannel != "" {
			fmt.Printf("✅ Test notification sent successfully to %q.\n", targetChannel)
		} else {
			fmt.Println("✅ Test notifications sent successfully to all configured channels.")
		}

		return nil
	},
}

func completeNotificationChannels(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	store := notify.NewDefaultStore()
	channels, err := store.List()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	var comps []string
	for _, ch := range channels {
		comps = append(comps, fmt.Sprintf("%s\t%s (%s)", ch.Name, ch.Type, ch.EffectiveTrigger()))
	}
	return comps, cobra.ShellCompDirectiveNoFileComp
}

func init() {
	notifyTestCmd.ValidArgsFunction = completeNotificationChannels

	notifyCmd.AddCommand(notifyListCmd)
	notifyCmd.AddCommand(notifyTestCmd)

	rootCmd.AddCommand(notifyCmd)
}
