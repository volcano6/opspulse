package backup

import (
	"testing"
)

func TestPathRemapper_Remap(t *testing.T) {
	rules := map[string]string{
		"/home/vol":       "/root",
		"/home/vol/.config": "/root/.config",
		"var/log/":          "/opt/log",
	}

	remapper := NewPathRemapper(rules)

	tests := []struct {
		input    string
		expected string
	}{
		{"/home/vol/.zshrc", "/root/.zshrc"},
		{"/home/vol/.config/git", "/root/.config/git"},
		{"/home/vol/.config", "/root/.config"},
		{"/home/vol", "/root"},
		{"/home/vol_extra", "/home/vol_extra"}, // Not a directory match
		{"/var/log/syslog", "/opt/log/syslog"},
		{"/etc/hosts", "/etc/hosts"}, // No match
	}

	for _, tc := range tests {
		actual := remapper.Remap(tc.input)
		if actual != tc.expected {
			t.Errorf("Remap(%q) = %q, expected %q", tc.input, actual, tc.expected)
		}
	}
}
