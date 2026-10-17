package capability

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/shellquote"
)

// Post-start health check kinds. A migration is only useful when the service it
// started is actually reachable, so the four checks answer different questions:
// does the container stay up, does the host listen on the published ports, does
// the database accept connections, and does an optional HTTP endpoint answer.
const (
	HealthKindContainer = "container"
	HealthKindPorts     = "ports"
	HealthKindDatabase  = "database"
	HealthKindHTTP      = "http"
)

// Post-start health check statuses. They are deliberately different from the
// capability Status vocabulary: a health check is about one service that is
// supposed to be running right now, not about whether a tool is installed.
//
//   - ok: the check ran and the service passed it.
//   - failed: the check ran and the service did not pass it.
//   - manual: the check could not be automated on this host, so a human has to
//     confirm the service instead of the tool claiming it is healthy.
//   - skipped: there was nothing to check (for example no HTTP URL configured).
const (
	HealthOK      = "ok"
	HealthFailed  = "failed"
	HealthManual  = "manual"
	HealthSkipped = "skipped"
)

// Docker daemon states the health probe reports. They decide whether the
// container/database checks could be attempted at all.
const (
	healthDockerRunning = "running"
	healthDockerStopped = "stopped"
	healthDockerMissing = "missing"
	healthDockerUnknown = "unknown"
)

// Readiness budget for the asynchronous checks (database ping, HTTP probe).
// About 30 seconds covers a service that still has to finish its first boot,
// while keeping a broken service from hanging a migration report.
const (
	defaultHealthRetries  = 15
	defaultHealthInterval = 2
)

// HealthCheckResult is one line of the post-start health report. NextStep is
// always filled for a non-ok check: a failed health check is only useful if it
// says what to do next.
type HealthCheckResult struct {
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Status   string `json:"status"`
	Detail   string `json:"detail,omitempty"`
	NextStep string `json:"next_step,omitempty"`
}

// ServiceHealth is the post-start verdict for one migrated service. Healthy is
// the strict claim "every check passed": a failed check proves the service
// broken, and a manual check (no ss/netstat/curl on the target, unparsable
// probe output) means the tool could not confirm it. Both are reported as not
// healthy, because a check that cannot prove usability must not be reported as
// usability - that is exactly the "process is running, so it must be fine"
// mistake this step exists to avoid. A deliberately skipped check (no HTTP URL
// configured, not a database service) does not degrade the verdict.
type ServiceHealth struct {
	Service   string              `json:"service"`
	Container string              `json:"container,omitempty"`
	Healthy   bool                `json:"healthy"`
	Checks    []HealthCheckResult `json:"checks"`
}

// HealthReport is the result of one post-start health probe.
type HealthReport struct {
	Server   string          `json:"server"`
	Services []ServiceHealth `json:"services"`
}

// Unhealthy returns the services that are not proven usable, so a caller can
// render the verdict without re-deriving the rule.
func (r *HealthReport) Unhealthy() []ServiceHealth {
	if r == nil {
		return nil
	}
	var out []ServiceHealth
	for _, service := range r.Services {
		if !service.Healthy {
			out = append(out, service)
		}
	}
	return out
}

// Failed returns the services with at least one failed check: the tool proved
// them broken. A caller that drives an exit code must use this, not Unhealthy,
// because a check that could not be automated is not evidence of a broken
// service and must not be reported as one.
func (r *HealthReport) Failed() []ServiceHealth {
	if r == nil {
		return nil
	}
	var out []ServiceHealth
	for _, service := range r.Services {
		if len(service.FailedChecks()) > 0 {
			out = append(out, service)
		}
	}
	return out
}

// Unverified returns the services with no failure but at least one check the
// tool could not automate. They are neither success nor failure: the operator
// still has to confirm them by hand, which the report says explicitly.
func (r *HealthReport) Unverified() []ServiceHealth {
	if r == nil {
		return nil
	}
	var out []ServiceHealth
	for _, service := range r.Services {
		if len(service.FailedChecks()) == 0 && len(service.ManualChecks()) > 0 {
			out = append(out, service)
		}
	}
	return out
}

