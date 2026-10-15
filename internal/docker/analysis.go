package docker

import (
	"fmt"
	"sort"
	"strings"
)

// SupportLevel classifies how faithfully a Docker attribute survives the
// supported single-host migration flow. The values are stable identifiers that
// both the human report and the structured (JSON) report share.
type SupportLevel string

const (
	// SupportSupported means the attribute is carried across a migration.
	SupportSupported SupportLevel = "supported"
	// SupportPartial means the attribute is carried but can change semantics or
	// needs manual preparation on the target host.
	SupportPartial SupportLevel = "partial"
	// SupportUnsupported means the attribute is dropped or cannot be reproduced
	// by the supported flow.
	SupportUnsupported SupportLevel = "unsupported"
)

// AttributeSupport is the support verdict for one Docker attribute of a container.
type AttributeSupport struct {
	Attribute string       `json:"attribute" yaml:"attribute"`
	Level     SupportLevel `json:"level" yaml:"level"`
	Detail    string       `json:"detail,omitempty" yaml:"detail,omitempty"`
}

// Database dump strategies reported by Analyze. A logical dump is consistent;
// a file-level copy is not, and the plan has to say which one applies.
const (
	// DatabaseStrategyLogicalDump means OpsPulse can take a consistent hot dump.
	DatabaseStrategyLogicalDump = "logical_dump"
	// DatabaseStrategyFileLevel means only the raw data directory can be copied.
	DatabaseStrategyFileLevel = "file_level"
)

// ExternalDependency is a host resource a container relies on that a migration
// cannot carry automatically: host directories, sockets, devices and networks.
type ExternalDependency struct {
	Kind     string `json:"kind" yaml:"kind"` // "bind_mount", "system_mount", "device", "network"
	Source   string `json:"source" yaml:"source"`
	Target   string `json:"target,omitempty" yaml:"target,omitempty"`
	Required bool   `json:"required" yaml:"required"`
	Reason   string `json:"reason,omitempty" yaml:"reason,omitempty"`
}

// DatabaseAnalysis describes the logical dump strategy for a database container.
// Strategy is "logical_dump" when OpsPulse can take a consistent hot dump and
// "file_level" when only the raw data directory can be copied.
type DatabaseAnalysis struct {
	Engine   string `json:"engine" yaml:"engine"`
	Tool     string `json:"tool,omitempty" yaml:"tool,omitempty"`
	Strategy string `json:"strategy" yaml:"strategy"`
	DataDir  string `json:"data_dir,omitempty" yaml:"data_dir,omitempty"`
}

// ContainerAnalysis is the read-only migration verdict for one container: what
// it is, which host resources it depends on, and which Docker attributes the
// supported flow can carry. It never carries secret values - environment
// variables are reported by name only.
type ContainerAnalysis struct {
	Name               string               `json:"name" yaml:"name"`
	Image              string               `json:"image" yaml:"image"`
	IsCompose          bool                 `json:"is_compose" yaml:"is_compose"`
	ComposeProject     string               `json:"compose_project,omitempty" yaml:"compose_project,omitempty"`
	ComposeService     string               `json:"compose_service,omitempty" yaml:"compose_service,omitempty"`
	ComposeWorkingDir  string               `json:"compose_working_dir,omitempty" yaml:"compose_working_dir,omitempty"`
	ComposeConfigFiles string               `json:"compose_config_files,omitempty" yaml:"compose_config_files,omitempty"`
	RestartPolicy      string               `json:"restart_policy,omitempty" yaml:"restart_policy,omitempty"`
	NamedVolumes       []VolumeMount        `json:"named_volumes,omitempty" yaml:"named_volumes,omitempty"`
	BindMounts         []VolumeMount        `json:"bind_mounts,omitempty" yaml:"bind_mounts,omitempty"`
	ExternalDeps       []ExternalDependency `json:"external_dependencies,omitempty" yaml:"external_dependencies,omitempty"`
	Database           *DatabaseAnalysis    `json:"database,omitempty" yaml:"database,omitempty"`
	SensitiveEnv       []string             `json:"sensitive_env,omitempty" yaml:"sensitive_env,omitempty"`
	Attributes         []AttributeSupport   `json:"attributes" yaml:"attributes"`
	Warnings           []string             `json:"warnings,omitempty" yaml:"warnings,omitempty"`
	Blockers           []string             `json:"blockers,omitempty" yaml:"blockers,omitempty"`
}

