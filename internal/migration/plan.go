// Package migration owns the read-only Docker migration plan: the artifact a
// user reviews (and later hands to the apply step) before anything on the
// source or the target host is touched.
//
// The package is deliberately free of I/O beyond reading and writing the plan
// file itself. Everything a plan states is derived from evidence collected
// through the single remote capability seam (internal/capability) and from the
// pure container analysis (internal/docker), so building a plan can never dump
// a database, archive a volume, start a service or overwrite a file.
package migration

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/volcano6/opspulse/internal/capability"
	"github.com/volcano6/opspulse/internal/config"
	"github.com/volcano6/opspulse/internal/docker"
	"github.com/volcano6/opspulse/internal/pathutil"
)

// PlanKind identifies the document type so a plan file cannot be confused with
// another OpsPulse artifact that happens to be YAML.
const PlanKind = "docker_migration_plan"

// PlanFormatVersion is the version of the plan document format. It is bumped
// whenever a reader could misinterpret an older or newer document; Load refuses
// anything it does not understand instead of guessing.
const PlanFormatVersion = 1

// PlanDirName is the directory (under the OpsPulse config directory) that holds
// plans saved with the default path.
const PlanDirName = "plans"

// Container modes as reported in a plan.
const (
	ModeCompose = "compose"
	ModeRun     = "run"
)

// Image acquisition strategies. They answer "where does the target get the
// image from", which decides whether an offline target is viable at all.
const (
	// ImagePullFromRegistry means the target can pull the image itself.
	ImagePullFromRegistry = "pull_from_registry"
	// ImagePreloadRequired means the target cannot reach a registry, so the
	// image has to be placed on it before the migration runs.
	ImagePreloadRequired = "preload_required"
	// ImageUnknown means the strategy could not be determined (for example
	// because no target server was given).
	ImageUnknown = "unknown"
)

// Consistency strategies recorded per service. They make the promise of the
// migration explicit: a logical dump is consistent, a file-level copy is not.
const (
	ConsistencyLogicalDump = "logical_dump"
	ConsistencyFileLevel   = "file_level"
	ConsistencyNone        = "none"
	ConsistencyArchive     = "archive"
)

// Risk codes are stable identifiers shared by the human report, the JSON
// report and the apply step (07). They are grouped by severity in RiskSummary.
const (
	// Hard blockers.
	RiskAnalysisBlocker          = "container_analysis_blocker"
	RiskSourceDockerUnavailable  = "source_docker_unavailable"
	RiskSourceResticMissing      = "source_restic_missing"
	RiskSourceHelperImageMissing = "source_helper_image_missing"
	RiskSourceDiskFull           = "source_disk_full"
	RiskSourceDiskTight          = "source_disk_tight"
	RiskTargetDockerUnavailable  = "target_docker_unavailable"
	RiskTargetComposeMissing     = "target_compose_missing"
	RiskTargetResticMissing      = "target_restic_missing"
	RiskTargetHelperImageMissing = "target_helper_image_missing"
	RiskTargetDiskFull           = "target_disk_full"
	RiskTargetPathUnavailable    = "target_path_unavailable"

	// Overridable warnings.
	RiskSensitiveEnv              = "sensitive_env"
	RiskExternalBindMount         = "external_bind_mount"
	RiskDeviceRequired            = "device_required"
	RiskCustomNetwork             = "custom_network_required"
	RiskAttributePartial          = "attribute_partial"
	RiskAttributeUnsupported      = "attribute_unsupported"
	RiskGeneratedCompose          = "generated_compose"
	RiskDatabaseFileLevel         = "database_file_level"
	RiskTargetRegistryUnreachable = "target_registry_unreachable"
	RiskTargetDiskTight           = "target_disk_tight"
	RiskTargetPathReadOnly        = "target_path_read_only"

	// Informational follow-ups.
	RiskTargetUnspecified = "target_unspecified"
	RiskBackupRepository  = "backup_repository_missing"
	RiskManualPreparation = "manual_preparation"
	RiskSystemMount       = "system_mount"
)

// Errors returned by the plan file helpers. They are sentinels so callers can
// turn them into actionable messages without string matching.
var (
	// ErrPlanFileExists is returned when saving would overwrite an existing
	// file. Plans are never overwritten implicitly.
	ErrPlanFileExists = errors.New("plan 文件已存在，拒绝覆盖")
	// ErrUnsupportedPlanVersion is returned when a plan file was written by a
	// different (older or newer) OpsPulse and cannot be interpreted.
	ErrUnsupportedPlanVersion = errors.New("unsupported plan format version")
	// ErrInvalidPlanDocument is returned when a file is not a migration plan.
	ErrInvalidPlanDocument = errors.New("not an OpsPulse migration plan")
)

// Risk is one entry of the plan's risk classification. Detail is what the
// operator has to know; Action is the manual step that resolves it.
type Risk struct {
	Code    string `yaml:"code" json:"code"`
	Service string `yaml:"service,omitempty" json:"service,omitempty"`
	Detail  string `yaml:"detail" json:"detail"`
	Action  string `yaml:"action,omitempty" json:"action,omitempty"`
}

