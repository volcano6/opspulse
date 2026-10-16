package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/volcano6/opspulse/internal/backup"
	"github.com/volcano6/opspulse/internal/notify"
	"github.com/volcano6/opspulse/internal/storage"
)

// restoreWebhookPayload is the wire shape this test asserts on. It is declared
// here on purpose: the test then pins what a consumer receives instead of
// mirroring an internal struct.
type restoreWebhookPayload struct {
	Event           string  `json:"event"`
	JobName         string  `json:"job_name"`
	Status          string  `json:"status"`
	Server          string  `json:"server"`
	Snapshot        string  `json:"snapshot"`
	DurationSeconds float64 `json:"duration_seconds"`
	Error           string  `json:"error"`
	Text            string  `json:"text"`
	Steps           []struct {
		Kind     string `json:"kind"`
		Label    string `json:"label"`
		Status   string `json:"status"`
		Detail   string `json:"detail"`
		NextStep string `json:"next_step"`
	} `json:"steps"`
	Warnings []string `json:"warnings"`
	Pending  []string `json:"pending"`
}

// TestRestoreNotificationPayloadCarriesStepsWarningsAndPending drives the real
// notification path (dispatcher + webhook notifier) against an httptest server:
// a partial restore must reach a failure-triggered channel carrying the
// snapshot, the duration, the per-step verdicts, the warnings and the work that
// is still outstanding.
func TestRestoreNotificationPayloadCarriesStepsWarningsAndPending(t *testing.T) {
	setTestHome(t, t.TempDir())

	var got restoreWebhookPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("decode webhook body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	store := notify.NewDefaultStore()
	// No "on" value: the default trigger is failure, which a partial restore
	// has to satisfy.
	if err := store.Save(notify.Channel{Name: "restore-ops", Type: notify.TypeWebhook, URL: server.URL}); err != nil {
		t.Fatalf("save notification channel: %v", err)
	}

	result := &backup.RestoreResult{
		RestoreRun: &storage.RestoreRun{
			JobName:         "blog",
			SnapshotID:      "snap-abcdef1234567890",
			SourceServer:    "vps-01",
			TargetServer:    "vps-02",
			Status:          "partial",
			DurationSeconds: 42.5,
			ErrorMessage:    "restore completed but container auto-start failed: compose up failed",
		},
		Steps: []backup.RestoreStep{
			{Kind: backup.RestoreStepFiles, Label: "文件恢复", Status: backup.RestoreStepSuccess, Detail: "快照已还原"},
			{Kind: backup.RestoreStepVolumes, Label: "volume 恢复", Status: backup.RestoreStepSkipped, Detail: "快照中没有命名卷需要恢复"},
			{Kind: backup.RestoreStepDatabase, Label: "数据库导入", Status: backup.RestoreStepSkipped, Detail: "快照中没有数据库导出需要导入"},
			{Kind: backup.RestoreStepCompose, Label: "Compose 启动", Status: backup.RestoreStepBlocked,
				Detail: "docker compose 未安装", NextStep: "在目标机手工执行 docker compose up -d"},
		},
		Warnings: []string{"external host mount /srv/data is missing on target vps-02"},
		Pending:  []string{"在目标机准备宿主机挂载 /srv/data"},
	}

	if errs := notifyRestoreResult(context.Background(), restoreNotificationEvent(result)); len(errs) > 0 {
		t.Fatalf("notification delivery failed: %v", errs)
	}

	if got.Event != "restore_partial" {
		t.Errorf("event = %q, want restore_partial", got.Event)
	}
	if got.JobName != "blog" || got.Status != "partial" || got.Server != "vps-02" {
		t.Errorf("job/status/server = %q/%q/%q, want blog/partial/vps-02", got.JobName, got.Status, got.Server)
	}
	if got.Snapshot != "snap-abcdef1234567890" {
		t.Errorf("snapshot = %q, want the full snapshot ID", got.Snapshot)
	}
	if got.DurationSeconds != 42.5 {
		t.Errorf("duration_seconds = %v, want 42.5", got.DurationSeconds)
	}
	if got.Error == "" {
		t.Error("a partial restore must carry the failure that made it partial")
	}

	if len(got.Steps) != 4 {
		t.Fatalf("steps = %d, want 4: %+v", len(got.Steps), got.Steps)
	}
	wantKinds := []string{backup.RestoreStepFiles, backup.RestoreStepVolumes, backup.RestoreStepDatabase, backup.RestoreStepCompose}
	for i, want := range wantKinds {
		if got.Steps[i].Kind != want {
			t.Errorf("step %d kind = %q, want %q", i, got.Steps[i].Kind, want)
		}
		if got.Steps[i].Status == "" {
			t.Errorf("step %d has no status", i)
		}
	}
	compose := got.Steps[3]
	if compose.Status != backup.RestoreStepBlocked || compose.NextStep == "" {
		t.Errorf("compose step = %+v, want blocked with a next step", compose)
	}

	if len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "/srv/data") {
		t.Errorf("warnings = %v, want the missing host mount", got.Warnings)
	}
	if len(got.Pending) != 1 || !strings.Contains(got.Pending[0], "/srv/data") {
		t.Errorf("pending = %v, want the outstanding host mount", got.Pending)
	}

	for _, want := range []string{"steps:", "warnings:", "pending:", "compose=blocked", "snapshot: snap-abc"} {
		if !strings.Contains(got.Text, want) {
			t.Errorf("notification text is missing %q:\n%s", want, got.Text)
		}
	}
}

// TestRestoreNotificationEvent_KeepsBackupPayloadShape pins the other half of
// the contract: a restore event must not be mistaken for a backup event.
func TestRestoreNotificationEvent_KeepsBackupPayloadShape(t *testing.T) {
	result := &backup.RestoreResult{
		RestoreRun: &storage.RestoreRun{JobName: "blog", Status: "success", TargetServer: "vps-02"},
		Steps:      []backup.RestoreStep{{Kind: backup.RestoreStepFiles, Label: "文件恢复", Status: backup.RestoreStepSuccess}},
	}

	event := restoreNotificationEvent(result)
	if event.Kind != notify.EventKindRestore {
		t.Errorf("event.Kind = %q, want %q", event.Kind, notify.EventKindRestore)
	}
	if event.Server != "vps-02" {
		t.Errorf("event.Server = %q, want the restore target", event.Server)
	}
	if len(event.Steps) != 1 || event.Steps[0].Label != "文件恢复" {
		t.Errorf("event.Steps = %+v, want the file step carried over", event.Steps)
	}
}
