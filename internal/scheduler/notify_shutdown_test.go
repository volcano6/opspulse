package scheduler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/volcano6/opspulse/internal/backup"
	"github.com/volcano6/opspulse/internal/notify"
)

// TestScheduler_LateNotificationIsReportedAfterShutdown covers the one path
// where a notification could disappear without a trace: an event enqueued after
// the dispatcher stopped would sit in the queue no worker reads any more.
// Producers stop before shutdown, so this is a late event - and a late event has
// to be visible, not silent.
func TestScheduler_LateNotificationIsReportedAfterShutdown(t *testing.T) {
	var deliveries int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&deliveries, 1)
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
		t.Fatalf("Save() failed: %v", err)
	}

	dispatcher := notify.NewDispatcherWithClient(notifyStore, server.Client())
	var buf bytes.Buffer
	sched := New(backup.NewStore(filepath.Join(tmpDir, "backups.yaml")), nil, dispatcher, &buf)

	sched.shutdownDispatcher(time.Second)
	sched.enqueueNotification(notify.Event{JobName: "late-job"})

	// Give a worker a chance to pick the event up if the guard were missing.
	time.Sleep(50 * time.Millisecond)

	if !strings.Contains(buf.String(), "already stopped") || !strings.Contains(buf.String(), "late-job") {
		t.Errorf("late notification was dropped silently; output was:\n%s", buf.String())
	}
	if got := atomic.LoadInt32(&deliveries); got != 0 {
		t.Errorf("deliveries = %d, want 0: nothing may be delivered after shutdown", got)
	}
}
