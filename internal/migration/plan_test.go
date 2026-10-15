package migration

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/volcano6/opspulse/internal/capability"
	"github.com/volcano6/opspulse/internal/docker"
)

// ── fixtures ────────────────────────────────────────────────────────────────

func healthyReport(server string) *capability.Report {
	return &capability.Report{
		Server:      server,
		Reachable:   true,
		Docker:      capability.Check{Name: "docker", Status: capability.StatusOK},
		Compose:     capability.Check{Name: "compose", Status: capability.StatusOK},
		Restic:      capability.Check{Name: "restic", Status: capability.StatusOK},
		HelperImage: capability.Check{Name: "helper_image", Status: capability.StatusOK},
		Registry:    capability.Check{Name: "registry", Status: capability.StatusOK},
		Disk:        capability.DiskSpace{Status: capability.StatusOK, Detail: "已用 20%，可用 40.0 GB"},
	}
}

// composeDatabaseContainer is a Compose-managed MySQL container with a named
// volume, an external bind mount and a sensitive environment variable: the
// shape that exercises every risk bucket at once.
func composeDatabaseContainer() *docker.ContainerInfo {
	return &docker.ContainerInfo{
		ID:            "1a2b3c4d5e6f",
		Name:          "blog-db",
		Image:         "mysql:8.0",
		RestartPolicy: "unless-stopped",
		Env:           []string{"PATH=/usr/bin", "MYSQL_ROOT_PASSWORD=supersecret", "MYSQL_DATABASE=blog"},
		Ports:         []docker.PortMapping{{HostPort: "3306", ContainerPort: "3306", Protocol: "tcp"}},
		Mounts: []docker.VolumeMount{
			{Type: "volume", Name: "blog-data", Source: "blog-data", Destination: "/var/lib/mysql"},
			{Type: "bind", Source: "/srv/blog/conf.d", Destination: "/etc/mysql/conf.d"},
		},
		Labels: map[string]string{
			docker.LabelComposeProject:     "blog",
			docker.LabelComposeService:     "db",
			docker.LabelComposeWorkingDir:  "/opt/blog",
			docker.LabelComposeConfigFiles: "/opt/blog/compose.yaml",
		},
	}
}

func evidenceFor(t *testing.T, info *docker.ContainerInfo) ContainerEvidence {
	t.Helper()
	analysis := docker.Analyze(info)
	if analysis == nil {
		t.Fatalf("Analyze(%q) returned nil", info.Name)
	}
	return ContainerEvidence{Info: info, Analysis: analysis}
}

func riskCodes(risks []Risk) map[string]bool {
	codes := make(map[string]bool, len(risks))
	for _, risk := range risks {
		codes[risk.Code] = true
	}
	return codes
}

// ── BuildPlan ───────────────────────────────────────────────────────────────

