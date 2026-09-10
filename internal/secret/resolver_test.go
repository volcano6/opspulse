package secret

import (
	"context"
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
