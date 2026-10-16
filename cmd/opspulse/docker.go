package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/capability"
	"github.com/volcano6/opspulse/internal/cliutil"
	"github.com/volcano6/opspulse/internal/docker"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/server"
)

// dockerProbeTimeout bounds every docker discovery round-trip. The capability
// probe already bounds its registry lookup with `timeout 10`, so this is a
// generous outer limit for a slow SSH link.
const dockerProbeTimeout = 60 * time.Second

// newDockerRemote builds the single remote capability seam for a target. It is
// a package-level variable so tests can inject a fake and never contact a real
// host.
var newDockerRemote = func(target executor.Target) capability.Remote {
	if target.IsLocal {
		return capability.NewRemote(executor.NewLocalExecutor())
	}
	store := server.NewDefaultStore()
	exec := executor.NewSSHExecutor().WithServerResolver(store.Get).WithWarnWriter(os.Stderr)
	return capability.NewRemote(exec)
}

var (
	dockerListAll     bool
	dockerListJSON    bool
	dockerInspectJSON bool
)

var dockerCmd = &cobra.Command{
	Use:   "docker",
	Short: "发现远端 Docker 服务并报告迁移能力",
	Long: `只读地发现远端服务器上的 Docker 服务，并报告每个容器的迁移能力与风险。

本组命令只做只读探测（docker ps / docker inspect / 环境能力检查），不会执行数据库
dump、volume 归档、restic 备份或 Compose up，也不会修改远端任何文件。

子命令：
  list     列出容器及每个容器的迁移摘要
  inspect  查看单个容器的结构化迁移分析（含远端能力与缺口提示）
  plan     为一个或多个容器生成只读、可保存的迁移 plan
  plan show 读取并显示已保存的 plan
  apply    按 plan 重新校验源/目标后安全执行迁移`,
}

var dockerListCmd = &cobra.Command{
	Use:   "list <server>",
	Short: "列出指定服务器上的 Docker 容器",
	Long: `列出指定服务器上的 Docker 容器，并标注每个容器是 Compose 管理还是普通 docker run。

示例：
  ops docker list vps-01          # 只列运行中的容器
  ops docker list vps-01 --all    # 连已停止的一起列
  ops docker list vps-01 --json   # 结构化输出，便于脚本处理`,
	Args:              cobra.ExactArgs(1),
	RunE:              runDockerList,
	ValidArgsFunction: completeServerNames,
}

var dockerInspectCmd = &cobra.Command{
	Use:   "inspect <server> <container>",
	Short: "查看单个容器的迁移分析与远端能力",
	Long: `对指定容器做只读的迁移分析：编排方式、镜像、端口、named volume、bind mount、
数据库类型、外部宿主机依赖、敏感环境变量与各 Docker 属性的支持级别；同时报告远端缺少
Docker / Compose / restic / helper 镜像 / 数据库工具或磁盘空间不足等缺口。

示例：
  ops docker inspect vps-01 blog-db
  ops docker inspect vps-01 blog-db --json`,
	Args:              cobra.ExactArgs(2),
	RunE:              runDockerInspect,
	ValidArgsFunction: completeDockerInspectArgs,
}

func runDockerList(_ *cobra.Command, args []string) error {
	serverName := args[0]
	target, remote, err := resolveDockerRemote(serverName)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), dockerProbeTimeout)
	defer cancel()

	containers, err := remote.ListContainers(ctx, target, dockerListAll)
	if err != nil {
		return dockerFriendlyError(serverName, err)
	}

	if dockerListJSON {
		doc := dockerListDocument{Server: serverName, Containers: make([]dockerListItemView, 0, len(containers))}
		for _, c := range containers {
			doc.Containers = append(doc.Containers, newDockerListItemView(c))
		}
		return writeJSON(os.Stdout, doc)
	}
	return printDockerListTable(os.Stdout, serverName, containers, dockerListAll)
}

func runDockerInspect(_ *cobra.Command, args []string) error {
	serverName, containerName := args[0], args[1]
	target, remote, err := resolveDockerRemote(serverName)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), dockerProbeTimeout)
	defer cancel()

	info, err := remote.InspectContainer(ctx, target, containerName)
	if err != nil {
		return dockerFriendlyError(serverName, err)
	}
	analysis := docker.Analyze(info)

	// Probe the host with the container's own host paths so the report covers the
	// directories a migration actually has to prepare.
	report, err := remote.Probe(ctx, target, capability.ProbeOptions{Paths: analysis.HostPaths()})
	if err != nil {
		return err
	}

	view := newDockerContainerView(info, analysis)
	if dockerInspectJSON {
		doc := dockerInspectDocument{Server: serverName, Container: view, Analysis: analysis, Capability: report}
		return writeJSON(os.Stdout, doc)
	}
	writeDockerInspectHuman(os.Stdout, view, analysis, report)
	return nil
}

