package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/volcano6/opspulse/internal/asset"
	"github.com/volcano6/opspulse/internal/backup"
	"github.com/volcano6/opspulse/internal/capability"
	"github.com/volcano6/opspulse/internal/docker"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/migration"
	"github.com/volcano6/opspulse/internal/server"
	"github.com/volcano6/opspulse/internal/shellquote"
	"github.com/volcano6/opspulse/internal/storage"
)

// dockerApplyTimeout bounds a whole migration. An apply run does one backup and
// one restore per service on top of the plan-time probes, so it gets a much
// larger budget than planning.
const dockerApplyTimeout = 60 * time.Minute

var (
	dockerApplyYes    bool
	dockerApplyDryRun bool
	dockerApplyJSON   bool
)

var dockerApplyCmd = &cobra.Command{
	Use:   "apply <plan-file>",
	Short: "按迁移 plan 安全执行 Docker 服务迁移",
	Long: `读取一份由 ` + "`ops docker plan`" + ` 保存的 plan，先按当前环境重新校验源容器与目标机，再复用现有的
容器备份与恢复引擎执行迁移。

执行前会重新检查：源容器是否仍存在且与 plan 一致、目标连接与 Docker/Compose/restic、
镜像获取方式、端口、路径与权限、helper 镜像、数据库条件，以及目标机是否已有同名容器、
同名 Compose 项目、已占用端口或已存在的项目目录。发现 hard blocker 时停止执行并逐条说明
差异——plan 不是绕过当前检查的后门。

默认保留服务名与项目名，不会为了避开冲突自动改名。plan 中显式改过的 target_ports /
target_paths 会被执行；独立且安全的服务继续执行，最终汇总成功、失败、跳过与人工处理项。
退出码非零表示有必需步骤失败或被跳过。本命令不承诺通用自动回滚。

示例：
  ops docker apply migration-plan.yaml            # 交互确认后执行
  ops docker apply migration-plan.yaml --dry-run  # 只做应用前校验，不执行
  ops docker apply migration-plan.yaml --yes      # 跳过交互确认（不绕过 hard blocker）
  ops docker apply migration-plan.yaml --json     # 结构化输出结果`,
	Args:              cobra.ExactArgs(1),
	RunE:              runDockerApply,
	ValidArgsFunction: completeDockerApplyArgs,
}

