package migration

import (
	"strings"
	"testing"

	"github.com/volcano6/opspulse/internal/capability"
	"github.com/volcano6/opspulse/internal/docker"
)

func planWithHealth(t *testing.T, url string) *Plan {
	t.Helper()
	info := composeDatabaseContainer()
	plan, err := BuildPlan(BuildInput{
		SourceServer:     "vps-a",
		SourceCapability: healthyReport("vps-a"),
		TargetServer:     "vps-b",
		TargetCapability: healthyReport("vps-b"),
		Containers:       []ContainerEvidence{evidenceFor(t, info)},
		Backup:           BackupDefaults{Backend: "s3:s3.example.com/bucket"},
		HealthURL:        url,
	})
	if err != nil {
		t.Fatalf("BuildPlan error: %v", err)
	}
	return plan
}

func TestBuildPlanRecordsHTTPHealthURL(t *testing.T) {
	plan := planWithHealth(t, "http://vps-b:8080/health")
	if plan.Services[0].Health == nil || plan.Services[0].Health.HTTPURL != "http://vps-b:8080/health" {
		t.Fatalf("service health = %+v, want the configured URL", plan.Services[0].Health)
	}
	if codes := riskCodes(plan.Risks.FollowUps); codes[RiskHealthHTTPNotConfigured] {
		t.Error("a plan with a configured HTTP check must not carry the not-configured follow-up")
	}
}

func TestBuildPlanWithoutHealthURLReportsSkippedFollowUp(t *testing.T) {
	plan := planWithHealth(t, "")
	if plan.Services[0].Health != nil {
		t.Errorf("service health = %+v, want nil when no URL is configured", plan.Services[0].Health)
	}
	if codes := riskCodes(plan.Risks.FollowUps); !codes[RiskHealthHTTPNotConfigured] {
		t.Errorf("follow-ups = %v, want the HTTP-not-configured notice", plan.Risks.FollowUps)
	}
	for _, risk := range plan.Risks.FollowUps {
		if risk.Code != RiskHealthHTTPNotConfigured {
			continue
		}
		if !strings.Contains(risk.Detail, "db") || risk.Action == "" {
			t.Errorf("follow-up = %+v, want it to name the service and offer an action", risk)
		}
	}
}

func TestBuildPlanRejectsHTTPURLForBatch(t *testing.T) {
	first := composeDatabaseContainer()
	second := composeDatabaseContainer()
	second.Name = "blog-web"
	second.Image = "nginx:alpine"
	second.Mounts = nil
	second.Labels[docker.LabelComposeService] = "web"

	_, err := BuildPlan(BuildInput{
		SourceServer:     "vps-a",
		SourceCapability: healthyReport("vps-a"),
		Containers:       []ContainerEvidence{evidenceFor(t, first), evidenceFor(t, second)},
		HealthURL:        "http://vps-b:8080/health",
	})
	if err == nil || !strings.Contains(err.Error(), "single-container") {
		t.Fatalf("error = %v, want a refusal: one URL cannot apply to a batch plan", err)
	}
}

func TestParseHealthURLValidation(t *testing.T) {
	if health, err := ParseHealthURL(""); err != nil || health != nil {
		t.Errorf("ParseHealthURL(\"\") = %+v/%v, want nil/nil", health, err)
	}
	if health, err := ParseHealthURL("  https://vps-b/health  "); err != nil || health.HTTPURL != "https://vps-b/health" {
		t.Errorf("ParseHealthURL() = %+v/%v, want a trimmed https URL", health, err)
	}
	for _, raw := range []string{"vps-b/health", "ftp://vps-b/health", "http://a/b|c", "http://a\nb"} {
		if _, err := ParseHealthURL(raw); err == nil {
			t.Errorf("ParseHealthURL(%q) should fail", raw)
		}
	}
}

func TestHealthTargetsDescribeEveryService(t *testing.T) {
	plan := planWithHealth(t, "http://vps-b:8080/health")

	// The plan feeds the 05 remote capability seam directly: this assignment
	// pins that shape, so a future refactor cannot introduce a second health
	// target type that the mocked seam no longer serves.
	var targets []capability.HealthTarget = HealthTargets(plan)
	if len(targets) != 1 {
		t.Fatalf("targets = %+v, want one", targets)
	}
	target := targets[0]
	if target.Service != "db" || target.Container != "blog-db" {
		t.Errorf("target identity = %+v, want the plan service and container", target)
	}
	if target.ComposeProject != "blog" || target.ComposeService != "db" {
		t.Errorf("target compose = %q/%q, want blog/db", target.ComposeProject, target.ComposeService)
	}
	if len(target.Ports) != 1 || target.Ports[0] != "3306/tcp" {
		t.Errorf("target ports = %v, want the published host port", target.Ports)
	}
	if target.DatabaseEngine != "mysql" {
		t.Errorf("target database = %q, want mysql", target.DatabaseEngine)
	}
	if target.HTTPURL != "http://vps-b:8080/health" {
		t.Errorf("target URL = %q, want the plan's HTTP check", target.HTTPURL)
	}
}

