package capability

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/volcano6/opspulse/internal/docker"
	"github.com/volcano6/opspulse/internal/executor"
)

// fakeExecutor is the test double for the shared executor. It returns canned
// output and records the script it was asked to run, so tests can assert on the
// generated (read-only) remote commands without a real host.
type fakeExecutor struct {
	out    string
	err    error
	script string
	task   string
}

func (f *fakeExecutor) Execute(_ context.Context, target executor.Target, taskName, script string, w io.Writer) (*executor.Result, error) {
	f.script = script
	f.task = taskName
	if w != nil && f.out != "" {
		_, _ = io.WriteString(w, f.out)
	}
	res := &executor.Result{ServerName: target.Name, Template: taskName, Success: f.err == nil}
	if f.err != nil {
		res.ExitCode = 1
		res.Error = f.err
	}
	return res, f.err
}

func (f *fakeExecutor) Test(_ context.Context, _ executor.Target) (time.Duration, string, error) {
	return time.Millisecond, "Linux", nil
}

func testTarget() executor.Target {
	return executor.Target{Name: "vps-01"}
}

// assertReadOnlyScript fails when a generated remote script contains a command
// that would mutate the target host. This is the automated guard for the
// ticket's "analysis never dumps, archives, backs up or starts Compose" rule.
func assertReadOnlyScript(t *testing.T, name, script string) {
	t.Helper()
	mutating := []string{
		"docker run",
		"docker rm",
		"docker rmi",
		"docker volume rm",
		"docker volume create",
		"docker compose up",
		"docker-compose up",
		"restic backup",
		"restic restore",
		"docker exec",
		"rm -rf",
		"mkfs",
	}
	for _, needle := range mutating {
		if strings.Contains(script, needle) {
			t.Errorf("%s contains a mutating command %q:\n%s", name, needle, script)
		}
	}
}

func TestBuildProbeScriptIsReadOnly(t *testing.T) {
	script, err := BuildProbeScript(ProbeOptions{Paths: []string{"/opt/blog", "/srv/blog/mysql"}})
	if err != nil {
		t.Fatalf("BuildProbeScript() error: %v", err)
	}
	assertReadOnlyScript(t, "probe script", script)

	for _, want := range []string{
		"command -v docker",
		"docker info",
		"docker image inspect",
		"docker manifest inspect",
		"command -v restic",
		"df -kP /",
		"/opt/blog",
		"/srv/blog/mysql",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("probe script missing %q:\n%s", want, script)
		}
	}
}

func TestBuildProbeScriptRejectsPipePath(t *testing.T) {
	if _, err := BuildProbeScript(ProbeOptions{Paths: []string{"/opt/a|b"}}); err == nil {
		t.Fatal("BuildProbeScript() should reject a path containing '|'")
	}
}

func TestBuildListAndInspectScriptsAreReadOnly(t *testing.T) {
	list := BuildListScript(true)
	assertReadOnlyScript(t, "list script", list)
	if !strings.Contains(list, "docker ps -a --format '{{json .}}'") {
		t.Errorf("list script = %q, want docker ps -a with json format", list)
	}

	inspect := BuildInspectScript("blog-db")
	assertReadOnlyScript(t, "inspect script", inspect)
	if !strings.Contains(inspect, "docker inspect --type container 'blog-db'") {
		t.Errorf("inspect script = %q, want a quoted docker inspect", inspect)
	}
	if !strings.Contains(inspect, dockerMissingSentinel) {
		t.Errorf("inspect script = %q, want the docker-missing sentinel", inspect)
	}
}

func TestParseProbeOutputHealthyHost(t *testing.T) {
	raw := strings.Join([]string{
		"---OPSPULSE-CAPABILITY---",
		"OS=Linux",
		"ARCH=x86_64",
		"DOCKER=present",
		"DOCKER_DAEMON=running",
		"COMPOSE=plugin",
		"RESTIC=present",
		"TOOL_tar=present",
		"TOOL_gzip=present",
		"TOOL_gunzip=present",
		"TOOL_base64=present",
		"DBTOOL_mysqldump=present",
		"DBTOOL_mysql=present",
		"DBTOOL_mariadb-dump=missing",
		"DBTOOL_pg_dump=missing",
		"DBTOOL_pg_dumpall=missing",
		"DBTOOL_psql=missing",
		"HELPER_IMAGE=present",
		"REGISTRY=reachable",
		"DISK=41943040 10485760 31457280 25%",
		"PATH=/opt/blog|yes|yes",
		"---OPSPULSE-CAPABILITY-END---",
	}, "\n")

	report := ParseProbeOutput("vps-01", raw)
	if !report.Reachable || report.Server != "vps-01" {
		t.Fatalf("report = %+v, want a reachable vps-01", report)
	}
	if report.Docker.Status != StatusOK || report.Compose.Status != StatusOK {
		t.Errorf("docker/compose = %s/%s, want ok/ok", report.Docker.Status, report.Compose.Status)
	}
	if report.Disk.Status != StatusOK || report.Disk.UsedPct != 25 {
		t.Errorf("disk = %+v, want ok at 25%%", report.Disk)
	}
	if len(report.Paths) != 1 || report.Paths[0].Status != StatusOK {
		t.Errorf("paths = %+v, want a single ok path", report.Paths)
	}
	if len(report.Notes) != 0 {
		t.Errorf("notes = %v, want none on a healthy host", report.Notes)
	}
}