func runDockerApply(cmd *cobra.Command, args []string) error {
	plan, err := migration.Load(args[0])
	if err != nil {
		return err
	}
	if strings.TrimSpace(plan.Target.Server) == "" {
		return errors.New("plan 未指定目标服务器，无法执行迁移：请用 `ops docker plan <server> <container> --target <target>` 重新生成 plan")
	}
	if len(plan.Services) == 0 {
		return errors.New("plan 中没有可迁移的服务")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, cancelTimeout := context.WithTimeout(ctx, dockerApplyTimeout)
	defer cancelTimeout()

	// stdout stays reserved for the JSON document; execution logs go to stderr
	// in that mode so a script can parse the result.
	out := io.Writer(os.Stdout)
	if dockerApplyJSON {
		out = os.Stderr
	}

	sourceTarget, sourceRemote, err := resolveDockerRemote(plan.Source.Server)
	if err != nil {
		return err
	}
	targetTarget, targetRemote, err := resolveDockerRemote(plan.Target.Server)
	if err != nil {
		return err
	}

	analyses := make(map[string]*docker.ContainerAnalysis, len(plan.Services))
	for _, service := range plan.Services {
		info, inspectErr := sourceRemote.InspectContainer(ctx, sourceTarget, service.Container)
		if inspectErr != nil {
			if errors.Is(inspectErr, docker.ErrContainerNotFound) {
				// Recorded as plan drift (source container missing) below.
				continue
			}
			return dockerFriendlyError(plan.Source.Server, inspectErr)
		}
		analyses[service.Container] = docker.Analyze(info)
	}

	sourceReport, err := sourceRemote.Probe(ctx, sourceTarget, capability.ProbeOptions{Paths: migration.PlanHostPaths(plan)})
	if err != nil {
		return err
	}
	targetReport, err := targetRemote.Probe(ctx, targetTarget, capability.ProbeOptions{
		Paths:  migration.PlanTargetPaths(plan),
		Images: migration.PlanImages(plan),
	})
	if err != nil {
		return err
	}
	targetContainers, err := targetRemote.ListContainers(ctx, targetTarget, true)
	if err != nil {
		return dockerFriendlyError(plan.Target.Server, err)
	}

	hasVolumes := planHasVolumes(plan)
	var risks migration.RiskSummary
	risks.Add(migration.CheckSourceRisks(sourceReport, hasVolumes)...)
	risks.Add(migration.CheckTargetRisks(plan.Target.Server, targetReport, hasVolumes)...)
	risks.Add(migration.VerifyProjectBatching(plan)...)
	risks.Add(migration.VerifySource(plan, analyses)...)
	risks.Add(migration.VerifyTarget(plan, targetContainers, targetReport.Paths)...)
	risks.Add(migration.VerifyTargetImages(plan, targetReport)...)
	risks.Add(migration.AdjustmentRisks(plan)...)

	blockedServices := map[string]string{}
	planWideBlocker := ""
	for _, risk := range risks.Blockers {
		if strings.TrimSpace(risk.Service) == "" {
			if planWideBlocker == "" {
				planWideBlocker = risk.Detail
			}
			continue
		}
		if _, exists := blockedServices[risk.Service]; !exists {
			blockedServices[risk.Service] = risk.Detail
		}
	}

	if dockerApplyDryRun {
		writeDockerApplyPreflight(out, plan, risks, blockedServices)
		writeDockerApplyJSONIfRequested(plan, risks, nil, blockedServices)
		if risks.HasBlockers() {
			return fmt.Errorf("预检发现 %d 个 hard blocker：未执行任何迁移步骤", len(risks.Blockers))
		}
		return nil
	}

	if planWideBlocker != "" {
		writeDockerApplyPreflight(out, plan, risks, blockedServices)
		writeDockerApplyJSONIfRequested(plan, risks, nil, blockedServices)
		return fmt.Errorf("存在阻止整次迁移的问题，已停止：%s", planWideBlocker)
	}

	writeDockerApplyPreflight(out, plan, risks, blockedServices)

	prompt := fmt.Sprintf("确认对目标机 %q 执行 %d 个服务的迁移（备份 → 恢复 → 启动）？[y/N]: ",
		plan.Target.Server, len(plan.Services)-len(blockedServices))
	if gateErr := confirmGate(cmd.InOrStdin(), out, readerIsInteractive(cmd.InOrStdin()), dockerApplyYes,
		prompt,
		errors.New("非交互环境下拒绝执行迁移：请确认预检结果后加 --yes 显式接受"),
		errors.New("已取消迁移")); gateErr != nil {
		return gateErr
	}

	migrator := newDockerApplyMigrator(confirmContainerSensitiveEnv(
		cmd.InOrStdin(), out, readerIsInteractive(cmd.InOrStdin()), dockerApplyYes))
	outcomes := runDockerApplyServices(ctx, plan, blockedServices, migrator, out)

	writeDockerApplySummary(out, plan, risks, outcomes)
	writeDockerApplyJSONIfRequested(plan, risks, outcomes, blockedServices)

	failed := 0
	for _, outcome := range outcomes {
		if outcome.Status != dockerApplyStatusSuccess {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("迁移未全部完成：%d/%d 个失败或被跳过（不承诺通用自动回滚，请按上面的汇总与人工处理项处理）",
			failed, len(plan.Services))
	}
	return nil
}

// Service outcome statuses shared by the human and the JSON report.
const (
	dockerApplyStatusSuccess = "success"
	dockerApplyStatusFailed  = "failed"
	dockerApplyStatusSkipped = "skipped"
)

// dockerApplyOutcome is the per-service result of an apply run.
type dockerApplyOutcome struct {
	Service string `json:"service"`
	Status  string `json:"status"`
	Detail  string `json:"detail,omitempty"`
}

// dockerApplyMigrator drives the existing backup and restore engines for one
// plan service. It is an interface so the CLI tests can substitute a fake and
// never reach a real host or restic repository.
type dockerApplyMigrator interface {
	Migrate(ctx context.Context, plan *migration.Plan, service migration.ServicePlan, out io.Writer) error
}

// newDockerApplyMigrator builds the production migrator. It is a package-level
// variable so tests can inject a fake.
var newDockerApplyMigrator = func(confirmSensitiveEnv func([]string) error) dockerApplyMigrator {
	return &engineDockerApplyMigrator{confirmSensitiveEnv: confirmSensitiveEnv}
}

// runDockerApplyServices migrates every service that is not blocked, continuing
// past failures so one unrelated service cannot hide the others' results.
func runDockerApplyServices(
	ctx context.Context,
	plan *migration.Plan,
	blocked map[string]string,
	migrator dockerApplyMigrator,
	out io.Writer,
) []dockerApplyOutcome {
	outcomes := make([]dockerApplyOutcome, 0, len(plan.Services))
	for _, service := range plan.Services {
		if detail, isBlocked := blocked[service.Name]; isBlocked {
			outcomes = append(outcomes, dockerApplyOutcome{
				Service: service.Name,
				Status:  dockerApplyStatusSkipped,
				Detail:  detail,
			})
			continue
		}
		_, _ = fmt.Fprintf(out, "\n==> 迁移服务 %s（容器 %s）\n", service.Name, service.Container)
		if err := migrator.Migrate(ctx, plan, service, out); err != nil {
			outcomes = append(outcomes, dockerApplyOutcome{
				Service: service.Name,
				Status:  dockerApplyStatusFailed,
				Detail:  err.Error(),
			})
			continue
		}
		outcomes = append(outcomes, dockerApplyOutcome{Service: service.Name, Status: dockerApplyStatusSuccess})
	}
	return outcomes
}

// planHasVolumes reports whether any planned service has a named volume, which
// decides whether the helper image is a prerequisite.
func planHasVolumes(plan *migration.Plan) bool {
	for _, service := range plan.Services {
		if len(service.Volumes) > 0 {
			return true
		}
	}
	return false
}

func completeDockerApplyArgs(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return nil, cobra.ShellCompDirectiveDefault
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

// writeDockerApplyPreflight renders the live re-validation verdict before any
// migration step runs.
func writeDockerApplyPreflight(w io.Writer, plan *migration.Plan, risks migration.RiskSummary, blocked map[string]string) {
	_, _ = fmt.Fprintf(w, "迁移预检（plan: %s → %s，服务 %d 个）\n", plan.Source.Server, plan.Target.Server, len(plan.Services))

	for _, service := range plan.Services {
		status := "就绪"
		if detail, ok := blocked[service.Name]; ok {
			status = "已阻止：" + detail
		}
		_, _ = fmt.Fprintf(w, "  - %s（容器 %s）: %s\n", service.Name, service.Container, status)
	}

	_, _ = fmt.Fprintln(w)
	writeDockerPlanRiskSummary(w, risks)
}

// writeDockerApplySummary prints the per-service results and the manual
// follow-ups. It never claims a generic rollback happened.
func writeDockerApplySummary(w io.Writer, plan *migration.Plan, risks migration.RiskSummary, outcomes []dockerApplyOutcome) {
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "迁移汇总:")
	counts := map[string]int{}
	for _, outcome := range outcomes {
		counts[outcome.Status]++
		label := dockerApplyStatusLabel(outcome.Status)
		if outcome.Detail != "" {
			_, _ = fmt.Fprintf(w, "  [%s] %s：%s\n", label, outcome.Service, outcome.Detail)
		} else {
			_, _ = fmt.Fprintf(w, "  [%s] %s\n", label, outcome.Service)
		}
	}
	_, _ = fmt.Fprintf(w, "  成功 %d / 失败 %d / 跳过 %d\n",
		counts[dockerApplyStatusSuccess], counts[dockerApplyStatusFailed], counts[dockerApplyStatusSkipped])

	if len(risks.Warnings) > 0 || len(risks.FollowUps) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "需要人工处理 / 注意:")
		writeDockerPlanRiskList(w, "⚠️  warning", risks.Warnings)
		writeDockerPlanRiskList(w, "ℹ️  后续步骤", risks.FollowUps)
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "注意：本命令不承诺通用自动回滚；失败或被跳过的服务需按上面的说明人工处理。")
}

