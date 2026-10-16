package migration

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/volcano6/opspulse/internal/capability"
	"github.com/volcano6/opspulse/internal/docker"
)

// Apply-time risk codes. They extend the plan vocabulary with the checks that
// only make sense against a live source and target: plan drift, target
// conflicts and the project-shape limits of the existing backup engine.
const (
	// Hard blockers.
	RiskApplySourceMissing           = "apply_source_container_missing"
	RiskApplySourceDrift             = "apply_source_drift"
	RiskApplyTargetContainerConflict = "apply_target_container_conflict"
	RiskApplyTargetProjectConflict   = "apply_target_project_conflict"
	RiskApplyTargetPortConflict      = "apply_target_port_conflict"
	RiskApplyTargetPathConflict      = "apply_target_path_conflict"
	RiskApplyTargetImageMissing      = "apply_target_image_missing"
	RiskApplyProjectMultiService     = "apply_project_multi_service"

	// Overridable warnings.
	RiskApplyAdjustment = "apply_target_adjustment"
)

// CheckSourceRisks re-runs the source capability classification (the same table
// BuildPlan used) against a live report, so apply re-checks the current host
// instead of trusting the plan's recorded verdict.
func CheckSourceRisks(report *capability.Report, hasVolumes bool) []Risk {
	return sourceRisks(report, hasVolumes)
}

// CheckTargetRisks re-runs the target capability classification against a live
// report.
func CheckTargetRisks(server string, report *capability.Report, hasVolumes bool) []Risk {
	return targetRisks(server, report, hasVolumes)
}

// VerifySource compares every plan service against the container that is
// running right now. A container that disappeared, or whose image, orchestration
// or mounts changed, is plan drift and blocks execution: the plan is a review
// record, not a bypass around the current state.
func VerifySource(plan *Plan, analyses map[string]*docker.ContainerAnalysis) []Risk {
	if plan == nil {
		return nil
	}
	var risks []Risk
	for _, service := range plan.Services {
		analysis := analyses[service.Container]
		if analysis == nil {
			risks = append(risks, Risk{
				Code:    RiskApplySourceMissing,
				Service: service.Name,
				Detail:  fmt.Sprintf("源机 %s 上找不到 plan 中的容器 %q：容器可能已删除、改名或停止运行", plan.Source.Server, service.Container),
				Action:  "用 `ops docker list <server> --all` 确认容器，然后重新生成 plan",
			})
			continue
		}
		risks = append(risks, sourceDriftRisks(service, analysis)...)
	}
	return risks
}

// sourceDriftRisks compares one plan service with its freshly analyzed
// container and reports every difference that would change what gets migrated.
func sourceDriftRisks(service ServicePlan, analysis *docker.ContainerAnalysis) []Risk {
	var details []string
	add := func(format string, args ...any) {
		details = append(details, fmt.Sprintf(format, args...))
	}

	if analysis.Image != service.Image {
		add("镜像从 %q 变为 %q", service.Image, analysis.Image)
	}
	if analysis.IsCompose != (service.Mode == ModeCompose) {
		add("编排方式从 %q 变化（现在 is_compose=%v）", service.Mode, analysis.IsCompose)
	}
	if analysis.ComposeProject != service.ComposeProject || analysis.ComposeService != service.ComposeService {
		add("Compose 项目/服务从 %q/%q 变为 %q/%q",
			service.ComposeProject, service.ComposeService, analysis.ComposeProject, analysis.ComposeService)
	}
	if got, want := joinSorted(volumeNames(analysis.NamedVolumes)), joinSorted(volumeNames(service.Volumes)); got != want {
		add("named volume 从 [%s] 变为 [%s]", want, got)
	}
	if got, want := joinSorted(mountSources(analysis.BindMounts)), joinSorted(mountSources(service.BindMounts)); got != want {
		add("bind mount 源路径从 [%s] 变为 [%s]", want, got)
	}
	if got, want := databaseSignature(analysis.Database), planDatabaseSignature(service.Database); got != want {
		add("数据库策略从 %q 变为 %q", want, got)
	}

	var risks []Risk
	if len(details) > 0 {
		risks = append(risks, Risk{
			Code:    RiskApplySourceDrift,
			Service: service.Name,
			Detail:  "plan 与当前源容器不一致：" + strings.Join(details, "；"),
			Action:  "环境已变化，请重新生成 plan 并复核后再执行（不要用旧 plan 绕过当前检查）",
		})
	}
	for _, blocker := range analysis.Blockers {
		risks = append(risks, Risk{
			Code:    RiskAnalysisBlocker,
			Service: service.Name,
			Detail:  blocker,
			Action:  "该容器现在无法按单容器迁移：请先手工处理后再重新生成 plan",
		})
	}
	return risks
}

