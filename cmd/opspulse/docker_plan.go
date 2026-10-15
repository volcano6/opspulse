package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/volcano6/opspulse/internal/backup"
	"github.com/volcano6/opspulse/internal/capability"
	"github.com/volcano6/opspulse/internal/docker"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/migration"
	"github.com/volcano6/opspulse/internal/version"
)

// dockerPlanTimeout bounds a whole plan run. A plan inspects one container per
// selected service plus one capability probe per endpoint, so a batch plan on a
// slow link needs more headroom than a single discovery call.
const dockerPlanTimeout = 5 * time.Minute

var (
	dockerPlanAll         bool
	dockerPlanTarget      string
	dockerPlanSave        string
	dockerPlanNoSave      bool
	dockerPlanJSON        bool
	dockerPlanWriteConfig bool
	dockerPlanYes         bool
	dockerPlanShowJSON    bool
)

var dockerPlanCmd = &cobra.Command{
	Use:   "plan <server> [container]",
	Short: "为一个或多个容器生成只读、可保存的迁移 plan",
	Long: `生成 Docker 迁移 plan：源、目标、服务名、配置、named volume、bind mount、数据库、
镜像获取方式、目标路径与端口，以及 hard blocker / warning / informational follow-up 三类风险。

plan 只做只读探测与计算：不写备份配置、不生成快照、不启动服务、不覆盖文件。默认把 plan
保存为可审查的 YAML 文件并打印路径（--no-save 可关闭），用 ` + "`ops docker plan show <file>`" + ` 可重新读取。

默认只处理显式指定的容器；要批量规划整台服务器上的运行中容器，必须显式加 --all。

示例：
  ops docker plan vps-01 blog-db
  ops docker plan vps-01 blog-db --target vps-02 --save migration-plan.yaml
  ops docker plan vps-01 --all --json
  ops docker plan vps-01 blog-db --write-config --yes`,
	Args:              cobra.RangeArgs(1, 2),
	RunE:              runDockerPlan,
	ValidArgsFunction: completeDockerPlanArgs,
}

var dockerPlanShowCmd = &cobra.Command{
	Use:   "show <plan-file>",
	Short: "读取并显示已保存的迁移 plan",
	Long: `读取一个由 ` + "`ops docker plan`" + ` 保存的 plan 文件并显示其内容。

只读取文件，不会连接任何服务器，也不会执行 plan 中的任何步骤；格式版本不匹配时会明确报错。

示例：
  ops docker plan show migration-plan.yaml
  ops docker plan show migration-plan.yaml --json`,
	Args: cobra.ExactArgs(1),
	RunE: runDockerPlanShow,
}

func runDockerPlan(cmd *cobra.Command, args []string) error {
	serverName := strings.TrimSpace(args[0])
	containerArg := ""
	if len(args) == 2 {
		containerArg = strings.TrimSpace(args[1])
	}
	if err := validateDockerPlanFlags(containerArg); err != nil {
		return err
	}

	sourceTarget, sourceRemote, err := resolveDockerRemote(serverName)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), dockerPlanTimeout)
	defer cancel()

	names, err := selectDockerPlanContainers(ctx, sourceRemote, sourceTarget, serverName, containerArg)
	if err != nil {
		return err
	}

	evidences := make([]migration.ContainerEvidence, 0, len(names))
	for _, name := range names {
		info, inspectErr := sourceRemote.InspectContainer(ctx, sourceTarget, name)
		if inspectErr != nil {
			return dockerFriendlyError(serverName, inspectErr)
		}
		evidences = append(evidences, migration.ContainerEvidence{Info: info, Analysis: docker.Analyze(info)})
	}

	// Probe the source with the host paths the migration actually touches, so
	// the plan's prerequisites cover real directories instead of "/" alone.
	paths := dockerPlanHostPaths(evidences)
	sourceReport, err := sourceRemote.Probe(ctx, sourceTarget, capability.ProbeOptions{Paths: paths})
	if err != nil {
		return err
	}

	targetName := strings.TrimSpace(dockerPlanTarget)
	var targetReport *capability.Report
	if targetName != "" {
		targetTarget, targetRemote, resolveErr := resolveDockerRemote(targetName)
		if resolveErr != nil {
			return resolveErr
		}
		targetReport, err = targetRemote.Probe(ctx, targetTarget, capability.ProbeOptions{Paths: paths})
		if err != nil {
			return err
		}
	}

	plan, err := migration.BuildPlan(migration.BuildInput{
		SourceServer:     serverName,
		SourceCapability: sourceReport,
		TargetServer:     targetName,
		TargetCapability: targetReport,
		Containers:       evidences,
		Backup:           dockerPlanBackupDefaults(),
		Generator:        fmt.Sprintf("ops %s", version.Version),
		Now:              time.Now(),
	})
	if err != nil {
		return err
	}

	if dockerPlanJSON {
		if err := writeJSON(os.Stdout, plan); err != nil {
			return err
		}
	} else {
		writeDockerPlanHuman(os.Stdout, plan)
	}

	savedPath, err := saveDockerPlan(plan, serverName, dockerPlanLabel(containerArg))
	if err != nil {
		return err
	}
	if savedPath != "" {
		// The JSON document on stdout must stay parseable, so the side note goes
		// to stderr in that mode.
		out := io.Writer(os.Stdout)
		if dockerPlanJSON {
			out = os.Stderr
		}
		_, _ = fmt.Fprintf(out, "plan 已保存: %s\n", savedPath)
	}

	if dockerPlanWriteConfig {
		return writeDockerPlanBackupConfig(cmd, plan, savedPath)
	}
	return nil
}

