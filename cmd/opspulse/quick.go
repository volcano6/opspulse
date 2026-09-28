package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/server"
	"github.com/volcano6/opspulse/internal/shellquote"
)

var (
	psAll          bool
	logsFollow     bool
	logsTail       string
	logsTimestamps bool
)

type remoteContainerItem struct {
	ID         string `json:"ID"`
	Names      string `json:"Names"`
	Image      string `json:"Image"`
	Command    string `json:"Command"`
	CreatedAt  string `json:"CreatedAt"`
	RunningFor string `json:"RunningFor"`
	Status     string `json:"Status"`
	Ports      string `json:"Ports"`
	State      string `json:"State"`
}

var psCmd = &cobra.Command{
	Use:   "ps <server>",
	Short: "列出远端服务器上的 Docker 容器",
	Long: `通过 SSH 快速列出指定远端服务器上的 Docker 容器。
输出等价于远端 docker ps（容器 ID、镜像、命令、状态、端口、名称）。

示例：
  ops ps vps-1            # 运行中的容器
  ops ps vps-1 -a         # 连已停止的一起列`,
	Args: cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		serverName := args[0]
		store := server.NewDefaultStore()
		srv, err := store.Get(serverName)
		if err != nil {
			return err
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		exec := executor.NewSSHExecutor().WithServerResolver(store.Get).WithWarnWriter(os.Stderr)
		target := executor.NewServerTarget(*srv)

		var buf bytes.Buffer
		script := buildDockerPsScript(psAll)

		res, err := exec.Execute(ctx, target, "docker-ps", script, &buf)
		if err != nil {
			if strings.Contains(buf.String(), "ERR_DOCKER_NOT_FOUND") {
				return fmt.Errorf("docker is not installed or not in PATH on server %q", serverName)
			}
			return fmt.Errorf("failed to list containers on %s: %w", serverName, err)
		}
		if !res.Success {
			return fmt.Errorf("docker ps failed on %s: %v", serverName, res.Error)
		}

		containers, err := parseDockerPsOutput(buf.String())
		if err != nil {
			return fmt.Errorf("parse container list: %w", err)
		}

		if len(containers) == 0 {
			if psAll {
				fmt.Printf("No containers found on server %q.\n", serverName)
			} else {
				fmt.Printf("No running containers found on server %q. (Use -a to show stopped containers)\n", serverName)
			}
			return nil
		}

		return renderDockerPsTable(os.Stdout, containers)
	},
}

func buildDockerPsScript(showAll bool) string {
	allFlag := ""
	if showAll {
		allFlag = "-a "
	}
	return fmt.Sprintf(`if ! command -v docker >/dev/null 2>&1; then
  echo "ERR_DOCKER_NOT_FOUND"
  exit 127
fi
docker ps %s--format '{{json .}}'
`, allFlag)
}

func parseDockerPsOutput(raw string) ([]remoteContainerItem, error) {
	var items []remoteContainerItem
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return items, nil
	}

	lines := strings.Split(trimmed, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || line == "ERR_DOCKER_NOT_FOUND" {
			continue
		}
		var item remoteContainerItem
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			return nil, fmt.Errorf("invalid json %q: %w", line, err)
		}
		items = append(items, item)
	}
	return items, nil
}

func renderDockerPsTable(w io.Writer, containers []remoteContainerItem) error {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(tw, "CONTAINER ID\tIMAGE\tCOMMAND\tCREATED\tSTATUS\tPORTS\tNAMES")
	_, _ = fmt.Fprintln(tw, "------------\t-----\t-------\t-------\t------\t-----\t-----")

	for _, c := range containers {
		id := c.ID
		if len(id) > 12 {
			id = id[:12]
		}

		cmdStr := c.Command
		if len(cmdStr) > 25 {
			cmdStr = cmdStr[:22] + "..."
		}

		created := c.RunningFor
		if created == "" {
			created = c.CreatedAt
		}

		ports := c.Ports
		if ports == "" {
			ports = "-"
		}

		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			id, c.Image, cmdStr, created, c.Status, ports, c.Names)
	}
	return tw.Flush()
}

var logsCmd = &cobra.Command{
	Use:   "logs <server> <container>",
	Short: "查看或跟随远端 Docker 容器日志",
	Long: `通过 SSH 查看或实时跟随远端服务器上 Docker 容器的日志。

示例：
  ops logs vps-1 nginx                    # 最近 100 行日志
  ops logs vps-1 nginx --tail 50          # 最近 50 行日志
  ops logs vps-1 nginx --follow           # 实时跟随（Ctrl+C 退出）
  ops logs vps-1 nginx --timestamps       # 每行前面加上时间戳`,
	Args: cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		serverName := args[0]
		containerName := args[1]

		store := server.NewDefaultStore()
		srv, err := store.Get(serverName)
		if err != nil {
			return err
		}

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		script, err := buildDockerLogsScript(containerName, logsTail, logsFollow, logsTimestamps)
		if err != nil {
			return err
		}

		exec := executor.NewSSHExecutor().WithServerResolver(store.Get).WithWarnWriter(os.Stderr)
		target := executor.NewServerTarget(*srv)

		res, err := exec.Execute(ctx, target, "docker-logs", script, os.Stdout)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return fmt.Errorf("failed to get logs on %s: %w", serverName, err)
		}
		if !res.Success {
			return fmt.Errorf("docker logs exited with code %d on %s", res.ExitCode, serverName)
		}

		return nil
	},
}

var validContainerNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

func validateContainerName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fmt.Errorf("container name cannot be empty")
	}
	if !validContainerNamePattern.MatchString(trimmed) {
		return fmt.Errorf("invalid container name %q: must match [a-zA-Z0-9][a-zA-Z0-9_.-]*", name)
	}
	return nil
}

func validateTail(tail string) error {
	trimmed := strings.TrimSpace(tail)
	if trimmed == "" || trimmed == "all" {
		return nil
	}
	if _, err := strconv.ParseUint(trimmed, 10, 64); err != nil {
		return fmt.Errorf("invalid --tail value %q: must be a positive integer or 'all'", tail)
	}
	return nil
}

func buildDockerLogsScript(container, tail string, follow, timestamps bool) (string, error) {
	if err := validateContainerName(container); err != nil {
		return "", err
	}
	if err := validateTail(tail); err != nil {
		return "", err
	}

	var flags []string
	if strings.TrimSpace(tail) != "" {
		flags = append(flags, "--tail", shellquote.Quote(strings.TrimSpace(tail)))
	}
	if timestamps {
		flags = append(flags, "-t")
	}
	if follow {
		flags = append(flags, "-f")
	}

	flagStr := ""
	if len(flags) > 0 {
		flagStr = strings.Join(flags, " ") + " "
	}

	return fmt.Sprintf("docker logs %s%s\n", flagStr, shellquote.Quote(strings.TrimSpace(container))), nil
}

func completeLogsArgs(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return completeServerNames(nil, nil, "")
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

func init() {
	psCmd.Flags().BoolVarP(&psAll, "all", "a", false, "显示全部容器（默认只显示运行中的）")
	psCmd.ValidArgsFunction = completeServerNames

	logsCmd.Flags().BoolVar(&logsFollow, "follow", false, "实时跟随日志输出")
	logsCmd.Flags().StringVarP(&logsTail, "tail", "n", "100", "从日志末尾显示的行数")
	logsCmd.Flags().BoolVar(&logsTimestamps, "timestamps", false, "显示时间戳")
	logsCmd.ValidArgsFunction = completeLogsArgs

	rootCmd.AddCommand(psCmd)
	rootCmd.AddCommand(logsCmd)
}