func dockerApplyStatusLabel(status string) string {
	switch status {
	case dockerApplyStatusSuccess:
		return "成功"
	case dockerApplyStatusFailed:
		return "失败"
	case dockerApplyStatusSkipped:
		return "跳过"
	default:
		return status
	}
}

type dockerApplyJSONDocument struct {
	Kind     string                `json:"kind"`
	Source   string                `json:"source"`
	Target   string                `json:"target"`
	Services []dockerApplyOutcome  `json:"services"`
	Risks    migration.RiskSummary `json:"risks"`
	Blocked  map[string]string     `json:"blocked_services,omitempty"`
}

// writeDockerApplyJSONIfRequested prints the structured result on stdout. The
// human report already went to stderr in this mode.
func writeDockerApplyJSONIfRequested(plan *migration.Plan, risks migration.RiskSummary, outcomes []dockerApplyOutcome, blocked map[string]string) {
	if !dockerApplyJSON {
		return
	}
	doc := dockerApplyJSONDocument{
		Kind:     "docker_migration_apply_result",
		Source:   plan.Source.Server,
		Target:   plan.Target.Server,
		Services: outcomes,
		Risks:    risks,
		Blocked:  blocked,
	}
	if doc.Services == nil {
		doc.Services = []dockerApplyOutcome{}
	}
	if err := writeJSON(os.Stdout, doc); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "写入 JSON 结果失败: %v\n", err)
	}
}

