// Package capability is the single high-level seam between the Docker
// discovery commands and a remote (or local) Docker host. It answers two
// questions without mutating anything:
//
//  1. Which containers run on the host? (ListContainers / InspectContainer)
//  2. Which migration prerequisites does the host have? (Probe)
//
// Both halves are exposed through the one Remote interface so that the Docker
// migration tickets (06/07/08) can mock a single seam in tests instead of
// re-implementing remote probes per command. Every command this package runs on
// a target is read-only: `command -v`, `docker info/ps/inspect/image inspect/
// manifest inspect`, `df`, and `test -w`.
package capability

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/volcano6/opspulse/internal/docker"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/shellquote"
)

// ErrDockerMissing is returned when the target host has no usable Docker CLI.
var ErrDockerMissing = errors.New("docker is not installed on the target host")

// dockerMissingSentinel is printed by the generated scripts when Docker is
// absent. It matches the marker the existing `ops ps` command already uses, so
// both entry points fail the same way.
const dockerMissingSentinel = "ERR_DOCKER_NOT_FOUND"

// Status is the tri-state (plus "unknown") outcome of a capability check.
type Status string

const (
	// StatusOK means the capability is present and usable.
	StatusOK Status = "ok"
	// StatusPartial means the capability exists but is incomplete or conditional.
	StatusPartial Status = "partial"
	// StatusMissing means the capability is absent.
	StatusMissing Status = "missing"
	// StatusUnknown means the capability could not be determined.
	StatusUnknown Status = "unknown"
)

// Check is one line of the capability report.
type Check struct {
	Name   string `json:"name" yaml:"name"`
	Status Status `json:"status" yaml:"status"`
	Detail string `json:"detail,omitempty" yaml:"detail,omitempty"`
	// Exists and Writable are only meaningful for path checks (StatusOK /
	// StatusPartial / StatusMissing on a directory). They are recorded
	// separately from Status because apply has to tell "directory already
	// exists" (a target conflict) apart from "directory is writable".
	Exists   bool `json:"exists,omitempty" yaml:"exists,omitempty"`
	Writable bool `json:"writable,omitempty" yaml:"writable,omitempty"`
}

// DiskSpace reports free space on the filesystem that holds the migration
// staging paths.
type DiskSpace struct {
	Status      Status  `json:"status" yaml:"status"`
	TotalGB     float64 `json:"total_gb,omitempty" yaml:"total_gb,omitempty"`
	AvailableGB float64 `json:"available_gb,omitempty" yaml:"available_gb,omitempty"`
	UsedPct     int     `json:"used_pct,omitempty" yaml:"used_pct,omitempty"`
	Detail      string  `json:"detail,omitempty" yaml:"detail,omitempty"`
}

// Report is the structured capability contract produced by Remote. It is the
// single output shape reused by `ops docker inspect` and, later, by the plan
// (06), apply (07) and health (08) steps.
type Report struct {
	Server        string    `json:"server" yaml:"server"`
	Reachable     bool      `json:"reachable" yaml:"reachable"`
	OS            string    `json:"os,omitempty" yaml:"os,omitempty"`
	Arch          string    `json:"arch,omitempty" yaml:"arch,omitempty"`
	Docker        Check     `json:"docker" yaml:"docker"`
	Compose       Check     `json:"compose" yaml:"compose"`
	Restic        Check     `json:"restic" yaml:"restic"`
	ShellTools    []Check   `json:"shell_tools" yaml:"shell_tools"`
	DatabaseTools []Check   `json:"database_tools" yaml:"database_tools"`
	HelperImage   Check     `json:"helper_image" yaml:"helper_image"`
	Registry      Check     `json:"registry" yaml:"registry"`
	Disk          DiskSpace `json:"disk" yaml:"disk"`
	Paths         []Check   `json:"paths" yaml:"paths"`
	Images        []Check   `json:"images,omitempty" yaml:"images,omitempty"`
	Notes         []string  `json:"notes,omitempty" yaml:"notes,omitempty"`
}

// ProbeOptions tunes a capability probe.
type ProbeOptions struct {
	// Paths are host paths whose existence and writability the report should
	// include - for example the Compose project directory and bind-mount
	// sources of the container being inspected.
	Paths []string
	// Images are container images whose local presence the report should
	// include. Apply uses this to confirm a plan's preload-required images are
	// actually present on a target that cannot pull them.
	Images []string
}

