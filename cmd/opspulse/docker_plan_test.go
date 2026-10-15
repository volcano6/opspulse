package main

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/volcano6/opspulse/internal/backup"
	"github.com/volcano6/opspulse/internal/capability"
	"github.com/volcano6/opspulse/internal/docker"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/migration"
	"github.com/volcano6/opspulse/internal/server"
)

// planFakeRemote is the capability.Remote double for the plan tests. It is
// keyed by target name so a single fake can serve both the source and the
// target endpoint without ever contacting a host.
type planFakeRemote struct {
	containers map[string][]docker.ContainerSummary
	infos      map[string]*docker.ContainerInfo
	reports    map[string]*capability.Report

	listErr    error
	inspectErr error
	probeErr   error

	listCalls    int
	inspectCalls int
	probePaths   map[string][]string
}

func (f *planFakeRemote) ListContainers(_ context.Context, target executor.Target, _ bool) ([]docker.ContainerSummary, error) {
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.containers[target.Name], nil
}

func (f *planFakeRemote) InspectContainer(_ context.Context, _ executor.Target, name string) (*docker.ContainerInfo, error) {
	f.inspectCalls++
	if f.inspectErr != nil {
		return nil, f.inspectErr
	}
	info, ok := f.infos[name]
	if !ok {
		return nil, docker.ErrContainerNotFound
	}
	return info, nil
}

func (f *planFakeRemote) Probe(_ context.Context, target executor.Target, opts capability.ProbeOptions) (*capability.Report, error) {
	if f.probePaths == nil {
		f.probePaths = map[string][]string{}
	}
	f.probePaths[target.Name] = opts.Paths
	if f.probeErr != nil {
		return nil, f.probeErr
	}
	if report, ok := f.reports[target.Name]; ok {
		return report, nil
	}
	return &capability.Report{Server: target.Name, Reachable: true}, nil
}

func installPlanRemote(t *testing.T, fake *planFakeRemote) {
	t.Helper()
	prev := newDockerRemote
	newDockerRemote = func(executor.Target) capability.Remote { return fake }
	t.Cleanup(func() { newDockerRemote = prev })
}

// resetDockerPlanFlagValues clears the package-level flag variables. They are
// shared by every run in one process, so a test that invokes the command twice
// (refuse, then accept) has to start each invocation from the defaults - which
// is what a fresh `ops` process does in real life.
func resetDockerPlanFlagValues() {
	dockerPlanAll = false
	dockerPlanTarget = ""
	dockerPlanSave = ""
	dockerPlanNoSave = false
	dockerPlanJSON = false
	dockerPlanWriteConfig = false
	dockerPlanYes = false
	dockerPlanShowJSON = false
}

func resetDockerPlanFlags(t *testing.T) {
	t.Helper()
	resetDockerPlanFlagValues()
	t.Cleanup(resetDockerPlanFlagValues)
}

// runPlanOps runs a command with a non-terminal stdin, so the confirmation
// gate behaves the way it does in a script instead of depending on whatever
// stdin the test binary inherited.
//
// stdout is captured through a temporary file rather than the shared
// captureStdout helper: that helper redirects os.Stdout to a pipe and only
// drains it after the command returns, so a plan JSON document larger than the
// pipe buffer would deadlock the test instead of failing it.
func runPlanOps(t *testing.T, args ...string) (string, error) {
	t.Helper()
	resetDockerPlanFlagValues()
	prevIn := rootCmd.InOrStdin()
	prevOutWriter := rootCmd.OutOrStdout()
	prevErrWriter := rootCmd.ErrOrStderr()
	rootCmd.SetIn(strings.NewReader(""))

	file, err := os.CreateTemp(t.TempDir(), "stdout-*.txt")
	if err != nil {
		t.Fatalf("create stdout capture file: %v", err)
	}
	// Both the process streams and the cobra writers have to point at the file:
	// the command renders to os.Stdout and asks cobra for its output stream, and
	// another test in this package may have pinned cobra's writer already.
	rootCmd.SetOut(file)
	rootCmd.SetErr(file)
	prevOutFile := os.Stdout
	os.Stdout = file
	var execErr error
	rootCmd.SetArgs(args)
	execErr = rootCmd.Execute()
	rootCmd.SetArgs(nil)
	os.Stdout = prevOutFile
	rootCmd.SetOut(prevOutWriter)
	rootCmd.SetErr(prevErrWriter)
	rootCmd.SetIn(prevIn)

	if closeErr := file.Close(); closeErr != nil {
		t.Fatalf("close stdout capture file: %v", closeErr)
	}
	data, readErr := os.ReadFile(file.Name())
	if readErr != nil {
		t.Fatalf("read captured stdout: %v", readErr)
	}
	return string(data), execErr
}