// engineDockerApplyMigrator is the production migrator: it reuses
// backup.Runner.RunContainerBackup for the source and backup.RestoreRunner for
// the target, so the migration goes through the same execution path as
// `ops backup run <server>:<container>` and `ops restore run`.
type engineDockerApplyMigrator struct {
	confirmSensitiveEnv func([]string) error
}

func (m *engineDockerApplyMigrator) Migrate(ctx context.Context, plan *migration.Plan, service migration.ServicePlan, out io.Writer) error {
	db, err := storage.OpenDefault()
	if err != nil {
		return fmt.Errorf("打开审计数据库失败: %w", err)
	}
	defer func() { _ = db.Close() }()

	store := backup.NewDefaultStore()
	serverStore := server.NewDefaultStore()
	assetStore := asset.NewDefaultStore()
	exec := executor.NewSSHExecutor().WithServerResolver(serverStore.Get).WithWarnWriter(out)

	backupRunner := backup.NewRunnerWithStores(exec, serverStore, storage.NewBackupRepo(db), store, assetStore)
	alias := migration.ApplyAlias(service)

	res, err := backupRunner.RunContainerBackup(ctx, backup.ContainerBackupOptions{
		Server:              plan.Source.Server,
		ContainerName:       service.Container,
		AliasName:           alias,
		ConfirmSensitiveEnv: m.confirmSensitiveEnv,
	}, out)
	if err != nil {
		return fmt.Errorf("源机备份失败: %w", err)
	}
	if res.RunRecord != nil && res.RunRecord.Untracked {
		return fmt.Errorf("容器备份 %q 完成但未被审计（缺少 SQLite 审计记录）", res.JobName)
	}
	if res.SnapshotID == "" {
		return fmt.Errorf("容器备份 %q 未返回快照 ID，无法继续恢复", res.JobName)
	}

	job, err := store.Get(res.JobName)
	if err != nil {
		return fmt.Errorf("读取刚写入的备份作业 %q 失败: %w", res.JobName, err)
	}

	restoreRunner := backup.NewRestoreRunner(exec, serverStore, storage.NewRestoreRepo(db), store, assetStore)
	adjustment := migration.Adjustments(service)
	if !adjustment.Any() {
		// A plan-driven migration is the scenario that may start services
		// without a separate flag: the plan is the operator's explicit
		// statement that this service should come up on the target.
		_, err = restoreRunner.Run(ctx, *job, backup.RestoreOptions{
			SnapshotID:   res.SnapshotID,
			TargetServer: plan.Target.Server,
			StartPolicy:  backup.StartPolicyStart,
			AliasName:    alias,
		}, out)
		if err != nil {
			return fmt.Errorf("目标机恢复失败: %w", err)
		}
		return nil
	}

	// The engine's auto-start cannot express target-side edits (it starts the
	// project exactly as the source declared it), so an adjusted service is
	// restored as files first and then started by the plan-driven step below.
	_, err = restoreRunner.Run(ctx, *job, backup.RestoreOptions{
		SnapshotID:   res.SnapshotID,
		TargetServer: plan.Target.Server,
		StartPolicy:  backup.StartPolicyNoStart,
		AliasName:    alias,
	}, out)
	if err != nil {
		return fmt.Errorf("目标机文件恢复失败: %w", err)
	}
	return applyTargetAdjustment(ctx, exec, plan, service, adjustment, alias, out)
}

