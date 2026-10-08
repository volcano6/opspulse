package main

import (
	"testing"

	"github.com/volcano6/opspulse/internal/storage"
)

// TestRestoreStatusLabel pins the history table's status column, the surface an
// operator uses to tell a validation failure (nothing was touched) apart from an
// execution or post-restore failure.
func TestRestoreStatusLabel(t *testing.T) {
	cases := []struct {
		name string
		run  storage.RestoreRun
		want string
	}{
		{"success", storage.RestoreRun{Status: "success"}, "SUCCESS"},
		{"dry run", storage.RestoreRun{Status: "dry-run"}, "DRY-RUN"},
		{"validation failure", storage.RestoreRun{Status: "failed", FailureStage: storage.RestoreStageValidation}, "FAILED (VALIDATION)"},
		{"execution failure", storage.RestoreRun{Status: "failed", FailureStage: storage.RestoreStageExecution}, "FAILED (EXECUTION)"},
		{"post restore failure", storage.RestoreRun{Status: "partial", FailureStage: storage.RestoreStagePostRestore}, "PARTIAL (POST_RESTORE)"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := restoreStatusLabel(tc.run); got != tc.want {
				t.Errorf("restoreStatusLabel(%+v) = %q, want %q", tc.run, got, tc.want)
			}
		})
	}
}
