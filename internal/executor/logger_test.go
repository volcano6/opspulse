package executor

import (
	"bytes"
	"fmt"
	"sync"
	"testing"
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
