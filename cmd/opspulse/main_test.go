package main

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/volcano6/opspulse/internal/secret"
)

// TestHandleRootErrorAppendsInstallHintOnce pins the CLI-layer interception
// contract: a failure that wraps ErrCLINotFound (the `op://` reference path)
// gets InstallHint() appended to stderr exactly once, while an ordinary error
// does not.
func TestHandleRootErrorAppendsInstallHintOnce(t *testing.T) {
	wrapped := fmt.Errorf("secret %q starts with op:// but the 1Password CLI is unavailable: %w", "op://Vault/Item/password", secret.ErrCLINotFound)

	var buf bytes.Buffer
	if code := handleRootError(wrapped, &buf); code != 1 {
		t.Fatalf("handleRootError() exit code = %d, want 1", code)
	}
	hint := secret.InstallHint()
	if got, want := bytes.Count(buf.Bytes(), []byte(hint)), 1; got != want {
		t.Fatalf("hint printed %d times, want %d; output:\n%s", got, want, buf.String())
	}
}

// TestHandleRootErrorSilentOnOrdinaryError pins the other side: an error that
// does not wrap ErrCLINotFound must not emit the hint.
func TestHandleRootErrorSilentOnOrdinaryError(t *testing.T) {
	var buf bytes.Buffer
	if code := handleRootError(errors.New("some other failure"), &buf); code != 1 {
		t.Fatalf("handleRootError() exit code = %d, want 1", code)
	}
	if buf.Len() != 0 {
		t.Fatalf("unexpected hint output for ordinary error: %q", buf.String())
	}
}

// TestEnsure1PCLIErrorDoesNotWrapCLINotFound pins the no-duplicate-hint
// property: the `ops 1p` subcommands already print InstallHint() themselves, so
// their non-interactive error must not wrap ErrCLINotFound — otherwise
// handleRootError would print the hint a second time.
func TestEnsure1PCLIErrorDoesNotWrapCLINotFound(t *testing.T) {
	err := missing1PCLINonInteractiveError()
	if err == nil {
		t.Fatal("missing1PCLINonInteractiveError() returned nil, want an error")
	}
	if errors.Is(err, secret.ErrCLINotFound) {
		t.Fatalf("missing1PCLINonInteractiveError() wraps ErrCLINotFound, which would double-print the hint: %v", err)
	}
	const want = "1Password CLI is required (install it, then re-run this command in an interactive terminal)"
	if err.Error() != want {
		t.Fatalf("missing1PCLINonInteractiveError() = %q, want %q", err.Error(), want)
	}
}
