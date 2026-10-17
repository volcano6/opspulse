package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/volcano6/opspulse/internal/capability"
	"github.com/volcano6/opspulse/internal/migration"
)

// dockerHealthTimeout bounds one post-start health probe. Every service gets a
// readiness budget inside the script (about 30s for the database/HTTP loops),
// so a batch plan needs room for several of them plus the SSH round-trips.
const dockerHealthTimeout = 5 * time.Minute

var (
	dockerHealthJSON   bool
	dockerHealthServer string
)

var dockerHealthCmd = &cobra.Command{
	Use:   "health <plan-file>",
	Short: "迁移后检查容器、端口、数据库与可选 HTTP 是否可用",
	Long: `按迁移 plan 检查目标机上的服务是否真的可用，而不是只看进程有没有起来。

对每个服务依次检查：
  - 容器：Compose 项目/服务标签解析到容器后，确认状态是 running 且没有处于 restarting、
    没有发生重启（重启次数 > 0 视为崩溃循环）；
  - 端口：plan 记录的每个固定宿主端口是否在监听（ss 或 netstat，都没有时如实报 manual）；
  - 数据库：MySQL/MariaDB 走 mysqladmin ping、PostgreSQL 走 pg_isready，在容器内轮询到就绪；
  - HTTP：仅当 plan 的 services[].health.http_url 配置了地址时才检查，未配置时明确显示 skipped。

本命令只读：不启动服务、不改配置、不写任何文件。每项非 ok 的检查都会给出下一步命令或人工确认方式。
默认检查 plan 记录的目标服务器，可用 --server 覆盖。

示例：
  ops docker health migration-plan.yaml
  ops docker health migration-plan.yaml --server vps-02
  ops docker health migration-plan.yaml --json`,
	Args: cobra.ExactArgs(1),
	RunE: runDockerHealth,
}

func runDockerHealth(_ *cobra.Command, args []string) error {
	plan, err := migration.Load(args[0])
	if err != nil {
		return err
	}
	if len(plan.Services) == 0 {
		return errors.New("plan 中没有需要检查的服务")
	}

	serverName := strings.TrimSpace(dockerHealthServer)
	if serverName == "" {
		serverName = strings.TrimSpace(plan.Target.Server)
	}
	if serverName == "" {
		return errors.New("plan 未记录目标服务器，无法执行健康检查：请用 --server <目标机> 指定，或重新生成带 --target 的 plan")
	}

	ctx, cancel := context.WithTimeout(context.Background(), dockerHealthTimeout)
	defer cancel()

	report, err := probeServiceHealth(ctx, serverName, migration.HealthTargets(plan))
	if err != nil {
		return err
	}

	// stdout carries only the JSON document in that mode, so the human report
	// and the "unhealthy" note go to stderr.
	out := io.Writer(os.Stdout)
	if dockerHealthJSON {
		out = os.Stderr
	}
	writeDockerHealthHuman(out, plan, report)
	if dockerHealthJSON {
		return writeJSON(os.Stdout, dockerHealthJSONDocument(serverName, report))
	}

	// Exit code contract: a service the tool proved broken fails the command, a
	// service it could not check is reported as work left for the operator. Only
	// the first is a failure of the migration.
	if failed := report.Failed(); len(failed) > 0 {
		return fmt.Errorf("%d/%d 个服务未通过健康检查：请按上面的「下一步」处理",
			len(failed), len(report.Services))
	}
	return nil
}

// probeServiceHealth runs the post-start health probe through the shared remote
// capability seam. It is the single implementation the standalone command and
// the apply step both call, so a migration cannot end with a different verdict
// than the one `ops docker health` reports.
func probeServiceHealth(ctx context.Context, serverName string, targets []capability.HealthTarget) (*capability.HealthReport, error) {
	if len(targets) == 0 {
		return &capability.HealthReport{Server: serverName}, nil
	}
	target, remote, err := resolveDockerRemote(serverName)
	if err != nil {
		return nil, err
	}
	return remote.HealthCheck(ctx, target, capability.HealthOptions{Targets: targets})
}

type dockerHealthServiceView struct {
	Service   string                         `json:"service"`
	Container string                         `json:"container,omitempty"`
	Healthy   bool                           `json:"healthy"`
	Checks    []capability.HealthCheckResult `json:"checks"`
}

