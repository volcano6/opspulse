package main

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/volcano6/opspulse/internal/backup"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/notify"
	"github.com/volcano6/opspulse/internal/server"
)

// cliRestoreExecutor stands in for a target host: it answers the snapshot
// listing with one snapshot and reports every other task as a success, while
// recording the tasks it ran so a test can assert which steps were attempted.
type cliRestoreExecutor struct {
	tasks []string
}

func (e *cliRestoreExecutor) Execute(_ context.Context, target executor.Target, taskName, _ string, output io.Writer) (*executor.Result, error) {
	e.tasks = append(e.tasks, taskName)
	if strings.HasPrefix(taskName, "snapshots-") && output != nil {
		_, _ = io.WriteString(output, `[{"id":"snap-abcdef1234567890","short_id":"snap-abc1","time":"2026-09-01T10:00:00Z"}]`)
	}
	return &executor.Result{
		ServerName: target.Name,
		Template:   taskName,
		Success:    true,
		ExitCode:   0,
		Duration:   time.Millisecond,
	}, nil
}

func (e *cliRestoreExecutor) Test(_ context.Context, _ executor.Target) (time.Duration, string, error) {
	return time.Millisecond, "Linux", nil
}

func (e *cliRestoreExecutor) ran(prefix string) bool {
	for _, task := range e.tasks {
		if strings.HasPrefix(task, prefix) {
			return true
		}
	}
	return false
}

// setupRestoreRunCLI wires the real `ops restore run` command against a temp
// home and a fake target, and captures the notification events. It resets the
// package-level flag variables so subtests do not leak into each other.
func setupRestoreRunCLI(t *testing.T) (*cliRestoreExecutor, *[]notify.Event) {
	t.Helper()
	setTestHome(t, t.TempDir())

	job := backup.Job{
		Name:    "my-app",
		Server:  "vps-01",
		Paths:   []string{"/data"},
		Backend: "/mnt/repo",
	}
	if err := backup.NewDefaultStore().Save(job); err != nil {
		t.Fatalf("seed backup store: %v", err)
	}
	serverStore := server.NewDefaultStore()
	for _, srv := range []server.Server{
		{Name: "vps-01", Host: "10.0.0.1", User: "root"},
		{Name: "vps-02", Host: "10.0.0.2", User: "root"},
	} {
		if err := serverStore.Save(srv); err != nil {
			t.Fatalf("seed server store: %v", err)
		}
	}

	fake := &cliRestoreExecutor{}
	origExec := newRestoreExecutor
	newRestoreExecutor = func(*server.Store) executor.Executor { return fake }
	t.Cleanup(func() { newRestoreExecutor = origExec })

	events := &[]notify.Event{}
	origNotify := notifyRestoreResult
	notifyRestoreResult = func(_ context.Context, event notify.Event) []error {
		*events = append(*events, event)
		return nil
	}
	t.Cleanup(func() { notifyRestoreResult = origNotify })

	resetRestoreRunFlags()
	t.Cleanup(resetRestoreRunFlags)

	return fake, events
}

// resetRestoreRunFlags restores the package-level flag variables the command
// reads, mirroring the defaults declared in init.
func resetRestoreRunFlags() {
	restoreRunSnapshot = "latest"
	restoreRunTargetServer = ""
	restoreRunTargetPath = ""
	restoreRunAssetID = ""
	restoreRunDryRun = false
	restoreRunStart = false
	restoreRunNoStart = false
	restoreRunAs = ""
	restoreRunYes = false
}

// runRestoreRunCLI parses args onto the command's real flag set and executes
// RunE, the same path `ops restore run` takes once cobra has parsed the line.
func runRestoreRunCLI(t *testing.T, args ...string) error {
	t.Helper()
	if err := restoreRunCmd.Flags().Parse(args); err != nil {
		t.Fatalf("parse restore run flags: %v", err)
	}
	return restoreRunCmd.RunE(restoreRunCmd, []string{"my-app"})
}