// Remote is the single high-level seam between the Docker migration commands
// and a remote (or local) Docker host.
type Remote interface {
	// Probe returns the host's migration capability report.
	Probe(ctx context.Context, target executor.Target, opts ProbeOptions) (*Report, error)
	// ListContainers returns the containers visible to Docker on the host.
	ListContainers(ctx context.Context, target executor.Target, includeStopped bool) ([]docker.ContainerSummary, error)
	// InspectContainer returns the structured runtime metadata of one container.
	InspectContainer(ctx context.Context, target executor.Target, name string) (*docker.ContainerInfo, error)
	// HealthCheck verifies that the services a migration started are actually
	// usable (container running, ports listening, database ready, optional HTTP
	// endpoint). It lives on the same seam as the other probes so every caller
	// mocks one interface, and it is read-only like them.
	HealthCheck(ctx context.Context, target executor.Target, opts HealthOptions) (*HealthReport, error)
}

// RemoteProber is the production Remote implementation. It talks to the target
// through the shared executor and never mutates the host.
type RemoteProber struct {
	exec executor.Executor
}

// NewRemote returns the production Remote backed by exec.
func NewRemote(exec executor.Executor) *RemoteProber {
	return &RemoteProber{exec: exec}
}

// Probe runs the read-only capability script and parses its output.
func (r *RemoteProber) Probe(ctx context.Context, target executor.Target, opts ProbeOptions) (*Report, error) {
	script, err := BuildProbeScript(opts)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	res, execErr := r.exec.Execute(ctx, target, "docker-capability-probe", script, &buf)
	if execErr != nil {
		return nil, fmt.Errorf("capability probe on %q failed: %w", target.Name, execErr)
	}
	if res != nil && !res.Success {
		return nil, fmt.Errorf("capability probe on %q exited with code %d: %v", target.Name, res.ExitCode, res.Error)
	}

	return ParseProbeOutput(target.Name, buf.String()), nil
}

// ListContainers lists the containers Docker reports on the target.
func (r *RemoteProber) ListContainers(ctx context.Context, target executor.Target, includeStopped bool) ([]docker.ContainerSummary, error) {
	var buf bytes.Buffer
	res, execErr := r.exec.Execute(ctx, target, "docker-ps", BuildListScript(includeStopped), &buf)
	out := buf.String()
	if strings.Contains(out, dockerMissingSentinel) {
		return nil, fmt.Errorf("%w (%s)", ErrDockerMissing, target.Name)
	}
	if execErr != nil {
		return nil, fmt.Errorf("failed to list containers on %q: %w%s", target.Name, execErr, outputSuffix(out))
	}
	if res != nil && !res.Success {
		return nil, fmt.Errorf("docker ps failed on %q: %v%s", target.Name, res.Error, outputSuffix(out))
	}
	return docker.ParsePsJSON(out)
}

// InspectContainer returns the structured metadata of one container.
func (r *RemoteProber) InspectContainer(ctx context.Context, target executor.Target, name string) (*docker.ContainerInfo, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("container name cannot be empty")
	}

	var buf bytes.Buffer
	res, execErr := r.exec.Execute(ctx, target, "docker-inspect", BuildInspectScript(name), &buf)
	out := buf.String()
	if strings.Contains(out, dockerMissingSentinel) {
		return nil, fmt.Errorf("%w (%s)", ErrDockerMissing, target.Name)
	}
	if execErr != nil || (res != nil && !res.Success) {
		if isMissingContainerOutput(out) {
			return nil, fmt.Errorf("%w: container %q on %q", docker.ErrContainerNotFound, name, target.Name)
		}
		if execErr != nil {
			return nil, fmt.Errorf("failed to inspect container %q on %q: %w%s", name, target.Name, execErr, outputSuffix(out))
		}
		return nil, fmt.Errorf("docker inspect %q failed on %q: %v%s", name, target.Name, res.Error, outputSuffix(out))
	}

	info, parseErr := docker.ParseInspectJSON(buf.Bytes())
	if parseErr != nil {
		return nil, fmt.Errorf("failed to parse inspect metadata for %q: %w", name, parseErr)
	}
	return info, nil
}

// isMissingContainerOutput recognizes Docker's "No such object" error for a
// container name that does not exist on the host.
func isMissingContainerOutput(out string) bool {
	return strings.Contains(out, "No such object") || strings.Contains(out, "No such container")
}