// ManualChecks returns the checks of one service that a human has to confirm
// because the tool could not.
func (s ServiceHealth) ManualChecks() []HealthCheckResult {
	var out []HealthCheckResult
	for _, check := range s.Checks {
		if check.Status == HealthManual {
			out = append(out, check)
		}
	}
	return out
}

// FailedChecks returns the checks of one service that failed.
func (s ServiceHealth) FailedChecks() []HealthCheckResult {
	var out []HealthCheckResult
	for _, check := range s.Checks {
		if check.Status == HealthFailed {
			out = append(out, check)
		}
	}
	return out
}

// HealthTarget describes one service to verify on the target host. Every field
// is derived from a migration plan: Container is the expected container name,
// ComposeProject/ComposeService let the probe resolve the real container by
// label (the restore engine picks the container name, not OpsPulse), Ports are
// the published host ports in "8080/tcp" form, DatabaseEngine selects the
// readiness command, and HTTPURL is the optional address that should answer.
type HealthTarget struct {
	Service        string   `json:"service"`
	Container      string   `json:"container,omitempty"`
	ComposeProject string   `json:"compose_project,omitempty"`
	ComposeService string   `json:"compose_service,omitempty"`
	Ports          []string `json:"ports,omitempty"`
	DatabaseEngine string   `json:"database_engine,omitempty"`
	HTTPURL        string   `json:"http_url,omitempty"`
}

// HealthOptions describes one health probe: the services to verify plus an
// optional override of the readiness retry budget.
type HealthOptions struct {
	Targets         []HealthTarget
	Retries         int
	IntervalSeconds int
}

// HealthCheck runs the read-only post-start health probe through the same
// remote seam as the capability probe, and parses its output. Like every other
// method on Remote it never mutates the target: the script only reads container
// state, listening sockets and (optionally) an HTTP endpoint.
func (r *RemoteProber) HealthCheck(ctx context.Context, target executor.Target, opts HealthOptions) (*HealthReport, error) {
	script, err := BuildHealthScript(opts)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	res, execErr := r.exec.Execute(ctx, target, "docker-health-check", script, &buf)
	out := buf.String()
	if execErr != nil {
		return nil, fmt.Errorf("health check on %q failed: %w%s", target.Name, execErr, outputSuffix(out))
	}
	if res != nil && !res.Success {
		return nil, fmt.Errorf("health check on %q exited with code %d: %v%s", target.Name, res.ExitCode, res.Error, outputSuffix(out))
	}

	return ParseHealthOutput(target.Name, opts, out), nil
}

// BuildHealthScript returns the post-start health probe script. Container and
// Compose names come from a plan, so they are validated and single-quoted
// before they reach the shell.
func BuildHealthScript(opts HealthOptions) (string, error) {
	retries, interval := healthRetrySettings(opts)

	var b strings.Builder
	b.WriteString(healthScriptHead)
	for index, target := range opts.Targets {
		if err := validateHealthTarget(target); err != nil {
			return "", err
		}
		b.WriteString(healthTargetScript(index, target, retries, interval))
	}
	b.WriteString(healthScriptTail)
	return b.String(), nil
}

// healthRetrySettings resolves the readiness budget, so the script generator
// and the report text always describe the same numbers.
func healthRetrySettings(opts HealthOptions) (retries, interval int) {
	retries, interval = opts.Retries, opts.IntervalSeconds
	if retries <= 0 {
		retries = defaultHealthRetries
	}
	if interval <= 0 {
		interval = defaultHealthInterval
	}
	return retries, interval
}

