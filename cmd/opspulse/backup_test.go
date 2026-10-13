package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestConfirmContainerSensitiveEnv(t *testing.T) {
	keys := []string{"API_TOKEN", "MYSQL_ROOT_PASSWORD"}

	t.Run("yes accepts without prompting", func(t *testing.T) {
		var out bytes.Buffer
		confirm := confirmContainerSensitiveEnv(strings.NewReader(""), &out, false, true)
		if err := confirm(keys); err != nil {
			t.Fatalf("confirm() error = %v, want nil with --yes", err)
		}
		if out.Len() != 0 {
			t.Errorf("--yes should not prompt, got output %q", out.String())
		}
	})

	t.Run("non-interactive refuses without yes", func(t *testing.T) {
		var out bytes.Buffer
		confirm := confirmContainerSensitiveEnv(strings.NewReader(""), &out, false, false)
		err := confirm(keys)
		if err == nil {
			t.Fatal("confirm() error = nil, want a refusal in a non-interactive shell")
		}
		if !strings.Contains(err.Error(), "non-interactive") || !strings.Contains(err.Error(), "--yes") {
			t.Errorf("error = %v, want it to name the restriction and the way out", err)
		}
	})

	t.Run("interactive accepts", func(t *testing.T) {
		var out bytes.Buffer
		confirm := confirmContainerSensitiveEnv(strings.NewReader("y\n"), &out, true, false)
		if err := confirm(keys); err != nil {
			t.Fatalf("confirm() error = %v, want nil after y", err)
		}
		if !strings.Contains(out.String(), "MYSQL_ROOT_PASSWORD") {
			t.Errorf("prompt should name the sensitive variables, got %q", out.String())
		}
	})

	t.Run("interactive decline aborts", func(t *testing.T) {
		var out bytes.Buffer
		confirm := confirmContainerSensitiveEnv(strings.NewReader("n\n"), &out, true, false)
		if err := confirm(keys); err == nil {
			t.Fatal("confirm() error = nil, want a cancellation error after n")
		}
	})
}