// outputSuffix renders the remote error text (Docker prints one short line to
// stderr on failure) so the CLI can show why a probe failed instead of only the
// remote exit code. Inspect output is never included on success, and this path
// only runs on failure, so container metadata cannot leak through it.
func outputSuffix(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > 200 {
			line = line[:200] + "..."
		}
		return ": " + line
	}
	return ""
}

// BuildListScript returns the read-only `docker ps` script used to enumerate
// containers. It never mutates host state.
func BuildListScript(includeStopped bool) string {
	allFlag := ""
	if includeStopped {
		allFlag = "-a "
	}
	return fmt.Sprintf(`if ! command -v docker >/dev/null 2>&1; then
  echo "%s"
  exit 127
fi
docker ps %s--format '{{json .}}'
`, dockerMissingSentinel, allFlag)
}

// BuildInspectScript returns the read-only `docker inspect` script for one
// container. The container name is single-quoted so it cannot break out of the
// command.
func BuildInspectScript(name string) string {
	return fmt.Sprintf(`if ! command -v docker >/dev/null 2>&1; then
  echo "%s" >&2
  exit 127
fi
docker inspect --type container %s
`, dockerMissingSentinel, shellquote.Quote(strings.TrimSpace(name)))
}

// BuildProbeScript returns the read-only capability probe script. Paths are
// validated and single-quoted before being embedded.
func BuildProbeScript(opts ProbeOptions) (string, error) {
	var b strings.Builder
	b.WriteString(probeScriptHead)

	quoted, err := probeQuotedPaths(opts.Paths)
	if err != nil {
		return "", err
	}
	if len(quoted) > 0 {
		fmt.Fprintf(&b, "\nfor opspulse_path in %s; do\n", strings.Join(quoted, " "))
		b.WriteString(probeScriptPaths)
	}

	images, err := probeQuotedImages(opts.Images)
	if err != nil {
		return "", err
	}
	if len(images) > 0 {
		fmt.Fprintf(&b, "\nif command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then\n  for opspulse_image in %s; do\n",
			strings.Join(images, " "))
		b.WriteString(probeScriptImages)
	}

	b.WriteString(probeScriptTail)
	return b.String(), nil
}

// probeQuotedPaths validates and single-quotes the paths to probe. It returns
// an error through BuildProbeScript for the first invalid path.
func probeQuotedPaths(paths []string) ([]string, error) {
	quoted := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if err := probePathErr(p); err != nil {
			return nil, err
		}
		quoted = append(quoted, shellquote.Quote(p))
	}
	return quoted, nil
}

// probeQuotedImages validates and single-quotes the image references to check.
// Image references never contain a newline or a pipe either, and both would
// corrupt the line-oriented probe output.
func probeQuotedImages(images []string) ([]string, error) {
	quoted := make([]string, 0, len(images))
	for _, image := range images {
		image = strings.TrimSpace(image)
		if image == "" {
			continue
		}
		if strings.ContainsAny(image, "\n\r") || strings.Contains(image, "|") {
			return nil, fmt.Errorf("invalid probe image %q: must not contain newlines or '|'", image)
		}
		quoted = append(quoted, shellquote.Quote(image))
	}
	return quoted, nil
}

// probePathErr rejects paths that would corrupt the probe's line-oriented
// output format. Real host paths never contain a newline or a pipe.
func probePathErr(p string) error {
	if strings.ContainsAny(p, "\n\r") {
		return fmt.Errorf("invalid probe path %q: must not contain newlines", p)
	}
	if strings.Contains(p, "|") {
		return fmt.Errorf("invalid probe path %q: must not contain '|'", p)
	}
	return nil
}

