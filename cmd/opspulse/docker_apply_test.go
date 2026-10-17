package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/volcano6/opspulse/internal/capability"
	"github.com/volcano6/opspulse/internal/docker"
	"github.com/volcano6/opspulse/internal/migration"
)

// fakeApplyMigrator replaces the production backup/restore driver so the CLI
// tests assert orchestration (which service, which alias, continue-on-failure)
// without touching a host or a restic repository.
type fakeApplyMigrator struct {
	calls  []applyCall
	failOn map[string]error
}

type applyCall struct {
	Source    string
	Target    string
	Service   string
	Container string
	Alias     string
}

func (f *fakeApplyMigrator) Migrate(_ context.Context, plan *migration.Plan, service migration.ServicePlan, _ io.Writer) error {
	f.calls = append(f.calls, applyCall{
		Source:    plan.Source.Server,
		Target:    plan.Target.Server,
		Service:   service.Name,
		Container: service.Container,
		Alias:     migration.ApplyAlias(service),
	})
	return f.failOn[service.Name]
}

func installFakeApplyMigrator(t *testing.T, fake *fakeApplyMigrator) {
	t.Helper()
	prev := newDockerApplyMigrator
	newDockerApplyMigrator = func(func([]string) error) dockerApplyMigrator { return fake }
	t.Cleanup(func() { newDockerApplyMigrator = prev })
}

func resetDockerApplyFlagValues() {
	dockerApplyYes = false
	dockerApplyDryRun = false
	dockerApplyJSON = false
	dockerApplyNoHealth = false
}

// runApplyOps runs one command with a non-terminal stdin, so the confirmation
// gate behaves like a script instead of depending on the test binary's stdin.
func runApplyOps(t *testing.T, args ...string) (string, error) {
	t.Helper()
	resetDockerApplyFlagValues()
	t.Cleanup(resetDockerApplyFlagValues)
	return runPlanOps(t, args...)
}

// writeApplyPlan builds and saves a real plan file, the same artifact the plan
// command produces.
func writeApplyPlan(t *testing.T, dir, source, target string, infos ...*docker.ContainerInfo) string {
	t.Helper()
	evidences := make([]migration.ContainerEvidence, 0, len(infos))
	for _, info := range infos {
		evidences = append(evidences, migration.ContainerEvidence{Info: info, Analysis: docker.Analyze(info)})
	}
	plan, err := migration.BuildPlan(migration.BuildInput{
		SourceServer:     source,
		SourceCapability: planReport(source),
		TargetServer:     target,
		TargetCapability: planReport(target),
		Containers:       evidences,
		Backup:           migration.BackupDefaults{Backend: "s3:s3.example.com/bucket"},
	})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	path := filepath.Join(dir, "migration-plan.yaml")
	if err := migration.Save(path, plan); err != nil {
		t.Fatalf("Save plan: %v", err)
	}
	return path
}

func applyFake(servers ...string) *planFakeRemote {
	return newPlanFake(servers...)
}

func TestDockerApplyRejectsPlanWithoutTarget(t *testing.T) {
	setTestHome(t, t.TempDir())
	installFakeApplyMigrator(t, &fakeApplyMigrator{})

	// Build a plan with no target server: apply must refuse before any probe.
	path := writeApplyPlan(t, t.TempDir(), "vps-a", "", planContainerInfo())

	out, err := runApplyOps(t, "docker", "apply", path)
	if err == nil || !strings.Contains(err.Error(), "目标服务器") {
		t.Fatalf("error = %v, want a refusal that names the missing target (output: %s)", err, out)
	}
}

