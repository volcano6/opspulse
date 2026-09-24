package backup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestResticRetryLockPreamble runs the generated preamble on a real shell
// against stub restic binaries. The preamble only ever executes on the target
// host, so string assertions cannot catch a broken fallback: if it produced a
// bare "--retry-lock" on a restic that predates the flag, every backup would
// fail on that host.
func TestResticRetryLockPreamble(t *testing.T) {
	// The generated scripts target bash (see BuildBackupScript's shebang), and
	// dash rejects `set -o pipefail`.
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}

	script := "set -euo pipefail\n" +
		resticRetryLockPreamble(retryLockVar{"RETRY_LOCK_2M", "2m"}) +
		"echo \"VAR=[$RETRY_LOCK_2M]\"\n" +
		"restic backup $RETRY_LOCK_2M --json\n"

	tests := []struct {
		name        string
		supportFlag bool
		wantVar     string
		wantArgs    []string
	}{
		{
			name:        "restic supports --retry-lock",
			supportFlag: true,
			wantVar:     "VAR=[--retry-lock 2m]",
			// The variable is intentionally expanded unquoted: the shell must
			// split it back into the flag and its duration.
			wantArgs: []string{"ARG:backup", "ARG:--retry-lock", "ARG:2m", "ARG:--json"},
		},
		{
			name:        "restic predates --retry-lock",
			supportFlag: false,
			wantVar:     "VAR=[]",
			wantArgs:    []string{"ARG:backup", "ARG:--json"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			binDir := t.TempDir()
			stub := fmt.Sprintf(`#!/bin/sh
if [ "$1" = snapshots ]; then
  if [ %q = true ]; then
    echo '  --retry-lock duration   retry if the repository is locked'
  fi
  exit 0
fi
for a in "$@"; do echo "ARG:$a"; done
`, fmt.Sprintf("%t", tc.supportFlag))

			stubPath := filepath.Join(binDir, "restic")
			if err := os.WriteFile(stubPath, []byte(stub), 0o755); err != nil { // #nosec G306 -- test stub
				t.Fatalf("write stub: %v", err)
			}

			cmd := exec.Command(shell, "-c", script)
			cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("preamble failed: %v\n%s", err, out)
			}

			got := string(out)
			if !strings.Contains(got, tc.wantVar) {
				t.Errorf("preamble produced %q, want it to contain %q", got, tc.wantVar)
			}
			for _, want := range tc.wantArgs {
				if !strings.Contains(got, want+"\n") {
					t.Errorf("restic was not called with %q; got:\n%s", want, got)
				}
			}
			if !tc.supportFlag {
				if strings.Contains(got, "ARG:--retry-lock") {
					t.Errorf("restic predating --retry-lock was still passed the flag:\n%s", got)
				}
				if !strings.Contains(got, "Notice:") {
					t.Errorf("preamble stayed silent about the missing flag:\n%s", got)
				}
			}
		})
	}
}

// TestGeneratedScriptsAreValidShell feeds every script builder through the
// shell's own parser. These scripts only ever run on the target host, so a
// quoting mistake would surface as a failed backup instead of a failed test.
func TestGeneratedScriptsAreValidShell(t *testing.T) {
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}

	job := Job{
		Name:      "site-backup",
		Server:    "vps-01",
		Paths:     []string{"/var/www", "/etc/nginx"},
		Backend:   "s3:s3.amazonaws.com/backup-bucket",
		Env:       map[string]string{"RESTIC_PASSWORD": "secret-pw"},
		Retention: &RetentionPolicy{KeepDaily: 7},
		Excludes:  []string{"*.log"},
		Tags:      []string{"prod"},
	}

	backupScript, err := BuildBackupScript(job)
	if err != nil {
		t.Fatalf("BuildBackupScript() error: %v", err)
	}

	scripts := map[string]string{
		"backup":         backupScript,
		"snapshots":      BuildSnapshotsScript(job),
		"restore":        BuildRestoreScript(job, "snap-1234", "/data/web", []string{"/var/www/*"}),
		"restore-dryrun": BuildRestoreDryRunScript(job, "snap-1234", []string{"/var/www/*"}),
	}

	for name, script := range scripts {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command(shell, "-n")
			cmd.Stdin = strings.NewReader(script)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("generated %s script is not valid shell: %v\n%s", name, err, out)
			}
		})
	}
}