func runDockerPlanShow(_ *cobra.Command, args []string) error {
	plan, err := migration.Load(args[0])
	if err != nil {
		return err
	}
	if dockerPlanShowJSON {
		return writeJSON(os.Stdout, plan)
	}
	writeDockerPlanHuman(os.Stdout, plan)
	return nil
}

// validateDockerPlanFlags keeps the "explicit selection" contract: a plan never
// processes a whole VPS because the user forgot an argument.
func validateDockerPlanFlags(containerArg string) error {
	switch {
	case containerArg == "" && !dockerPlanAll:
		return errors.New("请指定要规划的容器名，或用 --all 显式规划该服务器上所有运行中的容器（默认不批量处理整台 VPS）")
	case containerArg != "" && dockerPlanAll:
		return errors.New("不能同时指定容器名与 --all：请二选一")
	case dockerPlanNoSave && strings.TrimSpace(dockerPlanSave) != "":
		return errors.New("不能同时使用 --save 与 --no-save")
	default:
		return nil
	}
}

func selectDockerPlanContainers(
	ctx context.Context,
	remote capability.Remote,
	target executor.Target,
	serverName, containerArg string,
) ([]string, error) {
	if containerArg != "" {
		return []string{containerArg}, nil
	}

	containers, err := remote.ListContainers(ctx, target, false)
	if err != nil {
		return nil, dockerFriendlyError(serverName, err)
	}
	var names []string
	for _, c := range containers {
		if name := strings.TrimSpace(c.Name); name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("服务器 %q 上没有运行中的容器：先用 `ops docker list %s` 确认要迁移的服务", serverName, serverName)
	}
	sort.Strings(names)
	return names, nil
}