func TestDockerApplyDryRunReportsPreflightWithoutExecuting(t *testing.T) {
	home := seedPlanServers(t, "vps-a", "vps-b")
	resetDockerFlags(t)
	fake := applyFake("vps-a", "vps-b")
	fake.infos = map[string]*docker.ContainerInfo{"blog-db": planContainerInfo()}
	installPlanRemote(t, fake)
	migrator := &fakeApplyMigrator{}
	installFakeApplyMigrator(t, migrator)

	path := writeApplyPlan(t, home, "vps-a", "vps-b", planContainerInfo())
	out, err := runApplyOps(t, "docker", "apply", path, "--dry-run")
	if err != nil {
		t.Fatalf("apply --dry-run error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "迁移预检") || !strings.Contains(out, "就绪") {
		t.Errorf("preflight output missing:\n%s", out)
	}
	if len(migrator.calls) != 0 {
		t.Errorf("--dry-run must not execute any migration, got calls %+v", migrator.calls)
	}
}

func TestDockerApplyBlocksOnTargetConflict(t *testing.T) {
	home := seedPlanServers(t, "vps-a", "vps-b")
	resetDockerFlags(t)
	fake := applyFake("vps-a", "vps-b")
	fake.infos = map[string]*docker.ContainerInfo{"blog-db": planContainerInfo()}
	fake.containers["vps-b"] = []docker.ContainerSummary{{Name: "blog-db", State: "running"}}
	installPlanRemote(t, fake)
	migrator := &fakeApplyMigrator{}
	installFakeApplyMigrator(t, migrator)

	path := writeApplyPlan(t, home, "vps-a", "vps-b", planContainerInfo())
	out, err := runApplyOps(t, "docker", "apply", path, "--yes")
	if err == nil {
		t.Fatalf("apply must fail while the target has a same-named container:\n%s", out)
	}
	if !strings.Contains(out, "同名容器") {
		t.Errorf("conflict reason missing from output:\n%s", out)
	}
	if len(migrator.calls) != 0 {
		t.Errorf("a blocked service must not be executed, got %+v", migrator.calls)
	}
}

func TestDockerApplyBlocksOnSourceDrift(t *testing.T) {
	home := seedPlanServers(t, "vps-a", "vps-b")
	resetDockerFlags(t)
	fake := applyFake("vps-a", "vps-b")
	drifted := planContainerInfo()
	drifted.Image = "mysql:8.4"
	fake.infos = map[string]*docker.ContainerInfo{"blog-db": drifted}
	installPlanRemote(t, fake)
	migrator := &fakeApplyMigrator{}
	installFakeApplyMigrator(t, migrator)

	path := writeApplyPlan(t, home, "vps-a", "vps-b", planContainerInfo())
	out, err := runApplyOps(t, "docker", "apply", path, "--yes")
	if err == nil || !strings.Contains(err.Error(), "未全部完成") {
		t.Fatalf("apply should report an incomplete migration, got err=%v\n%s", err, out)
	}
	if len(migrator.calls) != 0 {
		t.Errorf("drifted service must be skipped, got %+v", migrator.calls)
	}
	if !strings.Contains(out, "mysql:8.4") {
		t.Errorf("drift detail must name the new image:\n%s", out)
	}
}

func TestDockerApplyRequiresYesInNonInteractiveShell(t *testing.T) {
	home := seedPlanServers(t, "vps-a", "vps-b")
	resetDockerFlags(t)
	fake := applyFake("vps-a", "vps-b")
	fake.infos = map[string]*docker.ContainerInfo{"blog-db": planContainerInfo()}
	installPlanRemote(t, fake)
	migrator := &fakeApplyMigrator{}
	installFakeApplyMigrator(t, migrator)

	path := writeApplyPlan(t, home, "vps-a", "vps-b", planContainerInfo())
	_, err := runApplyOps(t, "docker", "apply", path)
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("error = %v, want a non-interactive refusal pointing at --yes", err)
	}
	if len(migrator.calls) != 0 {
		t.Errorf("a refused confirmation must not execute, got %+v", migrator.calls)
	}
}

func TestDockerApplyAbortsWhenTargetDockerMissing(t *testing.T) {
	home := seedPlanServers(t, "vps-a", "vps-b")
	resetDockerFlags(t)
	fake := applyFake("vps-a", "vps-b")
	fake.infos = map[string]*docker.ContainerInfo{"blog-db": planContainerInfo()}
	broken := planReport("vps-b")
	broken.Docker = capability.Check{Name: "docker", Status: capability.StatusMissing, Detail: "未安装 Docker"}
	fake.reports["vps-b"] = broken
	installPlanRemote(t, fake)
	migrator := &fakeApplyMigrator{}
	installFakeApplyMigrator(t, migrator)

	path := writeApplyPlan(t, home, "vps-a", "vps-b", planContainerInfo())
	out, err := runApplyOps(t, "docker", "apply", path, "--yes")
	if err == nil || !strings.Contains(err.Error(), "阻止整次迁移") {
		t.Fatalf("error = %v, want a plan-wide abort\n%s", err, out)
	}
	if len(migrator.calls) != 0 {
		t.Errorf("a plan-wide blocker must stop every service, got %+v", migrator.calls)
	}
}