// VerifyTarget checks the live target for everything that must block the
// migration: a container or Compose project with the same name, a published
// port the plan needs, or a Compose project directory that already exists.
func VerifyTarget(plan *Plan, containers []docker.ContainerSummary, paths []capability.Check) []Risk {
	if plan == nil {
		return nil
	}
	pathBy := pathIndex(paths)
	var risks []Risk
	for _, service := range plan.Services {
		risks = append(risks, targetConflicts(service, containers, pathBy)...)
	}
	return risks
}

// VerifyTargetImages re-checks the images a plan marked as preload-required:
// such a target cannot pull them, so the image has to be present already. An
// image whose presence was not probed is left alone instead of guessed at.
func VerifyTargetImages(plan *Plan, report *capability.Report) []Risk {
	if plan == nil || report == nil {
		return nil
	}
	presence := make(map[string]capability.Check, len(report.Images))
	for _, image := range report.Images {
		presence[image.Name] = image
	}

	var risks []Risk
	for _, service := range plan.Services {
		check, ok := presence[service.Image]
		if !ok || check.Status != capability.StatusMissing {
			continue
		}
		if service.ImageAcquisition != ImagePreloadRequired {
			continue
		}
		risks = append(risks, Risk{
			Code:    RiskApplyTargetImageMissing,
			Service: service.Name,
			Detail:  fmt.Sprintf("目标机无法访问镜像仓库，且本地没有镜像 %s", service.Image),
			Action:  "先在目标机预置该镜像（docker pull 后 docker save/load，或放通仓库访问），再重新执行",
		})
	}
	return risks
}

func targetConflicts(service ServicePlan, containers []docker.ContainerSummary, pathBy map[string]capability.Check) []Risk {
	var risks []Risk
	project := serviceProjectName(service)

	for _, container := range containers {
		if container.Name == service.Container {
			risks = append(risks, Risk{
				Code:    RiskApplyTargetContainerConflict,
				Service: service.Name,
				Detail:  fmt.Sprintf("目标机已有同名容器 %q", container.Name),
				Action:  "先处理目标机上的同名容器（默认不改名）；确认无用后再执行迁移",
			})
		}
		if project != "" && container.ComposeProject() == project {
			risks = append(risks, Risk{
				Code:    RiskApplyTargetProjectConflict,
				Service: service.Name,
				Detail:  fmt.Sprintf("目标机已有 Compose 项目 %q（容器 %q）", project, container.Name),
				Action:  "同一项目名会与现有服务冲突；默认不改名，请先停用/删除目标项目或改用其他目标机",
			})
		}
	}

	used := publishedPortIndex(containers)
	for _, spec := range planPorts(service) {
		hostPort, protocol := portSpec(spec)
		if hostPort == "" {
			continue
		}
		if owner, ok := used[portKey(hostPort, protocol)]; ok {
			risks = append(risks, Risk{
				Code:    RiskApplyTargetPortConflict,
				Service: service.Name,
				Detail:  fmt.Sprintf("目标机端口 %s/%s 已被容器 %q 占用", hostPort, protocol, owner),
				Action:  "在 plan 中把 target_ports 改成其他宿主机端口，或先释放该端口",
			})
		}
	}

	for _, target := range service.TargetPaths {
		if target.Kind != "compose_project" || strings.TrimSpace(target.Target) == "" {
			continue
		}
		check, ok := pathBy[target.Target]
		if !ok || !check.Exists {
			continue
		}
		risks = append(risks, Risk{
			Code:    RiskApplyTargetPathConflict,
			Service: service.Name,
			Detail:  fmt.Sprintf("目标机目录 %s 已存在（%s）：迁移会向其中写入 Compose 文件", target.Target, check.Detail),
			Action:  "在 plan 中把该 compose_project 的 target 改到空目录，或先清理目标机上的旧项目目录",
		})
	}
	return risks
}

