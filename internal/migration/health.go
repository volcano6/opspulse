package migration

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/volcano6/opspulse/internal/capability"
	"github.com/volcano6/opspulse/internal/docker"
)

// HealthPlan is the optional post-start health configuration of one service. It
// is part of the plan because the plan is the artifact that drives execution:
// whether an HTTP endpoint should answer is a property of the migration, not of
// the host. An empty HTTPURL means the HTTP check is deliberately not
// configured, and the health report then says "skipped" instead of pretending
// the service is verified.
type HealthPlan struct {
	HTTPURL string `yaml:"http_url,omitempty" json:"http_url,omitempty"`
}

// HealthTargets collects the post-start health targets of a plan: the container
// that must stay up, the ports that must be listening, the database that must
// accept connections, and the optional HTTP endpoint. It is a pure function so
// the apply step and the standalone health command check exactly the same
// things.
func HealthTargets(plan *Plan) []capability.HealthTarget {
	if plan == nil {
		return nil
	}
	targets := make([]capability.HealthTarget, 0, len(plan.Services))
	for _, service := range plan.Services {
		targets = append(targets, HealthTargetFor(service))
	}
	return targets
}

// HealthTargetFor derives the post-start health target of one plan service.
func HealthTargetFor(service ServicePlan) capability.HealthTarget {
	target := capability.HealthTarget{
		Service:        service.Name,
		Container:      service.Container,
		ComposeProject: service.ComposeProject,
		ComposeService: service.ComposeService,
		Ports:          HealthPorts(service),
	}
	// A plain docker run container becomes a single-service Compose project on
	// the target, so the platform label still identifies it; only the Compose
	// service key differs (it is the sanitized generated name, which is also
	// what ApplyAlias uses as the project name).
	if target.ComposeProject == "" {
		target.ComposeProject = strings.TrimSpace(service.Name)
		target.ComposeService = docker.SanitizeServiceName(service.Name)
	}
	if service.Database != nil {
		target.DatabaseEngine = strings.TrimSpace(service.Database.Engine)
	}
	if service.Health != nil {
		target.HTTPURL = strings.TrimSpace(service.Health.HTTPURL)
	}
	return target
}

// HealthPorts returns the published host ports a service must be listening on
// after it starts, in the "port/protocol" form the health probe consumes. Only
// ports with a fixed host side qualify: a port the plan leaves to Docker cannot
// be checked against a known number.
func HealthPorts(service ServicePlan) []string {
	var ports []string
	for _, spec := range planPorts(service) {
		hostPort, protocol := portSpec(spec)
		if hostPort == "" {
			continue
		}
		ports = appendPort(ports, hostPort+"/"+protocol)
	}
	sort.Strings(ports)
	return ports
}

// HealthPlanRisks reports the services whose plan carries no HTTP health check,
// so the operator knows up front what the post-start report will not be able to
// prove. They are collapsed into one follow-up: on a batch plan the per-service
// repetition would bury the risks that actually differ per service, and the
// action is the same for all of them.
func HealthPlanRisks(plan *Plan) []Risk {
	if plan == nil {
		return nil
	}
	var services []string
	for _, service := range plan.Services {
		if service.Health != nil && strings.TrimSpace(service.Health.HTTPURL) != "" {
			continue
		}
		services = append(services, service.Name)
	}
	if len(services) == 0 {
		return nil
	}
	sort.Strings(services)
	return []Risk{{
		Code: RiskHealthHTTPNotConfigured,
		Detail: fmt.Sprintf("服务 %s 未配置 HTTP 健康检查：迁移后只能确认容器、端口与数据库，无法确认应用真的能对外服务",
			strings.Join(services, ", ")),
		Action: "如这些服务有可访问的地址，请在 plan 的 services[].health.http_url 填上（例如 http://<目标机>:8080/health）后重新执行",
	}}
}

// ErrInvalidHealthURL is returned for an HTTP health URL that could not be
// probed safely. It is a sentinel so the CLI can turn it into an actionable
// message without string matching.
var ErrInvalidHealthURL = errors.New("invalid health check URL")

// ParseHealthURL validates the optional HTTP health check address. It is
// validated at plan time - when re-running is cheap - instead of after a
// migration has already moved data, and single-quoting plus these checks are
// what keep the address from reaching the shell unquoted.
func ParseHealthURL(raw string) (*HealthPlan, error) {
	url := strings.TrimSpace(raw)
	if url == "" {
		return nil, nil
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("%w: %q 必须以 http:// 或 https:// 开头", ErrInvalidHealthURL, raw)
	}
	if strings.ContainsAny(url, "\n\r") || strings.Contains(url, "|") {
		return nil, fmt.Errorf("%w: %q 不能包含换行或 '|'", ErrInvalidHealthURL, raw)
	}
	return &HealthPlan{HTTPURL: url}, nil
}

// appendPort adds a port spec once, preserving order.
func appendPort(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