type dockerHealthDocument struct {
	Kind string `json:"kind"`
	// Healthy mirrors the command's exit-code contract: no service was proven
	// broken. A service whose checks could not be automated is listed in
	// NeedsManualCheck instead, because that is neither a pass nor a failure.
	Healthy          bool                      `json:"healthy"`
	Server           string                    `json:"server"`
	NeedsManualCheck []string                  `json:"needs_manual_check,omitempty"`
	Services         []dockerHealthServiceView `json:"services"`
}

func dockerHealthJSONDocument(serverName string, report *capability.HealthReport) dockerHealthDocument {
	doc := dockerHealthDocument{
		Kind:    "docker_health_result",
		Server:  serverName,
		Healthy: len(report.Failed()) == 0,
	}
	for _, service := range report.Unverified() {
		doc.NeedsManualCheck = append(doc.NeedsManualCheck, service.Service)
	}
	for _, service := range report.Services {
		doc.Services = append(doc.Services, dockerHealthServiceView{
			Service:   service.Service,
			Container: service.Container,
			Healthy:   service.Healthy,
			Checks:    service.Checks,
		})
	}
	if doc.Services == nil {
		doc.Services = []dockerHealthServiceView{}
	}
	return doc
}

// writeDockerHealthHuman renders the per-service health report. Every non-ok
// check prints its next step: a failed health check that does not say what to do
// leaves the operator exactly where the migration did.
func writeDockerHealthHuman(w io.Writer, plan *migration.Plan, report *capability.HealthReport) {
	_, _ = fmt.Fprintf(w, "健康检查（源 %s → 目标 %s，服务 %d 个）\n",
		plan.Source.Server, report.Server, len(report.Services))

	for _, service := range report.Services {
		status := dockerHealthServiceStatus(service)
		_, _ = fmt.Fprintf(w, "\n服务 %s", service.Service)
		if service.Container != "" {
			_, _ = fmt.Fprintf(w, "（容器 %s）", service.Container)
		}
		_, _ = fmt.Fprintf(w, ": %s\n", status)
		for _, check := range service.Checks {
			_, _ = fmt.Fprintf(w, "  [%s] %s：%s\n", check.Status, check.Label, check.Detail)
			if check.NextStep != "" {
				_, _ = fmt.Fprintf(w, "     下一步：%s\n", check.NextStep)
			}
		}
	}

	_, _ = fmt.Fprintln(w)
	writeDockerHealthConclusion(w, report)
}

// dockerHealthServiceStatus is the per-service verdict word used by the human
// report. It distinguishes "proven broken" from "could not be confirmed",
// because only the first one is a migration failure.
func dockerHealthServiceStatus(service capability.ServiceHealth) string {
	switch {
	case len(service.FailedChecks()) > 0:
		return "未通过"
	case len(service.ManualChecks()) > 0:
		return "需人工确认"
	default:
		return "通过"
	}
}

func writeDockerHealthConclusion(w io.Writer, report *capability.HealthReport) {
	total := len(report.Services)
	failed := report.Failed()
	manual := report.Unverified()

	if len(failed) > 0 {
		_, _ = fmt.Fprintf(w, "结论: %d/%d 个服务未通过健康检查；请按上面的「下一步」逐项处理，处理后可重新执行 `ops docker health <plan-file>` 复核。\n",
			len(failed), total)
	}
	if len(manual) > 0 {
		names := make([]string, 0, len(manual))
		for _, service := range manual {
			names = append(names, service.Service)
		}
		_, _ = fmt.Fprintf(w, "结论: %s 的部分检查项在目标机上无法自动执行，需要人工确认（见上面的「下一步」）。\n",
			strings.Join(names, ", "))
	}
	if len(failed) == 0 && len(manual) == 0 {
		_, _ = fmt.Fprintf(w, "结论: %d/%d 个服务通过健康检查（进程在跑不等于服务可用，这里已按容器/端口/数据库/HTTP 逐项确认）。\n",
			total, total)
	}
}

func init() {
	dockerHealthCmd.Flags().BoolVar(&dockerHealthJSON, "json", false, "以 JSON 输出健康检查结果（执行过程走 stderr）")
	dockerHealthCmd.Flags().StringVar(&dockerHealthServer, "server", "", "覆盖 plan 记录的目标服务器")

	dockerCmd.AddCommand(dockerHealthCmd)
}
