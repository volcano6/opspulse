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