func TestHealthTargetsForPlainContainer(t *testing.T) {
	info := composeDatabaseContainer()
	info.Labels = map[string]string{}
	info.Name = "wild-db"
	info.Mounts = nil
	info.Ports = nil

	plan, err := BuildPlan(BuildInput{
		SourceServer:     "vps-a",
		SourceCapability: healthyReport("vps-a"),
		Containers:       []ContainerEvidence{evidenceFor(t, info)},
	})
	if err != nil {
		t.Fatalf("BuildPlan error: %v", err)
	}

	target := HealthTargets(plan)[0]
	// A plain container becomes a single-service Compose project named after
	// its job, so the probe can still resolve it by label on the target.
	if target.ComposeProject != target.Service || target.ComposeService != docker.SanitizeServiceName(target.Service) {
		t.Errorf("target compose = %q/%q, want the generated project/service for %q",
			target.ComposeProject, target.ComposeService, target.Service)
	}
}

func TestHealthPortsUsesTargetPortEdits(t *testing.T) {
	service := ServicePlan{
		Name: "web",
		Config: ServiceConfig{
			Ports: []string{"8080:80", "443:443"},
		},
		TargetPorts: []string{"18080:80"},
	}
	ports := HealthPorts(service)
	if len(ports) != 1 || ports[0] != "18080/tcp" {
		t.Errorf("ports = %v, want the edited target port only", ports)
	}
}

func TestHealthPortsSkipsUnpublishedPorts(t *testing.T) {
	service := ServicePlan{Name: "web", Config: ServiceConfig{Ports: []string{"80", "127.0.0.1:8080:80"}}}
	ports := HealthPorts(service)
	if len(ports) != 1 || ports[0] != "8080/tcp" {
		t.Errorf("ports = %v, want only the published host port", ports)
	}
}

func TestPlanRoundTripsHealthConfig(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/plan.yaml"

	plan := planWithHealth(t, "http://vps-b:8080/health")
	if err := Save(path, plan); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if loaded.Services[0].Health == nil || loaded.Services[0].Health.HTTPURL != "http://vps-b:8080/health" {
		t.Errorf("loaded health = %+v, want the saved URL", loaded.Services[0].Health)
	}

	target := HealthTargets(loaded)[0]
	if target.HTTPURL != "http://vps-b:8080/health" {
		t.Errorf("loaded target URL = %q, want the persisted HTTP check", target.HTTPURL)
	}
}

func TestHealthTargetForUsesAliasForPlainContainers(t *testing.T) {
	service := ServicePlan{Name: "wild-db", Container: "wild-db"}
	target := HealthTargetFor(service)
	if target.ComposeProject != ApplyAlias(service) {
		t.Errorf("compose project = %q, want the apply alias %q so the probe resolves the same container apply started",
			target.ComposeProject, ApplyAlias(service))
	}
}

// TestHealthPlanRisksCollapseBatchServices pins the batch behaviour: one
// follow-up names every service without an HTTP check instead of repeating the
// same notice per service.
func TestHealthPlanRisksCollapseBatchServices(t *testing.T) {
	plan := &Plan{Services: []ServicePlan{{Name: "web"}, {Name: "db"}}}
	risks := HealthPlanRisks(plan)
	if len(risks) != 1 {
		t.Fatalf("risks = %+v, want one collapsed follow-up", risks)
	}
	if !strings.Contains(risks[0].Detail, "db, web") {
		t.Errorf("detail = %q, want both service names in sorted order", risks[0].Detail)
	}
	if risks[0].Code != RiskHealthHTTPNotConfigured {
		t.Errorf("code = %q, want %q", risks[0].Code, RiskHealthHTTPNotConfigured)
	}
}

func TestHealthPlanRisksSilentWhenEveryServiceConfigured(t *testing.T) {
	plan := &Plan{Services: []ServicePlan{
		{Name: "web", Health: &HealthPlan{HTTPURL: "http://a/health"}},
		{Name: "db", Health: &HealthPlan{HTTPURL: "http://b/health"}},
	}}
	if risks := HealthPlanRisks(plan); len(risks) != 0 {
		t.Errorf("risks = %+v, want none", risks)
	}
}

// TestHealthTargetsRejectsNothingAtPlanTimeButProbeDoes: the plan keeps the URL
// verbatim, and validation lives in the probe seam. This documents that split
// so a future edit does not start silently dropping bad URLs from the plan.
func TestHealthTargetCarriesEngineAndPortsToProbe(t *testing.T) {
	target := HealthTargetFor(ServicePlan{
		Name:     "db",
		Database: &DatabasePlan{Engine: "postgres"},
		Config:   ServiceConfig{Ports: []string{"5432:5432"}},
		Health:   &HealthPlan{HTTPURL: "http://vps-b/health"},
	})
	if target.DatabaseEngine != "postgres" || len(target.Ports) != 1 || target.Ports[0] != "5432/tcp" {
		t.Errorf("target = %+v, want the postgres engine and the published port", target)
	}
}