const probeScriptHead = `set +e
echo "---OPSPULSE-CAPABILITY---"
echo "OS=$(uname -s 2>/dev/null)"
echo "ARCH=$(uname -m 2>/dev/null)"

if command -v docker >/dev/null 2>&1; then
  echo "DOCKER=present"
  if docker info >/dev/null 2>&1; then
    echo "DOCKER_DAEMON=running"
  else
    echo "DOCKER_DAEMON=stopped"
  fi
else
  echo "DOCKER=missing"
  echo "DOCKER_DAEMON=unknown"
fi

if docker compose version >/dev/null 2>&1; then
  echo "COMPOSE=plugin"
elif command -v docker-compose >/dev/null 2>&1; then
  echo "COMPOSE=standalone"
else
  echo "COMPOSE=missing"
fi

if command -v restic >/dev/null 2>&1; then echo "RESTIC=present"; else echo "RESTIC=missing"; fi

for opspulse_tool in tar gzip gunzip base64; do
  if command -v "$opspulse_tool" >/dev/null 2>&1; then
    echo "TOOL_${opspulse_tool}=present"
  else
    echo "TOOL_${opspulse_tool}=missing"
  fi
done

for opspulse_tool in mysqldump mysql mariadb-dump pg_dump pg_dumpall psql; do
  if command -v "$opspulse_tool" >/dev/null 2>&1; then
    echo "DBTOOL_${opspulse_tool}=present"
  else
    echo "DBTOOL_${opspulse_tool}=missing"
  fi
done

if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
  if docker image inspect "${OPSPULSE_HELPER_IMAGE:-alpine:3.20}" >/dev/null 2>&1; then
    echo "HELPER_IMAGE=present"
  elif docker image inspect busybox:1.36 >/dev/null 2>&1; then
    echo "HELPER_IMAGE=fallback"
  elif docker image inspect alpine >/dev/null 2>&1; then
    echo "HELPER_IMAGE=fallback"
  elif docker image inspect busybox >/dev/null 2>&1; then
    echo "HELPER_IMAGE=fallback"
  else
    echo "HELPER_IMAGE=missing"
  fi

  if command -v timeout >/dev/null 2>&1; then OPSPULSE_TIMEOUT="timeout 10"; else OPSPULSE_TIMEOUT=""; fi
  if $OPSPULSE_TIMEOUT docker manifest inspect docker.io/library/alpine:3.20 >/dev/null 2>&1; then
    echo "REGISTRY=reachable"
  else
    echo "REGISTRY=unreachable"
  fi
else
  echo "HELPER_IMAGE=unknown"
  echo "REGISTRY=unknown"
fi

echo "DISK=$(df -kP / 2>/dev/null | awk 'NR==2 {print $2 " " $3 " " $4 " " $5}')"
`

const probeScriptPaths = `  if [ -e "$opspulse_path" ]; then opspulse_exists=yes; else opspulse_exists=no; fi
  if [ -e "$opspulse_path" ]; then
    if [ -w "$opspulse_path" ]; then opspulse_writable=yes; else opspulse_writable=no; fi
  else
    opspulse_parent=$(dirname "$opspulse_path" 2>/dev/null)
    if [ -n "$opspulse_parent" ] && [ -w "$opspulse_parent" ]; then opspulse_writable=yes; else opspulse_writable=no; fi
  fi
  echo "PATH=$opspulse_path|$opspulse_exists|$opspulse_writable"
done
`

const probeScriptImages = `    if docker image inspect "$opspulse_image" >/dev/null 2>&1; then
      echo "IMAGE=$opspulse_image|present"
    else
      echo "IMAGE=$opspulse_image|missing"
    fi
  done
fi
`

const probeScriptTail = `echo "---OPSPULSE-CAPABILITY-END---"
exit 0
`

// ParseProbeOutput turns the KEY=VALUE output of the capability script into a
// structured report. It is a pure function so it can be tested with canned
// output, and it never panics on malformed lines.
func ParseProbeOutput(server, raw string) *Report {
	values := make(map[string]string)
	var pathLines []string
	var imageLines []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "PATH":
			pathLines = append(pathLines, value)
			continue
		case "IMAGE":
			imageLines = append(imageLines, value)
			continue
		}
		values[key] = value
	}

	report := &Report{
		Server:        server,
		Reachable:     true,
		OS:            values["OS"],
		Arch:          values["ARCH"],
		Docker:        dockerCheck(values),
		Compose:       composeCheck(values),
		Restic:        presenceCheck("restic", values["RESTIC"]),
		ShellTools:    toolChecks("TOOL_", []string{"tar", "gzip", "gunzip", "base64"}, values),
		DatabaseTools: toolChecks("DBTOOL_", []string{"mysqldump", "mysql", "mariadb-dump", "pg_dump", "pg_dumpall", "psql"}, values),
		HelperImage:   helperImageCheck(values),
		Registry:      registryCheck(values),
		Disk:          diskCheck(values),
		Paths:         pathChecks(pathLines),
		Images:        imageChecks(imageLines),
	}
	report.Notes = reportHints(report)
	return report
}

