package capability

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

// healthyHealthOutput builds a probe result for one service: this is the
// line-oriented contract the script emits and the parser reads.
func healthyHealthOutput(serviceIndex int, container, port string) string {
	return strings.Join([]string{
		"---OPSPULSE-HEALTH---",
		"DOCKER=running",
		"CONTAINER|" + strconv.Itoa(serviceIndex) + "|" + container + "|running|false|0|2026-10-16T01:00:00Z",
		"PORT|" + strconv.Itoa(serviceIndex) + "|" + port + "|yes",
		"DB|" + strconv.Itoa(serviceIndex) + "|mysql|yes",
		"HTTP|" + strconv.Itoa(serviceIndex) + "|http://example.com/health|200|yes",
		"---OPSPULSE-HEALTH-END---",
	}, "\n")
}

// healthMutatingCommands are the operations the post-start probe must never
// perform. `docker exec` is deliberately not in the list here (unlike the
// discovery guard in capability_test.go): the database readiness probe runs
// `mysqladmin ping` / `pg_isready` through it, which reads state instead of
// changing it.
var healthMutatingCommands = []string{
	"docker start",
	"docker stop",
	"docker restart",
	"docker rm ",
	"docker run ",
	"docker compose up",
	"docker-compose up",
	"docker volume create",
	"restic restore",
	"rm -rf",
}

func assertHealthScriptReadOnly(t *testing.T, script string) {
	t.Helper()
	for _, needle := range healthMutatingCommands {
		if strings.Contains(script, needle) {
			t.Errorf("health script contains a mutating command %q:\n%s", needle, script)
		}
	}
}

func TestBuildHealthScriptIsReadOnly(t *testing.T) {
	script, err := BuildHealthScript(HealthOptions{Targets: []HealthTarget{{
		Service:        "db",
		Container:      "blog-db",
		ComposeProject: "blog",
		ComposeService: "db",
		Ports:          []string{"3306/tcp"},
		DatabaseEngine: "mysql",
		HTTPURL:        "http://127.0.0.1:8080/health",
	}}})
	if err != nil {
		t.Fatalf("BuildHealthScript() error: %v", err)
	}
	assertHealthScriptReadOnly(t, script)
}

func TestBuildHealthScriptChecksEveryDimension(t *testing.T) {
	script, err := BuildHealthScript(HealthOptions{Targets: []HealthTarget{{
		Service:        "db",
		Container:      "blog-db",
		ComposeProject: "blog",
		ComposeService: "db",
		Ports:          []string{"3306/tcp", "9090/udp"},
		DatabaseEngine: "postgres",
		HTTPURL:        "http://example.com/health",
	}}})
	if err != nil {
		t.Fatalf("BuildHealthScript() error: %v", err)
	}
	for _, want := range []string{
		"docker inspect --type container",
		"com.docker.compose.project=blog",
		"com.docker.compose.service=db",
		"ss -ltn",
		"ss -lun",
		"pg_isready",
		"curl -s -o /dev/null -w",
		"3306/tcp",
		"9090/udp",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("health script missing %q:\n%s", want, script)
		}
	}
}

func TestBuildHealthScriptRejectsUnsafeInputs(t *testing.T) {
	cases := []struct {
		name   string
		target HealthTarget
	}{
		{"pipe in container", HealthTarget{Container: "a|b"}},
		{"newline in container", HealthTarget{Container: "a\nb"}},
		{"pipe in compose service", HealthTarget{ComposeService: "a|b"}},
		{"non-numeric port", HealthTarget{Ports: []string{"http/tcp"}}},
		{"out of range port", HealthTarget{Ports: []string{"70000/tcp"}}},
		{"bad protocol", HealthTarget{Ports: []string{"80/icmp"}}},
		{"unsupported engine", HealthTarget{DatabaseEngine: "redis"}},
		{"url without scheme", HealthTarget{HTTPURL: "example.com/health"}},
		{"url with pipe", HealthTarget{HTTPURL: "http://a/b|c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := tc.target
			target.Service = "svc"
			if _, err := BuildHealthScript(HealthOptions{Targets: []HealthTarget{target}}); err == nil {
				t.Fatal("BuildHealthScript() should reject the target")
			}
		})
	}
}

