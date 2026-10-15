package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/volcano6/opspulse/internal/capability"
	"github.com/volcano6/opspulse/internal/docker"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/server"
)

// fakeDockerRemote is the single seam double used by the CLI tests: it stands in
// for capability.Remote so no test ever contacts a real host.
type fakeDockerRemote struct {
	containers []docker.ContainerSummary
	info       *docker.ContainerInfo
	report     *capability.Report

	listErr    error
	inspectErr error
	probeErr   error

	listAll    bool
	probePaths []string
}

func (f *fakeDockerRemote) ListContainers(_ context.Context, _ executor.Target, includeStopped bool) ([]docker.ContainerSummary, error) {
	f.listAll = includeStopped
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.containers, nil
}

func (f *fakeDockerRemote) InspectContainer(_ context.Context, _ executor.Target, name string) (*docker.ContainerInfo, error) {
	if f.inspectErr != nil {
		return nil, f.inspectErr
	}
	if f.info == nil {
		return nil, fmt.Errorf("no inspect fixture for %q", name)
	}
	return f.info, nil
}

func (f *fakeDockerRemote) Probe(_ context.Context, _ executor.Target, opts capability.ProbeOptions) (*capability.Report, error) {
	f.probePaths = opts.Paths
	if f.probeErr != nil {
		return nil, f.probeErr
	}
	if f.report == nil {
		return &capability.Report{Reachable: true}, nil
	}
	return f.report, nil
}

func installFakeDockerRemote(t *testing.T, fake *fakeDockerRemote) {
	t.Helper()
	prev := newDockerRemote
	newDockerRemote = func(executor.Target) capability.Remote { return fake }
	t.Cleanup(func() { newDockerRemote = prev })
}

func seedDockerServer(t *testing.T, name string) {
	t.Helper()
	setTestHome(t, t.TempDir())
	store := server.NewDefaultStore()
	if err := store.Save(server.Server{Name: name, Host: "198.51.100.10", Port: 22, User: "root"}); err != nil {
		t.Fatalf("seed server %q: %v", name, err)
	}
}

func resetDockerFlags(t *testing.T) {
	t.Helper()
	reset := func() { dockerListAll, dockerListJSON, dockerInspectJSON = false, false, false }
	reset()
	t.Cleanup(reset)
}

func runOps(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var execErr error
	out := captureStdout(t, func() {
		rootCmd.SetArgs(args)
		execErr = rootCmd.Execute()
	})
	rootCmd.SetArgs(nil)
	return out, execErr
}

func healthyReport() *capability.Report {
	return &capability.Report{
		Server:      "vps-01",
		Reachable:   true,
		OS:          "Linux",
		Docker:      capability.Check{Name: "docker", Status: capability.StatusOK},
		Compose:     capability.Check{Name: "compose", Status: capability.StatusOK},
		Restic:      capability.Check{Name: "restic", Status: capability.StatusMissing},
		HelperImage: capability.Check{Name: "helper_image", Status: capability.StatusOK},
		Registry:    capability.Check{Name: "registry", Status: capability.StatusOK},
		Disk:        capability.DiskSpace{Status: capability.StatusOK, Detail: "已用 20%，可用 30.0 GB"},
		Notes:       []string{"远端未安装 restic：备份与还原无法执行。"},
	}
}

func TestDockerListHumanOutput(t *testing.T) {
	seedDockerServer(t, "vps-01")
	resetDockerFlags(t)
	fake := &fakeDockerRemote{containers: []docker.ContainerSummary{
		{
			Name:   "blog-web",
			Image:  "nginx:alpine",
			Status: "Up 2 days",
			Ports:  "0.0.0.0:80->80/tcp",
			Labels: map[string]string{docker.LabelComposeProject: "blog", docker.LabelComposeService: "web"},
		},
		{Name: "wild-db", Image: "mysql:8.0", Status: "Up 1 hour", Ports: "3306/tcp"},
	}}
	installFakeDockerRemote(t, fake)

	out, err := runOps(t, "docker", "list", "vps-01")
	if err != nil {
		t.Fatalf("docker list error: %v", err)
	}
	if !strings.Contains(out, "blog-web") || !strings.Contains(out, "compose:blog") {
		t.Errorf("list output should mark the Compose project:\n%s", out)
	}
	if !strings.Contains(out, "wild-db") || !strings.Contains(out, "mysql") {
		t.Errorf("list output should mark the plain mysql container:\n%s", out)
	}
	if fake.listAll {
		t.Error("docker list without --all must not request stopped containers")
	}
}

func TestDockerListAllFlagReachesSeam(t *testing.T) {
	seedDockerServer(t, "vps-01")
	resetDockerFlags(t)
	fake := &fakeDockerRemote{}
	installFakeDockerRemote(t, fake)

	out, err := runOps(t, "docker", "list", "vps-01", "--all")
	if err != nil {
		t.Fatalf("docker list --all error: %v", err)
	}
	if !fake.listAll {
		t.Error("--all should be forwarded to the seam")
	}
	if !strings.Contains(out, "没有容器") {
		t.Errorf("empty host output = %q, want a 'no containers' note", out)
	}
}