// VerifyProjectBatching blocks a plan that selects several containers from one
// Compose project. The existing container backup engine names each backup job
// after the Compose project (that is how the project name is preserved on the
// target) and two jobs cannot share a name, so a batch would silently overwrite
// one backup job with another. Refusing is the honest answer until the engine
// can carry a project as a unit.
func VerifyProjectBatching(plan *Plan) []Risk {
	if plan == nil {
		return nil
	}
	byProject := map[string][]string{}
	for _, service := range plan.Services {
		if service.Mode != ModeCompose || strings.TrimSpace(service.ComposeProject) == "" {
			continue
		}
		byProject[service.ComposeProject] = append(byProject[service.ComposeProject], service.Name)
	}

	var risks []Risk
	for project, services := range byProject {
		if len(services) < 2 {
			continue
		}
		sort.Strings(services)
		risks = append(risks, Risk{
			Code:    RiskApplyProjectMultiService,
			Service: services[0],
			Detail: fmt.Sprintf("Compose 项目 %q 下有 %d 个服务（%s）被同一 plan 选中；现有迁移引擎按容器逐份执行且以项目名作为作业名，无法在一次执行中同时保留项目名",
				project, len(services), strings.Join(services, ", ")),
			Action: "为该项目下的服务逐个生成 plan 并分别执行，或手工迁移整个 Compose 项目",
		})
	}
	sort.Slice(risks, func(i, j int) bool { return risks[i].Service < risks[j].Service })
	return risks
}

// ServiceAdjustment is the target-side edit set recorded in a plan service:
// replacement published ports, replacement bind-mount host paths, and a moved
// Compose project directory. A zero value means "keep everything as planned".
type ServiceAdjustment struct {
	Ports       []string
	BindTargets map[string]string
	ProjectDir  string
}

// Any reports whether the plan service carries a target-side edit.
func (a ServiceAdjustment) Any() bool {
	return a.Ports != nil || len(a.BindTargets) > 0 || a.ProjectDir != ""
}

// Adjustments extracts the target-side edits a plan service carries. Applied
// edits are explicit: only a target that differs from the source is honored,
// so an untouched plan never rewrites anything.
func Adjustments(service ServicePlan) ServiceAdjustment {
	var adjustment ServiceAdjustment
	if len(service.TargetPorts) > 0 && !equalStrings(service.TargetPorts, service.Config.Ports) {
		adjustment.Ports = append([]string(nil), service.TargetPorts...)
	}

	destinations := make(map[string]string, len(service.BindMounts))
	for _, mount := range service.BindMounts {
		destinations[mount.Source] = mount.Destination
	}
	for _, target := range service.TargetPaths {
		switch target.Kind {
		case "compose_project":
			if target.Target != "" && target.Target != target.Source {
				adjustment.ProjectDir = target.Target
			}
		case "bind_mount":
			if target.Target == "" || target.Target == target.Source {
				continue
			}
			destination := destinations[target.Source]
			if destination == "" {
				continue
			}
			if adjustment.BindTargets == nil {
				adjustment.BindTargets = map[string]string{}
			}
			adjustment.BindTargets[destination] = target.Target
		}
	}
	return adjustment
}

// AdjustmentRisks reports the plan edits apply will write to the target, so the
// operator sees them before anything runs.
func AdjustmentRisks(plan *Plan) []Risk {
	if plan == nil {
		return nil
	}
	var risks []Risk
	for _, service := range plan.Services {
		adjustment := Adjustments(service)
		if !adjustment.Any() {
			continue
		}
		var parts []string
		if adjustment.Ports != nil {
			parts = append(parts, "端口改为 "+strings.Join(adjustment.Ports, ", "))
		}
		if adjustment.ProjectDir != "" {
			parts = append(parts, "项目目录改为 "+adjustment.ProjectDir)
		}
		if len(adjustment.BindTargets) > 0 {
			var pairs []string
			for destination, hostPath := range adjustment.BindTargets {
				pairs = append(pairs, fmt.Sprintf("%s → %s", destination, hostPath))
			}
			sort.Strings(pairs)
			parts = append(parts, "bind mount 宿主机路径 "+strings.Join(pairs, ", "))
		}
		risks = append(risks, Risk{
			Code:    RiskApplyAdjustment,
			Service: service.Name,
			Detail:  "plan 记录了目标机调整：" + strings.Join(parts, "；") + "（执行时会重写目标机上的 Compose 文件，注释与格式会丢失）",
			Action:  "确认这些调整符合预期；如需回退，请重新生成 plan",
		})
	}
	return risks
}

// serviceProjectName is the Compose project name a service becomes on the
// target: the original project for Compose services, and the backup job name
// (which the engine uses as the project name) for plain containers.
func serviceProjectName(service ServicePlan) string {
	if service.Mode == ModeCompose {
		return strings.TrimSpace(service.ComposeProject)
	}
	return strings.TrimSpace(service.Name)
}