func dockerCheck(values map[string]string) Check {
	switch values["DOCKER"] {
	case "present":
		if values["DOCKER_DAEMON"] == "running" {
			return Check{Name: "docker", Status: StatusOK, Detail: "Docker 已安装且 daemon 正在运行"}
		}
		return Check{Name: "docker", Status: StatusPartial, Detail: "Docker 已安装但 daemon 未运行"}
	case "missing":
		return Check{Name: "docker", Status: StatusMissing, Detail: "未安装 Docker，无法发现或迁移任何容器"}
	default:
		return Check{Name: "docker", Status: StatusUnknown}
	}
}

func composeCheck(values map[string]string) Check {
	switch values["COMPOSE"] {
	case "plugin":
		return Check{Name: "compose", Status: StatusOK, Detail: "docker compose 插件可用"}
	case "standalone":
		return Check{Name: "compose", Status: StatusOK, Detail: "独立式 docker-compose 可用"}
	case "missing":
		return Check{Name: "compose", Status: StatusMissing, Detail: "未检测到 docker compose / docker-compose，恢复后无法自动启动服务"}
	default:
		return Check{Name: "compose", Status: StatusUnknown}
	}
}

func presenceCheck(name, value string) Check {
	switch value {
	case "present":
		return Check{Name: name, Status: StatusOK}
	case "missing":
		return Check{Name: name, Status: StatusMissing}
	default:
		return Check{Name: name, Status: StatusUnknown}
	}
}

func toolChecks(prefix string, tools []string, values map[string]string) []Check {
	checks := make([]Check, 0, len(tools))
	for _, tool := range tools {
		checks = append(checks, presenceCheck(tool, values[prefix+tool]))
	}
	return checks
}

func helperImageCheck(values map[string]string) Check {
	switch values["HELPER_IMAGE"] {
	case "present":
		return Check{Name: "helper_image", Status: StatusOK, Detail: "卷归档所需的 helper 镜像已存在"}
	case "fallback":
		return Check{Name: "helper_image", Status: StatusPartial, Detail: "配置的 helper 镜像缺失，但有可用的回退镜像"}
	case "missing":
		return Check{Name: "helper_image", Status: StatusMissing, Detail: "没有可用的 helper 镜像（alpine/busybox），卷归档会失败"}
	default:
		return Check{Name: "helper_image", Status: StatusUnknown}
	}
}

func registryCheck(values map[string]string) Check {
	switch values["REGISTRY"] {
	case "reachable":
		return Check{Name: "registry", Status: StatusOK, Detail: "目标机可访问镜像仓库"}
	case "unreachable":
		return Check{Name: "registry", Status: StatusMissing, Detail: "目标机无法访问镜像仓库，镜像必须已存在于本地"}
	default:
		return Check{Name: "registry", Status: StatusUnknown}
	}
}

func diskCheck(values map[string]string) DiskSpace {
	fields := strings.Fields(values["DISK"])
	if len(fields) < 4 {
		return DiskSpace{Status: StatusUnknown, Detail: "无法读取磁盘信息"}
	}

	totalKB, err1 := strconv.ParseFloat(fields[0], 64)
	availKB, err2 := strconv.ParseFloat(fields[2], 64)
	usedPct, err3 := strconv.Atoi(strings.TrimSuffix(fields[3], "%"))
	if err1 != nil || err2 != nil || err3 != nil {
		return DiskSpace{Status: StatusUnknown, Detail: "无法解析磁盘信息"}
	}

	disk := DiskSpace{
		Status:      StatusOK,
		TotalGB:     roundGB(totalKB),
		AvailableGB: roundGB(availKB),
		UsedPct:     usedPct,
	}
	switch {
	case usedPct >= diskCriticalPct || disk.AvailableGB < diskCriticalFreeGB:
		disk.Status = StatusMissing
		disk.Detail = fmt.Sprintf("磁盘空间不足：已用 %d%%，可用 %.1f GB", usedPct, disk.AvailableGB)
	case usedPct >= diskWarnPct || disk.AvailableGB < diskWarnFreeGB:
		disk.Status = StatusPartial
		disk.Detail = fmt.Sprintf("磁盘偏紧：已用 %d%%，可用 %.1f GB", usedPct, disk.AvailableGB)
	default:
		disk.Detail = fmt.Sprintf("已用 %d%%，可用 %.1f GB", usedPct, disk.AvailableGB)
	}
	return disk
}