// Analyze builds the read-only migration analysis for an inspected container.
// It performs no I/O: the caller is responsible for the single `docker inspect`
// that produced info.
func Analyze(info *ContainerInfo) *ContainerAnalysis {
	if info == nil {
		return nil
	}

	a := &ContainerAnalysis{
		Name:               info.Name,
		Image:              info.Image,
		IsCompose:          info.IsCompose(),
		ComposeProject:     info.ComposeProject(),
		ComposeService:     info.ComposeService(),
		ComposeWorkingDir:  info.ComposeWorkingDir(),
		ComposeConfigFiles: info.Labels[LabelComposeConfigFiles],
		RestartPolicy:      info.RestartPolicy,
		SensitiveEnv:       SensitiveEnvVars(info),
	}

	for _, m := range info.Mounts {
		if IsNamedVolume(m) {
			a.NamedVolumes = append(a.NamedVolumes, m)
			continue
		}
		a.BindMounts = append(a.BindMounts, m)
	}
	sortMounts(a.NamedVolumes)
	sortMounts(a.BindMounts)

	a.ExternalDeps = externalDependencies(info)
	a.Database = analyzeDatabase(info)
	a.Attributes = attributeSupport(info, a)
	a.Warnings = collectWarnings(info, a)
	a.Blockers = collectBlockers(info)
	return a
}

