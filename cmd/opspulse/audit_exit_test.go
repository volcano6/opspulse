package main

import (
	"fmt"
	"testing"

	"github.com/volcano6/opspulse/internal/backup"
)

// TestCommandExitCodeUntracked pins the script-facing contract: an untracked
// success exits 3, which must differ from both success (0) and the generic
// failure code (1).
func TestCommandExitCodeUntracked(t *testing.T) {
	if backup.ExitUntracked == 0 || backup.ExitUntracked == 1 {
		t.Fatalf("backup.ExitUntracked = %d; must differ from success (0) and generic failure (1)", backup.ExitUntracked)
	}

	cases := []struct {
		name string
		err  error
	}{
		{
			name: "backup",
			err:  fmt.Errorf("container backup %q completed but was not audited: %w", "web-data", backup.ErrUntracked),
		},
		{
			name: "restore",
			err:  fmt.Errorf("restore run %q completed but was not audited: %w", "web-data", backup.ErrRestoreUntracked),
		},
		{
			name: "restore audit failure",
			err:  fmt.Errorf("restore failed: %w", backup.ErrRestoreAuditFailed),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := backup.ExitUntracked
			if tc.name == "restore audit failure" {
				want = 1
			}
			if got := commandExitCode(tc.err); got != want {
				t.Errorf("commandExitCode(%v) = %d; want %d", tc.err, got, want)
			}
		})
	}
}

// TestAllowUntrackedFlagDefaultsToFalse pins the fail-closed default on both
// entry points: emergency mode must be opt-in, never implied.
func TestAllowUntrackedFlagDefaultsToFalse(t *testing.T) {
	cases := []struct {
		name string
		path []string
	}{
		{"backup run", []string{"backup", "run"}},
		{"restore run", []string{"restore", "run"}},
		{"daemon", []string{"daemon"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target, _, err := rootCmd.Find(tc.path)
			if err != nil {
				t.Fatalf("rootCmd.Find(%v) error: %v", tc.path, err)
			}
			flag := target.Flags().Lookup("allow-untracked")
			if flag == nil {
				t.Fatalf("%s is missing the --allow-untracked flag", tc.name)
			}
			if flag.DefValue != "false" {
				t.Errorf("--allow-untracked default = %q; want false (fail-closed)", flag.DefValue)
			}
		})
	}
}
