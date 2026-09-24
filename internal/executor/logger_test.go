package executor

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPrefixedWriter_Basic(t *testing.T) {
	var buf bytes.Buffer
	pw := NewPrefixedWriter("[tag] ", &buf)

	n, err := pw.Write([]byte("line 1\nline 2\n"))
	if err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	if n != len("line 1\nline 2\n") {
		t.Errorf("expected %d bytes written, got %d", len("line 1\nline 2\n"), n)
	}

	expected := "[tag] line 1\n[tag] line 2\n"
	if buf.String() != expected {
		t.Errorf("expected %q, got %q", expected, buf.String())
	}
}

func TestPrefixedWriter_PartialAndFlush(t *testing.T) {
	var buf bytes.Buffer
	pw := NewPrefixedWriter("[tag] ", &buf)

	_, _ = pw.Write([]byte("partial message"))
	if buf.Len() != 0 {
		t.Errorf("expected buffer to hold partial line, got %q", buf.String())
	}

	if err := pw.Flush(); err != nil {
		t.Fatalf("Flush() error: %v", err)
	}
	expected := "[tag] partial message"
	if buf.String() != expected {
		t.Errorf("expected %q, got %q", expected, buf.String())
	}
}

func TestPrefixedWriter_ConcurrentWrites(t *testing.T) {
	var buf bytes.Buffer
	pw := NewPrefixedWriter("[test] ", &buf)

	const numGoroutines = 20
	const linesPerGoroutine = 50

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < linesPerGoroutine; j++ {
				_, _ = pw.Write([]byte(fmt.Sprintf("goroutine %d line %d\n", id, j)))
			}
		}(i)
	}

	wg.Wait()
	_ = pw.Flush()

	if buf.Len() == 0 {
		t.Error("expected non-empty buffer after concurrent writes")
	}
}

// TestLogPathFor_StaysInsideLogDir is the regression for the log-file path
// traversal: the name is caller-supplied (bootstrap passes a server name, the
// backup and restore runners pass a job name), so a name carrying separators or
// ".." must still land inside the logs directory.
func TestLogPathFor_StaysInsideLogDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("OPSPULSE_HOME", home)

	logDir := filepath.Join(home, "data", "logs")
	ts := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

	hostile := []struct{ name, label string }{
		{"../../etc/passwd", "parent traversal"},
		{"..", "dotdot"},
		{".", "dot"},
		{"", "empty"},
		{"   ", "blank"},
		{"a/b", "slash"},
		{`a\b`, "backslash"},
		{"a\nb", "newline"},
		{"a\x00b", "NUL"},
		{".hidden", "leading dot"},
		{"../sibling", "mixed traversal"},
		{strings.Repeat("n", 400), "overlong"},
		{"~", "tilde"},
	}
	for _, tc := range hostile {
		path, err := LogPathFor(tc.name, ts)
		if err != nil {
			t.Fatalf("%s (%q): LogPathFor error: %v", tc.label, tc.name, err)
		}
		if got := filepath.Dir(path); got != logDir {
			t.Errorf("%s (%q): log written to %q, want parent %q", tc.label, tc.name, got, logDir)
		}
		rel, err := filepath.Rel(logDir, path)
		if err != nil {
			t.Fatalf("%s (%q): Rel error: %v", tc.label, tc.name, err)
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Errorf("%s (%q): relative path %q escapes the logs directory", tc.label, tc.name, rel)
		}
		if base := filepath.Base(path); strings.ContainsAny(base, `/\`) {
			t.Errorf("%s (%q): file base %q still holds a separator", tc.label, tc.name, base)
		}
	}

	if info, err := os.Stat(logDir); err != nil || !info.IsDir() {
		t.Errorf("expected LogPathFor to create %q, got err=%v", logDir, err)
	}
}

// TestLogPathFor_KeepsReadableNames guards the other direction: sanitising must
// not mangle the names real callers pass, or operators lose the ability to find
// a job's log by name.
func TestLogPathFor_KeepsReadableNames(t *testing.T) {
	t.Setenv("OPSPULSE_HOME", t.TempDir())
	ts := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

	for _, name := range []string{"vps-01", "backup-web-data", "restore-blog-stack", "服务器-01", "a@b"} {
		path, err := LogPathFor(name, ts)
		if err != nil {
			t.Fatalf("LogPathFor(%q) error: %v", name, err)
		}
		base := filepath.Base(path)
		if !strings.Contains(base, name) {
			t.Errorf("LogPathFor(%q) produced %q, want it to contain the name", name, base)
		}
		if !strings.HasSuffix(base, "-20260924T120000.log") {
			t.Errorf("LogPathFor(%q) produced %q, want the timestamp suffix preserved", name, base)
		}
	}
}
