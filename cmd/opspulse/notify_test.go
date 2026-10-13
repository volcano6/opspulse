package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/volcano6/opspulse/internal/notify"
)

const notifyListToken = "T0000000/B0000000/SUPERSECRETTOKEN"

func seedNotificationChannel(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	setTestHome(t, home)

	store := notify.NewDefaultStore()
	if err := store.Save(notify.Channel{
		Name: "slack-ops",
		Type: notify.TypeWebhook,
		URL:  "https://hooks.slack.com/services/" + notifyListToken,
	}); err != nil {
		t.Fatalf("save notification channel: %v", err)
	}
}

func TestNotifyListRedactsURLByDefault(t *testing.T) {
	seedNotificationChannel(t)
	notifyListShowSecret = false
	t.Cleanup(func() { notifyListShowSecret = false })

	out := captureStdout(t, func() {
		rootCmd.SetArgs([]string{"notify", "list"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("notify list error: %v", err)
		}
	})

	if strings.Contains(out, "SUPERSECRETTOKEN") {
		t.Errorf("notify list leaked the webhook token:\n%s", out)
	}
	if !strings.Contains(out, "https://hooks.slack.com/<redacted>") {
		t.Errorf("notify list should show the redacted URL:\n%s", out)
	}
}

func TestNotifyListShowSecretRefusesNonInteractiveShell(t *testing.T) {
	seedNotificationChannel(t)
	notifyListShowSecret = false
	t.Cleanup(func() { notifyListShowSecret = false })

	var execErr error
	out := captureStdout(t, func() {
		rootCmd.SetIn(strings.NewReader(""))
		t.Cleanup(func() { rootCmd.SetIn(nil) })
		rootCmd.SetArgs([]string{"notify", "list", "--show-secret"})
		execErr = rootCmd.Execute()
	})

	if execErr == nil {
		t.Fatalf("notify list --show-secret in a non-interactive shell should be refused, output:\n%s", out)
	}
	if !strings.Contains(execErr.Error(), "non-interactive") {
		t.Errorf("error = %v, want it to name the non-interactive restriction", execErr)
	}
	if strings.Contains(out, "SUPERSECRETTOKEN") {
		t.Errorf("refused command still printed the token:\n%s", out)
	}
}

// TestNotifySecretOutputAllowedRequiresBothEndsToBeTerminals pins the output
// half of the --show-secret policy: an interactive terminal plus `> urls.txt`
// would still put the tokens in a file, so the verdict must look at both ends.
// The accepting case needs a real terminal and skips where there is none.
func TestNotifySecretOutputAllowedRequiresBothEndsToBeTerminals(t *testing.T) {
	if notifySecretOutputAllowed(strings.NewReader(""), &bytes.Buffer{}) {
		t.Error("a non-terminal stdin and stdout must be refused")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	if notifySecretOutputAllowed(r, w) {
		t.Error("a pipe must not count as a terminal on either end")
	}

	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil || !writerIsTerminal(tty) {
		t.Skip("no controlling terminal: the accepting case needs a real tty")
	}
	defer func() { _ = tty.Close() }()

	if notifySecretOutputAllowed(tty, &bytes.Buffer{}) {
		t.Error("an interactive stdin with redirected output must be refused: the tokens would land in the file")
	}
	if notifySecretOutputAllowed(strings.NewReader(""), tty) {
		t.Error("a non-terminal stdin must be refused even with a terminal stdout")
	}
	if !notifySecretOutputAllowed(tty, tty) {
		t.Error("stdin and stdout both a terminal should be allowed")
	}
}
