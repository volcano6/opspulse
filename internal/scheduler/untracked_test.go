package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/volcano6/opspulse/internal/backup"
	"github.com/volcano6/opspulse/internal/notify"
	"github.com/volcano6/opspulse/internal/storage"
)

// untrackedRunner reports an unaudited success without touching the network.
type untrackedRunner struct{}

func (untrackedRunner) Run(_ context.Context, job backup.Job, _ io.Writer) (*storage.BackupRun, error) {
	return &storage.BackupRun{
		JobName:    job.Name,
		ServerName: job.Server,
		Status:     "success",
		Untracked:  true,
	}, nil
}

// TestScheduler_ExecuteJobReportsUntrackedSuccess pins the daemon contract: an
// untracked success must surface as an error wrapping backup.ErrUntracked (so
// the process exits 3) and must reach notifications with the flag set.
func TestScheduler_ExecuteJobReportsUntrackedSuccess(t *testing.T) {
	payloadCh := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err == nil {
			select {
			case payloadCh <- payload:
			default:
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	notifyStore := notify.NewStore(filepath.Join(tmpDir, "notifications.yaml"))
	if err := notifyStore.Save(notify.Channel{
		Name: "alert-ch",
		Type: "webhook",
		URL:  server.URL,
		On:   "always",
	}); err != nil {
		t.Fatalf("Save(channel) error: %v", err)
	}

	backupStore := backup.NewStore(filepath.Join(tmpDir, "backups.yaml"))
	dispatcher := notify.NewDispatcherWithClient(notifyStore, server.Client())

	var buf bytes.Buffer
	sched := New(backupStore, untrackedRunner{}, dispatcher, &buf)

	job := backup.Job{Name: "sched-job", Server: "local", Backend: "/tmp/repo", Paths: []string{"/tmp/data"}}
	err := sched.executeJob(context.Background(), job)
	if !errors.Is(err, backup.ErrUntracked) {
		t.Fatalf("executeJob() error = %v; want it to wrap backup.ErrUntracked", err)
	}

	sched.shutdownDispatcher(5 * time.Second)

	select {
	case payload := <-payloadCh:
		if payload["untracked"] != true {
			t.Errorf("notification payload = %v; want untracked=true", payload)
		}
		if payload["status"] != "success" {
			t.Errorf("notification status = %v; want success alongside the untracked flag", payload["status"])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no notification was delivered for the untracked run")
	}
}

// TestScheduler_ExecuteJobKeepsAuditedSuccessQuiet is the negative control: a
// normal success must not be turned into an error or flagged as untracked.
func TestScheduler_ExecuteJobKeepsAuditedSuccessQuiet(t *testing.T) {
	var buf bytes.Buffer
	sched := New(backup.NewStore(filepath.Join(t.TempDir(), "backups.yaml")), auditedRunner{}, nil, &buf)

	job := backup.Job{Name: "sched-job", Server: "local", Backend: "/tmp/repo", Paths: []string{"/tmp/data"}}
	if err := sched.executeJob(context.Background(), job); err != nil {
		t.Fatalf("executeJob() error = %v; want nil for an audited success", err)
	}
}

type auditedRunner struct{}

func (auditedRunner) Run(_ context.Context, job backup.Job, _ io.Writer) (*storage.BackupRun, error) {
	return &storage.BackupRun{
		JobName:    job.Name,
		ServerName: job.Server,
		Status:     "success",
	}, nil
}