// healthTargetScript renders the per-service section of the probe script.
func healthTargetScript(index int, target HealthTarget, retries, interval int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n# service %d: %s\n", index, target.Service)
	b.WriteString(healthContainerScript(index, target))

	for _, port := range target.Ports {
		host, protocol := splitHealthPort(port)
		fmt.Fprintf(&b, `if ! command -v ss >/dev/null 2>&1 && ! command -v netstat >/dev/null 2>&1; then
  echo "PORT|%d|%s|unknown"
elif opspulse_port_listening %s %s; then
  echo "PORT|%d|%s|yes"
else
  echo "PORT|%d|%s|no"
fi
`, index, port, shellquote.Quote(host), shellquote.Quote(protocol), index, port, index, port)
	}

	if target.DatabaseEngine != "" {
		fmt.Fprintf(&b, `if [ "$OPSPULSE_DOCKER_OK" -ne 1 ] || [ -z "$opspulse_container" ]; then
  echo "DB|%d|%s|unknown"
else
  opspulse_db=no
  opspulse_try=0
  while [ "$opspulse_try" -lt %d ]; do
    if docker exec "$opspulse_container" sh -c %s >/dev/null 2>&1; then
      opspulse_db=yes
      break
    fi
    opspulse_try=$((opspulse_try + 1))
    sleep %d
  done
  echo "DB|%d|%s|$opspulse_db"
fi
`, index, target.DatabaseEngine, retries, shellquote.Quote(databaseProbeCommand(target.DatabaseEngine)),
			interval, index, target.DatabaseEngine)
	}

	if target.HTTPURL != "" {
		quotedURL := shellquote.Quote(target.HTTPURL)
		// The URL travels as a single-quoted variable: curl receives it
		// verbatim, and the report echoes the expansion (not shell syntax, and
		// not re-parsed for $( ) or backticks) so a URL is never able to run a
		// command on the target.
		fmt.Fprintf(&b, `opspulse_url=%s
if ! command -v curl >/dev/null 2>&1; then
  echo "HTTP|%d|$opspulse_url|tool-missing|no"
else
  opspulse_http=no
  opspulse_code=
  opspulse_try=0
  while [ "$opspulse_try" -lt %d ]; do
    opspulse_code=$(curl -s -o /dev/null -w '%%{http_code}' --max-time 5 "$opspulse_url" 2>/dev/null)
    case "$opspulse_code" in
      2*|3*) opspulse_http=yes; break ;;
    esac
    opspulse_try=$((opspulse_try + 1))
    sleep %d
  done
  echo "HTTP|%d|$opspulse_url|$opspulse_code|$opspulse_http"
fi
`, quotedURL, index, retries, interval, index)
	}
	return b.String()
}

// healthContainerScript renders the container-state section. A Compose service
// is resolved through its labels, because the container name on the target is
// chosen by the Compose engine and may differ from the source name.
func healthContainerScript(index int, target HealthTarget) string {
	fallback := shellquote.Quote(target.Container)
	resolve := "opspulse_container=" + fallback
	if target.ComposeProject != "" && target.ComposeService != "" {
		resolve = fmt.Sprintf("opspulse_container=$(opspulse_compose_container %s %s %s)",
			shellquote.Quote("label=com.docker.compose.project="+target.ComposeProject),
			shellquote.Quote("label=com.docker.compose.service="+target.ComposeService),
			fallback)
	}
	return fmt.Sprintf(`if [ "$OPSPULSE_DOCKER_OK" -ne 1 ]; then
  echo "CONTAINER|%d||unknown|unknown|unknown|"
else
  %s
  if [ -z "$opspulse_container" ]; then
    echo "CONTAINER|%d||unknown|unknown|unknown|"
  else
    opspulse_inspect=$(docker inspect --type container --format '{{.State.Status}}|{{.State.Restarting}}|{{.RestartCount}}|{{.State.StartedAt}}' "$opspulse_container" 2>/dev/null)
    if [ -z "$opspulse_inspect" ]; then
      echo "CONTAINER|%d|$opspulse_container|missing|unknown|unknown|"
    else
      echo "CONTAINER|%d|$opspulse_container|$opspulse_inspect"
    fi
  fi
fi
`, index, resolve, index, index, index)
}