func TestBuildPlanDescribesSourceTargetAndService(t *testing.T) {
	info := composeDatabaseContainer()
	plan, err := BuildPlan(BuildInput{
		SourceServer:     "vps-a",
		SourceCapability: healthyReport("vps-a"),
		TargetServer:     "vps-b",
		TargetCapability: healthyReport("vps-b"),
		Containers:       []ContainerEvidence{evidenceFor(t, info)},
		Backup:           BackupDefaults{Backend: "s3:s3.example.com/bucket", ExistingJobs: []string{"blog"}},
		Generator:        "ops test",
		Now:              time.Date(2026, 10, 15, 2, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("BuildPlan error: %v", err)
	}

	if plan.FormatVersion != PlanFormatVersion || plan.Kind != PlanKind {
		t.Fatalf("plan header = version %d kind %q, want %d/%q", plan.FormatVersion, plan.Kind, PlanFormatVersion, PlanKind)
	}
	if plan.GeneratedAt != "2026-10-15T02:00:00Z" {
		t.Errorf("GeneratedAt = %q, want the injected clock", plan.GeneratedAt)
	}
	if plan.Source.Server != "vps-a" || plan.Target.Server != "vps-b" {
		t.Errorf("endpoints = %q → %q, want vps-a → vps-b", plan.Source.Server, plan.Target.Server)
	}
	if len(plan.Services) != 1 {
		t.Fatalf("services = %d, want 1", len(plan.Services))
	}

	service := plan.Services[0]
	if service.Name != "db" || service.Container != "blog-db" || service.Mode != ModeCompose {
		t.Errorf("service identity = %+v, want name db / container blog-db / mode compose", service)
	}
	if service.Image != "mysql:8.0" || service.ImageAcquisition != ImagePullFromRegistry {
		t.Errorf("image = %q acquisition = %q, want mysql:8.0/pull_from_registry", service.Image, service.ImageAcquisition)
	}
	if service.Config.ComposeFile != "/opt/blog/compose.yaml" || service.Config.ComposeSource != "existing" {
		t.Errorf("config = %+v, want the existing Compose file", service.Config)
	}
	if len(service.Config.SensitiveEnv) != 1 || service.Config.SensitiveEnv[0] != "MYSQL_ROOT_PASSWORD" {
		t.Errorf("sensitive env = %v, want only the variable name", service.Config.SensitiveEnv)
	}
	if len(service.Volumes) != 1 || service.Volumes[0].Name != "blog-data" {
		t.Errorf("volumes = %+v, want the named volume", service.Volumes)
	}
	if len(service.BindMounts) != 1 || service.BindMounts[0].Source != "/srv/blog/conf.d" {
		t.Errorf("bind mounts = %+v, want the external bind mount", service.BindMounts)
	}
	if service.Database == nil || service.Database.Engine != "mysql" || service.Database.Strategy != ConsistencyLogicalDump {
		t.Errorf("database = %+v, want a mysql logical dump", service.Database)
	}
	if service.Consistency.Database != ConsistencyLogicalDump || service.Consistency.Volumes != ConsistencyArchive {
		t.Errorf("consistency = %+v, want logical_dump/archive", service.Consistency)
	}
	if len(service.TargetPorts) != 1 || service.TargetPorts[0] != "3306:3306" {
		t.Errorf("target ports = %v, want 3306:3306", service.TargetPorts)
	}

	kinds := map[string]string{}
	for _, target := range service.TargetPaths {
		kinds[target.Kind] = target.Target
	}
	if kinds["compose_project"] != "/opt/blog" {
		t.Errorf("target paths = %+v, want the Compose project directory", service.TargetPaths)
	}
	if kinds["bind_mount"] != "/srv/blog/conf.d" {
		t.Errorf("target paths = %+v, want the bind mount source", service.TargetPaths)
	}
	if kinds["named_volume"] != "blog-data" {
		t.Errorf("target paths = %+v, want the named volume", service.TargetPaths)
	}

	if service.BackupConfig == nil || service.BackupConfig.JobName != "db" {
		t.Fatalf("backup config = %+v, want a proposed job", service.BackupConfig)
	}
	if service.BackupConfig.Backend != "s3:s3.example.com/bucket" || service.BackupConfig.Exists {
		t.Errorf("backup config = %+v, want the inherited backend and a new job", service.BackupConfig)
	}
	if len(service.BackupConfig.Paths) != 2 {
		t.Errorf("backup paths = %v, want the project directory plus the external bind mount", service.BackupConfig.Paths)
	}
}

func TestBuildPlanClassifiesBlockersWarningsAndFollowUps(t *testing.T) {
	broken := healthyReport("vps-b")
	broken.Compose = capability.Check{Name: "compose", Status: capability.StatusMissing, Detail: "未检测到 docker compose"}
	broken.Restic = capability.Check{Name: "restic", Status: capability.StatusMissing}
	broken.HelperImage = capability.Check{Name: "helper_image", Status: capability.StatusMissing}
	broken.Registry = capability.Check{Name: "registry", Status: capability.StatusMissing}
	broken.Disk = capability.DiskSpace{Status: capability.StatusMissing, Detail: "已用 95%，可用 1.0 GB"}
	broken.Paths = []capability.Check{{Name: "/srv/blog/conf.d", Status: capability.StatusMissing, Detail: "目录不存在且父目录不可写"}}

	plan, err := BuildPlan(BuildInput{
		SourceServer:     "vps-a",
		SourceCapability: healthyReport("vps-a"),
		TargetServer:     "vps-b",
		TargetCapability: broken,
		Containers:       []ContainerEvidence{evidenceFor(t, composeDatabaseContainer())},
	})
	if err != nil {
		t.Fatalf("BuildPlan error: %v", err)
	}

	blockers := riskCodes(plan.Risks.Blockers)
	for _, want := range []string{
		RiskTargetComposeMissing,
		RiskTargetResticMissing,
		RiskTargetHelperImageMissing,
		RiskTargetDiskFull,
		RiskTargetPathUnavailable,
	} {
		if !blockers[want] {
			t.Errorf("blockers = %v, want %s", blockers, want)
		}
	}

	warnings := riskCodes(plan.Risks.Warnings)
	for _, want := range []string{RiskSensitiveEnv, RiskExternalBindMount, RiskTargetRegistryUnreachable} {
		if !warnings[want] {
			t.Errorf("warnings = %v, want %s", warnings, want)
		}
	}

	if len(plan.Risks.FollowUps) == 0 {
		t.Error("follow-ups are empty, want at least the manual preparation steps")
	}
	if !plan.HasBlockers() {
		t.Error("HasBlockers() = false, want true")
	}

	// A blocker must never be downgraded into a warning bucket.
	for _, code := range []string{RiskTargetComposeMissing, RiskTargetDiskFull} {
		if warnings[code] {
			t.Errorf("blocker %s also appears in warnings", code)
		}
	}
}

func TestBuildPlanWithoutTargetKeepsSourcePaths(t *testing.T) {
	plan, err := BuildPlan(BuildInput{
		SourceServer:     "vps-a",
		SourceCapability: healthyReport("vps-a"),
		Containers:       []ContainerEvidence{evidenceFor(t, composeDatabaseContainer())},
	})
	if err != nil {
		t.Fatalf("BuildPlan error: %v", err)
	}

	if plan.Target.Server != "" || plan.Target.Capability != nil {
		t.Errorf("target = %+v, want an unspecified endpoint", plan.Target)
	}
	for _, risk := range plan.Risks.Blockers {
		if strings.HasPrefix(risk.Code, "target_") {
			t.Errorf("unspecified target produced a target blocker: %+v", risk)
		}
	}
	if !riskCodes(plan.Risks.FollowUps)[RiskTargetUnspecified] {
		t.Errorf("follow-ups = %v, want %s", riskCodes(plan.Risks.FollowUps), RiskTargetUnspecified)
	}

	service := plan.Services[0]
	if service.ImageAcquisition != ImageUnknown {
		t.Errorf("image acquisition = %q, want unknown without a target", service.ImageAcquisition)
	}
	for _, target := range service.TargetPaths {
		if target.Source != target.Target {
			t.Errorf("target path %+v must keep the source path when no target is pinned", target)
		}
	}
}

func TestBuildPlanRecordsUnsupportedAttributes(t *testing.T) {
	info := composeDatabaseContainer()
	info.Devices = []string{"/dev/nvidia0:/dev/nvidia0"}
	info.Privileged = true

	plan, err := BuildPlan(BuildInput{
		SourceServer:     "vps-a",
		SourceCapability: healthyReport("vps-a"),
		Containers:       []ContainerEvidence{evidenceFor(t, info)},
	})
	if err != nil {
		t.Fatalf("BuildPlan error: %v", err)
	}

	service := plan.Services[0]
	if len(service.Attributes) == 0 {
		t.Fatal("attributes are empty, want the 05 support table")
	}
	levels := map[string]docker.SupportLevel{}
	for _, attr := range service.Attributes {
		levels[attr.Attribute] = attr.Level
	}
	if levels["device"] != docker.SupportUnsupported {
		t.Errorf("device attribute = %q, want unsupported", levels["device"])
	}
	if levels["privileged"] != docker.SupportPartial {
		t.Errorf("privileged attribute = %q, want partial", levels["privileged"])
	}

	warnings := riskCodes(plan.Risks.Warnings)
	for _, want := range []string{RiskAttributeUnsupported, RiskAttributePartial, RiskDeviceRequired} {
		if !warnings[want] {
			t.Errorf("warnings = %v, want %s", warnings, want)
		}
	}
}

func TestBuildPlanRequiresEvidence(t *testing.T) {
	if _, err := BuildPlan(BuildInput{Containers: []ContainerEvidence{{}}}); err == nil {
		t.Error("BuildPlan without a source server must fail")
	}
	if _, err := BuildPlan(BuildInput{SourceServer: "vps-a"}); err == nil {
		t.Error("BuildPlan without containers must fail")
	}
}

func TestJobNamesAreUniqueAndSafe(t *testing.T) {
	first := composeDatabaseContainer()
	second := composeDatabaseContainer()
	second.Name = "blog-db-2"

	odd := composeDatabaseContainer()
	odd.Name = "Odd Name/Container"
	odd.Labels = map[string]string{docker.LabelComposeProject: "blog"} // no service label

	evidences := []ContainerEvidence{
		evidenceFor(t, first),
		evidenceFor(t, second),
		evidenceFor(t, odd),
	}
	names := jobNames(evidences)

	if names[0] == names[1] {
		t.Errorf("names = %v, want two distinct jobs for one Compose service name", names)
	}
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			t.Fatalf("names = %v, want no empty job name", names)
		}
		if strings.ContainsAny(name, " /") {
			t.Errorf("job name %q must not contain spaces or slashes", name)
		}
	}
}

