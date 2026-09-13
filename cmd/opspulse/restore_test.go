package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestCheckRestoreConfirmation_YesBypassesPrompt(t *testing.T) {
	var in bytes.Buffer
	var out bytes.Buffer

	// When yes is true, should not prompt and should return nil immediately
	err := checkRestoreConfirmation(&in, &out, "my-app", "prod-srv", "/var/www", false, true)
	if err != nil {
		t.Fatalf("expected nil error when yes=true, got: %v", err)
	}
	if out.Len() > 0 {
		t.Errorf("expected no output when yes=true, got: %s", out.String())
	}
}

func TestCheckRestoreConfirmation_DryRunBypassesPrompt(t *testing.T) {
	var in bytes.Buffer
	var out bytes.Buffer

	// When dryRun is true, should not prompt even if yes is false
	err := checkRestoreConfirmation(&in, &out, "my-app", "prod-srv", "/var/www", true, false)
	if err != nil {
		t.Fatalf("expected nil error when dryRun=true, got: %v", err)
	}
	if out.Len() > 0 {
		t.Errorf("expected no output when dryRun=true, got: %s", out.String())
	}
}

func TestCheckRestoreConfirmation_InteractiveUserConfirm(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		expectErr bool
	}{
		{"User inputs y", "y\n", false},
		{"User inputs yes", "yes\n", false},
		{"User inputs YES uppercase", "YES\n", false},
		{"User inputs n", "n\n", true},
		{"User inputs no", "no\n", true},
		{"User inputs empty newline", "\n", true},
		{"EOF without input", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := strings.NewReader(tc.input)
			var out bytes.Buffer

			err := checkRestoreConfirmation(in, &out, "my-app", "prod-srv", "/var/www", false, false)
			if tc.expectErr && err == nil {
				t.Errorf("expected cancellation error for input %q, got nil", tc.input)
			}
			if !tc.expectErr && err != nil {
				t.Errorf("unexpected error for input %q: %v", tc.input, err)
			}
			if !strings.Contains(out.String(), "Warning: Restoring job \"my-app\" to prod-srv (/var/www)") {
				t.Errorf("expected warning in output, got: %s", out.String())
			}
		})
	}
}

func TestCheckRestoreConfirmation_DefaultTargetDir(t *testing.T) {
	in := strings.NewReader("y\n")
	var out bytes.Buffer

	// When targetPath is empty, should display "original backup paths"
	err := checkRestoreConfirmation(in, &out, "db-job", "backup-host", "", false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), "original backup paths") {
		t.Errorf("expected prompt to say 'original backup paths', got: %s", out.String())
	}
}

func TestRestoreRunCmd_YesFlagRegistered(t *testing.T) {
	flag := restoreRunCmd.Flags().Lookup("yes")
	if flag == nil {
		t.Fatal("expected 'yes' flag to be registered on restoreRunCmd")
	}
	if flag.Shorthand != "y" {
		t.Errorf("expected shorthand 'y', got %q", flag.Shorthand)
	}
	if flag.DefValue != "false" {
		t.Errorf("expected default value 'false', got %q", flag.DefValue)
	}
}
