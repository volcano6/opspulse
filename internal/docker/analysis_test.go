package docker

import (
	"strings"
	"testing"
)

func findAttribute(attrs []AttributeSupport, name string) (AttributeSupport, bool) {
	for _, a := range attrs {
		if a.Attribute == name {
			return a, true
		}
	}
	return AttributeSupport{}, false
}

func TestAnalyzePlainDatabaseContainer(t *testing.T) {
	info := &ContainerInfo{
		Name:          "wild-db",
		Image:         "mysql:8.0",
		RestartPolicy: "unless-stopped",
		Env:           []string{"PATH=/usr/bin", "MYSQL_ROOT_PASSWORD=supersecret"},
		Ports:         []PortMapping{{HostPort: "3306", ContainerPort: "3306", Protocol: "tcp"}},
		Mounts: []VolumeMount{
			{Type: "bind", Source: "/srv/blog/mysql", Destination: "/var/lib/mysql"},
		},
	}

	a := Analyze(info)
	if a == nil {
		t.Fatal("Analyze() returned nil")
	}
	if a.IsCompose {
		t.Error("a plain container must not be reported as Compose-managed")
	}
	if len(a.NamedVolumes) != 0 {
		t.Errorf("NamedVolumes = %v, want none", a.NamedVolumes)
	}
	if len(a.BindMounts) != 1 {
		t.Fatalf("BindMounts = %v, want the /srv/blog/mysql bind", a.BindMounts)
	}

	if a.Database == nil {
		t.Fatal("Database analysis missing for a mysql image")
	}
	if a.Database.Engine != EngineMySQL || a.Database.Strategy != "logical_dump" {
		t.Errorf("Database = %+v, want a mysql logical dump", a.Database)
	}
	if a.Database.DataDir != "/var/lib/mysql" {
		t.Errorf("Database.DataDir = %q, want /var/lib/mysql", a.Database.DataDir)
	}

	if len(a.SensitiveEnv) != 1 || a.SensitiveEnv[0] != "MYSQL_ROOT_PASSWORD" {
		t.Errorf("SensitiveEnv = %v, want [MYSQL_ROOT_PASSWORD]", a.SensitiveEnv)
	}

	if env, ok := findAttribute(a.Attributes, "environment"); !ok || env.Level != SupportPartial {
		t.Errorf("environment attribute = %+v (present=%v), want partial", env, ok)
	}
	if bind, ok := findAttribute(a.Attributes, "bind_mount"); !ok || bind.Level != SupportPartial {
		t.Errorf("bind_mount attribute = %+v (present=%v), want partial", bind, ok)
	}
	if _, ok := findAttribute(a.Attributes, "named_volume"); ok {
		t.Error("a bind-mounted container must not report a named_volume attribute")
	}
}

func TestAnalyzeComposeContainerPreservesOriginalFile(t *testing.T) {
	info := &ContainerInfo{
		Name:        "blog-web",
		Image:       "nginx:alpine",
		Entrypoint:  []string{"/docker-entrypoint.sh"},
		Healthcheck: true,
		Mounts: []VolumeMount{
			{Type: "volume", Name: "blog-html", Source: "blog-html", Destination: "/usr/share/nginx/html"},
		},
		Labels: map[string]string{
			LabelComposeProject:     "blog",
			LabelComposeService:     "web",
			LabelComposeWorkingDir:  "/opt/blog",
			LabelComposeConfigFiles: "/opt/blog/compose.yaml",
		},
	}

	a := Analyze(info)
	if !a.IsCompose || a.ComposeProject != "blog" || a.ComposeService != "web" {
		t.Fatalf("compose detection = %+v, want blog/web", a)
	}
	if len(a.NamedVolumes) != 1 || a.NamedVolumes[0].Name != "blog-html" {
		t.Errorf("NamedVolumes = %v, want the blog-html volume", a.NamedVolumes)
	}
	if entry, ok := findAttribute(a.Attributes, "entrypoint"); !ok || entry.Level != SupportSupported {
		t.Errorf("entrypoint attribute = %+v (present=%v), want supported for Compose", entry, ok)
	}
	if hc, ok := findAttribute(a.Attributes, "healthcheck"); !ok || hc.Level != SupportSupported {
		t.Errorf("healthcheck attribute = %+v (present=%v), want supported for Compose", hc, ok)
	}
}

