package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/volcano6/opspulse/internal/capability"
	"github.com/volcano6/opspulse/internal/migration"
)

func resetDockerHealthFlagValues() {
	dockerHealthJSON = false
	dockerHealthServer = ""
}

func runHealthOps(t *testing.T, args ...string) (string, error) {
	t.Helper()
	resetDockerHealthFlagValues()
	t.Cleanup(resetDockerHealthFlagValues)
	return runPlanOps(t, args...)
}

// healthReport builds the synthetic verdict the fake seam returns. Keeping the
// builder here (rather than inlining literals) makes the "proven broken" vs
// "could not be checked" distinction the tests exercise explicit.
func healthReport(server, service string, checks ...capability.HealthCheckResult) *capability.HealthReport {
	passed := true
	for _, check := range checks {
		if check.Status != capability.HealthOK && check.Status != capability.HealthSkipped {
			passed = false
		}
	}
	return &capability.HealthReport{Server: server, Services: []capability.ServiceHealth{{
		Service: service, Container: "blog-db", Healthy: passed, Checks: checks,
	}}}
}

func okCheck(kind, label, detail string) capability.HealthCheckResult {
	return capability.HealthCheckResult{Kind: kind, Label: label, Status: capability.HealthOK, Detail: detail}
}

func TestDockerHealthAllGreen(t *testing.T) {
	home := seedPlanServers(t, "vps-a", "vps-b")
	resetDockerFlags(t)
	fake := applyFake("vps-a", "vps-b")
	fake.healthReports = map[string]*capability.HealthReport{
		"vps-b": healthReport("vps-b", "db",
			okCheck(capability.HealthKindContainer, "容器状态", "容器 blog-db 正在运行"),
			okCheck(capability.HealthKindPorts, "端口 3306/tcp", "宿主机已在监听 3306/tcp"),
			okCheck(capability.HealthKindDatabase, "数据库 readiness", "数据库 mysql 已就绪"),
			capability.HealthCheckResult{Kind: capability.HealthKindHTTP, Label: "HTTP 检查", Status: capability.HealthSkipped, Detail: "未配置 HTTP 检查"},
		),
	}
	installPlanRemote(t, fake)

	path := writeApplyPlan(t, home, "vps-a", "vps-b", planContainerInfo())
	out, err := runHealthOps(t, "docker", "health", path)
	if err != nil {
		t.Fatalf("docker health error: %v\n%s", err, out)
	}
	for _, want := range []string{"健康检查", "服务 db", "通过", "容器 blog-db 正在运行", "数据库 readiness", "skipped"} {
		if !strings.Contains(out, want) {
			t.Errorf("health output is missing %q:\n%s", want, out)
		}
	}
	if len(fake.healthCalls) != 1 {
		t.Fatalf("health calls = %d, want exactly one probe", len(fake.healthCalls))
	}
	target := fake.healthCalls[0].Targets[0]
	if target.Service != "db" || target.Container != "blog-db" || target.DatabaseEngine != "mysql" {
		t.Errorf("health target = %+v, want the plan service described to the seam", target)
	}
	if len(target.Ports) != 1 || target.Ports[0] != "3306/tcp" {
		t.Errorf("health target ports = %v, want the planned published port", target.Ports)
	}
}

func TestDockerHealthFailsOnProvenBrokenService(t *testing.T) {
	home := seedPlanServers(t, "vps-a", "vps-b")
	resetDockerFlags(t)
	fake := applyFake("vps-a", "vps-b")
	fake.healthReports = map[string]*capability.HealthReport{
		"vps-b": healthReport("vps-b", "db", capability.HealthCheckResult{
			Kind: capability.HealthKindContainer, Label: "容器状态", Status: capability.HealthFailed,
			Detail: "容器 blog-db 本次启动后已重启 7 次（疑似崩溃循环）", NextStep: "在目标机执行 `docker logs --tail 200 blog-db`",
		}),
	}
	installPlanRemote(t, fake)

	path := writeApplyPlan(t, home, "vps-a", "vps-b", planContainerInfo())
	out, err := runHealthOps(t, "docker", "health", path)
	if err == nil || !strings.Contains(err.Error(), "未通过健康检查") {
		t.Fatalf("error = %v, want a health-check failure\n%s", err, out)
	}
	if !strings.Contains(out, "重启 7 次") || !strings.Contains(out, "docker logs") {
		t.Errorf("output must name the failure and the next step:\n%s", out)
	}
}

