package pathutil

import "testing"

func TestHasPathPrefix(t *testing.T) {
	tests := []struct {
		name   string
		p      string
		prefix string
		want   bool
	}{
		// Exact match.
		{"exact match", "/dev", "/dev", true},
		{"exact match nested", "/var/run/docker.sock", "/var/run/docker.sock", true},

		// True descendants.
		{"direct child", "/dev/shm", "/dev", true},
		{"deep descendant", "/proc/1/ns/net", "/proc", true},
		{"descendant of nested prefix", "/var/run/docker.sock", "/var/run", true},

		// The regression this helper exists for: sibling paths that merely
		// share a byte prefix must not be treated as descendants.
		{"sibling development", "/development/data", "/dev", false},
		{"sibling procurement", "/procurement/files", "/proc", false},
		{"sibling system", "/system/backups", "/sys", false},
		{"sibling runtime", "/runtime/app", "/run", false},
		{"sibling project dir", "/var/lib/opspulse/containers/nginx-other", "/var/lib/opspulse/containers/nginx", false},

		// Unrelated paths.
		{"unrelated", "/opt/blog", "/dev", false},
		{"prefix longer than p", "/dev", "/dev/shm", false},
		{"empty p", "", "/dev", false},

		// Trailing separators on prefix are ignored.
		{"trailing slash", "/dev/shm", "/dev/", true},
		{"trailing slashes", "/dev/shm", "/dev///", true},
		{"trailing slash sibling", "/development", "/dev/", false},

		// Degenerate prefixes.
		{"empty prefix never matches", "/dev", "", false},
		{"empty prefix with empty p", "", "", false},
		{"root prefix", "/dev", "/", true},
		{"root prefix exact", "/", "/", true},
		{"root prefix non-absolute", "dev", "/", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasPathPrefix(tt.p, tt.prefix); got != tt.want {
				t.Errorf("HasPathPrefix(%q, %q) = %v, want %v", tt.p, tt.prefix, got, tt.want)
			}
		})
	}
}