func TestAnalyzePlainContainerFlagsUnsupportedAttributes(t *testing.T) {
	info := &ContainerInfo{
		Name:       "gpu-app",
		Image:      "app:1",
		Entrypoint: []string{"/entry"},
		Privileged: true,
		CapAdd:     []string{"SYS_ADMIN"},
		Devices:    []string{"/dev/nvidia0:/dev/nvidia0"},
		Mounts: []VolumeMount{
			{Type: "bind", Source: "/var/run/docker.sock", Destination: "/var/run/docker.sock"},
		},
	}

	a := Analyze(info)

	if dev, ok := findAttribute(a.Attributes, "device"); !ok || dev.Level != SupportUnsupported {
		t.Errorf("device attribute = %+v (present=%v), want unsupported", dev, ok)
	}
	if entry, ok := findAttribute(a.Attributes, "entrypoint"); !ok || entry.Level != SupportPartial {
		t.Errorf("entrypoint attribute = %+v (present=%v), want partial for a plain container", entry, ok)
	}
	if _, ok := findAttribute(a.Attributes, "privileged"); !ok {
		t.Error("privileged attribute should be reported when the container is privileged")
	}
	if _, ok := findAttribute(a.Attributes, "capabilities"); !ok {
		t.Error("capabilities attribute should be reported when CapAdd is set")
	}

	// The docker socket is a system mount: reported as an external dependency but
	// never archived.
	foundSocket := false
	for _, dep := range a.ExternalDeps {
		if dep.Kind == "system_mount" && dep.Source == "/var/run/docker.sock" {
			foundSocket = true
		}
	}
	if !foundSocket {
		t.Errorf("ExternalDeps = %+v, want the docker socket as a system_mount", a.ExternalDeps)
	}
	if len(a.Warnings) == 0 {
		t.Error("expected warnings for a device/privileged container")
	}
	if len(a.Blockers) != 0 {
		t.Errorf("Blockers = %v, want none for a warning-only container", a.Blockers)
	}
}

func TestAnalyzeBlockers(t *testing.T) {
	missingImage := Analyze(&ContainerInfo{Name: "broken"})
	if len(missingImage.Blockers) == 0 {
		t.Error("a container without an image reference should be blocked")
	}

	sharedNet := Analyze(&ContainerInfo{
		Name:        "sidecar",
		Image:       "app:1",
		NetworkMode: "container:abc123",
	})
	if len(sharedNet.Blockers) == 0 {
		t.Error("network_mode=container:<id> should be blocked")
	}
	if nm, ok := findAttribute(sharedNet.Attributes, "network_mode"); !ok || nm.Level != SupportUnsupported {
		t.Errorf("network_mode attribute = %+v (present=%v), want unsupported", nm, ok)
	}
}

func TestAnalyzeHostPaths(t *testing.T) {
	a := Analyze(&ContainerInfo{
		Name:  "blog-web",
		Image: "nginx:alpine",
		Mounts: []VolumeMount{
			{Type: "volume", Name: "blog-html", Source: "blog-html", Destination: "/usr/share/nginx/html"},
			{Type: "bind", Source: "/srv/blog/html", Destination: "/usr/share/nginx/html"},
			{Type: "bind", Source: "/etc/localtime", Destination: "/etc/localtime"},
		},
		Labels: map[string]string{
			LabelComposeProject:    "blog",
			LabelComposeWorkingDir: "/opt/blog",
		},
	})

	got := a.HostPaths()
	want := map[string]bool{"/opt/blog": true, "/srv/blog/html": true}
	if len(got) != len(want) {
		t.Fatalf("HostPaths() = %v, want %v", got, want)
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("HostPaths() = %v, unexpected %q", got, p)
		}
	}
	for _, p := range got {
		if strings.Contains(p, "/etc/localtime") {
			t.Errorf("HostPaths() must skip system mounts, got %v", got)
		}
	}
}