func TestDockerHealthManualCheckDoesNotFailTheCommand(t *testing.T) {
	home := seedPlanServers(t, "vps-a", "vps-b")
	resetDockerFlags(t)
	fake := applyFake("vps-a", "vps-b")
	fake.healthReports = map[string]*capability.HealthReport{
		"vps-b": healthReport("vps-b", "db", capability.HealthCheckResult{
			Kind: capability.HealthKindPorts, Label: "端口 3306/tcp", Status: capability.HealthManual,
			Detail: "目标机缺少 ss/netstat，无法确认 3306/tcp 是否被监听", NextStep: "在目标机手工执行 `ss -ltn` 确认",
		}),
	}
	installPlanRemote(t, fake)

	path := writeApplyPlan(t, home, "vps-a", "vps-b", planContainerInfo())
	out, err := runHealthOps(t, "docker", "health", path)
	if err != nil {
		t.Fatalf("an unrunnable check must not fail the command: %v\n%s", err, out)
	}
	if !strings.Contains(out, "需人工确认") || !strings.Contains(out, "手工执行") {
		t.Errorf("output must ask for manual confirmation:\n%s", out)
	}
}

func TestDockerHealthUnconfiguredHTTPIsSkipped(t *testing.T) {
	home := seedPlanServers(t, "vps-a", "vps-b")
	resetDockerFlags(t)
	fake := applyFake("vps-a", "vps-b")
	fake.healthReports = map[string]*capability.HealthReport{
		"vps-b": healthReport("vps-b", "db",
			okCheck(capability.HealthKindContainer, "容器状态", "容器 blog-db 正在运行"),
			capability.HealthCheckResult{Kind: capability.HealthKindHTTP, Label: "HTTP 检查", Status: capability.HealthSkipped, Detail: "未配置 HTTP 检查（plan 的 services[].health.http_url 为空）"},
		),
	}
	installPlanRemote(t, fake)

	path := writeApplyPlan(t, home, "vps-a", "vps-b", planContainerInfo())
	out, err := runHealthOps(t, "docker", "health", path)
	if err != nil {
		t.Fatalf("docker health error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "[skipped] HTTP 检查：未配置 HTTP 检查") {
		t.Errorf("output must show the optional HTTP check as skipped:\n%s", out)
	}
}

func TestDockerHealthForwardsConfiguredHTTPURL(t *testing.T) {
	home := seedPlanServers(t, "vps-a", "vps-b")
	resetDockerFlags(t)
	resetDockerPlanFlags(t)
	fake := applyFake("vps-a", "vps-b")
	installPlanRemote(t, fake)

	// Generate the plan through the real command so --health-url is exercised
	// end to end, not just the builder.
	planPath := home + "/http-plan.yaml"
	if _, err := runPlanOps(t, "docker", "plan", "vps-a", "blog-db",
		"--target", "vps-b", "--save", planPath, "--health-url", "http://vps-b:8080/health"); err != nil {
		t.Fatalf("docker plan --health-url error: %v", err)
	}

	resetDockerFlags(t)
	fake.healthReports = map[string]*capability.HealthReport{
		"vps-b": healthReport("vps-b", "db", capability.HealthCheckResult{
			Kind: capability.HealthKindHTTP, Label: "HTTP 检查", Status: capability.HealthFailed,
			Detail: "HTTP http://vps-b:8080/health 返回 500（预期 2xx/3xx）", NextStep: "确认服务监听与反向代理配置后重试",
		}),
	}
	out, err := runHealthOps(t, "docker", "health", planPath)
	if err == nil {
		t.Fatalf("a 500 from the configured URL must fail the command:\n%s", out)
	}
	target := fake.healthCalls[0].Targets[0]
	if target.HTTPURL != "http://vps-b:8080/health" {
		t.Errorf("health target URL = %q, want the URL recorded by --health-url", target.HTTPURL)
	}
}

func TestDockerPlanRejectsBadHealthURL(t *testing.T) {
	home := seedPlanServers(t, "vps-a")
	resetDockerFlags(t)
	resetDockerPlanFlags(t)
	installPlanRemote(t, applyFake("vps-a"))

	_, err := runPlanOps(t, "docker", "plan", "vps-a", "blog-db", "--no-save", "--health-url", "vps-a/health")
	if err == nil || !strings.Contains(err.Error(), "http://") {
		t.Fatalf("error = %v, want a URL-scheme refusal", err)
	}
	_ = home
}

func TestDockerHealthJSONReportsManualBucket(t *testing.T) {
	home := seedPlanServers(t, "vps-a", "vps-b")
	resetDockerFlags(t)
	fake := applyFake("vps-a", "vps-b")
	fake.healthReports = map[string]*capability.HealthReport{
		"vps-b": healthReport("vps-b", "db", capability.HealthCheckResult{
			Kind: capability.HealthKindHTTP, Label: "HTTP 检查", Status: capability.HealthManual,
			Detail: "目标机没有 curl，无法执行 HTTP 检查", NextStep: "在目标机安装 curl，或手工访问确认",
		}),
	}
	installPlanRemote(t, fake)

	path := writeApplyPlan(t, home, "vps-a", "vps-b", planContainerInfo())
	out, err := runHealthOps(t, "docker", "health", path, "--json")
	if err != nil {
		t.Fatalf("docker health --json error: %v\n%s", err, out)
	}

	var doc dockerHealthDocument
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, out)
	}
	if doc.Kind != "docker_health_result" || doc.Server != "vps-b" {
		t.Errorf("doc header = %+v, want the health result for vps-b", doc)
	}
	if !doc.Healthy {
		t.Error("an unrunnable check must not flip the machine-readable healthy flag")
	}
	if len(doc.NeedsManualCheck) != 1 || doc.NeedsManualCheck[0] != "db" {
		t.Errorf("needs_manual_check = %v, want db listed explicitly", doc.NeedsManualCheck)
	}
	if len(doc.Services) != 1 || doc.Services[0].Healthy {
		t.Errorf("services = %+v, want one unproven service in the strict view", doc.Services)
	}
}