func TestDockerListJSONOutput(t *testing.T) {
	seedDockerServer(t, "vps-01")
	resetDockerFlags(t)
	fake := &fakeDockerRemote{containers: []docker.ContainerSummary{
		{Name: "wild-db", Image: "mysql:8.0", State: "running", Status: "Up 1 hour", Ports: "3306/tcp"},
	}}
	installFakeDockerRemote(t, fake)

	out, err := runOps(t, "docker", "list", "vps-01", "--json")
	if err != nil {
		t.Fatalf("docker list --json error: %v", err)
	}

	var doc dockerListDocument
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("docker list --json output is not valid JSON: %v\n%s", err, out)
	}
	if doc.Server != "vps-01" || len(doc.Containers) != 1 {
		t.Fatalf("doc = %+v, want one container on vps-01", doc)
	}
	if doc.Containers[0].Mode != "run" || doc.Containers[0].Database != "mysql" {
		t.Errorf("container view = %+v, want run/mysql", doc.Containers[0])
	}
}

func TestDockerInspectHumanRedactsEnvValues(t *testing.T) {
	seedDockerServer(t, "vps-01")
	resetDockerFlags(t)
	fake := &fakeDockerRemote{
		info: &docker.ContainerInfo{
			Name:          "blog-db",
			ID:            "1a2b3c4d5e6f",
			Image:         "mysql:8.0",
			RestartPolicy: "unless-stopped",
			Env:           []string{"PATH=/usr/bin", "MYSQL_ROOT_PASSWORD=supersecret"},
			Mounts:        []docker.VolumeMount{{Type: "bind", Source: "/srv/blog/mysql", Destination: "/var/lib/mysql"}},
		},
		report: healthyReport(),
	}
	installFakeDockerRemote(t, fake)

	out, err := runOps(t, "docker", "inspect", "vps-01", "blog-db")
	if err != nil {
		t.Fatalf("docker inspect error: %v", err)
	}
	if strings.Contains(out, "supersecret") {
		t.Errorf("inspect output leaked an environment value:\n%s", out)
	}
	if !strings.Contains(out, "MYSQL_ROOT_PASSWORD") {
		t.Errorf("inspect output should name the sensitive variable:\n%s", out)
	}
	if !strings.Contains(out, "logical_dump") {
		t.Errorf("inspect output should describe the database dump strategy:\n%s", out)
	}
	if !strings.Contains(out, "restic") {
		t.Errorf("inspect output should surface the missing restic prerequisite:\n%s", out)
	}

	if len(fake.probePaths) != 1 || fake.probePaths[0] != "/srv/blog/mysql" {
		t.Errorf("probe paths = %v, want the container bind-mount source", fake.probePaths)
	}
}

func TestDockerInspectJSONRedactsEnvValues(t *testing.T) {
	seedDockerServer(t, "vps-01")
	resetDockerFlags(t)
	fake := &fakeDockerRemote{
		info: &docker.ContainerInfo{
			Name:  "blog-db",
			ID:    "1a2b3c4d5e6f",
			Image: "mysql:8.0",
			Env:   []string{"MYSQL_ROOT_PASSWORD=supersecret"},
		},
		report: healthyReport(),
	}
	installFakeDockerRemote(t, fake)

	out, err := runOps(t, "docker", "inspect", "vps-01", "blog-db", "--json")
	if err != nil {
		t.Fatalf("docker inspect --json error: %v", err)
	}
	if strings.Contains(out, "supersecret") {
		t.Errorf("inspect --json leaked an environment value:\n%s", out)
	}

	var doc dockerInspectDocument
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("docker inspect --json output is not valid JSON: %v\n%s", err, out)
	}
	if doc.Server != "vps-01" || doc.Container.Name != "blog-db" {
		t.Fatalf("doc = %+v, want blog-db on vps-01", doc)
	}
	if len(doc.Container.EnvKeys) != 1 || doc.Container.EnvKeys[0] != "MYSQL_ROOT_PASSWORD" {
		t.Errorf("EnvKeys = %v, want only the variable name", doc.Container.EnvKeys)
	}
	if doc.Analysis == nil || doc.Analysis.Database == nil {
		t.Errorf("analysis = %+v, want a database verdict", doc.Analysis)
	}
	if doc.Capability == nil || doc.Capability.Restic.Status != capability.StatusMissing {
		t.Errorf("capability = %+v, want the restic gap", doc.Capability)
	}
}

func TestDockerInspectMissingDockerIsActionable(t *testing.T) {
	seedDockerServer(t, "vps-01")
	resetDockerFlags(t)
	installFakeDockerRemote(t, &fakeDockerRemote{inspectErr: capability.ErrDockerMissing})

	_, err := runOps(t, "docker", "inspect", "vps-01", "blog-db")
	if err == nil {
		t.Fatal("docker inspect should fail when Docker is missing")
	}
	if !strings.Contains(err.Error(), "未安装 Docker") {
		t.Errorf("error = %v, want an actionable Docker-missing message", err)
	}
}

func TestDockerInspectUnknownServer(t *testing.T) {
	seedDockerServer(t, "vps-01")
	resetDockerFlags(t)
	installFakeDockerRemote(t, &fakeDockerRemote{})

	_, err := runOps(t, "docker", "inspect", "ghost", "blog-db")
	if err == nil {
		t.Fatal("docker inspect against an unknown server should fail before any remote call")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error = %v, want it to name the unknown server", err)
	}
}