// dockerPlanHostPaths collects the host-side paths the migration has to
// prepare, across every selected container.
func dockerPlanHostPaths(evidences []migration.ContainerEvidence) []string {
	seen := make(map[string]bool)
	var paths []string
	for _, evidence := range evidences {
		for _, p := range evidence.Analysis.HostPaths() {
			if seen[p] {
				continue
			}
			seen[p] = true
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return paths
}

// dockerPlanBackupDefaults reads the repository settings a proposed backup job
// inherits. This is a read-only lookup: nothing is written to backups.yaml here.
func dockerPlanBackupDefaults() migration.BackupDefaults {
	defaults := migration.BackupDefaults{}
	store := backup.NewDefaultStore()
	if jobs, err := store.List(); err == nil {
		for _, job := range jobs {
			defaults.ExistingJobs = append(defaults.ExistingJobs, job.Name)
			if defaults.Backend == "" && strings.TrimSpace(job.Backend) != "" {
				defaults.Backend = job.Backend
				defaults.Env = job.Env
			}
		}
	}
	if defaults.Backend == "" {
		if repo := strings.TrimSpace(os.Getenv("RESTIC_REPOSITORY")); repo != "" {
			defaults.Backend = repo
			if password := os.Getenv("RESTIC_PASSWORD"); password != "" {
				defaults.Env = map[string]string{"RESTIC_PASSWORD": password}
			}
		}
	}
	return defaults
}

func dockerPlanLabel(containerArg string) string {
	if containerArg != "" {
		return containerArg
	}
	return "all"
}

// saveDockerPlan persists the plan unless the user asked for a pure read-only
// run. The default location is timestamped, and migration.Save refuses to
// overwrite an existing file, so a plan is never silently replaced.
func saveDockerPlan(plan *migration.Plan, serverName, label string) (string, error) {
	if dockerPlanNoSave {
		return "", nil
	}
	path := strings.TrimSpace(dockerPlanSave)
	if path == "" {
		path = migration.DefaultPlanPath(serverName, label, time.Now())
	}
	if err := migration.Save(path, plan); err != nil {
		return "", err
	}
	return path, nil
}

// writeDockerPlanBackupConfig is the only path in the plan command that writes
// anything: it shows the change summary and the sensitive-environment risk, then
// requires an explicit confirmation (interactive prompt or --yes) before the
// proposed backup job reaches backups.yaml.
func writeDockerPlanBackupConfig(cmd *cobra.Command, plan *migration.Plan, savedPath string) error {
	// The plan file deliberately never carries repository credentials, so the
	// inherited env is resolved again here - at write time - and only the job
	// that lands in backups.yaml receives it.
	jobs, err := dockerPlanBackupJobs(plan, dockerPlanBackupDefaults())
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	store := backup.NewDefaultStore()
	_, _ = fmt.Fprintf(out, "\n即将写入备份配置 %s：\n", store.FilePath())
	for _, job := range jobs {
		state := "新增"
		if job.exists {
			state = "更新"
		}
		_, _ = fmt.Fprintf(out, "  [%s] %s: server=%s paths=%s backend=%s tags=%s\n",
			state, job.Job.Name, job.Job.Server, strings.Join(job.Job.Paths, ","),
			job.Job.Backend, strings.Join(job.Job.Tags, ","))
	}
	if sensitive := dockerPlanSensitiveEnv(plan); len(sensitive) > 0 {
		_, _ = fmt.Fprintf(out, "敏感环境变量风险: %s 会原样写入生成的 compose.yaml 与备份快照（只列名字，不回显取值）\n",
			strings.Join(sensitive, ", "))
	}
	if savedPath != "" {
		_, _ = fmt.Fprintf(out, "对应 plan 文件: %s\n", savedPath)
	}

	prompt := fmt.Sprintf("确认写入上述 %d 个备份作业到 %s？[y/N]: ", len(jobs), store.FilePath())
	gateErr := confirmGate(cmd.InOrStdin(), out, readerIsInteractive(cmd.InOrStdin()), dockerPlanYes, prompt,
		errors.New("非交互环境下拒绝写入备份配置：请确认内容后加 --yes 显式接受"),
		errors.New("已取消写入备份配置"))
	if gateErr != nil {
		return gateErr
	}

	for _, job := range jobs {
		if saveErr := store.Save(job.Job); saveErr != nil {
			return fmt.Errorf("写入备份配置失败: %w", saveErr)
		}
	}
	_, _ = fmt.Fprintf(out, "✅ 已写入备份配置: %s\n", store.FilePath())
	return nil
}

type plannedBackupJob struct {
	Job    backup.Job
	exists bool
}

// dockerPlanBackupJobs turns the plan's proposals into concrete backup jobs,
// refusing when there is no repository to inherit (an empty backend would make
// the job unusable and backups.yaml invalid).
func dockerPlanBackupJobs(plan *migration.Plan, defaults migration.BackupDefaults) ([]plannedBackupJob, error) {
	var jobs []plannedBackupJob
	for _, service := range plan.Services {
		config := service.BackupConfig
		if config == nil {
			continue
		}
		if strings.TrimSpace(config.Backend) == "" {
			return nil, fmt.Errorf("无法写入备份作业 %q：backups.yaml 中没有可继承的 restic 仓库，且未设置 RESTIC_REPOSITORY；请先配置仓库", config.JobName)
		}
		jobs = append(jobs, plannedBackupJob{
			Job: backup.Job{
				Name:        config.JobName,
				Server:      config.Server,
				Paths:       config.Paths,
				Backend:     config.Backend,
				Env:         defaults.Env,
				Tags:        config.Tags,
				Description: config.Description,
			},
			exists: config.Exists,
		})
	}
	if len(jobs) == 0 {
		return nil, errors.New("plan 中没有可写入的备份配置")
	}
	return jobs, nil
}

func dockerPlanSensitiveEnv(plan *migration.Plan) []string {
	seen := make(map[string]bool)
	var names []string
	for _, service := range plan.Services {
		for _, name := range service.Config.SensitiveEnv {
			if seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func completeDockerPlanArgs(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return completeServerNames(nil, nil, "")
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

func writeDockerPlanHuman(w io.Writer, plan *migration.Plan) {
	_, _ = fmt.Fprintf(w, "迁移 plan（kind=%s，format_version=%d）\n", plan.Kind, plan.FormatVersion)
	_, _ = fmt.Fprintf(w, "生成时间: %s\n", plan.GeneratedAt)
	if plan.Generator != "" {
		_, _ = fmt.Fprintf(w, "生成器:   %s\n", plan.Generator)
	}
	writeDockerPlanEndpoint(w, "源", plan.Source)
	writeDockerPlanEndpoint(w, "目标", plan.Target)
	_, _ = fmt.Fprintf(w, "服务数:   %d\n", len(plan.Services))

	for i, service := range plan.Services {
		writeDockerPlanService(w, i+1, len(plan.Services), service)
	}

	_, _ = fmt.Fprintln(w)
	writeDockerPlanRiskSummary(w, plan.Risks)
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "提示: 本 plan 只读；执行前必须重新校验源机与目标机状态。")
}

func writeDockerPlanEndpoint(w io.Writer, label string, endpoint migration.Endpoint) {
	if strings.TrimSpace(endpoint.Server) == "" {
		_, _ = fmt.Fprintf(w, "%s:   未指定（目标路径与端口按源机原样记录）\n", label)
		return
	}
	_, _ = fmt.Fprintf(w, "%s:   %s", label, endpoint.Server)
	if report := endpoint.Capability; report != nil {
		_, _ = fmt.Fprintf(w, "（docker=%s compose=%s restic=%s helper=%s registry=%s disk=%s）",
			report.Docker.Status, report.Compose.Status, report.Restic.Status,
			report.HelperImage.Status, report.Registry.Status, report.Disk.Status)
	}
	_, _ = fmt.Fprintln(w)
}

func writeDockerPlanService(w io.Writer, index, total int, service migration.ServicePlan) {
	_, _ = fmt.Fprintf(w, "\n服务 %d/%d: %s（%s）\n", index, total, service.Name, dockerPlanMode(service))
	_, _ = fmt.Fprintf(w, "  容器:     %s\n", service.Container)
	_, _ = fmt.Fprintf(w, "  镜像:     %s（获取方式: %s）\n", service.Image, service.ImageAcquisition)
	if service.ImageAcquisitionNote != "" {
		_, _ = fmt.Fprintf(w, "            %s\n", service.ImageAcquisitionNote)
	}
	_, _ = fmt.Fprintf(w, "  配置:     %s（%s）\n", service.Config.ComposeFile, dockerPlanComposeSource(service.Config))
	if service.Config.ComposeWorkingDir != "" {
		_, _ = fmt.Fprintf(w, "  项目目录: %s\n", service.Config.ComposeWorkingDir)
	}
	if service.Config.RestartPolicy != "" {
		_, _ = fmt.Fprintf(w, "  重启策略: %s\n", service.Config.RestartPolicy)
	}
	if len(service.Config.Ports) > 0 {
		_, _ = fmt.Fprintf(w, "  端口:     %s\n", strings.Join(service.Config.Ports, ", "))
	}
	for _, volume := range service.Volumes {
		_, _ = fmt.Fprintf(w, "  卷:       %s → %s\n", volumeNameForPlan(volume), volume.Destination)
	}
	for _, mount := range service.BindMounts {
		_, _ = fmt.Fprintf(w, "  bind:     %s → %s\n", mount.Source, mount.Destination)
	}
	if service.Database != nil {
		tool := service.Database.Tool
		if tool == "" {
			tool = "-"
		}
		_, _ = fmt.Fprintf(w, "  数据库:   %s（策略 %s：%s）\n", service.Database.Engine, service.Database.Strategy, tool)
	}
	_, _ = fmt.Fprintf(w, "  一致性:   数据库=%s 卷=%s\n", service.Consistency.Database, service.Consistency.Volumes)
	if len(service.Config.SensitiveEnv) > 0 {
		_, _ = fmt.Fprintf(w, "  敏感变量: %s（只列名字，不回显取值）\n", strings.Join(service.Config.SensitiveEnv, ", "))
	}
	for _, target := range service.TargetPaths {
		_, _ = fmt.Fprintf(w, "  目标路径: [%s] %s → %s\n", target.Kind, target.Source, target.Target)
	}
	if len(service.TargetPorts) > 0 {
		_, _ = fmt.Fprintf(w, "  目标端口: %s\n", strings.Join(service.TargetPorts, ", "))
	}
	for _, attr := range service.Attributes {
		_, _ = fmt.Fprintf(w, "  属性:     %s=%s\n", attr.Attribute, attr.Level)
	}
	if config := service.BackupConfig; config != nil {
		state := "新增"
		if config.Exists {
			state = "更新"
		}
		_, _ = fmt.Fprintf(w, "  备份配置: [%s] job=%s server=%s paths=%s backend=%s\n",
			state, config.JobName, config.Server, strings.Join(config.Paths, ","), config.Backend)
	}
	writeDockerPlanRisks(w, service.Blockers, service.Warnings, service.FollowUps)
}

func dockerPlanMode(service migration.ServicePlan) string {
	if service.Mode == migration.ModeCompose {
		if service.ComposeProject != "" {
			return "compose:" + service.ComposeProject
		}
		return "compose"
	}
	return "run"
}

func dockerPlanComposeSource(config migration.ServiceConfig) string {
	if config.ComposeSource == "existing" {
		return "沿用现有 Compose 文件"
	}
	return "反向生成 compose.yaml"
}

func volumeNameForPlan(mount docker.VolumeMount) string {
	if strings.TrimSpace(mount.Name) != "" {
		return mount.Name
	}
	return mount.Source
}

func writeDockerPlanRiskSummary(w io.Writer, summary migration.RiskSummary) {
	_, _ = fmt.Fprintf(w, "风险汇总: hard blocker %d / warning %d / informational follow-up %d\n",
		len(summary.Blockers), len(summary.Warnings), len(summary.FollowUps))
	writeDockerPlanRisks(w, summary.Blockers, summary.Warnings, summary.FollowUps)
}

func writeDockerPlanRisks(w io.Writer, blockers, warnings, followUps []migration.Risk) {
	writeDockerPlanRiskList(w, "⛔ hard blocker", blockers)
	writeDockerPlanRiskList(w, "⚠️  warning", warnings)
	writeDockerPlanRiskList(w, "ℹ️  后续步骤", followUps)
}

func writeDockerPlanRiskList(w io.Writer, label string, risks []migration.Risk) {
	for _, risk := range risks {
		scope := ""
		if risk.Service != "" {
			scope = "[" + risk.Service + "] "
		}
		_, _ = fmt.Fprintf(w, "  %s %s%s\n", label, scope, risk.Detail)
		if risk.Action != "" {
			_, _ = fmt.Fprintf(w, "      → %s\n", risk.Action)
		}
	}
}

func init() {
	dockerPlanCmd.Flags().BoolVar(&dockerPlanAll, "all", false, "显式批量：规划该服务器上所有运行中的容器")
	dockerPlanCmd.Flags().StringVar(&dockerPlanTarget, "target", "", "目标服务器名（默认不指定：目标路径与端口按源机记录）")
	dockerPlanCmd.Flags().StringVar(&dockerPlanSave, "save", "", "plan 保存路径（默认保存到 OpsPulse 配置目录的 plans/ 下）")
	dockerPlanCmd.Flags().BoolVar(&dockerPlanNoSave, "no-save", false, "不保存 plan 文件，只打印（纯只读）")
	dockerPlanCmd.Flags().BoolVar(&dockerPlanJSON, "json", false, "以 JSON 输出 plan（stdout 只含 JSON）")
	dockerPlanCmd.Flags().BoolVar(&dockerPlanWriteConfig, "write-config", false,
		"确认后把 plan 中提议的备份作业写入 backups.yaml（默认不写）")
	dockerPlanCmd.Flags().BoolVarP(&dockerPlanYes, "yes", "y", false, "跳过写入备份配置前的交互确认（不包含 hard blocker）")

	dockerPlanShowCmd.Flags().BoolVar(&dockerPlanShowJSON, "json", false, "以 JSON 输出 plan")

	dockerPlanCmd.AddCommand(dockerPlanShowCmd)
	dockerCmd.AddCommand(dockerPlanCmd)
}