func TestDockerHealthServerOverrideAndMissingTarget(t *testing.T) {
	home := seedPlanServers(t, "vps-a", "vps-b")
	resetDockerFlags(t)
	fake := applyFake("vps-a", "vps-b")
	installPlanRemote(t, fake)

	// A plan without a target cannot be health-checked without --server.
	path := writeApplyPlan(t, home, "vps-a", "", planContainerInfo())
	if _, err := runHealthOps(t, "docker", "health", path); err == nil || !strings.Contains(err.Error(), "--server") {
		t.Fatalf("error = %v, want a refusal pointing at --server", err)
	}

	if _, err := runHealthOps(t, "docker", "health", path, "--server", "vps-b"); err != nil {
		t.Fatalf("--server should supply the missing target: %v", err)
	}
	if got := fake.healthCalls[len(fake.healthCalls)-1].Targets; len(got) != 1 {
		t.Errorf("health targets = %+v, want the plan service", got)
	}
}

func TestDockerHealthSurfacesProbeFailure(t *testing.T) {
	home := seedPlanServers(t, "vps-a", "vps-b")
	resetDockerFlags(t)
	fake := applyFake("vps-a", "vps-b")
	fake.healthErr = errors.New("ssh: connection refused")
	installPlanRemote(t, fake)

	path := writeApplyPlan(t, home, "vps-a", "vps-b", planContainerInfo())
	if _, err := runHealthOps(t, "docker", "health", path); err == nil {
		t.Fatal("a probe failure must surface instead of reporting a healthy service")
	}
}

func TestDockerHealthRejectsEmptyPlan(t *testing.T) {
	seedPlanServers(t, "vps-a")
	resetDockerFlags(t)
	installPlanRemote(t, applyFake("vps-a"))

	// A plan document with no services is not something the plan command
	// produces, but Load accepts it, so the health command must refuse it
	// instead of reporting a vacuous all-green result.
	dir := t.TempDir()
	path := filepath.Join(dir, "empty-plan.yaml")
	empty := &migration.Plan{
		FormatVersion: migration.PlanFormatVersion,
		Kind:          migration.PlanKind,
		Source:        migration.Endpoint{Server: "vps-a"},
		Target:        migration.Endpoint{Server: "vps-b"},
	}
	if err := migration.Save(path, empty); err != nil {
		t.Fatalf("save empty plan: %v", err)
	}
	if _, err := runHealthOps(t, "docker", "health", path); err == nil || !strings.Contains(err.Error(), "没有需要检查的服务") {
		t.Fatalf("error = %v, want an empty-plan refusal", err)
	}
}