// ── plan file ───────────────────────────────────────────────────────────────

func TestPlanSaveAndLoadRoundTrip(t *testing.T) {
	plan, err := BuildPlan(BuildInput{
		SourceServer:     "vps-a",
		SourceCapability: healthyReport("vps-a"),
		TargetServer:     "vps-b",
		TargetCapability: healthyReport("vps-b"),
		Containers:       []ContainerEvidence{evidenceFor(t, composeDatabaseContainer())},
		Backup:           BackupDefaults{Backend: "s3:s3.example.com/bucket"},
		Generator:        "ops test",
	})
	if err != nil {
		t.Fatalf("BuildPlan error: %v", err)
	}

	path := filepath.Join(t.TempDir(), "plan.yaml")
	if err := Save(path, plan); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved plan: %v", err)
	}
	if !strings.Contains(string(raw), "format_version: 1") {
		t.Errorf("saved plan does not carry its format version:\n%s", raw)
	}
	if !strings.Contains(string(raw), "kind: docker_migration_plan") {
		t.Errorf("saved plan does not carry its kind:\n%s", raw)
	}
	// Windows does not round-trip POSIX permission bits (os.Chmod(0600) reads
	// back as 0666), so the mode assertion only runs on the platforms where the
	// promise is meaningful.
	if runtime.GOOS != "windows" {
		fi, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("stat saved plan: %v", statErr)
		}
		if mode := fi.Mode().Perm(); mode != 0o600 {
			t.Errorf("plan mode = %o, want 600", mode)
		}
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if loaded.FormatVersion != PlanFormatVersion || loaded.Kind != PlanKind {
		t.Errorf("loaded header = %d/%q, want %d/%q", loaded.FormatVersion, loaded.Kind, PlanFormatVersion, PlanKind)
	}
	if loaded.Source.Server != "vps-a" || loaded.Target.Server != "vps-b" {
		t.Errorf("loaded endpoints = %q → %q, want vps-a → vps-b", loaded.Source.Server, loaded.Target.Server)
	}
	if len(loaded.Services) != 1 || loaded.Services[0].Container != "blog-db" {
		t.Fatalf("loaded services = %+v, want blog-db", loaded.Services)
	}
	if len(loaded.Services[0].TargetPaths) != len(plan.Services[0].TargetPaths) {
		t.Errorf("target paths lost in round-trip: %d vs %d",
			len(loaded.Services[0].TargetPaths), len(plan.Services[0].TargetPaths))
	}
	if loaded.Services[0].BackupConfig == nil || loaded.Services[0].BackupConfig.Backend != "s3:s3.example.com/bucket" {
		t.Errorf("backup config lost in round-trip: %+v", loaded.Services[0].BackupConfig)
	}
}

