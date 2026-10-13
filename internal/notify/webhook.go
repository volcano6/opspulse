package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// WebhookNotifier sends event notifications to generic HTTP/HTTPS webhooks (Slack, Discord, Feishu, etc.).
type WebhookNotifier struct {
	channel Channel
	client  *http.Client
}

// NewWebhookNotifier creates a WebhookNotifier for the given channel.
func NewWebhookNotifier(channel Channel, client *http.Client) *WebhookNotifier {
	if client == nil {
		client = &http.Client{
			Timeout: 10 * time.Second,
		}
	}
	return &WebhookNotifier{
		channel: channel,
		client:  client,
	}
}

// Channel returns the channel definition.
func (w *WebhookNotifier) Channel() Channel {
	return w.channel
}

type webhookPayload struct {
	Event           string    `json:"event"`
	JobName         string    `json:"job_name"`
	Status          string    `json:"status"`
	Server          string    `json:"server"`
	Snapshot        string    `json:"snapshot,omitempty"`
	DurationSeconds float64   `json:"duration_seconds"`
	Error           string    `json:"error,omitempty"`
	Timestamp       time.Time `json:"timestamp"`
	Text            string    `json:"text"`
	Content         string    `json:"content"`
	// Untracked mirrors Event.Untracked so machine consumers can tell an
	// unaudited success apart from a normal one.
	Untracked bool `json:"untracked,omitempty"`
}

// Send serializes the event into JSON and performs an HTTP POST request to the webhook URL with retries.
func (w *WebhookNotifier) Send(ctx context.Context, event Event) error {
	summaryText := formatSummaryText(event)

	payload := webhookPayload{
		Event:           "backup_" + event.Status,
		JobName:         event.JobName,
		Status:          event.Status,
		Server:          event.Server,
		Snapshot:        event.Snapshot,
		DurationSeconds: event.DurationSeconds,
		Error:           event.Error,
		Timestamp:       event.Timestamp,
		Text:            summaryText,
		Content:         summaryText,
		Untracked:       event.Untracked,
	}

	bodyData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal webhook payload: %w", err)
	}

	const maxAttempts = 3
	backoffs := []time.Duration{100 * time.Millisecond, 300 * time.Millisecond}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.channel.URL, bytes.NewReader(bodyData))
		if err != nil {
			// url.Error echoes the full URL, token included, so the message is
			// rebuilt from the redacted form while the cause stays unwrappable.
			return &redactedError{
				message: fmt.Sprintf("failed to create webhook request for %q (%s): %s",
					w.channel.Name, RedactURL(w.channel.URL), redactSecretOccurrences(err.Error(), w.channel.URL)),
				cause: err,
			}
		}

		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		req.Header.Set("User-Agent", "OpsPulse-Notifier/1.0")

		resp, err := w.client.Do(req)
		if err != nil {
			lastErr = &redactedError{
				message: fmt.Sprintf("webhook delivery to %q (%s) failed: %s",
					w.channel.Name, RedactURL(w.channel.URL), redactSecretOccurrences(err.Error(), w.channel.URL)),
				cause: err,
			}
		} else {
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				_ = resp.Body.Close()
				return nil
			}

			bodySnippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			_ = resp.Body.Close()
			trimmed := strings.TrimSpace(string(bodySnippet))
			if trimmed == "" {
				trimmed = "(empty response)"
			}
			// A provider that rejects a bad token usually echoes the request
			// URL back in its error body, so the snippet is redacted too.
			lastErr = fmt.Errorf("webhook %q (%s) returned non-2xx status %d: %s",
				w.channel.Name, RedactURL(w.channel.URL), resp.StatusCode, redactSecretOccurrences(trimmed, w.channel.URL))

			// Client errors (4xx) should not be retried
			if resp.StatusCode < 500 {
				return lastErr
			}
		}

		if attempt == maxAttempts-1 {
			break
		}

		delay := 300 * time.Millisecond
		if attempt < len(backoffs) {
			delay = backoffs[attempt]
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}

	return lastErr
}

func formatSummaryText(event Event) string {
	statusUpper := strings.ToUpper(event.Status)
	if event.Status == "success" {
		snapInfo := ""
		if event.Snapshot != "" {
			snapID := event.Snapshot
			if len(snapID) > 8 {
				snapID = snapID[:8]
			}
			snapInfo = fmt.Sprintf(", snapshot: %s", snapID)
		}
		if event.Untracked {
			return fmt.Sprintf("[OpsPulse] ⚠️ Backup job %q on %q SUCCEEDED but is UNTRACKED — its audit record could not be created (duration: %.2fs%s)",
				event.JobName, event.Server, event.DurationSeconds, snapInfo)
		}
		return fmt.Sprintf("[OpsPulse] ✅ Backup job %q on %q SUCCEEDED (duration: %.2fs%s)",
			event.JobName, event.Server, event.DurationSeconds, snapInfo)
	}

	errDetail := event.Error
	if errDetail == "" {
		errDetail = "unknown error"
	}
	untrackedNote := ""
	if event.Untracked {
		untrackedNote = " [UNTRACKED]"
	}
	return fmt.Sprintf("[OpsPulse] ❌ Backup job %q on %q %s: %s (duration: %.2fs)%s",
		event.JobName, event.Server, statusUpper, errDetail, event.DurationSeconds, untrackedNote)
}