// databaseProbeCommand is the in-container readiness command. It mirrors the
// probes the import scripts already use, so a service that imports data is
// checked the same way it is fed.
func databaseProbeCommand(engine string) string {
	switch engine {
	case "mysql":
		return `user="${MYSQL_USER:-root}"; export MYSQL_PWD="${MYSQL_ROOT_PASSWORD:-${MARIADB_ROOT_PASSWORD:-${MYSQL_PASSWORD:-}}}"; mysqladmin ping -u "$user" --silent`
	case "postgres":
		return `pg_isready -U "${POSTGRES_USER:-postgres}"`
	default:
		return ""
	}
}

// validateHealthTarget rejects values that would either corrupt the probe's
// line-oriented output or reach the shell unquoted.
func validateHealthTarget(target HealthTarget) error {
	for _, field := range []struct{ name, value string }{
		{"service", target.Service},
		{"container", target.Container},
		{"compose_project", target.ComposeProject},
		{"compose_service", target.ComposeService},
	} {
		if strings.ContainsAny(field.value, "\n\r") || strings.Contains(field.value, "|") {
			return fmt.Errorf("invalid health target %s %q: must not contain newlines or '|'", field.name, field.value)
		}
	}
	for _, port := range target.Ports {
		host, protocol := splitHealthPort(port)
		number, err := strconv.Atoi(host)
		if err != nil || number < 1 || number > 65535 {
			return fmt.Errorf("invalid health target port %q: host port must be 1-65535", port)
		}
		if protocol != "tcp" && protocol != "udp" {
			return fmt.Errorf("invalid health target port %q: protocol must be tcp or udp", port)
		}
	}
	if engine := strings.TrimSpace(target.DatabaseEngine); engine != "" && databaseProbeCommand(engine) == "" {
		return fmt.Errorf("unsupported health target database engine %q", engine)
	}
	if url := strings.TrimSpace(target.HTTPURL); url != "" {
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			return fmt.Errorf("invalid health check URL %q: must start with http:// or https://", url)
		}
		if strings.ContainsAny(url, "\n\r") || strings.Contains(url, "|") {
			return fmt.Errorf("invalid health check URL %q: must not contain newlines or '|'", url)
		}
	}
	return nil
}

// splitHealthPort splits "8080/tcp" into its port and protocol.
func splitHealthPort(spec string) (host, protocol string) {
	host = strings.TrimSpace(spec)
	protocol = "tcp"
	if i := strings.Index(host, "/"); i >= 0 {
		protocol = strings.ToLower(strings.TrimSpace(host[i+1:]))
		host = strings.TrimSpace(host[:i])
	}
	return host, protocol
}

const healthScriptHead = `set +e
echo "---OPSPULSE-HEALTH---"
OPSPULSE_DOCKER_OK=0
if command -v docker >/dev/null 2>&1; then
  if docker info >/dev/null 2>&1; then
    OPSPULSE_DOCKER_OK=1
    echo "DOCKER=running"
  else
    echo "DOCKER=stopped"
  fi
else
  echo "DOCKER=missing"
fi

# Resolve the container of a Compose service by label, so the check does not
# depend on the container name the Compose engine picked. Falls back to the
# expected name when nothing matches.
opspulse_compose_container() {
  opspulse_name=$(docker ps -a --filter "$1" --filter "$2" --format '{{.Names}}' 2>/dev/null | head -n 1)
  if [ -n "$opspulse_name" ]; then
    printf '%s' "$opspulse_name"
    return 0
  fi
  printf '%s' "$3"
}

# Report whether a host port is listening. Returns 2 when no inspection tool is
# available, so the caller reports "unknown" instead of a false "not listening".
opspulse_port_listening() {
  if command -v ss >/dev/null 2>&1; then
    if [ "$2" = "udp" ]; then
      ss -lun 2>/dev/null | awk '{print $4}' | grep -q "[:.]$1$"
    else
      ss -ltn 2>/dev/null | awk '{print $4}' | grep -q "[:.]$1$"
    fi
    return $?
  fi
  if command -v netstat >/dev/null 2>&1; then
    if [ "$2" = "udp" ]; then
      netstat -lun 2>/dev/null | awk '{print $4}' | grep -q "[:.]$1$"
    else
      netstat -ltn 2>/dev/null | awk '{print $4}' | grep -q "[:.]$1$"
    fi
    return $?
  fi
  return 2
}
`

