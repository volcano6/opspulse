package docker

import (
	"strings"
	"testing"
)

func TestParseBindString(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantSrc string
		wantDst string
		wantRO  bool
		wantOK  bool
	}{
		{
			name:    "Standard unix bind",
			input:   "/var/lib/data:/app/data",
			wantSrc: "/var/lib/data",
			wantDst: "/app/data",
			wantRO:  false,
			wantOK:  true,
		},
		{
			name:    "Unix bind with ro option",
			input:   "/etc/config:/config:ro",
			wantSrc: "/etc/config",
			wantDst: "/config",
			wantRO:  true,
			wantOK:  true,
		},
		{
			name:    "Unix bind with ro and selinux options",
			input:   "/opt/app:/app:ro,z",
			wantSrc: "/opt/app",
			wantDst: "/app",
			wantRO:  true,
			wantOK:  true,
		},
		{
			name:    "Windows backslash drive letter",
			input:   `C:\data:/app:ro`,
			wantSrc: `C:\data`,
			wantDst: `/app`,
			wantRO:  true,
			wantOK:  true,
		},
		{
			name:    "Windows forward slash drive letter",
			input:   "D:/data/web:/var/www:rw",
			wantSrc: "D:/data/web",
			wantDst: "/var/www",
			wantRO:  false,
			wantOK:  true,
		},
		{
			name:    "Named volume bind",
			input:   "vol_name:/var/lib/mysql",
			wantSrc: "vol_name",
			wantDst: "/var/lib/mysql",
			wantRO:  false,
			wantOK:  true,
		},
		{
			name:   "Invalid string",
			input:  "invalid-bind",
			wantOK: false,
		},
		{
			name:   "Empty string",
			input:  "",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, dst, ro, ok := parseBindString(tt.input)
			if ok != tt.wantOK {
				t.Fatalf("parseBindString(%q) ok = %v, wantOK = %v", tt.input, ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if src != tt.wantSrc {
				t.Errorf("src = %q, want %q", src, tt.wantSrc)
			}
			if dst != tt.wantDst {
				t.Errorf("dst = %q, want %q", dst, tt.wantDst)
			}
			if ro != tt.wantRO {
				t.Errorf("ro = %v, want %v", ro, tt.wantRO)
			}
		})
	}
}

func TestParseInspectJSON_WindowsBindsFallback(t *testing.T) {
	inspectJSON := `[
		{
			"Id": "test-container-id",
			"Name": "/my-app",
			"Config": {
				"Image": "nginx:latest"
			},
			"HostConfig": {
				"Binds": [
					"C:\\website:/usr/share/nginx/html:ro",
					"D:/logs:/var/log/nginx"
				]
			},
			"Mounts": []
		}
	]`

	info, err := ParseInspectJSON([]byte(inspectJSON))
	if err != nil {
		t.Fatalf("ParseInspectJSON() unexpected error: %v", err)
	}

	if len(info.Mounts) != 2 {
		t.Fatalf("expected 2 mounts, got %d", len(info.Mounts))
	}

	m1 := info.Mounts[0]
	if m1.Source != `C:\website` || m1.Destination != "/usr/share/nginx/html" || !m1.ReadOnly {
		t.Errorf("unexpected m1: %+v", m1)
	}

	m2 := info.Mounts[1]
	if m2.Source != "D:/logs" || m2.Destination != "/var/log/nginx" || m2.ReadOnly {
		t.Errorf("unexpected m2: %+v", m2)
	}
}