// TestRestoreDryRunScriptFlags runs the dry-run script against a stub restic
// and inspects the argv it receives. `restic ls` accepts only
// --host/--tag/--path; the previous --include made `ops restore run --dry-run
// --asset <id>` fail with "unknown flag: --include" on every invocation, which
// a substring assertion on the script alone would not prove.
func TestRestoreDryRunScriptFlags(t *testing.T) {
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}

	job := Job{
		Name:    "site-backup",
		Server:  "vps-01",
		Paths:   []string{"/var/www"},
		Backend: "s3:s3.amazonaws.com/backup-bucket",
		Env:     map[string]string{"RESTIC_PASSWORD": "secret-pw"},
	}

	script := BuildRestoreDryRunScript(job, "snap-1234", []string{"/var/www/uploads"})
	if strings.Contains(script, "--include") {
		t.Fatalf("restic ls does not support --include:\n%s", script)
	}

	binDir := t.TempDir()
	// The stub deliberately advertises no --retry-lock support, so the
	// preamble must leave the variable empty and the argv must be exact.
	stub := `#!/bin/sh
if [ "$1" = snapshots ]; then
  exit 0
fi
for a in "$@"; do echo "ARG:$a"; done
`
	stubPath := filepath.Join(binDir, "restic")
	if err := os.WriteFile(stubPath, []byte(stub), 0o755); err != nil { // #nosec G306 -- test stub
		t.Fatalf("write stub: %v", err)
	}

	cmd := exec.Command(shell, "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dry-run script failed: %v\n%s", err, out)
	}

	got := string(out)
	for _, want := range []string{"ARG:ls", "ARG:snap-1234", "ARG:--path", "ARG:/var/www/uploads"} {
		if !strings.Contains(got, want+"\n") {
			t.Errorf("restic was not called with %q; got:\n%s", want, got)
		}
	}

	// Every long flag handed to restic must be one `ls` actually defines.
	allowed := map[string]bool{"--path": true}
	for _, line := range strings.Split(got, "\n") {
		if !strings.HasPrefix(line, "ARG:--") {
			continue
		}
		flag := strings.TrimPrefix(line, "ARG:")
		if !allowed[flag] {
			t.Errorf("restic ls was passed unsupported flag %q; got:\n%s", flag, got)
		}
	}
}

// TestBuildBackupScript_InitOnlyWhenRepositoryMissing runs the backup script
// against a stub restic that records every invocation. Initializing on any
// failure would silently create a fresh, empty repository on an unreachable or
// mis-pointed backend, after which every backup "succeeds" against the wrong
// history.
func TestBuildBackupScript_InitOnlyWhenRepositoryMissing(t *testing.T) {
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}

	job := Job{
		Name:    "site-backup",
		Server:  "vps-01",
		Paths:   []string{"/var/www"},
		Backend: "/mnt/repo",
		Env:     map[string]string{"RESTIC_PASSWORD": "secret-pw"},
	}
	script, err := BuildBackupScript(job)
	if err != nil {
		t.Fatalf("BuildBackupScript() error: %v", err)
	}

	// The stub records each subcommand it is asked to run, so the test observes
	// whether `restic init` was actually executed rather than whether the
	// string appears in the script text.
	const stub = `#!/bin/sh
if [ "$1" = snapshots ] && [ "$2" = "--help" ]; then
  exit 0
fi
echo "$1" >> "$RESTIC_STUB_LOG"
if [ "$1" = snapshots ]; then
  if [ "$RESTIC_STUB_MODE" = notexist ]; then
    echo "Fatal: unable to open config file: stat /mnt/repo/config: no such file or directory" >&2
    echo "Is there a repository at the following location?" >&2
    echo "/mnt/repo" >&2
    exit 10
  fi
  echo "Fatal: unable to create lock in backend: context deadline exceeded" >&2
  exit 1
fi
exit 0
`

	tests := []struct {
		name        string
		mode        string
		wantInit    bool
		wantFailure bool
		wantText    string
	}{
		{
			name:     "missing repository is initialized",
			mode:     "notexist",
			wantInit: true,
		},
		{
			name:        "transient backend failure must not initialize",
			mode:        "transient",
			wantInit:    false,
			wantFailure: true,
			wantText:    "unable to create lock in backend",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			binDir := t.TempDir()
			logPath := filepath.Join(t.TempDir(), "restic-calls.log")
			if err := os.WriteFile(filepath.Join(binDir, "restic"), []byte(stub), 0o755); err != nil { // #nosec G306 -- test stub
				t.Fatalf("write stub: %v", err)
			}

			cmd := exec.Command(shell, "-c", script)
			cmd.Env = append(os.Environ(),
				"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"RESTIC_STUB_LOG="+logPath,
				"RESTIC_STUB_MODE="+tc.mode,
			)
			out, err := cmd.CombinedOutput()

			calls, readErr := os.ReadFile(logPath)
			if readErr != nil {
				t.Fatalf("stub was never invoked (%v); output:\n%s", readErr, out)
			}
			invoked := strings.Split(strings.TrimSpace(string(calls)), "\n")

			gotInit := false
			for _, c := range invoked {
				if c == "init" {
					gotInit = true
				}
			}
			if gotInit != tc.wantInit {
				t.Errorf("restic init invoked = %v, want %v; calls: %v", gotInit, tc.wantInit, invoked)
			}
			if tc.wantFailure && err == nil {
				t.Errorf("expected the script to fail on a transient error; output:\n%s", out)
			}
			if !tc.wantFailure && err != nil {
				t.Errorf("script failed: %v\n%s", err, out)
			}
			if tc.wantText != "" && !strings.Contains(string(out), tc.wantText) {
				t.Errorf("original restic error was not surfaced; output:\n%s", out)
			}
		})
	}
}
