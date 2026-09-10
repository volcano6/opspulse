package backup

import (
	"testing"
)

func TestPathRemapper_Remap(t *testing.T) {
	rules := map[string]string{
		"/home/user":       "/root",
		"/home/user/.config": "/root/.config",
		"var/log/":          "/opt/log",
	}

	remapper := NewPathRemapper(rules)

	tests := []struct {
		input    string
		expected string
	}{
		{"/home/user/.zshrc", "/root/.zshrc"},
		{"/home/user/.config/git", "/root/.config/git"},
		{"/home/user/.config", "/root/.config"},
		{"/home/user", "/root"},
		{"/home/user_extra", "/home/user_extra"}, // Not a directory match
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
