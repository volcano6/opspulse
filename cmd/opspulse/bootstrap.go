package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/bootstrap"
	"github.com/volcano6/opspulse/internal/server"
	"github.com/volcano6/opspulse/internal/template"
)

var (
	bootstrapTemplates []string
	bootstrapDryRun    bool
	bootstrapContinue  bool
)

var bootstrapCmd = &cobra.Command{
	Use:   "bootstrap <server1,server2...>",
	Short: "用指定脚本模板初始化服务器",
	Long: `通过 SSH 依次执行一系列脚本模板，初始化并配置一台或多台服务器。
日志实时输出到终端，并保存到 $XDG_DATA_HOME/opspulse/logs/。

示例：
  ops bootstrap web-01 -t base,docker     # 对一台服务器应用两个模板
  ops bootstrap web-01,db-01 -t docker    # 把同一个模板应用到多台服务器
  ops bootstrap local -t base --dry-run   # 在本机预演这次执行`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		if len(bootstrapTemplates) == 0 {
			return fmt.Errorf("at least one template must be specified using --templates or -t (e.g. -t docker,base)")
		}

		// Split servers by comma or space
		var serverNames []string
		for _, arg := range args {
			for _, s := range strings.Split(arg, ",") {
				if trimmed := strings.TrimSpace(s); trimmed != "" {
					serverNames = append(serverNames, trimmed)
				}
			}
		}

		// Split templates by comma or multiple flags
		var templateNames []string
		for _, item := range bootstrapTemplates {
			for _, t := range strings.Split(item, ",") {
				if trimmed := strings.TrimSpace(t); trimmed != "" {
					templateNames = append(templateNames, trimmed)
				}
			}
		}

		svc := bootstrap.NewDefaultService()
		opts := bootstrap.RunOptions{
			ServerNames:   serverNames,
			TemplateNames: templateNames,
			DryRun:        bootstrapDryRun,
			StopOnError:   !bootstrapContinue,
		}

		summary, err := svc.Run(context.Background(), opts, os.Stdout)
		if summary != nil {
			summary.PrintTable(os.Stdout)
		}
		if err != nil {
			return err
		}

		if summary != nil && summary.FailureCount > 0 {
			return fmt.Errorf("%d template(s) failed during bootstrap", summary.FailureCount)
		}

		return nil
	},
}

func completeBootstrapServerArgs(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	store := server.NewDefaultStore()
	servers, err := store.List()
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
		if !selected["local"] {
			comps = append(comps, fmt.Sprintf("%slocal\t本机执行", prefix))
		}
		for _, s := range servers {
			if !selected[s.Name] {
				if s.Description != "" {
					comps = append(comps, fmt.Sprintf("%s%s\t%s (%s)", prefix, s.Name, s.Host, s.Description))
				} else {
					comps = append(comps, fmt.Sprintf("%s%s\t%s", prefix, s.Name, s.Host))
				}
			}
		}
		return comps, cobra.ShellCompDirectiveNoFileComp
	}

	var comps []string
	if !selected["local"] {
		comps = append(comps, "local\t本机执行")
	}
	for _, s := range servers {
		if !selected[s.Name] {
			if s.Description != "" {
				comps = append(comps, fmt.Sprintf("%s\t%s (%s)", s.Name, s.Host, s.Description))
			} else {
				comps = append(comps, fmt.Sprintf("%s\t%s", s.Name, s.Host))
			}
		}
	}
	return comps, cobra.ShellCompDirectiveNoFileComp
}

func completeBootstrapTemplateFlag(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	loader := template.NewDefaultLoader()
	templates, err := loader.List()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}

	selected := make(map[string]bool)
	if idx := strings.LastIndex(toComplete, ","); idx != -1 {
		prefix := toComplete[:idx+1]
		currentParts := strings.Split(toComplete[:idx], ",")
		for _, p := range currentParts {
			if p = strings.TrimSpace(p); p != "" {
				name := p
				if colonIdx := strings.IndexAny(p, ":="); colonIdx != -1 {
					name = p[:colonIdx]
				}
				selected[name] = true
			}
		}

		var comps []string
		for _, t := range templates {
			if !selected[t.Metadata.Name] {
				if t.Metadata.Description != "" {
					comps = append(comps, fmt.Sprintf("%s%s\t%s", prefix, t.Metadata.Name, t.Metadata.Description))
				} else {
					comps = append(comps, prefix+t.Metadata.Name)
				}
			}
		}
		return comps, cobra.ShellCompDirectiveNoSpace | cobra.ShellCompDirectiveNoFileComp
	}

	var comps []string
	for _, t := range templates {
		if t.Metadata.Description != "" {
			comps = append(comps, fmt.Sprintf("%s\t%s", t.Metadata.Name, t.Metadata.Description))
		} else {
			comps = append(comps, t.Metadata.Name)
		}
	}
	return comps, cobra.ShellCompDirectiveNoFileComp
}

func init() {
	bootstrapCmd.Flags().StringSliceVarP(&bootstrapTemplates, "templates", "t", nil, "要执行的模板列表，逗号分隔（例如 -t base,security,docker 或 -t base -t docker）")
	bootstrapCmd.Flags().BoolVar(&bootstrapDryRun, "dry-run", false, "预演执行，不建立 SSH 连接")
	bootstrapCmd.Flags().BoolVar(&bootstrapContinue, "continue-on-error", false, "出错后继续执行剩余的模板/服务器")

	bootstrapCmd.ValidArgsFunction = completeBootstrapServerArgs
	_ = bootstrapCmd.RegisterFlagCompletionFunc("templates", completeBootstrapTemplateFlag)

	rootCmd.AddCommand(bootstrapCmd)
}
