package cliutil

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseParallelism(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		changed bool
		want    int
		wantErr bool
	}{
		{name: "unset flag means the default", spec: "", want: DefaultParallel},
		{name: "explicit zero means the default", spec: "0", changed: true, want: DefaultParallel},
		{name: "positive integer", spec: "3", changed: true, want: 3},
		{name: "surrounding spaces", spec: " 4 ", changed: true, want: 4},
		{name: "unlimited lowercase", spec: "unlimited", changed: true, want: Unlimited},
		{name: "unlimited uppercase", spec: "UNLIMITED", changed: true, want: Unlimited},
		{name: "unlimited mixed case", spec: "Unlimited", changed: true, want: Unlimited},
		{name: "negative integer is not a limit", spec: "-1", changed: true, wantErr: true},
		{name: "non-numeric", spec: "abc", changed: true, wantErr: true},
		{name: "fractional", spec: "1.5", changed: true, wantErr: true},
		{name: "empty string with changed flag", spec: "   ", changed: true, want: DefaultParallel},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var warn bytes.Buffer
			got, err := ParseParallelism(tt.spec, tt.changed, &warn)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseParallelism(%q) = %d, want error", tt.spec, got)
				}
				// The error is the only guidance a user gets, so it must name
				// both forms that work.
				for _, want := range []string{"positive integer", "unlimited"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not mention %q", err, want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseParallelism(%q) returned error: %v", tt.spec, err)
			}
			if got != tt.want {
				t.Errorf("ParseParallelism(%q) = %d, want %d", tt.spec, got, tt.want)
			}
		})
	}
}

// TestParseParallelismZeroDeprecation pins the migration notice: an explicit
// '--parallel 0' used to mean unlimited for 'ops backup run' and the default for
// 'ops exec'/'ops doctor', so a user who typed it must be told what it means now.
func TestParseParallelismZeroDeprecation(t *testing.T) {
	t.Run("explicit zero warns", func(t *testing.T) {
		var warn bytes.Buffer
		got, err := ParseParallelism("0", true, &warn)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != DefaultParallel {
			t.Errorf("got %d, want %d", got, DefaultParallel)
		}
		msg := warn.String()
		if msg == "" {
			t.Fatal("expected a deprecation warning on the warn writer, got none")
		}
		for _, want := range []string{"unlimited", "0"} {
			if !strings.Contains(msg, want) {
				t.Errorf("warning %q does not mention %q", msg, want)
			}
		}
	})

	t.Run("unset flag stays quiet", func(t *testing.T) {
		var warn bytes.Buffer
		if _, err := ParseParallelism("", false, &warn); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if warn.Len() != 0 {
			t.Errorf("unexpected warning for empty spec: %q", warn.String())
		}
	})

	t.Run("other values stay quiet", func(t *testing.T) {
		for _, spec := range []string{"1", "8", "unlimited"} {
			var warn bytes.Buffer
			if _, err := ParseParallelism(spec, true, &warn); err != nil {
				t.Fatalf("ParseParallelism(%q) returned error: %v", spec, err)
			}
			if warn.Len() != 0 {
				t.Errorf("ParseParallelism(%q) warned unexpectedly: %q", spec, warn.String())
			}
		}
	})

	t.Run("nil warn writer is tolerated", func(t *testing.T) {
		if _, err := ParseParallelism("0", true, nil); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}
