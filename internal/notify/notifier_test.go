package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestChannel_Validate(t *testing.T) {
	tests := []struct {
		name    string
		channel Channel
		wantErr bool
	}{
		{
			name: "valid webhook with default trigger",
			channel: Channel{
				Name: "slack-alerts",
				Type: "webhook",
				URL:  "https://hooks.slack.com/services/xxx",
			},
			wantErr: false,
		},
		{
			name: "valid webhook with explicit trigger",
			channel: Channel{
				Name: "discord-success",
				Type: "webhook",
				URL:  "http://localhost:8080/webhook",
				On:   "success",
			},
			wantErr: false,
		},
		{
			name: "empty name",
			channel: Channel{
				Name: "",
				Type: "webhook",
				URL:  "https://example.com/webhook",
			},
			wantErr: true,
		},
		{
			name: "unsupported type",
			channel: Channel{
				Name: "telegram-bot",
				Type: "telegram",
				URL:  "https://example.com/webhook",
			},
			wantErr: true,
		},
		{
			name: "invalid url scheme",
			channel: Channel{
				Name: "ftp-channel",
				Type: "webhook",
				URL:  "ftp://example.com/webhook",
			},
			wantErr: true,
		},
		{
			name: "empty url",
			channel: Channel{
				Name: "empty-url",
				Type: "webhook",
				URL:  "",
			},
			wantErr: true,
		},
		{
			name: "invalid trigger condition",
			channel: Channel{
				Name: "bad-trigger",
				Type: "webhook",
				URL:  "https://example.com/webhook",
				On:   "sometimes",
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.channel.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestStore_CRUD(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "notifications.yaml")
	store := NewStore(filePath)

	// 1. Initial list on empty store
	channels, err := store.List()
	if err != nil {
		t.Fatalf("List() failed: %v", err)
	}
	if len(channels) != 0 {
		t.Fatalf("List() returned %d channels, want 0", len(channels))
	}

	// 2. Save a new channel
	ch1 := Channel{
		Name: "slack-ops",
		Type: "webhook",
		URL:  "https://hooks.slack.com/services/aaa",
		On:   "failure",
	}
	if err := store.Save(ch1); err != nil {
		t.Fatalf("Save(ch1) failed: %v", err)
	}

	// 3. Get channel
	got, err := store.Get("slack-ops")
	if err != nil {
		t.Fatalf("Get('slack-ops') failed: %v", err)
	}
	if got.URL != ch1.URL || got.On != "failure" {
		t.Errorf("Get() = %+v, want %+v", got, ch1)
	}

	// 4. Update channel
	ch1Updated := Channel{
		Name: "slack-ops",
		Type: "webhook",
		URL:  "https://hooks.slack.com/services/bbb",
		On:   "always",
	}
	if err := store.Save(ch1Updated); err != nil {
		t.Fatalf("Save(ch1Updated) failed: %v", err)
	}
	gotUpdated, err := store.Get("slack-ops")
	if err != nil {
		t.Fatalf("Get updated failed: %v", err)
	}
	if gotUpdated.URL != "https://hooks.slack.com/services/bbb" || gotUpdated.On != "always" {
		t.Errorf("Get() after update = %+v", gotUpdated)
	}

	// 5. Add a second channel
	ch2 := Channel{
		Name: "discord-ops",
		Type: "webhook",
		URL:  "https://discord.com/api/webhooks/xxx",
		On:   "success",
	}
	if err := store.Save(ch2); err != nil {
		t.Fatalf("Save(ch2) failed: %v", err)
	}

	list, err := store.List()
	if err != nil {
		t.Fatalf("List() failed: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List() returned %d channels, want 2", len(list))
	}

	// 6. Delete channel
	if err := store.Delete("slack-ops"); err != nil {
		t.Fatalf("Delete('slack-ops') failed: %v", err)
	}
	listAfterDelete, err := store.List()
	if err != nil {
		t.Fatalf("List() failed: %v", err)
	}
	if len(listAfterDelete) != 1 || listAfterDelete[0].Name != "discord-ops" {
		t.Errorf("List() after delete unexpected: %+v", listAfterDelete)
	}

	// 7. Delete non-existent channel
	if err := store.Delete("non-existent"); err == nil {
		t.Error("expected error when deleting non-existent channel, got nil")
	}
}

func TestStore_StrictValidation(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("rejects duplicate channel names", func(t *testing.T) {
		path := filepath.Join(tmpDir, "dup.yaml")
		data := []byte(`channels:
  - name: my-channel
    type: webhook
    url: https://example.com/1
  - name: my-channel
    type: webhook
    url: https://example.com/2
`)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		store := NewStore(path)
		if _, err := store.List(); err == nil {
			t.Fatal("List() accepted duplicate channel names")
		}
	})

	t.Run("rejects unknown fields", func(t *testing.T) {
		path := filepath.Join(tmpDir, "unknown.yaml")
		data := []byte(`channels:
  - name: my-channel
    type: webhook
    url: https://example.com/1
    unknown_prop: invalid
`)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		store := NewStore(path)
		if _, err := store.List(); err == nil {
			t.Fatal("List() accepted unknown YAML field")
		}
	})
}

func TestWebhookNotifier_Send(t *testing.T) {
	var receivedPayload webhookPayload
	var receivedHeaders http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &receivedPayload)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok": true}`))
	}))
	defer server.Close()

	ch := Channel{
		Name: "test-webhook",
		Type: "webhook",
		URL:  server.URL,
		On:   "always",
	}

	notifier := NewWebhookNotifier(ch, server.Client())
	event := Event{
		JobName:         "blog-backup",
		Status:          "success",
		Server:          "vps-01",
		Snapshot:        "d4e5f6a7b8",
		DurationSeconds: 15.67,
		Timestamp:       time.Now(),
	}

	ctx := context.Background()
	if err := notifier.Send(ctx, event); err != nil {
		t.Fatalf("Send() failed: %v", err)
	}

	if receivedHeaders.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want application/json; charset=utf-8", receivedHeaders.Get("Content-Type"))
	}
	if receivedPayload.JobName != "blog-backup" {
		t.Errorf("payload.JobName = %q, want blog-backup", receivedPayload.JobName)
	}
	if receivedPayload.Status != "success" {
		t.Errorf("payload.Status = %q, want success", receivedPayload.Status)
	}
	if receivedPayload.Text == "" {
		t.Error("payload.Text must not be empty")
	}
}