const healthScriptTail = `echo "---OPSPULSE-HEALTH-END---"
exit 0
`

// ParseHealthOutput turns the probe's line-oriented output into a structured
// report. It is a pure function so it can be tested with canned output, and it
// never panics on malformed lines: anything it cannot read stays "manual", so an
// unproven service is never reported as healthy.
func ParseHealthOutput(server string, opts HealthOptions, raw string) *HealthReport {
	retries, interval := healthRetrySettings(opts)
	budget := timeBudgetText(retries, interval)

	dockerState := healthDockerUnknown
	containers := map[int][]string{}
	ports := map[int]map[string]string{}
	databases := map[int]string{}
	https := map[int][]string{}

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "---") {
			continue
		}
		if state, ok := strings.CutPrefix(line, "DOCKER="); ok {
			dockerState = strings.TrimSpace(state)
			continue
		}
		fields := strings.Split(line, "|")
		if len(fields) < 3 {
			continue
		}
		index, err := strconv.Atoi(fields[1])
		if err != nil || index < 0 || index >= len(opts.Targets) {
			continue
		}
		switch fields[0] {
		case "CONTAINER":
			containers[index] = fields
		case "PORT":
			if ports[index] == nil {
				ports[index] = map[string]string{}
			}
			ports[index][fields[2]] = healthField(fields, 3)
		case "DB":
			if fields[2] == opts.Targets[index].DatabaseEngine {
				databases[index] = healthField(fields, 3)
			}
		case "HTTP":
			https[index] = fields
		}
	}

	report := &HealthReport{Server: server, Services: make([]ServiceHealth, 0, len(opts.Targets))}
	for index, target := range opts.Targets {
		service := ServiceHealth{Service: target.Service, Container: target.Container}
		service.Checks = append(service.Checks, containerHealthCheck(dockerState, containers[index]))
		if name := healthField(containers[index], 2); name != "" {
			service.Container = name
		}
		for _, port := range target.Ports {
			service.Checks = append(service.Checks, portHealthCheck(port, ports[index][port]))
		}
		service.Checks = append(service.Checks, databaseHealthCheck(target, databases[index], budget))
		service.Checks = append(service.Checks, httpHealthCheck(target, https[index]))
		service.Healthy = serviceChecksPassed(service.Checks)
		report.Services = append(report.Services, service)
	}
	return report
}

// healthField returns one field of a probe line, or "" when the line is short
// or absent. Probe lines come straight off the wire, so every read is guarded.
func healthField(fields []string, index int) string {
	if index < 0 || index >= len(fields) {
		return ""
	}
	return strings.TrimSpace(fields[index])
}

// serviceChecksPassed is the single rule for "this service is proven usable":
// every check must be ok or deliberately skipped. A manual result means the tool
// could not confirm the service, which must not be reported as healthy.
func serviceChecksPassed(checks []HealthCheckResult) bool {
	for _, check := range checks {
		switch check.Status {
		case HealthOK, HealthSkipped:
		default:
			return false
		}
	}
	return true
}

