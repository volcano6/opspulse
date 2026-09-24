package backup

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateJobName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		// Names that already worked before this validation was added.
		{name: "simple", input: "web-backup", wantErr: false},
		{name: "alnum dashes", input: "vps01-etc", wantErr: false},
		{name: "single char", input: "a", wantErr: false},
		{name: "dots underscores at", input: "a.b_c@d", wantErr: false},
		{name: "chinese letters", input: "备份任务", wantErr: false},
		{name: "chinese with digits", input: "备份-01", wantErr: false},
		{name: "accented letters", input: "sauvegarde-données", wantErr: false},
		{name: "max length", input: strings.Repeat("a", MaxJobNameLength), wantErr: false},
		{name: "max length multibyte", input: strings.Repeat("备", MaxJobNameLength), wantErr: false},

		// Old behavior accepted all of these; the new contract must reject them.
		{name: "empty", input: "", wantErr: true},
		{name: "whitespace only", input: "   ", wantErr: true},
		{name: "forward slash", input: "a/b", wantErr: true},
		{name: "backslash", input: `a\b`, wantErr: true},
		{name: "double dot", input: "..", wantErr: true},
		{name: "embedded double dot", input: "a..b", wantErr: true},
		{name: "leading dot", input: ".hidden", wantErr: true},
		{name: "leading dash", input: "-dash", wantErr: true},
		{name: "space", input: "has space", wantErr: true},
		{name: "tab", input: "tab\tname", wantErr: true},
		{name: "newline", input: "line\nname", wantErr: true},
		{name: "nul control", input: "bad\x00name", wantErr: true},
		{name: "shell metachar", input: "job;rm", wantErr: true},
		{name: "too long", input: strings.Repeat("a", MaxJobNameLength+1), wantErr: true},
		{name: "too long multibyte", input: strings.Repeat("备", MaxJobNameLength+1), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateJobName(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ValidateJobName(%q) = nil, want error", tt.input)
				}
				if !errors.Is(err, ErrInvalidJobName) {
					t.Fatalf("ValidateJobName(%q) = %v, want ErrInvalidJobName", tt.input, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateJobName(%q) = %v, want nil", tt.input, err)
			}
		})
	}
}

// validJob returns a job that satisfies every validation rule so each test only
// varies the field under test.
func validJob() Job {
	return Job{
		Name:    "web-backup",
		Server:  "vps-01",
		Paths:   []string{"/var/www"},
		Backend: "/mnt/backup",
	}
}

func TestJob_Validate_BackwardCompatibleSentinels(t *testing.T) {
	// internal/backup/store_test.go relies on the unwrapped sentinel for these
	// pre-existing rejection cases; keep returning them verbatim.
	tests := []struct {
		name    string
		mutate  func(*Job)
		wantErr error
	}{
		{
			name:    "missing name",
			mutate:  func(j *Job) { j.Name = "" },
			wantErr: ErrInvalidJobName,
		},
		{
			name:    "missing server",
			mutate:  func(j *Job) { j.Server = "" },
			wantErr: ErrInvalidJobServer,
		},
		{
			name:    "missing paths",
			mutate:  func(j *Job) { j.Paths = nil },
			wantErr: ErrInvalidJobPaths,
		},
		{
			name:    "blank path",
			mutate:  func(j *Job) { j.Paths = []string{"   "} },
			wantErr: ErrInvalidJobPaths,
		},
		{
			name:    "missing backend",
			mutate:  func(j *Job) { j.Backend = "" },
			wantErr: ErrInvalidJobBackend,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := validJob()
			tt.mutate(&job)
			if err := job.Validate(); err != tt.wantErr {
				t.Fatalf("Validate() = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestJob_Validate_RejectsUnsafeInput(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*Job)
		wantErrIs error
	}{
		// Relative paths used to be accepted and produced remote scripts that
		// operated on the SSH session's working directory.
		{
			name:      "relative path",
			mutate:    func(j *Job) { j.Paths = []string{"var/www"} },
			wantErrIs: ErrInvalidJobPaths,
		},
		{
			name:      "tilde path",
			mutate:    func(j *Job) { j.Paths = []string{"~/.config"} },
			wantErrIs: ErrInvalidJobPaths,
		},
		{
			name:      "mixed absolute and relative",
			mutate:    func(j *Job) { j.Paths = []string{"/var/www", "etc/nginx"} },
			wantErrIs: ErrInvalidJobPaths,
		},
		// Invalid env identifiers used to be silently dropped from the script.
		{
			name:      "env leading digit",
			mutate:    func(j *Job) { j.Env = map[string]string{"1BAD": "x"} },
			wantErrIs: ErrInvalidJobEnv,
		},
		{
			name:      "env with dash",
			mutate:    func(j *Job) { j.Env = map[string]string{"HAS-DASH": "x"} },
			wantErrIs: ErrInvalidJobEnv,
		},
		{
			name:      "env with space",
			mutate:    func(j *Job) { j.Env = map[string]string{"HAS SPACE": "x"} },
			wantErrIs: ErrInvalidJobEnv,
		},
		// A multi-line schedule must never reach the generated scripts.
		{
			name:      "multi-line schedule",
			mutate:    func(j *Job) { j.Schedule = "0 2 * * *\nrm -rf /" },
			wantErrIs: ErrInvalidJobSchedule,
		},
		{
			name:      "control char schedule",
			mutate:    func(j *Job) { j.Schedule = "0 2 * * *\x00" },
			wantErrIs: ErrInvalidJobSchedule,
		},
		{
			name:      "oversized schedule",
			mutate:    func(j *Job) { j.Schedule = strings.Repeat("0 ", 100) },
			wantErrIs: ErrInvalidJobSchedule,
		},
		{
			name:      "unsafe name",
			mutate:    func(j *Job) { j.Name = "../../etc/passwd" },
			wantErrIs: ErrInvalidJobName,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := validJob()
			tt.mutate(&job)
			err := job.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want error (%v)", tt.wantErrIs)
			}
			if !errors.Is(err, tt.wantErrIs) {
				t.Fatalf("Validate() = %v, want error wrapping %v", err, tt.wantErrIs)
			}
		})
	}
}

func TestJob_Validate_AcceptsPreviouslyValidJobs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Job)
	}{
		{name: "baseline", mutate: func(*Job) {}},
		{name: "chinese name", mutate: func(j *Job) { j.Name = "数据库备份" }},
		{name: "chinese unicode name", mutate: func(j *Job) { j.Name = "备份" }},
		{name: "valid env", mutate: func(j *Job) { j.Env = map[string]string{"RESTIC_PASSWORD": "x", "_A1": "y"} }},
		{name: "descriptor schedule", mutate: func(j *Job) { j.Schedule = "@daily" }},
		{name: "cron schedule", mutate: func(j *Job) { j.Schedule = "0 2 * * *" }},
		{name: "assets only", mutate: func(j *Job) { j.Paths = nil; j.Assets = []string{"web-data"} }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := validJob()
			tt.mutate(&job)
			if err := job.Validate(); err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}
