package secret

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolver_Resolve_Plain(t *testing.T) {
	r := NewResolver()
	ctx := context.Background()

	val, err := r.Resolve(ctx, "plain-value")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "plain-value" {
		t.Errorf("expected plain-value, got %q", val)
	}
}

func TestResolver_ResolveMap_Mixed(t *testing.T) {
	r := NewResolver()
	ctx := context.Background()

	input := map[string]string{
		"PLAIN": "value1",
	}

	// We don't test actual op:// resolution in CI because we don't assume op is installed and configured
	// But we test that it correctly identifies non-op values
	result, err := r.ResolveMap(ctx, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result["PLAIN"] != "value1" {
		t.Errorf("expected value1, got %q", result["PLAIN"])
	}
}

func TestResolver_Resolve_Op_NotInstalled(t *testing.T) {
	// Pin the CLI to a path that cannot exist so the test exercises the failure
	// path rather than invoking whatever op this machine happens to have. A real
	// op.exe under WSL blocks on Desktop App authorisation for tens of seconds
	// (and has no cache to absorb it), which made this test both slow and
	// dependent on the developer's 1Password state.
	t.Setenv("OPSPULSE_OP_PATH", filepath.Join(t.TempDir(), "op-not-installed"))

	r := NewResolver()
	ctx := context.Background()

	// If op is not installed or we just try an op:// URI, it should fail nicely.
	// In CI, op might not be installed. If it is, it might fail auth.
	// Here we just check that it either returns the not installed error or fails trying to run it.
	_, err := r.Resolve(ctx, "op://vault/item/field")
	if err == nil {
		t.Errorf("expected error when resolving op:// without proper auth/installation")
	} else if !strings.Contains(err.Error(), "1Password") && !strings.Contains(err.Error(), "failed to read") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestResolve_PassthroughPreservesOriginal(t *testing.T) {
	r := &Resolver{cli: CLI{}}
	ctx := context.Background()

	tests := []struct {
		name  string
		value string
	}{
		{name: "plain", value: "plain-value"},
		{name: "surrounding spaces", value: "  plain-value  "},
		{name: "tabs", value: "\ttabbed\t"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.Resolve(ctx, tt.value)
			if err != nil {
				t.Fatalf("Resolve(%q) error = %v, want nil", tt.value, err)
			}
			if got != tt.value {
				t.Fatalf("Resolve(%q) = %q, want the original untrimmed value", tt.value, got)
			}
		})
	}
}

func TestResolvePassword_PassthroughPreservesOriginal(t *testing.T) {
	r := &Resolver{cli: CLI{}}
	ctx := context.Background()

	tests := []struct {
		name string
		ref  string
	}{
		{name: "plain", ref: "plain-value"},
		{name: "surrounding spaces", ref: "  plain-value  "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.ResolvePassword(ctx, tt.ref)
			if err != nil {
				t.Fatalf("ResolvePassword(%q) error = %v, want nil", tt.ref, err)
			}
			if got != tt.ref {
				t.Fatalf("ResolvePassword(%q) = %q, want the original untrimmed value", tt.ref, got)
			}
		})
	}
}

func TestResolve_OpCLINotFound(t *testing.T) {
	r := &Resolver{cli: CLI{}}
	ctx := context.Background()

	_, err := r.Resolve(ctx, "op://vault/item/field")
	if !errors.Is(err, ErrCLINotFound) {
		t.Fatalf("Resolve() error = %v, want ErrCLINotFound", err)
	}
	if !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("Resolve() error = %q, want it to mention the CLI being unavailable", err)
	}
}

func TestResolve_OpReturnsCRLF(t *testing.T) {
	// The stub prints the Windows op.exe signature: the secret followed by a
	// \r\n pair. Resolve must preserve it verbatim; ResolvePassword must strip it.
	script, _ := writeStubCLI(t, `printf 'secret\r\n'`)

	r := &Resolver{cli: CLI{Path: script}}
	ctx := context.Background()

	plain, err := r.Resolve(ctx, "op://vault/item/field")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if plain != "secret\r\n" {
		t.Fatalf("Resolve() = %q, want %q (no CRLF strip)", plain, "secret\r\n")
	}

	password, err := r.ResolvePassword(ctx, "op://vault/item/field")
	if err != nil {
		t.Fatalf("ResolvePassword() error = %v", err)
	}
	if password != "secret" {
		t.Fatalf("ResolvePassword() = %q, want %q (CRLF stripped)", password, "secret")
	}
}