func TestParseHealthOutputHealthyService(t *testing.T) {
	opts := HealthOptions{Targets: []HealthTarget{{
		Service:        "db",
		Container:      "blog-db",
		Ports:          []string{"3306/tcp"},
		DatabaseEngine: "mysql",
		HTTPURL:        "http://example.com/health",
	}}}
	raw := strings.Join([]string{
		"---OPSPULSE-HEALTH---",
		"DOCKER=running",
		"CONTAINER|0|blog-db|running|false|0|2026-10-16T01:00:00Z",
		"PORT|0|3306/tcp|yes",
		"DB|0|mysql|yes",
		"HTTP|0|http://example.com/health|200|yes",
		"---OPSPULSE-HEALTH-END---",
	}, "\n")

	report := ParseHealthOutput("vps-b", opts, raw)
	if len(report.Services) != 1 {
		t.Fatalf("services = %+v, want one", report.Services)
	}
	service := report.Services[0]
	if !service.Healthy {
		t.Errorf("service = %+v, want healthy", service)
	}
	if len(report.Unhealthy()) != 0 || len(report.Failed()) != 0 || len(report.Unverified()) != 0 {
		t.Errorf("report buckets = %d/%d/%d, want an all-pass service in none of them",
			len(report.Unhealthy()), len(report.Failed()), len(report.Unverified()))
	}
	if service.Container != "blog-db" {
		t.Errorf("container = %q, want the name reported by the probe", service.Container)
	}
	kinds := map[string]string{}
	for _, check := range service.Checks {
		kinds[check.Kind] = check.Status
	}
	for kind, want := range map[string]string{
		HealthKindContainer: HealthOK,
		HealthKindPorts:     HealthOK,
		HealthKindDatabase:  HealthOK,
		HealthKindHTTP:      HealthOK,
	} {
		if kinds[kind] != want {
			t.Errorf("check %s = %q, want %q", kind, kinds[kind], want)
		}
	}
}

func TestParseHealthOutputSkipsUnconfiguredHTTP(t *testing.T) {
	opts := HealthOptions{Targets: []HealthTarget{{Service: "web", Container: "blog-web"}}}
	raw := strings.Join([]string{
		"DOCKER=running",
		"CONTAINER|0|blog-web|running|false|0|2026-10-16T01:00:00Z",
	}, "\n")

	report := ParseHealthOutput("vps-b", opts, raw)
	checks := report.Services[0].Checks
	http := findCheck(t, checks, HealthKindHTTP)
	if http.Status != HealthSkipped {
		t.Errorf("HTTP check = %+v, want skipped when no URL is configured", http)
	}
	if !strings.Contains(http.Detail, "未配置") {
		t.Errorf("HTTP detail = %q, want an explicit 'not configured' note", http.Detail)
	}
	if db := findCheck(t, checks, HealthKindDatabase); db.Status != HealthSkipped {
		t.Errorf("database check = %+v, want skipped for a non-database service", db)
	}
	if !report.Services[0].Healthy {
		t.Errorf("service = %+v, want a service whose only checks are skipped/ok", report.Services[0])
	}
}

func TestParseHealthOutputReportsRestartLoop(t *testing.T) {
	opts := HealthOptions{Targets: []HealthTarget{{Service: "web", Container: "blog-web"}}}
	raw := strings.Join([]string{
		"DOCKER=running",
		"CONTAINER|0|blog-web|running|false|7|2026-10-16T01:00:00Z",
	}, "\n")

	service := ParseHealthOutput("vps-b", opts, raw).Services[0]
	if service.Healthy {
		t.Fatal("a container that restarted 7 times must not be reported healthy")
	}
	check := findCheck(t, service.Checks, HealthKindContainer)
	if !strings.Contains(check.Detail, "重启 7 次") {
		t.Errorf("container detail = %q, want the restart count", check.Detail)
	}
	if check.NextStep == "" || !strings.Contains(check.NextStep, "docker logs") {
		t.Errorf("next step = %q, want a log-inspection command", check.NextStep)
	}
}

