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
	Short: "Manage and test alert notification channels",
	Long: `Inspect configured webhook notification channels and verify alert delivery.

Configuration file: $XDG_CONFIG_HOME/opspulse/notifications.yaml`,
}

var notifyListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all configured notification channels",
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
	Short: "Send a test notification to verify webhook delivery",
	Long: `Send a test event payload to verify delivery to configured notification channels.
If [channel-name] is provided, only that channel is tested. Otherwise, all channels are tested.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		store := notify.NewDefaultStore()
		dispatcher := notify.NewDispatcher(store)

		var targetChannel string
		if len(args) > 0 {
			targetChannel = args[0]
			fmt.Printf("Testing notification channel %q...\n", targetChannel)
		} else {
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