// RiskSummary is the three-way classification every plan carries: hard blockers
// stop execution, warnings are visible and overridable, follow-ups are manual
// work the operator has to do themselves.
type RiskSummary struct {
	Blockers  []Risk `yaml:"blockers,omitempty" json:"blockers,omitempty"`
	Warnings  []Risk `yaml:"warnings,omitempty" json:"warnings,omitempty"`
	FollowUps []Risk `yaml:"follow_ups,omitempty" json:"follow_ups,omitempty"`
}

// Endpoint describes one side of a migration. An empty Server means the target
// was not pinned: the plan then keeps source paths and ports and says so.
type Endpoint struct {
	Server     string             `yaml:"server,omitempty" json:"server,omitempty"`
	Capability *capability.Report `yaml:"capability,omitempty" json:"capability,omitempty"`
}

// ServiceConfig is the configuration half of a service plan: where the
// orchestration file lives, what it declares, and which environment variable
// names (never values) it carries.
type ServiceConfig struct {
	ComposeFile       string   `yaml:"compose_file,omitempty" json:"compose_file,omitempty"`
	ComposeWorkingDir string   `yaml:"compose_working_dir,omitempty" json:"compose_working_dir,omitempty"`
	ComposeSource     string   `yaml:"compose_source" json:"compose_source"` // existing | generated
	RestartPolicy     string   `yaml:"restart_policy,omitempty" json:"restart_policy,omitempty"`
	Ports             []string `yaml:"ports,omitempty" json:"ports,omitempty"`
	Command           []string `yaml:"command,omitempty" json:"command,omitempty"`
	WorkingDir        string   `yaml:"working_dir,omitempty" json:"working_dir,omitempty"`
	EnvKeys           []string `yaml:"env_keys,omitempty" json:"env_keys,omitempty"`
	SensitiveEnv      []string `yaml:"sensitive_env,omitempty" json:"sensitive_env,omitempty"`
}

// DatabasePlan is the database half of a service plan.
type DatabasePlan struct {
	Engine   string `yaml:"engine" json:"engine"`
	Tool     string `yaml:"tool,omitempty" json:"tool,omitempty"`
	Strategy string `yaml:"strategy" json:"strategy"`
	DataDir  string `yaml:"data_dir,omitempty" json:"data_dir,omitempty"`
}

// Consistency records what the migration guarantees about data consistency.
type Consistency struct {
	Database string `yaml:"database" json:"database"`
	Volumes  string `yaml:"volumes" json:"volumes"`
}

// TargetPath is one source→target path mapping. It is editable in the plan file
// so a service can be adapted to the target host without renaming it.
type TargetPath struct {
	Kind   string `yaml:"kind" json:"kind"` // compose_project | bind_mount | named_volume
	Source string `yaml:"source" json:"source"`
	Target string `yaml:"target" json:"target"`
	Note   string `yaml:"note,omitempty" json:"note,omitempty"`
}