func TestWebhookNotifier_Send_ErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`invalid token`))
	}))
	defer server.Close()

	ch := Channel{
		Name: "failing-webhook",
		Type: "webhook",
		URL:  server.URL,
	}

	notifier := NewWebhookNotifier(ch, server.Client())
	event := Event{
		JobName:   "blog-backup",
		Status:    "failed",
		Server:    "vps-01",
		Error:     "disk full",
		Timestamp: time.Now(),
	}

	err := notifier.Send(context.Background(), event)
	if err == nil {
		t.Fatal("expected error on 400 Bad Request, got nil")
	}
}

func TestDispatcher_Filtering(t *testing.T) {
	var (
		failureCount int32
		successCount int32
		alwaysCount  int32
	)

	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&failureCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer failServer.Close()

	succServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&successCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer succServer.Close()

	alwaysServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&alwaysCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer alwaysServer.Close()

	tmpDir := t.TempDir()
	store := NewStore(filepath.Join(tmpDir, "notifications.yaml"))
	_ = store.Save(Channel{Name: "fail-ch", Type: "webhook", URL: failServer.URL, On: "failure"})
	_ = store.Save(Channel{Name: "succ-ch", Type: "webhook", URL: succServer.URL, On: "success"})
	_ = store.Save(Channel{Name: "alw-ch", Type: "webhook", URL: alwaysServer.URL, On: "always"})

	dispatcher := NewDispatcherWithClient(store, http.DefaultClient)
	ctx := context.Background()

	// 1. Dispatch a FAILURE event
	failEvent := Event{
		JobName:   "db-backup",
		Status:    "failed",
		Server:    "vps-01",
		Error:     "connection timeout",
		Timestamp: time.Now(),
	}
	errs := dispatcher.Dispatch(ctx, failEvent)
	if len(errs) > 0 {
		t.Fatalf("Dispatch(failed) errors: %v", errs)
	}

	if atomic.LoadInt32(&failureCount) != 1 {
		t.Errorf("fail-ch count = %d, want 1", failureCount)
	}
	if atomic.LoadInt32(&successCount) != 0 {
		t.Errorf("succ-ch count = %d, want 0", successCount)
	}
	if atomic.LoadInt32(&alwaysCount) != 1 {
		t.Errorf("alw-ch count = %d, want 1", alwaysCount)
	}

	// 2. Dispatch a SUCCESS event
	succEvent := Event{
		JobName:         "db-backup",
		Status:          "success",
		Server:          "vps-01",
		Snapshot:        "12345678",
		DurationSeconds: 5.0,
		Timestamp:       time.Now(),
	}
	errs = dispatcher.Dispatch(ctx, succEvent)
	if len(errs) > 0 {
		t.Fatalf("Dispatch(success) errors: %v", errs)
	}

	if atomic.LoadInt32(&failureCount) != 1 {
		t.Errorf("fail-ch count after success event = %d, want 1", failureCount)
	}
	if atomic.LoadInt32(&successCount) != 1 {
		t.Errorf("succ-ch count after success event = %d, want 1", successCount)
	}
	if atomic.LoadInt32(&alwaysCount) != 2 {
		t.Errorf("alw-ch count after success event = %d, want 2", alwaysCount)
	}
}

func TestDispatcher_SendTest(t *testing.T) {
	var testCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&testCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	store := NewStore(filepath.Join(tmpDir, "notifications.yaml"))
	_ = store.Save(Channel{Name: "test-ch", Type: "webhook", URL: server.URL})

	dispatcher := NewDispatcherWithClient(store, http.DefaultClient)
	if err := dispatcher.SendTest(context.Background(), "test-ch"); err != nil {
		t.Fatalf("SendTest() failed: %v", err)
	}
	if atomic.LoadInt32(&testCount) != 1 {
		t.Errorf("testCount = %d, want 1", testCount)
	}

	// Non-existent channel
	if err := dispatcher.SendTest(context.Background(), "non-existent"); err == nil {
		t.Error("expected error for non-existent channel in SendTest, got nil")
	}
}