func containerHealthCheck(dockerState string, fields []string) HealthCheckResult {
	const label = "容器状态"
	result := HealthCheckResult{Kind: HealthKindContainer, Label: label}

	switch dockerState {
	case healthDockerMissing:
		result.Status = HealthFailed
		result.Detail = "目标机未安装 Docker，无法确认容器是否在运行"
		result.NextStep = "先在目标机安装并启动 Docker，再重新执行健康检查"
		return result
	case healthDockerStopped:
		result.Status = HealthFailed
		result.Detail = "目标机 Docker daemon 未运行，无法确认容器是否在运行"
		result.NextStep = "在目标机启动 Docker daemon（例如 systemctl start docker）后重新执行健康检查"
		return result
	case healthDockerRunning:
	default:
		result.Status = HealthManual
		result.Detail = "未能从目标机取得 Docker 状态"
		result.NextStep = "确认目标机可连接后重新执行 `ops docker health <plan-file>`"
		return result
	}

	name := healthField(fields, 2)
	state := healthField(fields, 3)
	restarting := healthField(fields, 4)
	restarts := healthField(fields, 5)
	startedAt := healthField(fields, 6)
	if len(fields) < 7 {
		result.Status = HealthManual
		result.Detail = "健康探测没有返回容器状态"
		result.NextStep = "在目标机执行 `docker ps -a` 确认服务容器"
		return result
	}
	if name == "" {
		result.Status = HealthManual
		result.Detail = "目标机上找不到该服务的容器（Compose 项目/服务标签与预期容器名都未命中）"
		result.NextStep = "在目标机执行 `docker ps -a` 确认容器名；若容器名与 plan 记录不一致，请修正 plan 的 services[].container 后重试"
		return result
	}
	if state == "missing" {
		result.Status = HealthFailed
		result.Detail = fmt.Sprintf("容器 %s 在目标机上不存在", name)
		result.NextStep = "在目标机执行 `docker ps -a` 确认；若迁移未完成，请重新执行 `ops docker apply <plan-file>`"
		return result
	}
	if strings.EqualFold(restarting, "true") || state == "restarting" {
		result.Status = HealthFailed
		result.Detail = fmt.Sprintf("容器 %s 处于 restarting：正在反复重启，服务并未可用", name)
		result.NextStep = fmt.Sprintf("在目标机执行 `docker logs --tail 200 %s` 查看崩溃原因，修复后重新启动服务", name)
		return result
	}
	if state != "running" {
		result.Status = HealthFailed
		result.Detail = fmt.Sprintf("容器 %s 当前状态为 %s，服务未在运行", name, state)
		result.NextStep = fmt.Sprintf("在目标机执行 `docker logs --tail 200 %s` 查看原因，并用 `docker compose up -d` 重新启动服务", name)
		return result
	}
	if count, err := strconv.Atoi(restarts); err == nil && count > 0 {
		result.Status = HealthFailed
		result.Detail = fmt.Sprintf("容器 %s 本次启动后已重启 %d 次（疑似崩溃循环）", name, count)
		result.NextStep = fmt.Sprintf("在目标机执行 `docker logs --tail 200 %s` 查看重启原因", name)
		return result
	}

	result.Status = HealthOK
	result.Detail = fmt.Sprintf("容器 %s 正在运行", name)
	if startedAt != "" {
		result.Detail += "（启动于 " + startedAt + "）"
	}
	return result
}

func portHealthCheck(spec, state string) HealthCheckResult {
	host, _ := splitHealthPort(spec)
	result := HealthCheckResult{Kind: HealthKindPorts, Label: "端口 " + spec}
	switch state {
	case "yes":
		result.Status = HealthOK
		result.Detail = fmt.Sprintf("宿主机已在监听 %s", spec)
	case "no":
		result.Status = HealthFailed
		result.Detail = fmt.Sprintf("宿主机没有在 %s 上监听", spec)
		result.NextStep = fmt.Sprintf("在目标机执行 `ss -ltn | grep %s` 确认端口；端口被占用或服务未起来时，先按容器状态的建议处理", host)
	case "unknown":
		result.Status = HealthManual
		result.Detail = fmt.Sprintf("目标机缺少 ss/netstat，无法确认 %s 是否被监听", spec)
		result.NextStep = fmt.Sprintf("在目标机手工执行 `ss -ltn` 或 `curl -sS -o /dev/null http://127.0.0.1:%s/` 确认", host)
	default:
		result.Status = HealthManual
		result.Detail = fmt.Sprintf("未取得 %s 的监听信息", spec)
		result.NextStep = fmt.Sprintf("在目标机手工执行 `ss -ltn | grep %s` 确认", host)
	}
	return result
}

