package docker

import (
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