// TestRestoreRunCmd_StartPolicyEndToEnd drives the whole command: the default
// must leave a cross-machine file restore without a manifest stopped, --no-start
// must skip every container step, and --start must run the generic auto-start.
// It also pins the per-step report the operator and the notification see.
func TestRestoreRunCmd_StartPolicyEndToEnd(t *testing.T) {
	cases := []struct {
		name              string
		args              []string
		wantStarted       bool
		wantComposeStatus string
		wantStdout        string
	}{
		{
			name:              "cross-machine default leaves services stopped",
			args:              []string{"--target-server", "vps-02", "--yes"},
			wantStarted:       false,
			wantComposeStatus: backup.RestoreStepSkipped,
			wantStdout:        "未发现与本任务匹配的容器 manifest",
		},
		{
			name:              "--no-start restores files only",
			args:              []string{"--target-server", "vps-02", "--no-start", "--yes"},
			wantStarted:       false,
			wantComposeStatus: backup.RestoreStepSkipped,
			wantStdout:        "已按 --no-start 跳过",
		},
		{
			name:              "--start runs the generic auto-start",
			args:              []string{"--target-server", "vps-02", "--start", "--yes"},
			wantStarted:       true,
			wantComposeStatus: backup.RestoreStepSuccess,
			wantStdout:        "通用 auto-start 已启动",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake, events := setupRestoreRunCLI(t)

			var err error
			stdout := captureStdout(t, func() {
				err = runRestoreRunCLI(t, tc.args...)
			})
			if err != nil {
				t.Fatalf("restore run returned error: %v\noutput:\n%s", err, stdout)
			}

			if got := fake.ran("autostart-"); got != tc.wantStarted {
				t.Errorf("auto-start ran = %v, want %v (tasks: %v)", got, tc.wantStarted, fake.tasks)
			}

			// The report must list all four steps with their verdicts.
			for _, want := range []string{"恢复结果", "文件恢复", "volume 恢复", "数据库导入", "Compose 启动"} {
				if !strings.Contains(stdout, want) {
					t.Errorf("restore report is missing %q:\n%s", want, stdout)
				}
			}
			if !strings.Contains(stdout, tc.wantStdout) {
				t.Errorf("restore report is missing %q:\n%s", tc.wantStdout, stdout)
			}

			if len(*events) != 1 {
				t.Fatalf("notified events = %d, want 1", len(*events))
			}
			event := (*events)[0]
			if event.Kind != notify.EventKindRestore {
				t.Errorf("event kind = %q, want %q", event.Kind, notify.EventKindRestore)
			}
			if event.Status != "success" || event.Server != "vps-02" {
				t.Errorf("event status/server = %q/%q, want success/vps-02", event.Status, event.Server)
			}
			if event.Snapshot == "" {
				t.Error("the notification must carry the restored snapshot")
			}
			if len(event.Steps) != 4 {
				t.Fatalf("event steps = %d, want 4", len(event.Steps))
			}
			compose := event.Steps[3]
			if compose.Kind != backup.RestoreStepCompose || compose.Status != tc.wantComposeStatus {
				t.Errorf("compose step = %+v, want %s/%s", compose, backup.RestoreStepCompose, tc.wantComposeStatus)
			}
			if !tc.wantStarted && len(event.Pending) == 0 {
				t.Error("a restore that left the services stopped must notify the follow-up")
			}
		})
	}
}

// TestRestoreRunCmd_MutuallyExclusiveStartFlags makes sure the two switches are
// rejected before anything runs: silently picking one would hide a scripted
// mistake.
func TestRestoreRunCmd_MutuallyExclusiveStartFlags(t *testing.T) {
	fake, events := setupRestoreRunCLI(t)

	err := runRestoreRunCLI(t, "--target-server", "vps-02", "--start", "--no-start", "--yes")
	if err == nil {
		t.Fatal("expected --start with --no-start to be rejected")
	}
	if !strings.Contains(err.Error(), "--start") || !strings.Contains(err.Error(), "--no-start") {
		t.Errorf("error should name both flags, got: %v", err)
	}
	if len(fake.tasks) != 0 {
		t.Errorf("nothing should run when the flags conflict, ran: %v", fake.tasks)
	}
	if len(*events) != 0 {
		t.Errorf("a rejected run must not notify, got %d events", len(*events))
	}
}