func TestDockerApplyExecutesServicesPreservingNames(t *testing.T) {
	home := seedPlanServers(t, "vps-a", "vps-b")
	resetDockerFlags(t)
	fake := applyFake("vps-a", "vps-b")
	fake.infos = map[string]*docker.ContainerInfo{"blog-db": planContainerInfo()}
	installPlanRemote(t, fake)
	migrator := &fakeApplyMigrator{}
	installFakeApplyMigrator(t, migrator)

	path := writeApplyPlan(t, home, "vps-a", "vps-b", planContainerInfo())
	out, err := runApplyOps(t, "docker", "apply", path, "--yes")
	if err != nil {
		t.Fatalf("apply error: %v\n%s", err, out)
	}
	if len(migrator.calls) != 1 {
		t.Fatalf("migrator calls = %+v, want one", migrator.calls)
	}
	call := migrator.calls[0]
	if call.Source != "vps-a" || call.Target != "vps-b" || call.Container != "blog-db" {
		t.Errorf("call = %+v, want the planned source/target/container", call)
	}
	if call.Alias != "blog" {
		t.Errorf("alias = %q, want the preserved Compose project name", call.Alias)
	}
	if !strings.Contains(out, "迁移汇总") || !strings.Contains(out, "成功 1 / 失败 0 / 跳过 0") {
		t.Errorf("summary missing:\n%s", out)
	}
	// A successful migration ends with the post-start health verdict, so the
	// operator sees that "the service is usable" was actually verified.
	if len(fake.healthCalls) != 1 {
		t.Fatalf("health calls = %d, want the apply run to verify the started service", len(fake.healthCalls))
	}
	if !strings.Contains(out, "健康检查") || !strings.Contains(out, "通过健康检查") {
		t.Errorf("apply output must include the health verdict:\n%s", out)
	}
}

func TestDockerApplyFailsWhenHealthCheckFindsBrokenService(t *testing.T) {
	home := seedPlanServers(t, "vps-a", "vps-b")
	resetDockerFlags(t)
	fake := applyFake("vps-a", "vps-b")
	fake.infos = map[string]*docker.ContainerInfo{"blog-db": planContainerInfo()}
	fake.healthReports = map[string]*capability.HealthReport{
		"vps-b": healthReport("vps-b", "db", capability.HealthCheckResult{
			Kind: capability.HealthKindPorts, Label: "端口 3306/tcp", Status: capability.HealthFailed,
			Detail: "宿主机没有在 3306/tcp 上监听", NextStep: "在目标机执行 `ss -ltn | grep 3306` 确认端口",
		}),
	}
	installPlanRemote(t, fake)
	installFakeApplyMigrator(t, &fakeApplyMigrator{})

	path := writeApplyPlan(t, home, "vps-a", "vps-b", planContainerInfo())
	out, err := runApplyOps(t, "docker", "apply", path, "--yes")
	if err == nil || !strings.Contains(err.Error(), "未全部完成") {
		t.Fatalf("error = %v, want the migration to fail when the service is not usable\n%s", err, out)
	}
	if !strings.Contains(out, "成功 0 / 失败 1 / 跳过 0") {
		t.Errorf("a service that started but failed its health check must be reported failed:\n%s", out)
	}
	if !strings.Contains(out, "ss -ltn") {
		t.Errorf("summary must carry the health check's next step:\n%s", out)
	}
}

func TestDockerApplyNoHealthSkipsTheProbe(t *testing.T) {
	home := seedPlanServers(t, "vps-a", "vps-b")
	resetDockerFlags(t)
	fake := applyFake("vps-a", "vps-b")
	fake.infos = map[string]*docker.ContainerInfo{"blog-db": planContainerInfo()}
	installPlanRemote(t, fake)
	installFakeApplyMigrator(t, &fakeApplyMigrator{})

	path := writeApplyPlan(t, home, "vps-a", "vps-b", planContainerInfo())
	out, err := runApplyOps(t, "docker", "apply", path, "--yes", "--no-health")
	if err != nil {
		t.Fatalf("apply --no-health error: %v\n%s", err, out)
	}
	if len(fake.healthCalls) != 0 {
		t.Errorf("--no-health must skip the probe, got %+v", fake.healthCalls)
	}
	if strings.Contains(out, "健康检查") {
		t.Errorf("--no-health must not print a health verdict:\n%s", out)
	}
}

