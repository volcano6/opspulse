package backup

import (
	"bytes"
	"strings"
	"testing"

	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/storage"
)

// TestRestoreReport_AggregateStatus pins the backward-compatible aggregate
// vocabulary against every step status: a failed file restore is a failure,
// anything else failed or blocked is a partial recovery, and a deliberately
// skipped step is not a degradation.
func TestRestoreReport_AggregateStatus(t *testing.T) {
	cases := []struct {
		name  string
		steps map[string]string
		want  string
	}{
		{
			name:  "everything succeeded",
			steps: map[string]string{RestoreStepFiles: RestoreStepSuccess, RestoreStepVolumes: RestoreStepSuccess, RestoreStepDatabase: RestoreStepSuccess, RestoreStepCompose: RestoreStepSuccess},
			want:  "success",
		},
		{
			name:  "files only, container steps skipped by design",
			steps: map[string]string{RestoreStepFiles: RestoreStepSuccess, RestoreStepVolumes: RestoreStepSkipped, RestoreStepDatabase: RestoreStepSkipped, RestoreStepCompose: RestoreStepSkipped},
			want:  "success",
		},
		{
			name:  "file restore failed",
			steps: map[string]string{RestoreStepFiles: RestoreStepFailed, RestoreStepCompose: RestoreStepBlocked},
			want:  "failed",
		},
		{
			name:  "compose start failed after the files landed",
			steps: map[string]string{RestoreStepFiles: RestoreStepSuccess, RestoreStepCompose: RestoreStepFailed},
			want:  "partial",
		},
		{
			name:  "compose start blocked by a missing engine",
			steps: map[string]string{RestoreStepFiles: RestoreStepSuccess, RestoreStepCompose: RestoreStepBlocked},
			want:  "partial",
		},
		{
			name:  "database left to the operator",
			steps: map[string]string{RestoreStepFiles: RestoreStepSuccess, RestoreStepDatabase: RestoreStepManual},
			want:  "success",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := newRestoreReport(&storage.RestoreRun{Status: "running"})
			for kind, status := range tc.steps {
				result.setStep(kind, status, "", "")
			}
			if got := result.aggregateStatus(false); got != tc.want {
				t.Errorf("aggregateStatus() = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("dry run", func(t *testing.T) {
		result := newRestoreReport(&storage.RestoreRun{Status: "running"})
		result.setStep(RestoreStepFiles, RestoreStepSuccess, "", "")
		if got := result.aggregateStatus(true); got != "dry-run" {
			t.Errorf("aggregateStatus(dryRun=true) = %q, want dry-run", got)
		}
	})
}

// TestNewRestoreReport_SeedsEveryStep pins that a step the run never reached is
// still reported: a result that silently omits a step is exactly what the
// per-step report exists to prevent.
func TestNewRestoreReport_SeedsEveryStep(t *testing.T) {
	result := newRestoreReport(&storage.RestoreRun{Status: "running"})
	if len(result.Steps) != len(restoreStepOrder) {
		t.Fatalf("report seeded %d steps, want %d", len(result.Steps), len(restoreStepOrder))
	}
	for i, want := range restoreStepOrder {
		if result.Steps[i].Kind != want.kind || result.Steps[i].Label != want.label {
			t.Errorf("step %d = %+v, want kind %q label %q", i, result.Steps[i], want.kind, want.label)
		}
		if result.Steps[i].Status != RestoreStepSkipped {
			t.Errorf("step %d status = %q, want skipped", i, result.Steps[i].Status)
		}
	}
}

// TestClassifyStepStatus pins the one distinction the runner can make from a
// remote result: exit 127 means the target is missing a prerequisite (Compose
// engine, helper image), which is a blocker rather than a failure.
func TestClassifyStepStatus(t *testing.T) {
	if got := classifyStepStatus(&executor.Result{ExitCode: 127}); got != RestoreStepBlocked {
		t.Errorf("classifyStepStatus(exit 127) = %q, want blocked", got)
	}
	if got := classifyStepStatus(&executor.Result{ExitCode: 1}); got != RestoreStepFailed {
		t.Errorf("classifyStepStatus(exit 1) = %q, want failed", got)
	}
	if got := classifyStepStatus(nil); got != RestoreStepFailed {
		t.Errorf("classifyStepStatus(nil) = %q, want failed", got)
	}
}

// TestWriteRestoreResult_RendersStepsWarningsAndPending pins the console
// contract: every step, its next command, the warnings and the outstanding
// items are visible without opening the log file.
func TestWriteRestoreResult_RendersStepsWarningsAndPending(t *testing.T) {
	result := newRestoreReport(&storage.RestoreRun{
		JobName:      "blog",
		SnapshotID:   "snap-abcdef1234567890",
		TargetServer: "vps-02",
		Status:       "partial",
	})
	result.setStep(RestoreStepFiles, RestoreStepSuccess, "快照已还原", "")
	result.setStep(RestoreStepCompose, RestoreStepBlocked, "docker compose 未安装", "在目标机手工执行 docker compose up -d")
	result.addWarning("external host mount /srv/data is missing on target vps-02")
	result.addPending("在目标机准备宿主机挂载 /srv/data")

	var buf bytes.Buffer
	writeRestoreResult(&buf, result, 12.34)
	out := buf.String()

	for _, want := range []string{
		`任务 "blog"`,
		"snap-abc",
		"vps-02",
		"12.34s",
		"PARTIAL",
		"[success] 文件恢复：快照已还原",
		"[blocked] Compose 启动：docker compose 未安装",
		"下一步：在目标机手工执行 docker compose up -d",
		"警告：",
		"/srv/data",
		"未完成事项：",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("restore result is missing %q:\n%s", want, out)
		}
	}
}

// TestRestoreStartHint_CarriesTheOperatorsOptions pins the follow-up command:
// it has to be runnable as printed, so the target options the operator already
// chose are repeated instead of being silently dropped.
func TestRestoreStartHint_CarriesTheOperatorsOptions(t *testing.T) {
	got := restoreStartHint("blog", RestoreOptions{
		TargetServer: "vps-02",
		TargetPath:   "/data/blog",
		AliasName:    "blog-v2",
	})
	want := "ops restore run blog --target-server vps-02 --target-path /data/blog --as blog-v2 --start"
	if got != want {
		t.Errorf("restoreStartHint() = %q, want %q", got, want)
	}

	if got := restoreStartHint("blog", RestoreOptions{}); got != "ops restore run blog --start" {
		t.Errorf("restoreStartHint() with no options = %q", got)
	}
}