// BackupConfigPlan is the backup job the plan proposes for this service. It is
// recorded in the plan so the operator can review the change before anything is
// written to backups.yaml.
type BackupConfigPlan struct {
	JobName     string   `yaml:"job_name" json:"job_name"`
	Server      string   `yaml:"server" json:"server"`
	Paths       []string `yaml:"paths,omitempty" json:"paths,omitempty"`
	Backend     string   `yaml:"backend,omitempty" json:"backend,omitempty"`
	Tags        []string `yaml:"tags,omitempty" json:"tags,omitempty"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Exists      bool     `yaml:"exists" json:"exists"`
}

// ServicePlan is the per-container plan.
type ServicePlan struct {
	Name                 string                      `yaml:"name" json:"name"`
	Container            string                      `yaml:"container" json:"container"`
	Mode                 string                      `yaml:"mode" json:"mode"`
	ComposeProject       string                      `yaml:"compose_project,omitempty" json:"compose_project,omitempty"`
	ComposeService       string                      `yaml:"compose_service,omitempty" json:"compose_service,omitempty"`
	Image                string                      `yaml:"image" json:"image"`
	ImageAcquisition     string                      `yaml:"image_acquisition" json:"image_acquisition"`
	ImageAcquisitionNote string                      `yaml:"image_acquisition_note,omitempty" json:"image_acquisition_note,omitempty"`
	Config               ServiceConfig               `yaml:"config" json:"config"`
	Volumes              []docker.VolumeMount        `yaml:"volumes,omitempty" json:"volumes,omitempty"`
	BindMounts           []docker.VolumeMount        `yaml:"bind_mounts,omitempty" json:"bind_mounts,omitempty"`
	ExternalDependencies []docker.ExternalDependency `yaml:"external_dependencies,omitempty" json:"external_dependencies,omitempty"`
	Database             *DatabasePlan               `yaml:"database,omitempty" json:"database,omitempty"`
	Consistency          Consistency                 `yaml:"consistency" json:"consistency"`
	Attributes           []docker.AttributeSupport   `yaml:"attributes,omitempty" json:"attributes,omitempty"`
	TargetPaths          []TargetPath                `yaml:"target_paths,omitempty" json:"target_paths,omitempty"`
	TargetPorts          []string                    `yaml:"target_ports,omitempty" json:"target_ports,omitempty"`
	BackupConfig         *BackupConfigPlan           `yaml:"backup_config,omitempty" json:"backup_config,omitempty"`
	Blockers             []Risk                      `yaml:"blockers,omitempty" json:"blockers,omitempty"`
	Warnings             []Risk                      `yaml:"warnings,omitempty" json:"warnings,omitempty"`
	FollowUps            []Risk                      `yaml:"follow_ups,omitempty" json:"follow_ups,omitempty"`
}

// Plan is the saved, versioned migration plan.
type Plan struct {
	FormatVersion int           `yaml:"format_version" json:"format_version"`
	Kind          string        `yaml:"kind" json:"kind"`
	GeneratedAt   string        `yaml:"generated_at" json:"generated_at"`
	Generator     string        `yaml:"generator,omitempty" json:"generator,omitempty"`
	Source        Endpoint      `yaml:"source" json:"source"`
	Target        Endpoint      `yaml:"target" json:"target"`
	Services      []ServicePlan `yaml:"services" json:"services"`
	Risks         RiskSummary   `yaml:"risks" json:"risks"`
}

// ContainerEvidence pairs the inspected container metadata with its read-only
// migration analysis. BuildPlan consumes the pair, never the raw host.
type ContainerEvidence struct {
	Info     *docker.ContainerInfo
	Analysis *docker.ContainerAnalysis
}

// BackupDefaults are the repository settings a proposed backup job inherits
// from the existing configuration.
type BackupDefaults struct {
	Backend      string
	Env          map[string]string
	ExistingJobs []string
}

// BuildInput is every piece of evidence a plan is built from. It is a plain
// value: no host, store or clock is touched by BuildPlan itself.
type BuildInput struct {
	SourceServer     string
	SourceCapability *capability.Report
	TargetServer     string
	TargetCapability *capability.Report
	Containers       []ContainerEvidence
	Backup           BackupDefaults
	Generator        string
	Now              time.Time
}

// BuildPlan turns collected evidence into a plan. It performs no I/O and never
// mutates its input.
func BuildPlan(in BuildInput) (*Plan, error) {
	source := strings.TrimSpace(in.SourceServer)
	if source == "" {
		return nil, errors.New("source server is required to build a migration plan")
	}
	if len(in.Containers) == 0 {
		return nil, errors.New("at least one container is required to build a migration plan")
	}

	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}

	plan := &Plan{
		FormatVersion: PlanFormatVersion,
		Kind:          PlanKind,
		GeneratedAt:   now.UTC().Format(time.RFC3339),
		Generator:     in.Generator,
		Source:        Endpoint{Server: source, Capability: in.SourceCapability},
		Target:        Endpoint{Server: strings.TrimSpace(in.TargetServer), Capability: in.TargetCapability},
	}

	names := jobNames(in.Containers)
	plan.Services = make([]ServicePlan, 0, len(in.Containers))
	for i, evidence := range in.Containers {
		service, err := buildService(evidence, names[i], in)
		if err != nil {
			return nil, err
		}
		plan.Services = append(plan.Services, service)
	}

	plan.Risks = collectRisks(plan, in)
	return plan, nil
}

// HasBlockers reports whether anything in the plan forbids execution.
func (p *Plan) HasBlockers() bool {
	return p != nil && len(p.Risks.Blockers) > 0
}

// jobNames derives the backup job name of every selected container. A Compose
// service name is preferred (it is the name the operator uses), and a collision
// inside one batch falls back to the container name so two services can never
// share a job.
func jobNames(evidences []ContainerEvidence) []string {
	used := make(map[string]bool, len(evidences))
	names := make([]string, 0, len(evidences))
	for _, evidence := range evidences {
		base := ""
		if evidence.Analysis != nil {
			base = evidence.Analysis.ComposeService
		}
		if strings.TrimSpace(base) == "" && evidence.Info != nil {
			base = evidence.Info.Name
		}
		name := sanitizeJobName(base)
		if name == "" {
			name = "service"
		}
		if used[name] {
			name = sanitizeJobName(name + "-" + containerName(evidence))
		}
		for used[name] {
			name += "-x"
		}
		used[name] = true
		names = append(names, name)
	}
	return names
}

func containerName(evidence ContainerEvidence) string {
	if evidence.Info == nil {
		return ""
	}
	return evidence.Info.Name
}

// sanitizeJobName makes a name safe as a backup job name: lowercase ASCII with
// '.', '_' and '-', no leading separator and no "..".
func sanitizeJobName(name string) string {
	lowered := strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range lowered {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.ReplaceAll(b.String(), "..", ".")
	return strings.Trim(out, "-.")
}

func buildService(evidence ContainerEvidence, jobName string, in BuildInput) (ServicePlan, error) {
	info, analysis := evidence.Info, evidence.Analysis
	if info == nil || analysis == nil {
		return ServicePlan{}, fmt.Errorf("container evidence for %q needs both inspect metadata and analysis", jobName)
	}

	service := ServicePlan{
		Name:                 jobName,
		Container:            info.Name,
		ComposeProject:       analysis.ComposeProject,
		ComposeService:       analysis.ComposeService,
		Image:                info.Image,
		Config:               buildServiceConfig(info, analysis),
		Volumes:              append([]docker.VolumeMount(nil), analysis.NamedVolumes...),
		BindMounts:           append([]docker.VolumeMount(nil), analysis.BindMounts...),
		ExternalDependencies: append([]docker.ExternalDependency(nil), analysis.ExternalDeps...),
		Attributes:           append([]docker.AttributeSupport(nil), analysis.Attributes...),
	}
	service.Mode = ModeRun
	if analysis.IsCompose {
		service.Mode = ModeCompose
	}

	service.Database = buildDatabasePlan(analysis)
	service.Consistency = buildConsistency(analysis)
	service.ImageAcquisition, service.ImageAcquisitionNote = imageAcquisition(info, in)
	service.TargetPaths = buildTargetPaths(info, analysis)
	service.TargetPorts = formatPorts(info.Ports)
	service.BackupConfig = buildBackupConfig(service, info, in)
	service.Blockers, service.Warnings, service.FollowUps = serviceRisks(analysis, service)
	return service, nil
}

func buildServiceConfig(info *docker.ContainerInfo, analysis *docker.ContainerAnalysis) ServiceConfig {
	config := ServiceConfig{
		ComposeWorkingDir: analysis.ComposeWorkingDir,
		ComposeSource:     "generated",
		RestartPolicy:     info.RestartPolicy,
		Ports:             formatPorts(info.Ports),
		Command:           append([]string(nil), info.Command...),
		SensitiveEnv:      append([]string(nil), analysis.SensitiveEnv...),
	}
	if wd := strings.TrimSpace(info.WorkingDir); wd != "" && wd != "/" {
		config.WorkingDir = wd
	}
	config.EnvKeys = envKeys(info.Env)

	projectDir := backupProjectDir(info)
	composeFile := path.Join(projectDir, "compose.yaml")
	if analysis.IsCompose {
		config.ComposeSource = "existing"
		if files := firstConfigFile(analysis.ComposeConfigFiles); files != "" {
			composeFile = files
		}
	}
	config.ComposeFile = composeFile
	return config
}

// firstConfigFile picks the first entry of the Compose
// com.docker.compose.project.config_files label.
func firstConfigFile(label string) string {
	first, _, _ := strings.Cut(label, ",")
	return strings.TrimSpace(first)
}

func envKeys(env []string) []string {
	var keys []string
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if name = strings.TrimSpace(name); name != "" {
			keys = append(keys, name)
		}
	}
	return keys
}

func buildDatabasePlan(analysis *docker.ContainerAnalysis) *DatabasePlan {
	if analysis.Database == nil {
		return nil
	}
	return &DatabasePlan{
		Engine:   analysis.Database.Engine,
		Tool:     analysis.Database.Tool,
		Strategy: analysis.Database.Strategy,
		DataDir:  analysis.Database.DataDir,
	}
}

func buildConsistency(analysis *docker.ContainerAnalysis) Consistency {
	consistency := Consistency{Database: ConsistencyNone, Volumes: ConsistencyNone}
	if analysis.Database != nil {
		if analysis.Database.Strategy == docker.DatabaseStrategyFileLevel {
			consistency.Database = ConsistencyFileLevel
		} else {
			consistency.Database = ConsistencyLogicalDump
		}
	}
	if len(analysis.NamedVolumes) > 0 {
		consistency.Volumes = ConsistencyArchive
	}
	return consistency
}

// imageAcquisition decides how the target obtains the image. Without a pinned
// target nothing can be decided, and saying so is better than guessing.
func imageAcquisition(info *docker.ContainerInfo, in BuildInput) (string, string) {
	image := strings.TrimSpace(info.Image)
	if image == "" {
		return ImageUnknown, "容器没有可识别的镜像引用"
	}
	if strings.TrimSpace(in.TargetServer) == "" {
		return ImageUnknown, "未指定目标服务器：无法判断目标机是否可拉取 " + image
	}
	if in.TargetCapability == nil {
		return ImageUnknown, "缺少目标机能力报告：无法判断镜像获取方式"
	}
	switch in.TargetCapability.Registry.Status {
	case capability.StatusOK:
		return ImagePullFromRegistry, "目标机可访问镜像仓库，可直接 docker pull " + image
	case capability.StatusMissing:
		return ImagePreloadRequired, "目标机无法访问镜像仓库：迁移前必须在目标机预置 " + image
	default:
		return ImageUnknown, "无法判断目标机的镜像仓库可达性，请手工确认 " + image + " 在目标机是否可用"
	}
}

// backupProjectDir mirrors the directory convention of the container backup
// runner: a Compose project keeps its own working directory, a plain
// docker run container gets an OpsPulse-managed directory.
func backupProjectDir(info *docker.ContainerInfo) string {
	if dir := strings.TrimSpace(info.ComposeWorkingDir()); dir != "" {
		return dir
	}
	return path.Join("/var/lib/opspulse/containers", sanitizeJobName(info.Name))
}

func volumeName(mount docker.VolumeMount) string {
	if strings.TrimSpace(mount.Name) != "" {
		return mount.Name
	}
	return mount.Source
}

func buildTargetPaths(info *docker.ContainerInfo, analysis *docker.ContainerAnalysis) []TargetPath {
	var paths []TargetPath
	projectDir := backupProjectDir(info)
	note := "保留原 Compose 项目目录名；如需改动，请编辑本 plan 的 target"
	if !analysis.IsCompose {
		note = "反向生成的 compose.yaml 存放目录；如需改动，请编辑本 plan 的 target"
	}
	paths = append(paths, TargetPath{Kind: "compose_project", Source: projectDir, Target: projectDir, Note: note})

	for _, mount := range analysis.BindMounts {
		paths = append(paths, TargetPath{
			Kind:   "bind_mount",
			Source: mount.Source,
			Target: mount.Source,
			Note:   "宿主机目录需在目标机预先创建并授权，内容由备份/还原携带",
		})
	}
	for _, mount := range analysis.NamedVolumes {
		name := volumeName(mount)
		paths = append(paths, TargetPath{
			Kind:   "named_volume",
			Source: name,
			Target: name,
			Note:   "Docker named volume：由 docker volume create 重建后导入归档",
		})
	}
	return paths
}

func buildBackupConfig(service ServicePlan, info *docker.ContainerInfo, in BuildInput) *BackupConfigPlan {
	projectDir := backupProjectDir(info)
	paths := []string{projectDir}
	for _, mount := range service.BindMounts {
		if docker.IsSystemMount(mount.Source) {
			continue
		}
		if !pathutil.HasPathPrefix(mount.Source, projectDir) {
			paths = append(paths, mount.Source)
		}
	}
	return &BackupConfigPlan{
		JobName:     service.Name,
		Server:      strings.TrimSpace(in.SourceServer),
		Paths:       paths,
		Backend:     strings.TrimSpace(in.Backup.Backend),
		Tags:        []string{"container", service.Name},
		Description: fmt.Sprintf("Container backup for %s on %s (from migration plan)", service.Container, strings.TrimSpace(in.SourceServer)),
		Exists:      contains(in.Backup.ExistingJobs, service.Name),
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func formatPorts(ports []docker.PortMapping) []string {
	var out []string
	for _, port := range ports {
		if s := port.String(); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// serviceRisks classifies everything the analysis of one container implies.
func serviceRisks(analysis *docker.ContainerAnalysis, service ServicePlan) (blockers, warnings, followUps []Risk) {
	for _, blocker := range analysis.Blockers {
		blockers = append(blockers, Risk{
			Code:   RiskAnalysisBlocker,
			Detail: blocker,
			Action: "该容器无法按单容器迁移：请手工处理（例如拆分为独立服务）后再生成 plan",
		})
	}

	if len(analysis.SensitiveEnv) > 0 {
		names := strings.Join(analysis.SensitiveEnv, ", ")
		warnings = append(warnings, Risk{
			Code:   RiskSensitiveEnv,
			Detail: fmt.Sprintf("疑似敏感环境变量 %s 会原样写入生成的 compose.yaml 与备份快照", names),
			Action: "优先改用外部 secret 引用（env 文件或 Docker secret）；写入备份配置前需要显式确认",
		})
	}

	for _, dep := range analysis.ExternalDeps {
		switch dep.Kind {
		case "bind_mount":
			warnings = append(warnings, Risk{
				Code:   RiskExternalBindMount,
				Detail: fmt.Sprintf("外部 bind mount %s → %s 的内容随备份携带，但目标机目录必须预先存在并授权", dep.Source, dep.Target),
				Action: fmt.Sprintf("在目标机准备 %s（磁盘、权限、挂载点）", dep.Source),
			})
		case "device":
			warnings = append(warnings, Risk{
				Code:   RiskDeviceRequired,
				Detail: fmt.Sprintf("容器依赖宿主机设备 %s，迁移不会携带设备本身", dep.Source),
				Action: "确认目标机具备同名设备，否则该服务无法启动",
			})
		case "network":
			warnings = append(warnings, Risk{
				Code:   RiskCustomNetwork,
				Detail: fmt.Sprintf("容器使用自定义网络 %s，迁移不会自动创建该网络", dep.Source),
				Action: fmt.Sprintf("在目标机执行 docker network create %s", dep.Source),
			})
		case "system_mount":
			followUps = append(followUps, Risk{
				Code:   RiskSystemMount,
				Detail: fmt.Sprintf("系统挂载 %s → %s 不归档，由目标机 Docker 运行时自动提供", dep.Source, dep.Target),
			})
		}
	}

	for _, attr := range analysis.Attributes {
		if coveredAttribute(attr.Attribute) {
			continue
		}
		switch attr.Level {
		case docker.SupportUnsupported:
			warnings = append(warnings, Risk{
				Code:   RiskAttributeUnsupported,
				Detail: fmt.Sprintf("Docker 属性 %s 不受支持（%s）：迁移后会丢失", attr.Attribute, attr.Detail),
				Action: "确认该属性可以丢失，或手工在目标机补齐",
			})
		case docker.SupportPartial:
			warnings = append(warnings, Risk{
				Code:   RiskAttributePartial,
				Detail: fmt.Sprintf("Docker 属性 %s 只能部分保留（%s）：语义可能变化", attr.Attribute, attr.Detail),
				Action: "迁移后在目标机逐项核对该属性",
			})
		}
	}

	if !analysis.IsCompose {
		warnings = append(warnings, Risk{
			Code:   RiskGeneratedCompose,
			Detail: "普通 docker run 容器：迁移时反向生成单 service compose.yaml，未覆盖的字段需人工核对",
			Action: "迁移后对比 docker inspect 与生成的 compose.yaml",
		})
	}

	if analysis.Database != nil && analysis.Database.Strategy == docker.DatabaseStrategyFileLevel {
		warnings = append(warnings, Risk{
			Code:   RiskDatabaseFileLevel,
			Detail: fmt.Sprintf("数据库 %s 没有受支持的逻辑导出策略：只能做文件级复制，不保证一致性", analysis.Database.Engine),
			Action: "迁移前停写或使用数据库自带的备份工具，并人工确认数据一致性",
		})
	}

	if service.Mode == ModeRun || !analysis.IsCompose {
		followUps = append(followUps, Risk{
			Code:   RiskManualPreparation,
			Detail: "目标机需要 Compose 引擎来启动本服务（docker compose 或 docker-compose）",
			Action: "确认目标机已安装 Compose，或用 docker run 手工复现",
		})
	}

	return blockers, warnings, followUps
}

// coveredAttribute reports attributes whose risk is already expressed by a
// dedicated code, so the plan does not report the same risk twice.
func coveredAttribute(attribute string) bool {
	switch attribute {
	case "bind_mount", "environment":
		return true
	default:
		return false
	}
}

// collectRisks aggregates endpoint risks and per-service risks into the plan's
// classification. Duplicate (code, service, detail) triples are collapsed so a
// batch plan stays readable.
func collectRisks(plan *Plan, in BuildInput) RiskSummary {
	summary := RiskSummary{}
	hasVolumes := false
	for _, service := range plan.Services {
		if len(service.Volumes) > 0 {
			hasVolumes = true
		}
	}

	summary.add(sourceRisks(in.SourceCapability, hasVolumes)...)

	if plan.Target.Server == "" {
		summary.FollowUps = append(summary.FollowUps, Risk{
			Code:   RiskTargetUnspecified,
			Detail: "未指定目标服务器：目标路径与端口暂按源机原样记录",
			Action: "执行前用 --target 指定目标服务器重新生成 plan，并在目标机复核路径与端口",
		})
	} else {
		summary.add(targetRisks(plan.Target.Server, in.TargetCapability, hasVolumes)...)
	}

	for _, service := range plan.Services {
		summary.add(scoped(service.Blockers, service.Name)...)
		summary.add(scoped(service.Warnings, service.Name)...)
		summary.add(scoped(service.FollowUps, service.Name)...)
	}

	if strings.TrimSpace(in.Backup.Backend) == "" {
		summary.FollowUps = append(summary.FollowUps, Risk{
			Code:   RiskBackupRepository,
			Detail: "没有可继承的 restic 仓库（backups.yaml 与 RESTIC_REPOSITORY 都为空）",
			Action: "先配置仓库（ops backup run 或 backups.yaml），否则无法写入备份配置、也无法执行备份",
		})
	}
	return summary
}

func scoped(risks []Risk, service string) []Risk {
	out := make([]Risk, 0, len(risks))
	for _, risk := range risks {
		risk.Service = service
		out = append(out, risk)
	}
	return out
}

// sourceRisks classifies the source host prerequisites.
func sourceRisks(report *capability.Report, hasVolumes bool) []Risk {
	if report == nil {
		return []Risk{{
			Code:   RiskSourceDockerUnavailable,
			Detail: "缺少源机能力报告：无法确认 Docker / restic / helper 镜像 / 磁盘前置条件",
			Action: "重新运行 ops docker plan 以获取能力报告",
		}}
	}

	var risks []Risk
	if report.Docker.Status != capability.StatusOK {
		risks = append(risks, Risk{
			Code:   RiskSourceDockerUnavailable,
			Detail: fmt.Sprintf("源机 Docker 不可用（%s）：%s", report.Docker.Status, report.Docker.Detail),
			Action: "在源机安装并启动 Docker 后重新生成 plan",
		})
	}
	if report.Restic.Status != capability.StatusOK {
		risks = append(risks, Risk{
			Code:   RiskSourceResticMissing,
			Detail: "源机没有可用的 restic：备份步骤无法执行",
			Action: "在源机安装 restic 后重新生成 plan",
		})
	}
	if hasVolumes && report.HelperImage.Status == capability.StatusMissing {
		risks = append(risks, Risk{
			Code:   RiskSourceHelperImageMissing,
			Detail: "源机没有可用的 helper 镜像（alpine/busybox）：named volume 归档会失败",
			Action: "在源机预置 alpine:3.20（或用 OPSPULSE_HELPER_IMAGE 指定镜像）",
		})
	}
	risks = append(risks, diskRisks(report, true)...)
	return risks
}

// targetRisks classifies the target host prerequisites. It only runs when a
// target server was pinned: an unspecified target must not invent blockers.
func targetRisks(server string, report *capability.Report, hasVolumes bool) []Risk {
	if report == nil {
		return []Risk{{
			Code:   RiskTargetDockerUnavailable,
			Detail: fmt.Sprintf("目标机 %s 缺少能力报告：无法确认 Docker / Compose / restic / 路径 / 磁盘前置条件", server),
			Action: "重新运行 ops docker plan --target 以获取目标机能力报告",
		}}
	}

	var risks []Risk
	if report.Docker.Status != capability.StatusOK {
		risks = append(risks, Risk{
			Code:   RiskTargetDockerUnavailable,
			Detail: fmt.Sprintf("目标机 %s 的 Docker 不可用（%s）：%s", server, report.Docker.Status, report.Docker.Detail),
			Action: "在目标机安装并启动 Docker 后重新生成 plan",
		})
	}
	if report.Compose.Status != capability.StatusOK {
		risks = append(risks, Risk{
			Code:   RiskTargetComposeMissing,
			Detail: fmt.Sprintf("目标机 %s 没有可用的 Compose（%s）：迁移后无法启动服务", server, report.Compose.Status),
			Action: "在目标机安装 docker compose 插件或 docker-compose",
		})
	}
	if report.Restic.Status != capability.StatusOK {
		risks = append(risks, Risk{
			Code:   RiskTargetResticMissing,
			Detail: fmt.Sprintf("目标机 %s 没有可用的 restic：无法从仓库还原数据", server),
			Action: "在目标机安装 restic 后重新生成 plan",
		})
	}
	if hasVolumes && report.HelperImage.Status == capability.StatusMissing {
		risks = append(risks, Risk{
			Code:   RiskTargetHelperImageMissing,
			Detail: fmt.Sprintf("目标机 %s 没有可用的 helper 镜像（alpine/busybox）：named volume 还原会失败", server),
			Action: "在目标机预置 alpine:3.20（或用 OPSPULSE_HELPER_IMAGE 指定镜像）",
		})
	}
	if report.Registry.Status == capability.StatusMissing {
		risks = append(risks, Risk{
			Code:   RiskTargetRegistryUnreachable,
			Detail: fmt.Sprintf("目标机 %s 无法访问镜像仓库：所有镜像必须预先存在于目标机", server),
			Action: "在目标机预置所需镜像（docker pull 后 docker save/load，或提前放通仓库访问）",
		})
	}
	risks = append(risks, diskRisks(report, false)...)

	for _, check := range report.Paths {
		switch check.Status {
		case capability.StatusMissing:
			risks = append(risks, Risk{
				Code:   RiskTargetPathUnavailable,
				Detail: fmt.Sprintf("目标机路径 %s 不可用（%s）", check.Name, check.Detail),
				Action: fmt.Sprintf("在目标机创建并授权 %s", check.Name),
			})
		case capability.StatusPartial:
			risks = append(risks, Risk{
				Code:   RiskTargetPathReadOnly,
				Detail: fmt.Sprintf("目标机路径 %s 需要预处理（%s）", check.Name, check.Detail),
				Action: fmt.Sprintf("确认 %s 的属主与权限", check.Name),
			})
		}
	}
	return risks
}

// diskRisks turns the capability disk verdict into a blocker (no room at all)
// or a warning (tight). A migration copies the whole service, so both matter.
func diskRisks(report *capability.Report, isSource bool) []Risk {
	endpoint := "目标机"
	if isSource {
		endpoint = "源机"
	}
	switch report.Disk.Status {
	case capability.StatusMissing:
		return []Risk{{
			Code:   diskRiskCode(isSource, true),
			Detail: fmt.Sprintf("%s磁盘空间不足：%s", endpoint, report.Disk.Detail),
			Action: "清理或扩容后再执行迁移",
		}}
	case capability.StatusPartial:
		return []Risk{{
			Code:   diskRiskCode(isSource, false),
			Detail: fmt.Sprintf("%s磁盘偏紧：%s", endpoint, report.Disk.Detail),
			Action: "预留足够空间，避免备份/还原中途失败",
		}}
	default:
		return nil
	}
}

func diskRiskCode(isSource, full bool) string {
	if isSource {
		if full {
			return RiskSourceDiskFull
		}
		return RiskSourceDiskTight
	}
	if full {
		return RiskTargetDiskFull
	}
	return RiskTargetDiskTight
}

// add appends risks, skipping exact duplicates so a batch plan does not repeat
// the same endpoint risk once per service.
func (r *RiskSummary) add(risks ...Risk) {
	for _, risk := range risks {
		switch {
		case r.contains(r.Blockers, risk):
		case r.contains(r.Warnings, risk):
		case r.contains(r.FollowUps, risk):
		case risk.isBlocker():
			r.Blockers = append(r.Blockers, risk)
		case risk.isFollowUp():
			r.FollowUps = append(r.FollowUps, risk)
		default:
			r.Warnings = append(r.Warnings, risk)
		}
	}
}

func (r *RiskSummary) contains(list []Risk, risk Risk) bool {
	for _, existing := range list {
		if existing.Code == risk.Code && existing.Service == risk.Service && existing.Detail == risk.Detail {
			return true
		}
	}
	return false
}

// isBlocker and isFollowUp decide which bucket a risk belongs to. The code
// tables are the single source of truth so a new risk cannot silently land in
// the wrong severity.
func (r Risk) isBlocker() bool {
	switch r.Code {
	case RiskAnalysisBlocker, RiskSourceDockerUnavailable, RiskSourceResticMissing,
		RiskSourceHelperImageMissing, RiskSourceDiskFull, RiskTargetDockerUnavailable,
		RiskTargetComposeMissing, RiskTargetResticMissing, RiskTargetHelperImageMissing,
		RiskTargetDiskFull, RiskTargetPathUnavailable:
		return true
	default:
		return false
	}
}

func (r Risk) isFollowUp() bool {
	switch r.Code {
	case RiskTargetUnspecified, RiskBackupRepository, RiskManualPreparation, RiskSystemMount:
		return true
	default:
		return false
	}
}

// Encode renders the plan as the YAML artifact written to disk. A short header
// states what the file is; the body stays machine-readable.
func (p *Plan) Encode() ([]byte, error) {
	if p == nil {
		return nil, errors.New("plan is nil")
	}
	body, err := yaml.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("marshal migration plan: %w", err)
	}
	header := fmt.Sprintf("# OpsPulse Docker 迁移 plan（format_version=%d）\n"+
		"# 只读决策记录：本文件不会被执行，也不会修改源机或目标机。\n"+
		"# 可手工编辑 services[].target_paths / target_ports，执行前需重新校验。\n",
		PlanFormatVersion)
	return append([]byte(header), body...), nil
}

// Save writes the plan to path. It never overwrites an existing file: a plan is
// a reviewable record, and silently replacing one would destroy the evidence a
// previous review was based on.
func Save(path string, p *Plan) error {
	clean := strings.TrimSpace(path)
	if clean == "" {
		return errors.New("plan path cannot be empty")
	}
	data, err := p.Encode()
	if err != nil {
		return err
	}
	if dir := filepath.Dir(clean); dir != "" && dir != "." {
		// Plans live beside the other OpsPulse config files, so they use the
		// same permissions: a private directory and a 0600 document.
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create plan directory %q: %w", dir, err)
		}
	}
	file, err := os.OpenFile(clean, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: %s（请换一个文件名，或用 --save 指定新路径）", ErrPlanFileExists, clean)
		}
		return fmt.Errorf("write plan %q: %w", clean, err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write plan %q: %w", clean, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("write plan %q: %w", clean, err)
	}
	return nil
}

// Load reads a saved plan. It rejects documents of another kind or format
// version instead of interpreting them.
func Load(path string) (*Plan, error) {
	clean := strings.TrimSpace(path)
	if clean == "" {
		return nil, errors.New("plan path cannot be empty")
	}
	data, err := os.ReadFile(clean)
	if err != nil {
		return nil, fmt.Errorf("read plan %q: %w", clean, err)
	}

	var plan Plan
	if err := yaml.Unmarshal(data, &plan); err != nil {
		return nil, fmt.Errorf("parse plan %q: %w", clean, err)
	}
	if plan.Kind != PlanKind {
		return nil, fmt.Errorf("%w: %s（kind=%q）", ErrInvalidPlanDocument, clean, plan.Kind)
	}
	if plan.FormatVersion != PlanFormatVersion {
		return nil, fmt.Errorf("%w: %s 的 format_version=%d，本版本只支持 %d（请用生成该文件的 OpsPulse 版本读取）",
			ErrUnsupportedPlanVersion, clean, plan.FormatVersion, PlanFormatVersion)
	}
	return &plan, nil
}

// DefaultPlanPath returns the path a plan is saved to when the user does not
// pass --save. Plans are namespaced by server and label under the OpsPulse
// config directory, and the timestamp keeps consecutive plans distinct.
func DefaultPlanPath(server, label string, now time.Time) string {
	if now.IsZero() {
		now = time.Now()
	}
	name := fmt.Sprintf("%s-%s-%s.yaml",
		now.UTC().Format("20060102T150405Z"),
		sanitizeJobName(server),
		sanitizeJobName(label))
	return filepath.Join(config.Dir(), PlanDirName, name)
}