func TestParseHealthOutputReportsRestartingState(t *testing.T) {
	opts := HealthOptions{Targets: []HealthTarget{{Service: "web", Container: "blog-web"}}}
	raw := "DOCKER=running\nCONTAINER|0|blog-web|restarting|true|0|\n"

	service := ParseHealthOutput("vps-b", opts, raw).Services[0]
	if service.Healthy {
		t.Fatal("a restarting container must not be reported healthy")
	}
	check := findCheck(t, service.Checks, HealthKindContainer)
	if !strings.Contains(check.Detail, "restarting") {
		t.Errorf("container detail = %q, want the restarting state", check.Detail)
	}
}

func TestParseHealthOutputReportsPortNotListening(t *testing.T) {
	opts := HealthOptions{Targets: []HealthTarget{{Service: "web", Ports: []string{"8080/tcp"}}}}
	raw := strings.Join([]string{
		"DOCKER=running",
		"CONTAINER|0|blog-web|running|false|0|",
		"PORT|0|8080/tcp|no",
	}, "\n")

	service := ParseHealthOutput("vps-b", opts, raw).Services[0]
	check := findCheck(t, service.Checks, HealthKindPorts)
	if check.Status != HealthFailed {
		t.Fatalf("port check = %+v, want failed", check)
	}
	if !strings.Contains(check.NextStep, "ss -ltn") {
		t.Errorf("port next step = %q, want a manual listening check", check.NextStep)
	}
	if service.Healthy {
		t.Error("a service with a dead port must not be healthy")
	}
}

func TestParseHealthOutputPortUnknownIsManualNotFailure(t *testing.T) {
	opts := HealthOptions{Targets: []HealthTarget{{Service: "web", Ports: []string{"8080/tcp"}}}}
	raw := "DOCKER=running\nCONTAINER|0|blog-web|running|false|0|\nPORT|0|8080/tcp|unknown\n"

	service := ParseHealthOutput("vps-b", opts, raw).Services[0]
	check := findCheck(t, service.Checks, HealthKindPorts)
	if check.Status != HealthManual {
		t.Fatalf("port check = %+v, want manual without ss/netstat", check)
	}
	if service.Healthy {
		t.Error("a check the tool could not run must not be reported as healthy")
	}
	if len(service.ManualChecks()) != 1 {
		t.Errorf("manual checks = %+v, want the port check", service.ManualChecks())
	}
	if len(service.FailedChecks()) != 0 {
		t.Errorf("failed checks = %+v, want none: an unrunnable check is not a failure", service.FailedChecks())
	}
}

func TestParseHealthOutputDatabaseNotReady(t *testing.T) {
	opts := HealthOptions{Targets: []HealthTarget{{Service: "db", DatabaseEngine: "postgres"}}}
	raw := strings.Join([]string{
		"DOCKER=running",
		"CONTAINER|0|blog-db|running|false|0|",
		"DB|0|postgres|no",
	}, "\n")

	service := ParseHealthOutput("vps-b", opts, raw).Services[0]
	check := findCheck(t, service.Checks, HealthKindDatabase)
	if check.Status != HealthFailed || !strings.Contains(check.Detail, "没有就绪") {
		t.Errorf("database check = %+v, want a not-ready failure", check)
	}
	if !strings.Contains(check.NextStep, "pg_isready") {
		t.Errorf("database next step = %q, want the readiness command", check.NextStep)
	}
}

func TestParseHealthOutputHTTPFailure(t *testing.T) {
	opts := HealthOptions{Targets: []HealthTarget{{
		Service: "web",
		HTTPURL: "http://example.com/health",
	}}}
	raw := strings.Join([]string{
		"DOCKER=running",
		"CONTAINER|0|blog-web|running|false|0|",
		"HTTP|0|http://example.com/health|500|no",
	}, "\n")

	service := ParseHealthOutput("vps-b", opts, raw).Services[0]
	check := findCheck(t, service.Checks, HealthKindHTTP)
	if check.Status != HealthFailed || !strings.Contains(check.Detail, "500") {
		t.Errorf("HTTP check = %+v, want a failure that names the status code", check)
	}
}