func TestParseProbeOutputMissingPrerequisites(t *testing.T) {
	raw := strings.Join([]string{
		"DOCKER=present",
		"DOCKER_DAEMON=stopped",
		"COMPOSE=missing",
		"RESTIC=missing",
		"TOOL_tar=present",
		"TOOL_gzip=present",
		"TOOL_gunzip=present",
		"TOOL_base64=present",
		"HELPER_IMAGE=missing",
		"REGISTRY=unreachable",
		"DISK=41943040 40894464 1048576 98%",
		"PATH=/opt/blog|no|no",
	}, "\n")

	report := ParseProbeOutput("vps-02", raw)
	if report.Docker.Status != StatusPartial {
		t.Errorf("docker = %s, want partial for a stopped daemon", report.Docker.Status)
	}
	if report.Compose.Status != StatusMissing || report.Restic.Status != StatusMissing {
		t.Errorf("compose/restic = %s/%s, want missing/missing", report.Compose.Status, report.Restic.Status)
	}
	if report.HelperImage.Status != StatusMissing {
		t.Errorf("helper image = %s, want missing", report.HelperImage.Status)
	}
	if report.Registry.Status != StatusMissing {
		t.Errorf("registry = %s, want missing", report.Registry.Status)
	}
	if report.Disk.Status != StatusMissing {
		t.Errorf("disk = %+v, want missing at 98%% used", report.Disk)
	}
	if report.Paths[0].Status != StatusMissing {
		t.Errorf("paths = %+v, want missing", report.Paths)
	}

	hints := strings.Join(report.Notes, "\n")
	for _, want := range []string{"daemon", "compose", "restic", "helper", "镜像仓库", "磁盘"} {
		if !strings.Contains(hints, want) {
			t.Errorf("hints missing %q:\n%s", want, hints)
		}
	}
}

func TestParseProbeOutputUnknownHost(t *testing.T) {
	report := ParseProbeOutput("vps-03", "garbage without keys\n")
	if report.Docker.Status != StatusUnknown || report.Compose.Status != StatusUnknown {
		t.Errorf("docker/compose = %s/%s, want unknown/unknown", report.Docker.Status, report.Compose.Status)
	}
	if report.Disk.Status != StatusUnknown {
		t.Errorf("disk = %s, want unknown", report.Disk.Status)
	}
}

func TestRemoteProberListContainers(t *testing.T) {
	exec := &fakeExecutor{out: `{"ID":"abc","Names":"web","Image":"nginx:alpine","State":"running","Status":"Up 1 day","Ports":"0.0.0.0:80->80/tcp","Labels":"com.docker.compose.project=blog"}` + "\n"}
	prober := NewRemote(exec)

	containers, err := prober.ListContainers(context.Background(), testTarget(), false)
	if err != nil {
		t.Fatalf("ListContainers() error: %v", err)
	}
	if len(containers) != 1 || !containers[0].IsCompose() {
		t.Fatalf("containers = %+v, want one Compose container", containers)
	}
	assertReadOnlyScript(t, "list script", exec.script)
}

func TestRemoteProberListContainersDockerMissing(t *testing.T) {
	prober := NewRemote(&fakeExecutor{out: dockerMissingSentinel + "\n", err: errors.New("exit 127")})
	_, err := prober.ListContainers(context.Background(), testTarget(), false)
	if !errors.Is(err, ErrDockerMissing) {
		t.Fatalf("ListContainers() error = %v, want ErrDockerMissing", err)
	}
}

func TestRemoteProberInspectContainer(t *testing.T) {
	exec := &fakeExecutor{out: `[{"Id":"abc","Name":"/web","Config":{"Image":"nginx:alpine","Labels":{}},"HostConfig":{},"Mounts":[]}]`}
	prober := NewRemote(exec)

	info, err := prober.InspectContainer(context.Background(), testTarget(), "web")
	if err != nil {
		t.Fatalf("InspectContainer() error: %v", err)
	}
	if info.Name != "web" || info.Image != "nginx:alpine" {
		t.Errorf("info = %+v, want web/nginx:alpine", info)
	}
	assertReadOnlyScript(t, "inspect script", exec.script)
}

func TestRemoteProberInspectContainerNotFound(t *testing.T) {
	prober := NewRemote(&fakeExecutor{out: "Error: No such object: ghost\n", err: errors.New("exit 1")})
	_, err := prober.InspectContainer(context.Background(), testTarget(), "ghost")
	if !errors.Is(err, docker.ErrContainerNotFound) {
		t.Fatalf("InspectContainer() error = %v, want ErrContainerNotFound", err)
	}
}

func TestRemoteProberProbeParsesOutput(t *testing.T) {
	exec := &fakeExecutor{out: "DOCKER=present\nDOCKER_DAEMON=running\nCOMPOSE=plugin\nRESTIC=present\nDISK=1024 512 512 50%\n"}
	prober := NewRemote(exec)

	report, err := prober.Probe(context.Background(), testTarget(), ProbeOptions{Paths: []string{"/opt/blog"}})
	if err != nil {
		t.Fatalf("Probe() error: %v", err)
	}
	if report.Docker.Status != StatusOK {
		t.Errorf("docker = %s, want ok", report.Docker.Status)
	}
	if !strings.Contains(exec.script, "/opt/blog") {
		t.Errorf("probe script did not include the requested path:\n%s", exec.script)
	}
	assertReadOnlyScript(t, "probe script", exec.script)
}