func TestSaveRefusesToOverwriteAnExistingPlan(t *testing.T) {
	plan, err := BuildPlan(BuildInput{
		SourceServer:     "vps-a",
		SourceCapability: healthyReport("vps-a"),
		Containers:       []ContainerEvidence{evidenceFor(t, composeDatabaseContainer())},
	})
	if err != nil {
		t.Fatalf("BuildPlan error: %v", err)
	}

	path := filepath.Join(t.TempDir(), "plan.yaml")
	if err := Save(path, plan); err != nil {
		t.Fatalf("first Save error: %v", err)
	}
	err = Save(path, plan)
	if !errors.Is(err, ErrPlanFileExists) {
		t.Fatalf("second Save error = %v, want ErrPlanFileExists", err)
	}
	if !strings.Contains(err.Error(), "--save") {
		t.Errorf("error %q should tell the user how to pick another path", err)
	}
}

func TestLoadRejectsForeignDocuments(t *testing.T) {
	dir := t.TempDir()

	otherKind := filepath.Join(dir, "other.yaml")
	if err := os.WriteFile(otherKind, []byte("format_version: 1\nkind: something_else\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, err := Load(otherKind); !errors.Is(err, ErrInvalidPlanDocument) {
		t.Errorf("Load(other kind) error = %v, want ErrInvalidPlanDocument", err)
	}

	otherVersion := filepath.Join(dir, "future.yaml")
	if err := os.WriteFile(otherVersion, []byte("format_version: 99\nkind: docker_migration_plan\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, err := Load(otherVersion); !errors.Is(err, ErrUnsupportedPlanVersion) {
		t.Errorf("Load(future version) error = %v, want ErrUnsupportedPlanVersion", err)
	}

	if _, err := Load(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Error("Load of a missing file must fail")
	}
}

func TestDefaultPlanPathLivesUnderTheConfigDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("OPSPULSE_HOME", home)

	path := DefaultPlanPath("vps-a", "blog-db", time.Date(2026, 10, 15, 2, 0, 0, 0, time.UTC))
	wantDir := filepath.Join(home, PlanDirName)
	if filepath.Dir(path) != wantDir {
		t.Errorf("plan directory = %q, want %q", filepath.Dir(path), wantDir)
	}
	if filepath.Base(path) != "20261015T020000Z-vps-a-blog-db.yaml" {
		t.Errorf("plan file = %q, want a timestamped, server-scoped name", filepath.Base(path))
	}
}