// resolveDockerRemote resolves a server name from the inventory and builds the
// capability seam for it.
func resolveDockerRemote(serverName string) (executor.Target, capability.Remote, error) {
	store := server.NewDefaultStore()
	target, err := executor.ResolveTarget(store, serverName)
	if err != nil {
		return executor.Target{}, nil, err
	}
	return target, newDockerRemote(target), nil
}

// dockerFriendlyError turns the seam's sentinel errors into actionable Chinese
// messages, leaving everything else untouched.
func dockerFriendlyError(serverName string, err error) error {
	switch {
	case errors.Is(err, capability.ErrDockerMissing):
		return fmt.Errorf("远端服务器 %q 未安装 Docker：无法发现或分析容器（请先在目标机安装并启动 Docker）", serverName)
	case errors.Is(err, docker.ErrContainerNotFound):
		return fmt.Errorf("服务器 %q 上找不到该容器：请用 `ops docker list %s` 确认名称", serverName, serverName)
	default:
		return err
	}
}

func completeDockerInspectArgs(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return completeServerNames(nil, nil, "")
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

// dockerMode labels a container as Compose-managed or a plain docker run
// workload.
func dockerMode(isCompose bool, project string) string {
	if !isCompose {
		return "run"
	}
	if project == "" {
		return "compose"
	}
	return "compose:" + project
}

func shortContainerID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func printDockerListTable(w io.Writer, serverName string, containers []docker.ContainerSummary, includeStopped bool) error {
	if len(containers) == 0 {
		if includeStopped {
			_, _ = fmt.Fprintf(w, "服务器 %q 上没有容器。\n", serverName)
		} else {
			_, _ = fmt.Fprintf(w, "服务器 %q 上没有运行中的容器。（用 --all 连已停止的一起列）\n", serverName)
		}
		return nil
	}

	tw := cliutil.NewTabWriter(w)
	_, _ = fmt.Fprintln(tw, "CONTAINER\tIMAGE\tMODE\tDATABASE\tSTATUS\tPORTS")
	_, _ = fmt.Fprintln(tw, "---------\t-----\t----\t--------\t------\t-----")
	for _, c := range containers {
		db := c.DatabaseEngine()
		if db == "" {
			db = "-"
		}
		status := c.Status
		if status == "" {
			status = c.State
		}
		if status == "" {
			status = "-"
		}
		ports := c.Ports
		if ports == "" {
			ports = "-"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			c.Name, c.Image, dockerMode(c.IsCompose(), c.ComposeProject()), db, status, ports)
	}
	return tw.Flush()
}

func writeDockerInspectHuman(w io.Writer, view dockerContainerView, analysis *docker.ContainerAnalysis, report *capability.Report) {
	_, _ = fmt.Fprintf(w, "容器: %s\n", view.Name)
	_, _ = fmt.Fprintf(w, "  ID:        %s\n", shortContainerID(view.ID))
	_, _ = fmt.Fprintf(w, "  镜像:      %s\n", view.Image)
	_, _ = fmt.Fprintf(w, "  编排:      %s\n", composeDescription(analysis))
	if view.RestartPolicy != "" {
		_, _ = fmt.Fprintf(w, "  重启策略:  %s\n", view.RestartPolicy)
	}
	if view.NetworkMode != "" {
		_, _ = fmt.Fprintf(w, "  网络模式:  %s\n", view.NetworkMode)
	}
	if len(view.Ports) > 0 {
		_, _ = fmt.Fprintf(w, "  端口:      %s\n", strings.Join(view.Ports, ", "))
	}

	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "挂载:")
	if len(analysis.NamedVolumes) == 0 {
		_, _ = fmt.Fprintln(w, "  named volume: 无")
	} else {
		for _, m := range analysis.NamedVolumes {
			_, _ = fmt.Fprintf(w, "  named volume: %s → %s\n", m.Name, m.Destination)
		}
	}
	if len(analysis.BindMounts) == 0 {
		_, _ = fmt.Fprintln(w, "  bind mount:   无")
	} else {
		for _, m := range analysis.BindMounts {
			_, _ = fmt.Fprintf(w, "  bind mount:   %s → %s\n", m.Source, m.Destination)
		}
	}

	if len(analysis.ExternalDeps) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "外部宿主机依赖:")
		for _, d := range analysis.ExternalDeps {
			requirement := "可选"
			if d.Required {
				requirement = "必需"
			}
			target := ""
			if d.Target != "" {
				target = " → " + d.Target
			}
			_, _ = fmt.Fprintf(w, "  [%s] %s%s（%s）\n", requirement, d.Source, target, d.Reason)
		}
	}

	if analysis.Database != nil {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "数据库:")
		_, _ = fmt.Fprintf(w, "  engine:   %s\n", analysis.Database.Engine)
		strategy := analysis.Database.Strategy
		if analysis.Database.Tool != "" {
			strategy = fmt.Sprintf("%s (%s)", strategy, analysis.Database.Tool)
		}
		_, _ = fmt.Fprintf(w, "  策略:     %s\n", strategy)
		if analysis.Database.DataDir != "" {
			_, _ = fmt.Fprintf(w, "  数据目录: %s\n", analysis.Database.DataDir)
		}
	}

	if len(analysis.SensitiveEnv) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintf(w, "疑似敏感环境变量（不回显取值）: %s\n", strings.Join(analysis.SensitiveEnv, ", "))
	}

	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Docker 属性支持:")
	attrTable := cliutil.NewTabWriter(w)
	_, _ = fmt.Fprintln(attrTable, "  属性\t级别\t说明")
	_, _ = fmt.Fprintln(attrTable, "  ----\t----\t----")
	for _, attr := range analysis.Attributes {
		_, _ = fmt.Fprintf(attrTable, "  %s\t%s\t%s\n", attr.Attribute, attr.Level, attr.Detail)
	}
	_ = attrTable.Flush()

	_, _ = fmt.Fprintln(w)
	writeCapabilitySection(w, report)

	if len(analysis.Blockers) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "阻止项:")
		for _, b := range analysis.Blockers {
			_, _ = fmt.Fprintf(w, "  ✗ %s\n", b)
		}
	}
	if len(analysis.Warnings) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "警告:")
		for _, warning := range analysis.Warnings {
			_, _ = fmt.Fprintf(w, "  - %s\n", warning)
		}
	}
}