func TestDockerApplyHealthCheckFailuresDoNotAffectSkippedServices(t *testing.T) {
	home := seedPlanServers(t, "vps-a", "vps-b")
	resetDockerFlags(t)
	fake := applyFake("vps-a", "vps-b")
	fake.infos = map[string]*docker.ContainerInfo{"blog-db": planContainerInfo()}
	// The only container is blocked before execution, so there is nothing to
	// health-check and the probe must not run against a service that was never
	// started.
	fake.containers["vps-b"] = []docker.ContainerSummary{{Name: "blog-db", State: "running"}}
	installPlanRemote(t, fake)
	installFakeApplyMigrator(t, &fakeApplyMigrator{})

	path := writeApplyPlan(t, home, "vps-a", "vps-b", planContainerInfo())
	if _, err := runApplyOps(t, "docker", "apply", path, "--yes"); err == nil {
		t.Fatal("apply must fail while the target has a same-named container")
	}
	if len(fake.healthCalls) != 0 {
		t.Errorf("a blocked service must not be health-checked, got %+v", fake.healthCalls)
	}
}

func TestDockerApplyContinuesAfterIndependentFailure(t *testing.T) {
	home := seedPlanServers(t, "vps-a", "vps-b")
	resetDockerFlags(t)

	shop := planContainerInfo()
	shop.Name = "shop-web"
	shop.Image = "nginx:alpine"
	shop.Env = []string{"PATH=/usr/bin"}
	shop.Mounts = nil
	shop.Ports = nil
	shop.Labels[docker.LabelComposeProject] = "shop"
	shop.Labels[docker.LabelComposeService] = "web"

	fake := applyFake("vps-a", "vps-b")
	fake.infos = map[string]*docker.ContainerInfo{"blog-db": planContainerInfo(), "shop-web": shop}
	installPlanRemote(t, fake)

	migrator := &fakeApplyMigrator{failOn: map[string]error{"db": errors.New("restic 快照丢失")}}
	installFakeApplyMigrator(t, migrator)

	path := writeApplyPlan(t, home, "vps-a", "vps-b", planContainerInfo(), shop)
	out, err := runApplyOps(t, "docker", "apply", path, "--yes")
	if err == nil || !strings.Contains(err.Error(), "未全部完成") {
		t.Fatalf("error = %v, want an incomplete-migration error\n%s", err, out)
	}
	if len(migrator.calls) != 2 {
		t.Fatalf("calls = %+v, want both services attempted", migrator.calls)
	}
	if !strings.Contains(out, "成功 1 / 失败 1 / 跳过 0") {
		t.Errorf("summary missing the split result:\n%s", out)
	}
}

func TestDockerApplyJSONReportsOutcomes(t *testing.T) {
	home := seedPlanServers(t, "vps-a", "vps-b")
	resetDockerFlags(t)
	fake := applyFake("vps-a", "vps-b")
	fake.infos = map[string]*docker.ContainerInfo{"blog-db": planContainerInfo()}
	installPlanRemote(t, fake)
	installFakeApplyMigrator(t, &fakeApplyMigrator{})

	path := writeApplyPlan(t, home, "vps-a", "vps-b", planContainerInfo())
	out, err := runApplyOps(t, "docker", "apply", path, "--yes", "--json")
	if err != nil {
		t.Fatalf("apply --json error: %v\n%s", err, out)
	}
	var doc struct {
		Kind     string `json:"kind"`
		Target   string `json:"target"`
		Services []struct {
			Service string `json:"service"`
			Status  string `json:"status"`
		} `json:"services"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, out)
	}
	if doc.Kind != "docker_migration_apply_result" || doc.Target != "vps-b" {
		t.Errorf("doc header = %+v, want the apply result for vps-b", doc)
	}
	if len(doc.Services) != 1 || doc.Services[0].Status != dockerApplyStatusSuccess {
		t.Errorf("services = %+v, want one successful service", doc.Services)
	}
}