// applyTargetAdjustment performs the target-side plan edits the restore engine
// cannot express: it moves the restored project directory when the plan changed
// it, rewrites the Compose file's published ports and bind-mount host paths,
// imports the named volumes recorded in the container manifest, then starts the
// project and imports the database dump.
func applyTargetAdjustment(
	ctx context.Context,
	exec executor.Executor,
	plan *migration.Plan,
	service migration.ServicePlan,
	adjustment migration.ServiceAdjustment,
	alias string,
	out io.Writer,
) error {
	target, err := executor.ResolveTarget(server.NewDefaultStore(), plan.Target.Server)
	if err != nil {
		return err
	}

	projectDir := strings.TrimSpace(service.Config.ComposeWorkingDir)
	if projectDir == "" {
		projectDir = path.Dir(service.Config.ComposeFile)
	}
	if projectDir == "" || projectDir == "." {
		return errors.New("plan 缺少 Compose 项目目录，无法执行目标机调整")
	}
	composeFile := path.Base(service.Config.ComposeFile)
	if composeFile == "" || composeFile == "." {
		composeFile = "compose.yaml"
	}

	if adjustment.ProjectDir != "" && adjustment.ProjectDir != projectDir {
		moveScript := fmt.Sprintf("set -e\nif [ -e %s ]; then echo '目标目录已存在，拒绝覆盖' >&2; exit 1; fi\nmkdir -p %s\nmv %s %s\n",
			shellquote.Quote(adjustment.ProjectDir),
			shellquote.Quote(path.Dir(adjustment.ProjectDir)),
			shellquote.Quote(projectDir),
			shellquote.Quote(adjustment.ProjectDir))
		if _, err := runApplyScript(ctx, exec, target, "apply-move-project-"+service.Name, moveScript, out); err != nil {
			return fmt.Errorf("移动项目目录到 %s 失败: %w", adjustment.ProjectDir, err)
		}
		_, _ = fmt.Fprintf(out, "  -> 项目目录已按 plan 从 %s 移动到 %s\n", projectDir, adjustment.ProjectDir)
		projectDir = adjustment.ProjectDir
	}

	if adjustment.Ports != nil || len(adjustment.BindTargets) > 0 {
		composePath := path.Join(projectDir, composeFile)
		raw, err := readApplyTextFile(ctx, exec, target, composePath)
		if err != nil {
			return fmt.Errorf("读取目标机 Compose 文件 %s 失败: %w", composePath, err)
		}
		updated, err := docker.OverrideCompose([]byte(raw), composeServiceKey(service, alias), docker.ComposeOverride{
			Ports:       adjustment.Ports,
			BindTargets: adjustment.BindTargets,
		})
		if err != nil {
			return fmt.Errorf("按 plan 重写 Compose 文件失败: %w", err)
		}
		if err := writeApplyTextFile(ctx, exec, target, composePath, string(updated)); err != nil {
			return fmt.Errorf("写回目标机 Compose 文件失败: %w", err)
		}
		_, _ = fmt.Fprintf(out, "  -> 已按 plan 调整 %s 的端口/挂载路径（注释与格式会丢失）\n", composePath)
	}

	manifest := readApplyManifest(ctx, exec, target, projectDir)
	if manifest != nil {
		for _, volume := range manifest.Volumes {
			if volume.Archive == "" {
				continue
			}
			archivePath := path.Join(projectDir, volume.Archive)
			_, _ = fmt.Fprintf(out, "  -> 导入 named volume %q（%s）\n", volume.OriginalName, volume.Archive)
			if _, err := runApplyScript(ctx, exec, target, "apply-volume-"+volume.OriginalName,
				docker.BuildVolumeImportScript(volume.OriginalName, archivePath), out); err != nil {
				return fmt.Errorf("导入 named volume %q 失败: %w", volume.OriginalName, err)
			}
		}
	}

	if _, err := runApplyScript(ctx, exec, target, "apply-compose-up-"+service.Name,
		applyComposeUpScript(projectDir, composeFile, alias), out); err != nil {
		return fmt.Errorf("在目标机启动 Compose 项目失败: %w", err)
	}
	_, _ = fmt.Fprintf(out, "  -> 已在 %s 启动项目 %s\n", projectDir, alias)

	if manifest != nil && manifest.Database != nil && manifest.Database.Dump != "" {
		container := manifest.Database.Container
		if container == "" || container == manifest.App {
			container = alias
		}
		dumpPath := path.Join(projectDir, manifest.Database.Dump)
		importScript, err := docker.BuildImportScript(manifest.Database.Engine, container, dumpPath)
		if err != nil {
			return fmt.Errorf("构建数据库导入脚本失败: %w", err)
		}
		_, _ = fmt.Fprintf(out, "  -> 导入数据库 %s（%s）\n", manifest.Database.Engine, manifest.Database.Dump)
		if _, err := runApplyScript(ctx, exec, target, "apply-import-db-"+service.Name, importScript, out); err != nil {
			return fmt.Errorf("导入数据库失败: %w", err)
		}
	}
	return nil
}