func composeDescription(analysis *docker.ContainerAnalysis) string {
	if !analysis.IsCompose {
		return "普通 docker run（未发现 Compose 标签）"
	}
	desc := "Docker Compose"
	if analysis.ComposeProject != "" {
		desc += " 项目 " + analysis.ComposeProject
	}
	if analysis.ComposeService != "" {
		desc += " / 服务 " + analysis.ComposeService
	}
	if analysis.ComposeWorkingDir != "" {
		desc += "（" + analysis.ComposeWorkingDir + "）"
	}
	return desc
}

func writeCapabilitySection(w io.Writer, report *capability.Report) {
	if report == nil {
		return
	}
	_, _ = fmt.Fprintf(w, "远端能力 (%s):\n", report.Server)

	rows := [][2]string{
		{"docker", string(report.Docker.Status)},
		{"compose", string(report.Compose.Status)},
		{"restic", string(report.Restic.Status)},
		{"helper image", string(report.HelperImage.Status)},
		{"registry", string(report.Registry.Status)},
		{"disk", string(report.Disk.Status)},
	}
	tw := cliutil.NewTabWriter(w)
	for _, row := range rows {
		_, _ = fmt.Fprintf(tw, "  %s\t%s\n", row[0], row[1])
	}
	_ = tw.Flush()

	if report.Disk.Detail != "" {
		_, _ = fmt.Fprintf(w, "  disk: %s\n", report.Disk.Detail)
	}
	for _, tool := range report.ShellTools {
		if tool.Status != capability.StatusOK {
			_, _ = fmt.Fprintf(w, "  shell 工具 %s: %s\n", tool.Name, tool.Status)
		}
	}
	for _, tool := range report.DatabaseTools {
		if tool.Status != capability.StatusOK {
			_, _ = fmt.Fprintf(w, "  数据库工具 %s: %s（宿主机可选，dump/import 默认在容器内执行）\n", tool.Name, tool.Status)
		}
	}
	for _, p := range report.Paths {
		_, _ = fmt.Fprintf(w, "  路径 %s: %s（%s）\n", p.Name, p.Status, p.Detail)
	}
	if len(report.Notes) > 0 {
		_, _ = fmt.Fprintln(w, "  提示:")
		for _, note := range report.Notes {
			_, _ = fmt.Fprintf(w, "    - %s\n", note)
		}
	}
}