func seedPlanServers(t *testing.T, names ...string) string {
	t.Helper()
	home := t.TempDir()
	setTestHome(t, home)
	store := server.NewDefaultStore()
	for _, name := range names {
		if err := store.Save(server.Server{Name: name, Host: "198.51.100.10", Port: 22, User: "root"}); err != nil {
			t.Fatalf("seed server %q: %v", name, err)
		}
	}
	return home
}

func planReport(server string) *capability.Report {
	return &capability.Report{
		Server:      server,
		Reachable:   true,
		OS:          "Linux",
		Docker:      capability.Check{Name: "docker", Status: capability.StatusOK},
		Compose:     capability.Check{Name: "compose", Status: capability.StatusOK},
		Restic:      capability.Check{Name: "restic", Status: capability.StatusOK},
		HelperImage: capability.Check{Name: "helper_image", Status: capability.StatusOK},
		Registry:    capability.Check{Name: "registry", Status: capability.StatusOK},
		Disk:        capability.DiskSpace{Status: capability.StatusOK, Detail: "已用 20%，可用 40.0 GB"},
	}
}

func planContainerInfo() *docker.ContainerInfo {
	return &docker.ContainerInfo{
		ID:            "1a2b3c4d5e6f",
		Name:          "blog-db",
		Image:         "mysql:8.0",
		RestartPolicy: "unless-stopped",
		Env:           []string{"PATH=/usr/bin", "MYSQL_ROOT_PASSWORD=supersecret"},
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

func newPlanFake(servers ...string) *planFakeRemote {
	fake := &planFakeRemote{
		containers: map[string][]docker.ContainerSummary{},
		infos:      map[string]*docker.ContainerInfo{"blog-db": planContainerInfo()},
		reports:    map[string]*capability.Report{},
	}
	for _, name := range servers {
		fake.reports[name] = planReport(name)
	}
	return fake
}

// listTree returns every file under root as a slash-separated relative path.
// Lock files are skipped: filelock leaves "<name>.lock" next to every config it
// guards, and that is an implementation detail, not an artifact of the command
// under test.
func listTree(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(d.Name(), ".lock") {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(files)
	return files
}

func TestDockerPlanSingleContainerIsReadOnly(t *testing.T) {
	home := seedPlanServers(t, "vps-a")
	resetDockerPlanFlags(t)
	resetDockerFlags(t)
	fake := newPlanFake("vps-a")
	installPlanRemote(t, fake)

	out, err := runPlanOps(t, "docker", "plan", "vps-a", "blog-db", "--no-save")
	if err != nil {
		t.Fatalf("docker plan error: %v", err)
	}

	for _, want := range []string{"迁移 plan", "blog-db", "mysql:8.0", "目标路径", "目标端口", "一致性", "风险汇总"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan output is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "supersecret") {
		t.Errorf("plan output leaked an environment value:\n%s", out)
	}
	if !strings.Contains(out, "MYSQL_ROOT_PASSWORD") {
		t.Errorf("plan output should name the sensitive variable:\n%s", out)
	}

	// --no-save is a pure read-only run: no plan file, no backup config.
	if files := listTree(t, home); len(files) != 1 || files[0] != "servers.yaml" {
		t.Errorf("files under the config home = %v, want only servers.yaml", files)
	}
	if fake.listCalls != 0 {
		t.Errorf("list calls = %d, want 0 for a single-container plan", fake.listCalls)
	}
	if fake.inspectCalls != 1 {
		t.Errorf("inspect calls = %d, want 1", fake.inspectCalls)
	}
	if got := fake.probePaths["vps-a"]; len(got) != 2 || got[0] != "/opt/blog" || got[1] != "/srv/blog/conf.d" {
		t.Errorf("probe paths = %v, want the Compose directory and the bind-mount source", got)
	}
}

func TestDockerPlanSavesByDefaultWithoutTouchingBackupConfig(t *testing.T) {
	home := seedPlanServers(t, "vps-a")
	resetDockerPlanFlags(t)
	resetDockerFlags(t)
	installPlanRemote(t, newPlanFake("vps-a"))

	out, err := runPlanOps(t, "docker", "plan", "vps-a", "blog-db")
	if err != nil {
		t.Fatalf("docker plan error: %v", err)
	}
	if !strings.Contains(out, "plan 已保存: ") {
		t.Fatalf("plan output should print the saved path:\n%s", out)
	}

	files := listTree(t, home)
	if len(files) != 2 {
		t.Fatalf("files under the config home = %v, want servers.yaml plus one plan", files)
	}
	planFile := ""
	for _, file := range files {
		if file != "servers.yaml" {
			planFile = file
		}
	}
	if !strings.HasPrefix(planFile, "plans/") || !strings.HasSuffix(planFile, "-vps-a-blog-db.yaml") {
		t.Errorf("plan file = %q, want plans/<timestamp>-vps-a-blog-db.yaml", planFile)
	}
	if _, statErr := os.Stat(filepath.Join(home, "backups.yaml")); statErr == nil {
		t.Error("plan mode must not create backups.yaml")
	}
}

func TestDockerPlanRequiresExplicitSelection(t *testing.T) {
	seedPlanServers(t, "vps-a")
	resetDockerPlanFlags(t)
	resetDockerFlags(t)
	installPlanRemote(t, newPlanFake("vps-a"))

	_, err := runPlanOps(t, "docker", "plan", "vps-a", "--no-save")
	if err == nil || !strings.Contains(err.Error(), "--all") {
		t.Errorf("error = %v, want a hint that --all is required for a batch", err)
	}

	_, err = runPlanOps(t, "docker", "plan", "vps-a", "blog-db", "--all", "--no-save")
	if err == nil || !strings.Contains(err.Error(), "--all") {
		t.Errorf("error = %v, want a conflict between the container argument and --all", err)
	}
}

func TestDockerPlanAllCoversEveryRunningContainer(t *testing.T) {
	seedPlanServers(t, "vps-a")
	resetDockerPlanFlags(t)
	resetDockerFlags(t)

	fake := newPlanFake("vps-a")
	fake.containers["vps-a"] = []docker.ContainerSummary{
		{Name: "blog-db", Image: "mysql:8.0", Labels: map[string]string{docker.LabelComposeProject: "blog", docker.LabelComposeService: "db"}},
		{Name: "blog-web", Image: "nginx:alpine", Labels: map[string]string{docker.LabelComposeProject: "blog", docker.LabelComposeService: "web"}},
	}
	web := planContainerInfo()
	web.Name = "blog-web"
	web.Image = "nginx:alpine"
	web.Env = []string{"PATH=/usr/bin"}
	web.Labels[docker.LabelComposeService] = "web"
	fake.infos["blog-web"] = web
	installPlanRemote(t, fake)

	out, err := runPlanOps(t, "docker", "plan", "vps-a", "--all", "--no-save", "--json")
	if err != nil {
		t.Fatalf("docker plan --all error: %v", err)
	}

	var plan migration.Plan
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("plan --json is not valid JSON: %v\n%s", err, out)
	}
	if len(plan.Services) != 2 {
		t.Fatalf("services = %d, want both running containers", len(plan.Services))
	}
	if plan.FormatVersion != migration.PlanFormatVersion || plan.Kind != migration.PlanKind {
		t.Errorf("plan header = %d/%q, want the versioned document", plan.FormatVersion, plan.Kind)
	}
	names := []string{plan.Services[0].Name, plan.Services[1].Name}
	sort.Strings(names)
	if names[0] != "db" || names[1] != "web" {
		t.Errorf("service names = %v, want the Compose service names db/web", names)
	}
}

func TestDockerPlanSavesAndReloads(t *testing.T) {
	home := seedPlanServers(t, "vps-a")
	resetDockerPlanFlags(t)
	resetDockerFlags(t)
	installPlanRemote(t, newPlanFake("vps-a"))

	path := filepath.Join(home, "migration-plan.yaml")
	out, err := runPlanOps(t, "docker", "plan", "vps-a", "blog-db", "--save", path)
	if err != nil {
		t.Fatalf("docker plan --save error: %v", err)
	}
	if !strings.Contains(out, "plan 已保存: "+path) {
		t.Errorf("plan output should name the saved file:\n%s", out)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved plan: %v", err)
	}
	if !strings.Contains(string(raw), "format_version: 1") {
		t.Errorf("saved plan lacks a format version:\n%s", raw)
	}

	// Reloading a saved plan is a pure file read: no server call at all.
	installPlanRemote(t, &planFakeRemote{infos: map[string]*docker.ContainerInfo{}})
	showOut, err := runPlanOps(t, "docker", "plan", "show", path)
	if err != nil {
		t.Fatalf("docker plan show error: %v", err)
	}
	for _, want := range []string{"迁移 plan", "blog-db", "目标路径"} {
		if !strings.Contains(showOut, want) {
			t.Errorf("plan show output is missing %q:\n%s", want, showOut)
		}
	}

	jsonOut, err := runPlanOps(t, "docker", "plan", "show", path, "--json")
	if err != nil {
		t.Fatalf("docker plan show --json error: %v", err)
	}
	var reloaded migration.Plan
	if err := json.Unmarshal([]byte(jsonOut), &reloaded); err != nil {
		t.Fatalf("plan show --json is not valid JSON: %v\n%s", err, jsonOut)
	}
	if len(reloaded.Services) != 1 || reloaded.Services[0].Container != "blog-db" {
		t.Errorf("reloaded services = %+v, want blog-db", reloaded.Services)
	}
}

func TestDockerPlanRefusesToOverwriteSavedPlan(t *testing.T) {
	home := seedPlanServers(t, "vps-a")
	resetDockerPlanFlags(t)
	resetDockerFlags(t)
	installPlanRemote(t, newPlanFake("vps-a"))

	path := filepath.Join(home, "migration-plan.yaml")
	if err := os.WriteFile(path, []byte("keep me"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	_, err := runPlanOps(t, "docker", "plan", "vps-a", "blog-db", "--save", path)
	if err == nil || !strings.Contains(err.Error(), "拒绝覆盖") {
		t.Fatalf("error = %v, want an explicit refusal to overwrite", err)
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read fixture: %v", readErr)
	}
	if string(raw) != "keep me" {
		t.Errorf("existing plan file was modified: %q", raw)
	}
}

func TestDockerPlanClassifiesTargetBlockersAndWarnings(t *testing.T) {
	seedPlanServers(t, "vps-a", "vps-b")
	resetDockerPlanFlags(t)
	resetDockerFlags(t)

	fake := newPlanFake("vps-a")
	broken := planReport("vps-b")
	broken.Compose = capability.Check{Name: "compose", Status: capability.StatusMissing}
	broken.Registry = capability.Check{Name: "registry", Status: capability.StatusMissing}
	broken.Paths = []capability.Check{{Name: "/srv/blog/conf.d", Status: capability.StatusMissing, Detail: "目录不存在且父目录不可写"}}
	fake.reports["vps-b"] = broken
	installPlanRemote(t, fake)

	out, err := runPlanOps(t, "docker", "plan", "vps-a", "blog-db", "--target", "vps-b", "--no-save", "--json")
	if err != nil {
		t.Fatalf("docker plan --target error: %v", err)
	}

	var plan migration.Plan
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("plan --json is not valid JSON: %v\n%s", err, out)
	}
	if plan.Target.Server != "vps-b" || plan.Target.Capability == nil {
		t.Fatalf("target endpoint = %+v, want vps-b with its capability report", plan.Target)
	}

	blockers := map[string]bool{}
	for _, risk := range plan.Risks.Blockers {
		blockers[risk.Code] = true
	}
	for _, want := range []string{migration.RiskTargetComposeMissing, migration.RiskTargetPathUnavailable} {
		if !blockers[want] {
			t.Errorf("blockers = %v, want %s", blockers, want)
		}
	}

	warnings := map[string]bool{}
	for _, risk := range plan.Risks.Warnings {
		warnings[risk.Code] = true
	}
	for _, want := range []string{migration.RiskSensitiveEnv, migration.RiskExternalBindMount, migration.RiskTargetRegistryUnreachable} {
		if !warnings[want] {
			t.Errorf("warnings = %v, want %s", warnings, want)
		}
	}

	if plan.Services[0].ImageAcquisition != migration.ImagePreloadRequired {
		t.Errorf("image acquisition = %q, want preload_required for an offline target", plan.Services[0].ImageAcquisition)
	}
	if len(plan.Risks.FollowUps) == 0 {
		t.Error("follow-ups are empty, want the manual preparation steps")
	}
	if got := fake.probePaths["vps-b"]; len(got) != 2 {
		t.Errorf("target probe paths = %v, want the same host paths as the source", got)
	}
}

func TestDockerPlanWriteConfigRequiresConfirmation(t *testing.T) {
	home := seedPlanServers(t, "vps-a")
	resetDockerPlanFlags(t)
	resetDockerFlags(t)
	installPlanRemote(t, newPlanFake("vps-a"))

	store := backup.NewDefaultStore()
	if err := store.Save(backup.Job{
		Name:    "existing",
		Server:  "vps-a",
		Paths:   []string{"/srv/data"},
		Backend: "s3:s3.example.com/bucket",
		Env:     map[string]string{"RESTIC_PASSWORD": "s3cret-repo-pw"},
	}); err != nil {
		t.Fatalf("seed backup job: %v", err)
	}

	// Non-interactive without --yes: refuse before writing anything.
	out, err := runPlanOps(t, "docker", "plan", "vps-a", "blog-db", "--write-config", "--no-save")
	if err == nil {
		t.Fatalf("--write-config without --yes must fail in a non-interactive shell:\n%s", out)
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("error = %v, want it to point at --yes", err)
	}
	if !strings.Contains(out, "MYSQL_ROOT_PASSWORD") {
		t.Errorf("the change summary must show the sensitive-environment risk:\n%s", out)
	}
	if !strings.Contains(out, "即将写入备份配置") {
		t.Errorf("the change summary must precede the write:\n%s", out)
	}
	jobs, listErr := store.List()
	if listErr != nil {
		t.Fatalf("list jobs: %v", listErr)
	}
	if len(jobs) != 1 || jobs[0].Name != "existing" {
		t.Fatalf("jobs = %+v, want the seeded job only", jobs)
	}

	// With --yes the confirmed change is written.
	planPath := filepath.Join(home, "migration-plan.yaml")
	out, err = runPlanOps(t, "docker", "plan", "vps-a", "blog-db", "--write-config", "--yes", "--save", planPath)
	if err != nil {
		t.Fatalf("docker plan --write-config --yes error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "✅ 已写入备份配置") {
		t.Errorf("output should confirm the write:\n%s", out)
	}

	jobs, listErr = store.List()
	if listErr != nil {
		t.Fatalf("list jobs: %v", listErr)
	}
	if len(jobs) != 2 {
		t.Fatalf("jobs = %+v, want the seeded job plus the planned one", jobs)
	}
	var planned *backup.Job
	for i := range jobs {
		if jobs[i].Name == "db" {
			planned = &jobs[i]
		}
	}
	if planned == nil {
		t.Fatalf("jobs = %+v, want a job named after the Compose service", jobs)
	}
	if planned.Server != "vps-a" || planned.Backend != "s3:s3.example.com/bucket" {
		t.Errorf("planned job = %+v, want the source server and the inherited backend", planned)
	}
	if planned.Env["RESTIC_PASSWORD"] != "s3cret-repo-pw" {
		t.Errorf("planned job env = %v, want the repository credentials inherited from the existing job", planned.Env)
	}
	if len(planned.Paths) != 2 || planned.Paths[0] != "/opt/blog" {
		t.Errorf("planned paths = %v, want the Compose project directory plus the external bind mount", planned.Paths)
	}
	if _, statErr := os.Stat(filepath.Join(home, "backups.yaml")); statErr != nil {
		t.Errorf("backups.yaml should exist after --write-config --yes: %v", statErr)
	}

	// The reviewable plan must never carry the repository credential.
	rawPlan, readErr := os.ReadFile(planPath)
	if readErr != nil {
		t.Fatalf("read saved plan: %v", readErr)
	}
	if strings.Contains(string(rawPlan), "s3cret-repo-pw") {
		t.Errorf("saved plan leaked the repository credential:\n%s", rawPlan)
	}
}

func TestDockerPlanWriteConfigWithoutRepositoryFails(t *testing.T) {
	seedPlanServers(t, "vps-a")
	resetDockerPlanFlags(t)
	resetDockerFlags(t)
	t.Setenv("RESTIC_REPOSITORY", "")
	installPlanRemote(t, newPlanFake("vps-a"))

	_, err := runPlanOps(t, "docker", "plan", "vps-a", "blog-db", "--write-config", "--yes", "--no-save")
	if err == nil || !strings.Contains(err.Error(), "仓库") {
		t.Fatalf("error = %v, want a refusal that names the missing repository", err)
	}
}

func TestDockerPlanShowRejectsForeignPlanFile(t *testing.T) {
	seedPlanServers(t, "vps-a")
	resetDockerPlanFlags(t)
	resetDockerFlags(t)

	path := filepath.Join(t.TempDir(), "future-plan.yaml")
	if err := os.WriteFile(path, []byte("format_version: 99\nkind: docker_migration_plan\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	_, err := runPlanOps(t, "docker", "plan", "show", path)
	if err == nil || !strings.Contains(err.Error(), "format_version") {
		t.Fatalf("error = %v, want an unsupported-version refusal", err)
	}
}