// composeServiceKey is the key the service has inside the Compose file:
// the original Compose service name for Compose-managed containers, and the
// generated (sanitized) name for plain containers.
func composeServiceKey(service migration.ServicePlan, alias string) string {
	if service.Mode == migration.ModeCompose && strings.TrimSpace(service.ComposeService) != "" {
		return service.ComposeService
	}
	return docker.SanitizeServiceName(alias)
}

// applyComposeUpScript starts the restored project with the explicit Compose
// file name recorded in the plan, instead of guessing one of the four default
// file names.
func applyComposeUpScript(projectDir, composeFile, projectName string) string {
	return fmt.Sprintf(`set -e
if docker compose version >/dev/null 2>&1; then
  COMPOSE="docker compose"
elif command -v docker-compose >/dev/null 2>&1; then
  COMPOSE="docker-compose"
else
  echo "Error: docker compose not found" >&2
  exit 127
fi
cd %s
export COMPOSE_PROJECT_NAME=%s
$COMPOSE -f %s up -d
`, shellquote.Quote(projectDir), shellquote.Quote(projectName), shellquote.Quote(composeFile))
}

// readApplyManifest reads the container manifest the backup engine wrote into
// the restored project directory. A missing manifest is not an error: the
// migration then starts the project without volume/database steps.
func readApplyManifest(ctx context.Context, exec executor.Executor, target executor.Target, projectDir string) *docker.ContainerManifest {
	for _, manifestPath := range []string{docker.ManifestPath(projectDir), docker.LegacyManifestPath(projectDir)} {
		raw, err := readApplyTextFile(ctx, exec, target, manifestPath)
		if err != nil || strings.TrimSpace(raw) == "" {
			continue
		}
		manifest, parseErr := docker.UnmarshalManifest([]byte(raw))
		if parseErr != nil {
			continue
		}
		return manifest
	}
	return nil
}

func readApplyTextFile(ctx context.Context, exec executor.Executor, target executor.Target, remotePath string) (string, error) {
	var buf strings.Builder
	script := fmt.Sprintf("cat %s", shellquote.Quote(remotePath))
	res, err := exec.Execute(ctx, target, "apply-read", script, &buf)
	if err != nil {
		return "", err
	}
	if res != nil && !res.Success {
		return "", fmt.Errorf("读取 %s 失败（exit %d）", remotePath, res.ExitCode)
	}
	return buf.String(), nil
}

func writeApplyTextFile(ctx context.Context, exec executor.Executor, target executor.Target, remotePath, content string) error {
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	script := fmt.Sprintf("mkdir -p %s && printf '%%s' %s | base64 -d > %s",
		shellquote.Quote(path.Dir(remotePath)), shellquote.Quote(encoded), shellquote.Quote(remotePath))
	_, err := runApplyScript(ctx, exec, target, "apply-write", script, io.Discard)
	return err
}

// runApplyScript runs one plan-driven step and turns a non-zero exit into an
// error so the per-service result is accurate.
func runApplyScript(ctx context.Context, exec executor.Executor, target executor.Target, name, script string, out io.Writer) (*executor.Result, error) {
	res, err := exec.Execute(ctx, target, name, script, out)
	if err != nil {
		return res, err
	}
	if res != nil && !res.Success {
		if res.Error != nil {
			return res, res.Error
		}
		return res, fmt.Errorf("远程命令 %s 退出码 %d", name, res.ExitCode)
	}
	return res, nil
}

func init() {
	dockerApplyCmd.Flags().BoolVarP(&dockerApplyYes, "yes", "y", false,
		"跳过执行前的交互确认（不绕过 hard blocker）")
	dockerApplyCmd.Flags().BoolVar(&dockerApplyDryRun, "dry-run", false,
		"只做应用前的重新校验与冲突检查，不执行任何备份/恢复/启动")
	dockerApplyCmd.Flags().BoolVar(&dockerApplyJSON, "json", false,
		"以 JSON 输出预检结果与逐服务结果（执行日志走 stderr）")

	dockerCmd.AddCommand(dockerApplyCmd)
}