type dockerListItemView struct {
	Name           string `json:"name"`
	ID             string `json:"id"`
	Image          string `json:"image"`
	Mode           string `json:"mode"`
	ComposeProject string `json:"compose_project,omitempty"`
	ComposeService string `json:"compose_service,omitempty"`
	Database       string `json:"database,omitempty"`
	State          string `json:"state,omitempty"`
	Status         string `json:"status,omitempty"`
	Ports          string `json:"ports,omitempty"`
}

type dockerListDocument struct {
	Server     string               `json:"server"`
	Containers []dockerListItemView `json:"containers"`
}

func newDockerListItemView(c docker.ContainerSummary) dockerListItemView {
	return dockerListItemView{
		Name:           c.Name,
		ID:             c.ID,
		Image:          c.Image,
		Mode:           dockerMode(c.IsCompose(), c.ComposeProject()),
		ComposeProject: c.ComposeProject(),
		ComposeService: c.ComposeService(),
		Database:       c.DatabaseEngine(),
		State:          c.State,
		Status:         c.Status,
		Ports:          c.Ports,
	}
}

// dockerContainerView is the secret-free projection of an inspected container.
// Environment variables are reported by name only: values are never included in
// either the human or the structured report.
type dockerContainerView struct {
	Name           string   `json:"name"`
	ID             string   `json:"id"`
	Image          string   `json:"image"`
	Mode           string   `json:"mode"`
	ComposeProject string   `json:"compose_project,omitempty"`
	ComposeService string   `json:"compose_service,omitempty"`
	RestartPolicy  string   `json:"restart_policy,omitempty"`
	NetworkMode    string   `json:"network_mode,omitempty"`
	Ports          []string `json:"ports,omitempty"`
	Mounts         []string `json:"mounts,omitempty"`
	EnvKeys        []string `json:"env_keys,omitempty"`
}

type dockerInspectDocument struct {
	Server     string                    `json:"server"`
	Container  dockerContainerView       `json:"container"`
	Analysis   *docker.ContainerAnalysis `json:"analysis"`
	Capability *capability.Report        `json:"capability"`
}

func newDockerContainerView(info *docker.ContainerInfo, analysis *docker.ContainerAnalysis) dockerContainerView {
	view := dockerContainerView{
		Name:           info.Name,
		ID:             info.ID,
		Image:          info.Image,
		Mode:           dockerMode(analysis.IsCompose, analysis.ComposeProject),
		ComposeProject: analysis.ComposeProject,
		ComposeService: analysis.ComposeService,
		RestartPolicy:  info.RestartPolicy,
		NetworkMode:    info.NetworkMode,
	}
	for _, p := range info.Ports {
		if s := p.String(); s != "" {
			view.Ports = append(view.Ports, s)
		}
	}
	sort.Strings(view.Ports)
	for _, m := range info.Mounts {
		if s := m.String(); s != "" {
			view.Mounts = append(view.Mounts, s)
		}
	}
	sort.Strings(view.Mounts)
	for _, env := range info.Env {
		name, _, _ := strings.Cut(env, "=")
		if name = strings.TrimSpace(name); name != "" {
			view.EnvKeys = append(view.EnvKeys, name)
		}
	}
	sort.Strings(view.EnvKeys)
	return view
}

func writeJSON(w io.Writer, doc any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(doc)
}

func init() {
	dockerListCmd.Flags().BoolVarP(&dockerListAll, "all", "a", false, "显示全部容器（默认只显示运行中的）")
	dockerListCmd.Flags().BoolVar(&dockerListJSON, "json", false, "以 JSON 输出结构化结果")
	dockerInspectCmd.Flags().BoolVar(&dockerInspectJSON, "json", false, "以 JSON 输出结构化结果（环境变量只含名字，不含取值）")

	dockerCmd.AddCommand(dockerListCmd)
	dockerCmd.AddCommand(dockerInspectCmd)
	rootCmd.AddCommand(dockerCmd)
}
