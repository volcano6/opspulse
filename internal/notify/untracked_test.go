package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestFormatSummaryText_Untracked pins the human-facing marker: an untracked
// success must not render like an ordinary success.
func TestFormatSummaryText_Untracked(t *testing.T) {
	base := Event{JobName: "web-data", Status: "success", Server: "vps-01", DurationSeconds: 1.5}

	normal := formatSummaryText(base)
	if strings.Contains(normal, "UNTRACKED") {
		t.Errorf("normal success text = %q; must not mention UNTRACKED", normal)
	}

	base.Untracked = true
	untracked := formatSummaryText(base)
	if !strings.Contains(untracked, "UNTRACKED") {
		t.Errorf("untracked success text = %q; want it to mention UNTRACKED", untracked)
	}
	if untracked == normal {
		t.Error("an untracked success rendered identically to a normal success")
	}

	failed := formatSummaryText(Event{
		JobName: "web-data", Status: "failed", Server: "vps-01",
		Error: "boom", DurationSeconds: 1, Untracked: true,
	})
	if !strings.Contains(failed, "UNTRACKED") {
		t.Errorf("untracked failure text = %q; want it to mention UNTRACKED", failed)
	}
}

func sendEvent(t *testing.T, event Event) []byte {
	t.Helper()

	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	notifier := NewWebhookNotifier(Channel{
		Name: "test-webhook",
		Type: "webhook",
		URL:  server.URL,
		On:   "always",
	}, server.Client())

	if err := notifier.Send(context.Background(), event); err != nil {
		t.Fatalf("Send() error: %v", err)
	}
	return body
}

// TestDispatcher_UntrackedSuccessReachesFailureTrigger pins the routing
// contract: an untracked success keeps status "success", so a channel that
// relies on the documented "failure" default - the trigger a channel uses when
// it sets no `on` - would drop it unless the untracked flag drives the routing
// decision. Without that, the default configuration never learns about the gap.
func TestDispatcher_UntrackedSuccessReachesFailureTrigger(t *testing.T) {
	var deliveries int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&deliveries, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	store := NewStore(filepath.Join(t.TempDir(), "notifications.yaml"))
	if err := store.Save(Channel{Name: "ops-alerts", Type: "webhook", URL: server.URL}); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	dispatcher := NewDispatcherWithClient(store, server.Client())
	ctx := context.Background()

	if errs := dispatcher.Dispatch(ctx, Event{
		JobName: "web-data", Status: "success", Server: "vps-01", Untracked: true, Timestamp: time.Now(),
	}); len(errs) > 0 {
		t.Fatalf("Dispatch(untracked success) errors: %v", errs)
	}
	if got := atomic.LoadInt32(&deliveries); got != 1 {
		t.Errorf("deliveries = %d, want 1: a failure-triggered channel must hear about an untracked success", got)
	}

	// Negative control: the same channel must stay quiet for an audited success.
	if errs := dispatcher.Dispatch(ctx, Event{
		JobName: "web-data", Status: "success", Server: "vps-01", Timestamp: time.Now(),
	}); len(errs) > 0 {
		t.Fatalf("Dispatch(audited success) errors: %v", errs)
	}
	if got := atomic.LoadInt32(&deliveries); got != 1 {
		t.Errorf("deliveries = %d, want 1: an audited success must not reach a failure-triggered channel", got)
	}
}

// TestDispatcher_UntrackedSuccessDoesNotReachSuccessTrigger is the counterpart
// to the failure-routing contract: an audit gap must never be announced on a
// success-only channel as if the run were a normal, audited success.
func TestDispatcher_UntrackedSuccessDoesNotReachSuccessTrigger(t *testing.T) {
	var deliveries int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&deliveries, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	store := NewStore(filepath.Join(t.TempDir(), "notifications.yaml"))
	if err := store.Save(Channel{Name: "success-only", Type: "webhook", URL: server.URL, On: "success"}); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	dispatcher := NewDispatcherWithClient(store, server.Client())

	if errs := dispatcher.Dispatch(context.Background(), Event{
		JobName: "web-data", Status: "success", Server: "vps-01", Untracked: true, Timestamp: time.Now(),
	}); len(errs) > 0 {
		t.Fatalf("Dispatch(untracked success) errors: %v", errs)
	}
	if got := atomic.LoadInt32(&deliveries); got != 0 {
		t.Errorf("deliveries = %d, want 0: a success-only channel must not report an unaudited run as success", got)
	}
}

// TestWebhookNotifier_Send_Untracked pins the machine-facing marker.
func TestWebhookNotifier_Send_Untracked(t *testing.T) {
	body := sendEvent(t, Event{
		JobName:         "web-data",
		Status:          "success",
		Server:          "vps-01",
		DurationSeconds: 1.5,
		Timestamp:       time.Now(),
		Untracked:       true,
	})

	var payload webhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if !payload.Untracked {
		t.Error("payload.Untracked = false; want true")
	}
	if !strings.Contains(payload.Text, "UNTRACKED") {
		t.Errorf("payload.Text = %q; want it to mention UNTRACKED", payload.Text)
	}
	if !strings.Contains(string(body), `"untracked":true`) {
		t.Errorf("raw body = %s; want the untracked key present", body)
	}
}

// TestWebhookNotifier_Send_OmitsUntrackedWhenFalse keeps the payload of an
// ordinary run byte-compatible with previous releases.
func TestWebhookNotifier_Send_OmitsUntrackedWhenFalse(t *testing.T) {
	body := sendEvent(t, Event{
		JobName:         "web-data",
		Status:          "success",
		Server:          "vps-01",
		DurationSeconds: 1.5,
		Timestamp:       time.Now(),
	})

	if strings.Contains(string(body), "untracked") {
		t.Errorf("raw body = %s; want no untracked key for an audited run", body)
	}
}