// ApplyAlias is the name the existing backup engine should use for a service:
// the original Compose project for Compose services and the plan's job name for
// plain containers. The engine writes this name into the container manifest, so
// restoring it preserves the service/project name on the target instead of
// renaming anything.
func ApplyAlias(service ServicePlan) string {
	if name := serviceProjectName(service); name != "" {
		return name
	}
	return strings.TrimSpace(service.Name)
}

// PlanHostPaths collects every host path the migration has to probe on the
// source, across all services.
func PlanHostPaths(plan *Plan) []string {
	if plan == nil {
		return nil
	}
	var paths []string
	for _, service := range plan.Services {
		paths = appendUnique(paths, service.Config.ComposeWorkingDir)
		for _, mount := range service.BindMounts {
			if docker.IsSystemMount(mount.Source) {
				continue
			}
			paths = appendUnique(paths, mount.Source)
		}
	}
	sort.Strings(paths)
	return paths
}

// PlanTargetPaths collects the host paths the migration writes on the target,
// honoring any edits recorded in the plan.
func PlanTargetPaths(plan *Plan) []string {
	if plan == nil {
		return nil
	}
	var paths []string
	for _, service := range plan.Services {
		for _, target := range service.TargetPaths {
			switch target.Kind {
			case "compose_project", "bind_mount":
				paths = appendUnique(paths, target.Target)
			}
		}
	}
	sort.Strings(paths)
	return paths
}

// PlanImages collects the container images the migration needs on the target.
func PlanImages(plan *Plan) []string {
	if plan == nil {
		return nil
	}
	var images []string
	for _, service := range plan.Services {
		images = appendUnique(images, service.Image)
	}
	sort.Strings(images)
	return images
}

// planPorts is the published-port set a service will bind on the target: the
// plan's edited ports when present, otherwise the ports recorded from the
// source container.
func planPorts(service ServicePlan) []string {
	if len(service.TargetPorts) > 0 {
		return service.TargetPorts
	}
	return service.Config.Ports
}

// portSpec splits a Compose/Docker port specification into its host port and
// protocol. A spec that publishes no fixed host port (":80" or "80") returns an
// empty host port, because it can never conflict with a planned port.
func portSpec(spec string) (string, string) {
	s := strings.TrimSpace(spec)
	if s == "" {
		return "", ""
	}
	protocol := "tcp"
	if i := strings.Index(s, "/"); i >= 0 {
		protocol = strings.ToLower(strings.TrimSpace(s[i+1:]))
		s = s[:i]
	}

	parts := strings.Split(s, ":")
	if len(parts) < 2 {
		return "", ""
	}
	hostPort := strings.TrimSpace(parts[len(parts)-2])
	if _, err := strconv.Atoi(hostPort); err != nil {
		return "", ""
	}
	return hostPort, protocol
}

func portKey(hostPort, protocol string) string {
	if protocol == "" {
		protocol = "tcp"
	}
	return hostPort + "/" + protocol
}

// publishedPortIndex maps every host port bound by a running target container
// to that container's name.
func publishedPortIndex(containers []docker.ContainerSummary) map[string]string {
	index := map[string]string{}
	for _, container := range containers {
		if !strings.EqualFold(container.State, "running") && container.State != "" {
			continue
		}
		for _, port := range docker.ParsePublishedPorts(container.Ports) {
			key := portKey(port.HostPort, port.Protocol)
			if _, ok := index[key]; !ok {
				index[key] = container.Name
			}
		}
	}
	return index
}

func pathIndex(checks []capability.Check) map[string]capability.Check {
	index := make(map[string]capability.Check, len(checks))
	for _, check := range checks {
		index[check.Name] = check
	}
	return index
}

func volumeNames(mounts []docker.VolumeMount) []string {
	names := make([]string, 0, len(mounts))
	for _, mount := range mounts {
		name := strings.TrimSpace(mount.Name)
		if name == "" {
			name = strings.TrimSpace(mount.Source)
		}
		if name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func mountSources(mounts []docker.VolumeMount) []string {
	sources := make([]string, 0, len(mounts))
	for _, mount := range mounts {
		if source := strings.TrimSpace(mount.Source); source != "" {
			sources = append(sources, source)
		}
	}
	sort.Strings(sources)
	return sources
}

func databaseSignature(db *docker.DatabaseAnalysis) string {
	if db == nil {
		return ""
	}
	return db.Engine + "/" + db.Strategy
}

func planDatabaseSignature(db *DatabasePlan) string {
	if db == nil {
		return ""
	}
	return db.Engine + "/" + db.Strategy
}

func joinSorted(values []string) string {
	sorted := append([]string(nil), values...)
	sort.Strings(sorted)
	return strings.Join(sorted, ",")
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func appendUnique(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
