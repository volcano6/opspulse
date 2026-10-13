package notify

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A webhook token is the credential itself: it lives in the URL, so every
// string that leaves OpsPulse (list output, error text, logs, notifications)
// must carry the redacted form only.
func TestRedactURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "slack token in path",
			raw:  "https://hooks.slack.com/services/T0000000/B0000000/XXXXXXXXXXXXXXXXXXXXXXXX",
			want: "https://hooks.slack.com/<redacted>",
		},
		{
			name: "discord token in path",
			raw:  "https://discord.com/api/webhooks/000000000000000000/XXXXXXXXXXXXXXXXXXXXXXXX",
			want: "https://discord.com/<redacted>",
		},
		{
			name: "token in query string",
			raw:  "https://alerts.example.com/notify?token=supersecret",
			want: "https://alerts.example.com/<redacted>",
		},
		{
			name: "userinfo is a credential too",
			raw:  "https://bot:supersecret@alerts.example.com/hook",
			want: "https://<redacted>@alerts.example.com/<redacted>",
		},
		{
			name: "bare host carries no secret",
			raw:  "https://alerts.example.com",
			want: "https://alerts.example.com",
		},
		{
			name: "bare host with root path",
			raw:  "https://alerts.example.com/",
			want: "https://alerts.example.com",
		},
		{
			name: "port is preserved for diagnosis",
			raw:  "http://127.0.0.1:8080/hooks/abcdef",
			want: "http://127.0.0.1:8080/<redacted>",
		},
		{
			name: "unparsable value is replaced wholesale",
			raw:  "://not a url",
			want: "<redacted-url>",
		},
		{
			name: "empty stays empty",
			raw:  "   ",
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := RedactURL(tc.raw); got != tc.want {
				t.Errorf("RedactURL(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// TestWebhookNotifier_SendErrorsDoNotLeakToken covers the transport-level
// failures: net/http wraps the request URL into *url.Error, so a naive
// fmt.Errorf("%w") would print the token into the terminal, the daemon log and
// any notification that carries the error text.
func TestWebhookNotifier_SendErrorsDoNotLeakToken(t *testing.T) {
	const token = "T0000000/B0000000/SUPERSECRETTOKEN"
	event := Event{JobName: "blog", Status: "failed", Server: "vps-01", Error: "restic failed"}

	t.Run("connection failure", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		url := srv.URL + "/services/" + token
		srv.Close() // closed port: client.Do fails with a *url.Error carrying the URL

		notifier := NewWebhookNotifier(Channel{Name: "slack-ops", Type: TypeWebhook, URL: url}, srv.Client())
		err := notifier.Send(context.Background(), event)
		if err == nil {
			t.Fatal("Send() error = nil, want a connection failure")
		}
		assertNoTokenLeak(t, err.Error(), token, srv.URL)
	})

	t.Run("malformed url", func(t *testing.T) {
		raw := "http://\x7f/services/" + token
		notifier := NewWebhookNotifier(Channel{Name: "slack-ops", Type: TypeWebhook, URL: raw}, nil)
		err := notifier.Send(context.Background(), event)
		if err == nil {
			t.Fatal("Send() error = nil, want a request-creation failure")
		}
		assertNoTokenLeak(t, err.Error(), token, "")
	})

	t.Run("rejected token echoed in response body", func(t *testing.T) {
		var seen string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = r.URL.Path
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, "invalid_token: %s", r.URL.String())
		}))
		defer srv.Close()

		notifier := NewWebhookNotifier(Channel{Name: "slack-ops", Type: TypeWebhook, URL: srv.URL + "/services/" + token}, srv.Client())
		err := notifier.Send(context.Background(), event)
		if err == nil {
			t.Fatal("Send() error = nil, want a non-2xx failure")
		}
		if !strings.Contains(seen, token) {
			t.Fatalf("test server did not receive the token path: %q", seen)
		}
		assertNoTokenLeak(t, err.Error(), token, srv.URL)
	})
}

func assertNoTokenLeak(t *testing.T, message, token, host string) {
	t.Helper()
	if strings.Contains(message, token) {
		t.Errorf("error message leaks the webhook token: %s", message)
	}
	if !strings.Contains(message, "redacted") {
		t.Errorf("error message should show the redacted URL instead: %s", message)
	}
	if host != "" && !strings.Contains(message, host) {
		t.Errorf("error message lost the host %q, which is what makes it diagnosable: %s", host, message)
	}
}
