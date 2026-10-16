package migration

import (
	"strings"
	"testing"
	"time"

	"github.com/volcano6/opspulse/internal/capability"
	"github.com/volcano6/opspulse/internal/docker"
)

func applyPlan(t *testing.T, infos ...*docker.ContainerInfo) *Plan {
	t.Helper()
	evidences := make([]ContainerEvidence, 0, len(infos))
	for _, info := range infos {
		evidences = append(evidences, evidenceFor(t, info))
	}
	plan, err := BuildPlan(BuildInput{
		SourceServer:     "vps-a",
		SourceCapability: healthyReport("vps-a"),
		TargetServer:     "vps-b",
		TargetCapability: healthyReport("vps-b"),
		Containers:       evidences,
		Backup:           BackupDefaults{Backend: "s3:s3.example.com/bucket"},
		Now:              time.Date(2026, 10, 15, 2, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("BuildPlan error: %v", err)
	}
	return plan
}

func singleServicePlan(t *testing.T) *Plan {
	t.Helper()
	return applyPlan(t, composeDatabaseContainer())
}

func TestApplyVerifySourceAcceptsAnUnchangedContainer(t *testing.T) {
	plan := singleServicePlan(t)
	analyses := map[string]*docker.ContainerAnalysis{"blog-db": docker.Analyze(composeDatabaseContainer())}

	if risks := VerifySource(plan, analyses); len(risks) != 0 {
		t.Fatalf("VerifySource = %v, want no risks for an unchanged container", risks)
	}
}

func TestApplyVerifySourceBlocksDisappearedContainer(t *testing.T) {
	plan := singleServicePlan(t)
	risks := VerifySource(plan, map[string]*docker.ContainerAnalysis{})

	if !riskCodes(risks)[RiskApplySourceMissing] {
		t.Fatalf("risks = %v, want %s", risks, RiskApplySourceMissing)
	}
	if !risks[0].isBlocker() {
		t.Errorf("missing source container must be a hard blocker")
	}
}

func TestApplyVerifySourceBlocksDrift(t *testing.T) {
	plan := singleServicePlan(t)
	drifted := composeDatabaseContainer()
	drifted.Image = "mysql:8.4"

	risks := VerifySource(plan, map[string]*docker.ContainerAnalysis{"blog-db": docker.Analyze(drifted)})
	codes := riskCodes(risks)
	if !codes[RiskApplySourceDrift] {
		t.Fatalf("risks = %v, want %s", risks, RiskApplySourceDrift)
	}
	if !strings.Contains(risks[0].Detail, "mysql:8.4") {
		t.Errorf("drift detail = %q, want it to name the new image", risks[0].Detail)
	}
	if !risks[0].isBlocker() {
		t.Errorf("source drift must be a hard blocker")
	}
}

func TestApplyVerifyTargetBlocksContainerProjectPortAndPathConflicts(t *testing.T) {
	plan := singleServicePlan(t)

	containers := []docker.ContainerSummary{{
		Name:  "blog-db",
		State: "running",
		Ports: "0.0.0.0:3306->3306/tcp, [::]:3306->3306/tcp",
		Labels: map[string]string{
			docker.LabelComposeProject: "blog",
		},
	}}
	paths := []capability.Check{{Name: "/opt/blog", Status: capability.StatusOK, Exists: true, Writable: true, Detail: "目录存在且可写"}}

	risks := VerifyTarget(plan, containers, paths)
	codes := riskCodes(risks)
	for _, want := range []string{
		RiskApplyTargetContainerConflict,
		RiskApplyTargetProjectConflict,
		RiskApplyTargetPortConflict,
		RiskApplyTargetPathConflict,
	} {
		if !codes[want] {
			t.Errorf("risks = %v, want %s", risks, want)
		}
	}
	for _, risk := range risks {
		if !risk.isBlocker() {
			t.Errorf("target conflict %s must be a hard blocker", risk.Code)
		}
	}
}

func TestApplyVerifyTargetAllowsCleanTarget(t *testing.T) {
	plan := singleServicePlan(t)

	// A different container on a different port must not be reported, and a
	// not-yet-created project directory is exactly what the plan expects.
	containers := []docker.ContainerSummary{{
		Name:  "other-app",
		State: "running",
		Ports: "0.0.0.0:8080->80/tcp",
		Labels: map[string]string{
			docker.LabelComposeProject: "other",
		},
	}}
	paths := []capability.Check{{Name: "/opt/blog", Status: capability.StatusPartial, Writable: true, Detail: "目录不存在，但父目录可写"}}

	if risks := VerifyTarget(plan, containers, paths); len(risks) != 0 {
		t.Fatalf("VerifyTarget = %v, want no conflicts on a clean target", risks)
	}
}

func TestApplyVerifyProjectBatchingBlocksMultiServiceProject(t *testing.T) {
	web := composeDatabaseContainer()
	web.Name = "blog-web"
	web.Image = "nginx:alpine"
	web.Env = []string{"PATH=/usr/bin"}
	web.Mounts = nil
	web.Ports = nil
	web.Labels[docker.LabelComposeService] = "web"

	plan := applyPlan(t, composeDatabaseContainer(), web)
	risks := VerifyProjectBatching(plan)
	if !riskCodes(risks)[RiskApplyProjectMultiService] {
		t.Fatalf("risks = %v, want %s", risks, RiskApplyProjectMultiService)
	}
	if !risks[0].isBlocker() {
		t.Errorf("a multi-service Compose batch must be a hard blocker")
	}
}

func TestApplyProjectBatchingAllowsSingleServiceProject(t *testing.T) {
	if risks := VerifyProjectBatching(singleServicePlan(t)); len(risks) != 0 {
		t.Fatalf("risks = %v, want no batching blocker for a single-service project", risks)
	}
}

func TestApplyAdjustmentsExtractTargetEdits(t *testing.T) {
	plan := singleServicePlan(t)
	service := plan.Services[0]
	if Adjustments(service).Any() {
		t.Fatalf("an untouched plan must not carry adjustments")
	}

	service.TargetPorts = []string{"13306:3306"}
	service.TargetPaths = []TargetPath{
		{Kind: "compose_project", Source: "/opt/blog", Target: "/srv/blog"},
		{Kind: "bind_mount", Source: "/srv/blog/conf.d", Target: "/data/blog/conf.d"},
	}
	adjustment := Adjustments(service)
	if !adjustment.Any() {
		t.Fatalf("edited plan service should carry adjustments")
	}
	if len(adjustment.Ports) != 1 || adjustment.Ports[0] != "13306:3306" {
		t.Errorf("ports = %v, want the edited target port", adjustment.Ports)
	}
	if adjustment.ProjectDir != "/srv/blog" {
		t.Errorf("project dir = %q, want /srv/blog", adjustment.ProjectDir)
	}
	if got := adjustment.BindTargets["/etc/mysql/conf.d"]; got != "/data/blog/conf.d" {
		t.Errorf("bind targets = %v, want the destination keyed edit", adjustment.BindTargets)
	}

	if !riskCodes(AdjustmentRisks(&Plan{Services: []ServicePlan{service}}))[RiskApplyAdjustment] {
		t.Errorf("AdjustmentRisks should report the plan edits")
	}
}

func TestApplyCheckTargetRisksReusesCapabilityTable(t *testing.T) {
	broken := healthyReport("vps-b")
	broken.Compose = capability.Check{Name: "compose", Status: capability.StatusMissing}

	risks := CheckTargetRisks("vps-b", broken, true)
	if !riskCodes(risks)[RiskTargetComposeMissing] {
		t.Fatalf("risks = %v, want %s", risks, RiskTargetComposeMissing)
	}
}

func TestApplyVerifyTargetImagesBlocksMissingPreloadImage(t *testing.T) {
	plan := singleServicePlan(t)
	plan.Services[0].ImageAcquisition = ImagePreloadRequired

	report := healthyReport("vps-b")
	report.Registry = capability.Check{Name: "registry", Status: capability.StatusMissing}
	report.Images = []capability.Check{{Name: "mysql:8.0", Status: capability.StatusMissing}}

	risks := VerifyTargetImages(plan, report)
	if !riskCodes(risks)[RiskApplyTargetImageMissing] {
		t.Fatalf("risks = %v, want %s", risks, RiskApplyTargetImageMissing)
	}
	if !risks[0].isBlocker() {
		t.Errorf("a missing preload-required image must be a hard blocker")
	}
}

func TestApplyVerifyTargetImagesIgnoresPresentImage(t *testing.T) {
	plan := singleServicePlan(t)
	plan.Services[0].ImageAcquisition = ImagePreloadRequired
	report := healthyReport("vps-b")
	report.Images = []capability.Check{{Name: "mysql:8.0", Status: capability.StatusOK}}

	if risks := VerifyTargetImages(plan, report); len(risks) != 0 {
		t.Fatalf("risks = %v, want no blocker when the preload image is present", risks)
	}
}

func TestApplyAliasPreservesProjectName(t *testing.T) {
	plan := singleServicePlan(t)
	if got := ApplyAlias(plan.Services[0]); got != "blog" {
		t.Errorf("ApplyAlias(compose) = %q, want the Compose project name", got)
	}

	run := ServicePlan{Name: "web", Mode: ModeRun}
	if got := ApplyAlias(run); got != "web" {
		t.Errorf("ApplyAlias(run) = %q, want the plan job name", got)
	}
}