func databaseHealthCheck(target HealthTarget, state, budget string) HealthCheckResult {
	result := HealthCheckResult{Kind: HealthKindDatabase, Label: "数据库 readiness"}
	if target.DatabaseEngine == "" {
		result.Status = HealthSkipped
		result.Detail = "该服务不是受支持的数据库容器，未配置数据库 readiness 检查"
		return result
	}

	tool := databaseProbeTool(target.DatabaseEngine)
	switch state {
	case "yes":
		result.Status = HealthOK
		result.Detail = fmt.Sprintf("数据库 %s 已就绪（%s 通过）", target.DatabaseEngine, tool)
	case "no":
		result.Status = HealthFailed
		result.Detail = fmt.Sprintf("数据库 %s 在 %s 内没有就绪", target.DatabaseEngine, budget)
		result.NextStep = fmt.Sprintf("在目标机执行 `docker exec <容器> %s` 确认；必要时查看 `docker logs <容器>`", tool)
	default:
		result.Status = HealthManual
		result.Detail = fmt.Sprintf("未能确认数据库 %s 是否就绪", target.DatabaseEngine)
		result.NextStep = fmt.Sprintf("在目标机执行 `docker exec <容器> %s` 手工确认", tool)
	}
	return result
}

func httpHealthCheck(target HealthTarget, fields []string) HealthCheckResult {
	result := HealthCheckResult{Kind: HealthKindHTTP, Label: "HTTP 检查"}
	url := strings.TrimSpace(target.HTTPURL)
	if url == "" {
		result.Status = HealthSkipped
		result.Detail = "未配置 HTTP 检查（plan 的 services[].health.http_url 为空）"
		return result
	}
	manualHint := fmt.Sprintf("在目标机手工执行 `curl -sS -o /dev/null -w '%%{http_code}' %s` 确认", url)
	if len(fields) < 5 {
		result.Status = HealthManual
		result.Detail = fmt.Sprintf("没有取得 %s 的 HTTP 检查结果", url)
		result.NextStep = manualHint
		return result
	}

	code, verdict := healthField(fields, 3), healthField(fields, 4)
	switch verdict {
	case "tool-missing":
		result.Status = HealthManual
		result.Detail = "目标机没有 curl，无法执行 HTTP 检查"
		result.NextStep = fmt.Sprintf("在目标机安装 curl，或手工访问 %s 确认", url)
	case "yes":
		result.Status = HealthOK
		result.Detail = fmt.Sprintf("HTTP %s 返回 %s", url, code)
	default:
		result.Status = HealthFailed
		result.Detail = fmt.Sprintf("HTTP %s 返回 %s（预期 2xx/3xx）", url, code)
		result.NextStep = fmt.Sprintf("确认服务监听与反向代理配置后重试；%s", manualHint)
	}
	return result
}

// databaseProbeTool is the user-facing name of the readiness command, used in
// the report detail and the next-step hint.
func databaseProbeTool(engine string) string {
	switch engine {
	case "mysql":
		return "mysqladmin ping"
	case "postgres":
		return "pg_isready"
	default:
		return engine
	}
}

// timeBudgetText renders the readiness budget for the report, e.g. "约 30s".
func timeBudgetText(retries, interval int) string {
	return fmt.Sprintf("约 %ds", retries*interval)
}