// Disk thresholds for migration feasibility. They are intentionally local to
// this package: `ops doctor` measures long-term host health, while a migration
// needs enough headroom for a full copy of the container data.
const (
	diskWarnPct        = 85
	diskCriticalPct    = 90
	diskWarnFreeGB     = 5
	diskCriticalFreeGB = 2
)

func roundGB(kb float64) float64 {
	return float64(int64(kb/(1024*1024)*10)) / 10
}

func pathChecks(lines []string) []Check {
	checks := make([]Check, 0, len(lines))
	for _, line := range lines {
		parts := strings.Split(line, "|")
		if len(parts) != 3 {
			continue
		}
		p, exists, writable := parts[0], parts[1], parts[2]
		check := Check{
			Name:     p,
			Exists:   exists == "yes",
			Writable: writable == "yes",
		}
		switch {
		case exists == "yes" && writable == "yes":
			check.Status = StatusOK
			check.Detail = "目录存在且可写"
		case exists == "yes":
			check.Status = StatusPartial
			check.Detail = "目录存在但当前用户不可写"
		case writable == "yes":
			check.Status = StatusPartial
			check.Detail = "目录不存在，但父目录可写，可在迁移时创建"
		default:
			check.Status = StatusMissing
			check.Detail = "目录不存在且父目录不可写"
		}
		checks = append(checks, check)
	}
	return checks
}

// imageChecks turns the probe's IMAGE lines into per-image presence checks.
// A name whose probe line is malformed is skipped rather than guessed.
func imageChecks(lines []string) []Check {
	checks := make([]Check, 0, len(lines))
	for _, line := range lines {
		image, state, ok := strings.Cut(line, "|")
		image = strings.TrimSpace(image)
		if !ok || image == "" {
			continue
		}
		switch strings.TrimSpace(state) {
		case "present":
			checks = append(checks, Check{Name: image, Status: StatusOK, Detail: "目标机已有该镜像"})
		case "missing":
			checks = append(checks, Check{Name: image, Status: StatusMissing, Detail: "目标机没有该镜像，需要 docker pull 或预置"})
		default:
			checks = append(checks, Check{Name: image, Status: StatusUnknown})
		}
	}
	return checks
}

// reportHints turns the missing/partial checks into the user-facing hints the
// CLI prints after the capability table.
func reportHints(r *Report) []string {
	var hints []string
	if r.Docker.Status == StatusMissing {
		hints = append(hints, "远端未安装 Docker：无法发现或迁移容器。")
	} else if r.Docker.Status == StatusPartial {
		hints = append(hints, "远端 Docker daemon 未运行：请先在目标机启动 Docker。")
	}
	if r.Compose.Status == StatusMissing {
		hints = append(hints, "远端缺少 docker compose / docker-compose：迁移后无法自动启动 Compose 服务。")
	}
	if r.Restic.Status == StatusMissing {
		hints = append(hints, "远端未安装 restic：备份与还原无法执行。")
	}
	for _, tool := range r.ShellTools {
		if tool.Status == StatusMissing {
			hints = append(hints, fmt.Sprintf("远端缺少 %s：卷归档/数据库压缩会失败。", tool.Name))
		}
	}
	if r.HelperImage.Status == StatusMissing {
		hints = append(hints, "远端没有可用的 helper 镜像（alpine/busybox）：卷归档会失败；可预置镜像或用 OPSPULSE_HELPER_IMAGE 指定。")
	} else if r.HelperImage.Status == StatusPartial {
		hints = append(hints, "远端缺少配置的 helper 镜像，但有可用的回退镜像；建议预置 alpine:3.20。")
	}
	if r.Registry.Status == StatusMissing {
		hints = append(hints, "远端无法访问镜像仓库：镜像必须已存在于本地，否则迁移无法拉取。")
	}
	if r.Disk.Status == StatusMissing || r.Disk.Status == StatusPartial {
		hints = append(hints, fmt.Sprintf("远端磁盘空间紧张（%s）：迁移前请清理或扩容。", r.Disk.Detail))
	}
	for _, p := range r.Paths {
		if p.Status == StatusMissing {
			hints = append(hints, fmt.Sprintf("远端路径 %s 不可写：迁移前需创建并授权。", p.Name))
		}
	}
	for _, image := range r.Images {
		if image.Status == StatusMissing {
			hints = append(hints, fmt.Sprintf("远端没有镜像 %s：迁移前需 docker pull 或预置。", image.Name))
		}
	}
	return hints
}