func TestIsSystemMount(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   bool
	}{
		// Sockets and runtime-injected configuration.
		{"docker socket", "/var/run/docker.sock", true},
		{"docker socket short path", "/run/docker.sock", true},
		{"localtime", "/etc/localtime", true},
		{"resolv.conf", "/etc/resolv.conf", true},

		// Virtual filesystems, matched on path boundaries.
		{"proc root", "/proc", true},
		{"proc entry", "/proc/1/ns/net", true},
		{"sys root", "/sys", true},
		{"sys subtree", "/sys/fs/cgroup", true},
		{"dev root", "/dev", true},
		{"dev shm", "/dev/shm", true},
		{"run root", "/run", true},
		{"run subtree", "/run/systemd", true},

		// Regression: directories that merely share a byte prefix with a
		// system directory are legitimate data and must still be archived.
		{"development dir", "/development/data", false},
		{"procurement dir", "/procurement/files", false},
		{"system dir", "/system/backups", false},
		{"runtime dir", "/runtime/app", false},

		// Ordinary data.
		{"data dir", "/data/app", false},
		{"opt dir", "/opt/blog", false},
		{"windows drive path", `C:\data`, false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSystemMount(tt.source); got != tt.want {
				t.Errorf("IsSystemMount(%q) = %v, want %v", tt.source, got, tt.want)
			}
		})
	}
}

// TestPortMappingString covers the generated compose.yaml "ports" entries. An
// unpublished port used to be rendered as "127.0.0.1::80", which compose
// rejects.
func TestPortMappingString(t *testing.T) {
	tests := []struct {
		name string
		p    PortMapping
		want string
	}{
		{"published", PortMapping{HostPort: "8080", ContainerPort: "80", Protocol: "tcp"}, "8080:80"},
		{"published udp", PortMapping{HostPort: "8080", ContainerPort: "53", Protocol: "udp"}, "8080:53/udp"},
		{"bound to loopback", PortMapping{HostIP: "127.0.0.1", HostPort: "8080", ContainerPort: "80", Protocol: "tcp"}, "127.0.0.1:8080:80"},
		{"wildcard host ip is elided", PortMapping{HostIP: "0.0.0.0", HostPort: "8080", ContainerPort: "80", Protocol: "tcp"}, "8080:80"},
		{"ipv6 wildcard host ip is elided", PortMapping{HostIP: "::", HostPort: "8080", ContainerPort: "80", Protocol: "tcp"}, "8080:80"},
		{"exposed only", PortMapping{ContainerPort: "80", Protocol: "tcp"}, "80"},
		{"exposed only, udp", PortMapping{ContainerPort: "53", Protocol: "udp"}, "53/udp"},
		{"host ip without host port stays malformed-free", PortMapping{HostIP: "127.0.0.1", ContainerPort: "80", Protocol: "tcp"}, "80"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.p.String()
			if got != tt.want {
				t.Errorf("PortMapping%+v.String() = %q, want %q", tt.p, got, tt.want)
			}
			if strings.Contains(got, "::") || strings.HasSuffix(got, ":") {
				t.Errorf("PortMapping%+v.String() = %q, which is not a valid port mapping", tt.p, got)
			}
		})
	}
}

func TestDatabaseEngine(t *testing.T) {
	tests := []struct {
		image string
		want  string
	}{
		// Recognized databases, including registry, library and bitnami prefixes.
		{"mysql:8.0", "mysql"},
		{"mariadb:10", "mysql"},
		{"percona:8", "mysql"},
		{"library/postgres:16-alpine", "postgres"},
		{"postgres", "postgres"},
		{"postgresql:15", "postgres"},
		{"bitnami/mysql:8.0", "mysql"},
		{"bitnami/postgresql:16", "postgres"},
		{"docker.io/library/mysql", "mysql"},
		{"registry.example.com:5000/team/mysql:8", "mysql"},
		{"registry.example.com/team/postgres@sha256:deadbeef", "postgres"},
		{"MySQL:8.0", "mysql"},

		// Look-alikes must not be treated as databases: doing so skips the
		// physical volume and triggers a dump that can never succeed.
		{"my-mysql-exporter:1.0", ""},
		{"mysql-exporter", ""},
		{"postgres-backup", ""},
		{"postgresql-exporter", ""},
		{"nginx:alpine", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.image, func(t *testing.T) {
			info := &ContainerInfo{Image: tt.image}
			if got := info.DatabaseEngine(); got != tt.want {
				t.Errorf("DatabaseEngine(%q) = %q, want %q", tt.image, got, tt.want)
			}
			if info.IsDatabase() != (tt.want != "") {
				t.Errorf("IsDatabase(%q) = %v, want %v", tt.image, info.IsDatabase(), tt.want != "")
			}
		})
	}
}