func TestParseHealthOutputDockerMissingFailsContainerCheck(t *testing.T) {
	opts := HealthOptions{Targets: []HealthTarget{{Service: "web", Container: "blog-web"}}}
	service := ParseHealthOutput("vps-b", opts, "DOCKER=missing\n").Services[0]
	check := findCheck(t, service.Checks, HealthKindContainer)
	if check.Status != HealthFailed || !strings.Contains(check.Detail, "未安装 Docker") {
		t.Errorf("container check = %+v, want an actionable Docker-missing failure", check)
	}
	if service.Healthy {
		t.Error("a service whose host has no Docker must not be healthy")
	}
}

func TestParseHealthOutputContainerMissing(t *testing.T) {
	opts := HealthOptions{Targets: []HealthTarget{{Service: "web", Container: "blog-web"}}}
	raw := "DOCKER=running\nCONTAINER|0|blog-web|missing|unknown|unknown|\n"

	service := ParseHealthOutput("vps-b", opts, raw).Services[0]
	check := findCheck(t, service.Checks, HealthKindContainer)
	if check.Status != HealthFailed || !strings.Contains(check.Detail, "不存在") {
		t.Errorf("container check = %+v, want a missing-container failure", check)
	}
}

func TestParseHealthOutputMalformedLinesStayManual(t *testing.T) {
	opts := HealthOptions{Targets: []HealthTarget{{Service: "web", Container: "blog-web"}}}
	service := ParseHealthOutput("vps-b", opts, "garbage without keys\n").Services[0]
	if service.Healthy {
		t.Error("output the parser cannot read must not be reported healthy")
	}
	check := findCheck(t, service.Checks, HealthKindContainer)
	if check.Status != HealthManual {
		t.Errorf("container check = %+v, want manual", check)
	}
}

func TestParseHealthOutputIgnoresUnknownServiceIndex(t *testing.T) {
	opts := HealthOptions{Targets: []HealthTarget{{Service: "web"}}}
	raw := "DOCKER=running\nCONTAINER|9|x|running|false|0|\nCONTAINER|0|prod-web|running|false|0|\n"
	service := ParseHealthOutput("vps-b", opts, raw).Services[0]
	if service.Container != "prod-web" {
		t.Errorf("container = %q, want the line for index 0", service.Container)
	}
}

func TestRemoteProberHealthCheck(t *testing.T) {
	exec := &fakeExecutor{out: healthyHealthOutput(0, "blog-db", "3306/tcp")}
	prober := NewRemote(exec)

	report, err := prober.HealthCheck(context.Background(), testTarget(), HealthOptions{Targets: []HealthTarget{{
		Service:        "db",
		Container:      "blog-db",
		Ports:          []string{"3306/tcp"},
		DatabaseEngine: "mysql",
	}}})
	if err != nil {
		t.Fatalf("HealthCheck() error: %v", err)
	}
	if exec.task != "docker-health-check" {
		t.Errorf("task = %q, want docker-health-check", exec.task)
	}
	if !report.Services[0].Healthy {
		t.Errorf("services = %+v, want a healthy verdict", report.Services)
	}
	assertHealthScriptReadOnly(t, exec.script)
}

func TestRemoteProberHealthCheckExecutionFailure(t *testing.T) {
	prober := NewRemote(&fakeExecutor{out: "boom", err: errors.New("ssh broken")})
	if _, err := prober.HealthCheck(context.Background(), testTarget(), HealthOptions{Targets: []HealthTarget{{Service: "web"}}}); err == nil {
		t.Fatal("HealthCheck() should surface an execution failure")
	}
}

func findCheck(t *testing.T, checks []HealthCheckResult, kind string) HealthCheckResult {
	t.Helper()
	for _, check := range checks {
		if check.Kind == kind {
			return check
		}
	}
	t.Fatalf("no %s check in %+v", kind, checks)
	return HealthCheckResult{}
}