// HostPaths returns the host-side paths a migration has to prepare on the
// target: the Compose project directory and the sources of non-system bind
// mounts. Named volumes are managed by Docker and are deliberately excluded.
func (a *ContainerAnalysis) HostPaths() []string {
	if a == nil {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	add(a.ComposeWorkingDir)
	for _, m := range a.BindMounts {
		if IsSystemMount(m.Source) {
			continue
		}
		add(m.Source)
	}
	sort.Strings(out)
	return out
}

func sortMounts(mounts []VolumeMount) {
	sort.Slice(mounts, func(i, j int) bool {
		if mounts[i].Destination != mounts[j].Destination {
			return mounts[i].Destination < mounts[j].Destination
		}
		return mounts[i].Source < mounts[j].Source
	})
}

func externalDependencies(info *ContainerInfo) []ExternalDependency {
	var deps []ExternalDependency
	for _, m := range info.Mounts {
		if IsNamedVolume(m) {
			continue
		}
		if IsSystemMount(m.Source) {
			deps = append(deps, ExternalDependency{
				Kind:   "system_mount",
				Source: m.Source,
				Target: m.Destination,
				Reason: "运行时注入的宿主机路径，不会归档，由目标机的 Docker 自动提供",
			})
			continue
		}
		deps = append(deps, ExternalDependency{
			Kind:     "bind_mount",
			Source:   m.Source,
			Target:   m.Destination,
			Required: true,
			Reason:   "宿主机目录需在目标机预先准备",
		})
	}
	for _, d := range info.Devices {
		deps = append(deps, ExternalDependency{
			Kind:     "device",
			Source:   d,
			Required: true,
			Reason:   "宿主机设备/GPU 无法随迁移携带",
		})
	}
	if nm := strings.TrimSpace(info.NetworkMode); isCustomNetwork(nm) {
		deps = append(deps, ExternalDependency{
			Kind:     "network",
			Source:   nm,
			Required: true,
			Reason:   "目标机需存在同名 Docker 网络",
		})
	}
	return deps
}

// isCustomNetwork reports whether a network mode names a user-defined network
// that a migration has to recreate, as opposed to Docker's built-in modes.
func isCustomNetwork(mode string) bool {
	switch mode {
	case "", "default", "bridge", "host", "none":
		return false
	}
	return !strings.HasPrefix(mode, "container:")
}

func analyzeDatabase(info *ContainerInfo) *DatabaseAnalysis {
	engine := info.DatabaseEngine()
	if engine == "" {
		return nil
	}

	db := &DatabaseAnalysis{Engine: engine, Strategy: DatabaseStrategyLogicalDump}
	switch engine {
	case EngineMySQL:
		db.Tool = "mysqldump --single-transaction --quick --all-databases"
	case EnginePostgres:
		db.Tool = "pg_dumpall"
	default:
		db.Strategy = DatabaseStrategyFileLevel
	}

	for _, m := range info.Mounts {
		if info.IsDatabaseDataDir(m.Destination) {
			db.DataDir = m.Destination
			break
		}
	}
	return db
}

func attributeSupport(info *ContainerInfo, a *ContainerAnalysis) []AttributeSupport {
	var attrs []AttributeSupport
	add := func(name string, level SupportLevel, detail string) {
		attrs = append(attrs, AttributeSupport{Attribute: name, Level: level, Detail: detail})
	}

	add("image", SupportSupported, info.Image)

	if rp := strings.TrimSpace(info.RestartPolicy); rp != "" && rp != "no" {
		add("restart_policy", SupportSupported, rp)
	}
	if len(info.Ports) > 0 {
		add("ports", SupportSupported, formatPorts(info.Ports))
	}
	if len(info.Env) > 0 {
		if len(a.SensitiveEnv) > 0 {
			add("environment", SupportPartial,
				fmt.Sprintf("含疑似凭据变量 %s，会原样写入生成的配置与备份", strings.Join(a.SensitiveEnv, ", ")))
		} else {
			add("environment", SupportSupported, "")
		}
	}
	if len(info.Command) > 0 {
		add("command", SupportSupported, strings.Join(info.Command, " "))
	}
	if wd := strings.TrimSpace(info.WorkingDir); wd != "" && wd != "/" {
		add("working_dir", SupportSupported, wd)
	}
	if len(a.NamedVolumes) > 0 {
		add("named_volume", SupportSupported, mountSources(a.NamedVolumes))
	}
	if len(a.BindMounts) > 0 {
		add("bind_mount", SupportPartial, mountSources(a.BindMounts))
	}
	if len(info.Devices) > 0 {
		add("device", SupportUnsupported, strings.Join(info.Devices, ", "))
	}

	if nm := strings.TrimSpace(info.NetworkMode); nm != "" {
		switch {
		case nm == "host":
			add("network_mode", SupportSupported, "host")
		case strings.HasPrefix(nm, "container:"):
			add("network_mode", SupportUnsupported, nm)
		case isCustomNetwork(nm):
			add("network_mode", SupportPartial, nm)
		}
	}

	if len(info.Entrypoint) > 0 {
		if info.IsCompose() {
			add("entrypoint", SupportSupported, "由原 Compose 文件保留")
		} else {
			add("entrypoint", SupportPartial, "反向生成的 compose 不写 entrypoint，若覆盖过镜像默认值需手工补回")
		}
	}
	if info.Healthcheck {
		if info.IsCompose() {
			add("healthcheck", SupportSupported, "由原 Compose 文件保留")
		} else {
			add("healthcheck", SupportPartial, "反向生成的 compose 不写 healthcheck")
		}
	}
	if info.Privileged {
		add("privileged", SupportPartial, "目标机需要显式授权特权模式")
	}
	if len(info.CapAdd) > 0 {
		add("capabilities", SupportPartial, strings.Join(info.CapAdd, ", "))
	}
	return attrs
}

func collectWarnings(info *ContainerInfo, a *ContainerAnalysis) []string {
	var warnings []string
	for _, dep := range a.ExternalDeps {
		switch dep.Kind {
		case "bind_mount":
			warnings = append(warnings, fmt.Sprintf("外部 bind mount %s → %s：目标机需预先准备该目录", dep.Source, dep.Target))
		case "device":
			warnings = append(warnings, fmt.Sprintf("宿主机设备 %s：目标机必须具备同样的设备", dep.Source))
		case "network":
			warnings = append(warnings, fmt.Sprintf("自定义网络 %s：目标机需存在同名网络", dep.Source))
		}
	}
	if len(a.SensitiveEnv) > 0 {
		warnings = append(warnings, fmt.Sprintf("疑似敏感环境变量 %s：反向生成的配置会原样包含取值", strings.Join(a.SensitiveEnv, ", ")))
	}
	if !info.IsCompose() {
		warnings = append(warnings, "普通 docker run 容器：迁移时反向生成单 service compose.yaml，未覆盖的字段需人工核对")
	}
	if info.Privileged {
		warnings = append(warnings, "容器以特权模式运行：目标机需要显式授权")
	}
	if len(info.CapAdd) > 0 {
		warnings = append(warnings, fmt.Sprintf("容器追加了 capabilities %s", strings.Join(info.CapAdd, ", ")))
	}
	return warnings
}

func collectBlockers(info *ContainerInfo) []string {
	var blockers []string
	if strings.TrimSpace(info.Image) == "" {
		blockers = append(blockers, "容器没有可识别的镜像引用，无法确定要迁移的运行环境")
	}
	if nm := strings.TrimSpace(info.NetworkMode); strings.HasPrefix(nm, "container:") {
		blockers = append(blockers, fmt.Sprintf("容器共享另一个容器的网络命名空间（network_mode=%s），无法按单容器迁移", nm))
	}
	return blockers
}

func formatPorts(ports []PortMapping) string {
	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		if s := p.String(); s != "" {
			parts = append(parts, s)
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

func mountSources(mounts []VolumeMount) string {
	parts := make([]string, 0, len(mounts))
	for _, m := range mounts {
		parts = append(parts, m.String())
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}
