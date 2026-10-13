package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/cliutil"
	"github.com/volcano6/opspulse/internal/notify"
)

var notifyCmd = &cobra.Command{
	Use:   "notify",
	Short: "管理与测试告警通知渠道",
	Long: `查看已配置的 Webhook 通知渠道并验证告警投递。

配置文件：$XDG_CONFIG_HOME/opspulse/notifications.yaml

示例：
  ops notify list                 # 列出全部已配置渠道（URL 默认脱敏）
  ops notify list --show-secret   # 显示完整 webhook URL（仅限交互式终端）
  ops notify test                 # 向全部渠道发送测试事件
  ops notify test ops-alerts      # 仅向指定渠道发送测试事件`,
}

// notifyListShowSecret prints full webhook URLs instead of the redacted form.
// The flag is the deliberate opt-in the docs point at: webhook tokens are part
// of the URL, so the safe default must be the redacted one.
var notifyListShowSecret bool

// notifySecretOutputAllowed reports whether --show-secret may print full URLs.
// Both ends must be a terminal: an interactive stdin plus `> urls.txt` would
// still write the tokens into a file, and the warning line would be swallowed
// by the same redirection.
func notifySecretOutputAllowed(in io.Reader, out io.Writer) bool {
	return readerIsInteractive(in) && writerIsTerminal(out)
}

var notifyListCmd = &cobra.Command{
	Use:   "list",
	Short: "列出全部已配置通知渠道",
	Long: `列出全部已配置通知渠道。

Webhook URL 的 path 通常就是鉴权 token，因此默认只显示 scheme://host/<redacted>。
需要核对完整 URL 时用 --show-secret：该选项要求 stdin 与 stdout 都是交互式终端，
否则直接拒绝，避免凭据被写进脚本输出、重定向文件或 CI 日志。`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if notifyListShowSecret && !notifySecretOutputAllowed(cmd.InOrStdin(), os.Stdout) {
			return fmt.Errorf("refusing to print full webhook secrets in a non-interactive shell (stdin and stdout must both be a terminal); re-run 'ops notify list --show-secret' in an interactive terminal without redirecting its output")
		}

		store := notify.NewDefaultStore()
		channels, err := store.List()
		if err != nil {
			return fmt.Errorf("failed to list notification channels: %w", err)
		}

		if len(channels) == 0 {
			fmt.Printf("No notification channels configured.\nAdd channels to %s\n", store.FilePath())
			return nil
		}

		if notifyListShowSecret {
			fmt.Fprintln(os.Stderr, "⚠️  --show-secret: the URLs below contain live webhook credentials. Do not paste, redirect or log them.")
		}

		tw := cliutil.NewTabWriter(os.Stdout)
		_, _ = fmt.Fprintln(tw, "NAME\tTYPE\tTRIGGER\tURL")
		_, _ = fmt.Fprintln(tw, "----\t----\t-------\t---")

		for _, ch := range channels {
			urlCell := notify.RedactURL(ch.URL)
			if notifyListShowSecret {
				urlCell = ch.URL
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
				ch.Name, ch.Type, ch.EffectiveTrigger(), urlCell)
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

	notifyListCmd.Flags().BoolVar(&notifyListShowSecret, "show-secret", false,
		"显示完整 webhook URL（含 token），仅限交互式终端")

	notifyCmd.AddCommand(notifyListCmd)
	notifyCmd.AddCommand(notifyTestCmd)

	rootCmd.AddCommand(notifyCmd)
}
