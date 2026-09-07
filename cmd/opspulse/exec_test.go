package main

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/volcano6/opspulse/internal/executor"
)

func TestParseExecArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantServer  string
		wantCommand string
	}{
		{
			name:        "single quoted command",
			args:        []string{"oracle-sg", "docker ps --format 'table {{.Names}}\t{{.Status}}'"},
			wantServer:  "oracle-sg",
			wantCommand: "docker ps --format 'table {{.Names}}\t{{.Status}}'",
		},
		{
			name:        "multiple arguments command",
			args:        []string{"racknerd-la", "df", "-h", "/"},
			wantServer:  "racknerd-la",
			wantCommand: "df -h /",
		},
		{
			name:        "complex piped command",
			args:        []string{"vps-01", "cat /var/log/nginx/access.log | grep 404 | wc -l"},
			wantServer:  "vps-01",
			wantCommand: "cat /var/log/nginx/access.log | grep 404 | wc -l",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serverName := tt.args[0]
			commandStr := strings.Join(tt.args[1:], " ")

			if serverName != tt.wantServer {
				t.Errorf("serverName = %q, want %q", serverName, tt.wantServer)
			}
			if commandStr != tt.wantCommand {
				t.Errorf("commandStr = %q, want %q", commandStr, tt.wantCommand)
			}
		})
	}
}

func TestCommandExitCode(t *testing.T) {
	err := fmt.Errorf("remote command failed: %w", &executor.ExecutionError{ExitCode: 42, Server: "vps-01"})
	if got := commandExitCode(err); got != 42 {
		t.Fatalf("commandExitCode() = %d, want 42", got)
	}
	if got := commandExitCode(fmt.Errorf("ordinary error")); got != 1 {
		t.Fatalf("commandExitCode() = %d, want 1", got)
	}
}

func TestLinePrefixWriter(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex

	w := NewLinePrefixWriter("srv-1", "", &buf, &mu)
	_, _ = w.Write([]byte("line 1\nline 2\n"))
	w.Flush()

	got := buf.String()
	want := "[srv-1] line 1\n[srv-1] line 2\n"
	if got != want {
		t.Errorf("LinePrefixWriter output = %q, want %q", got, want)
	}

	// Test partial line and flush
	buf.Reset()
	w2 := NewLinePrefixWriter("srv-2", "", &buf, &mu)
	_, _ = w2.Write([]byte("incomplete line"))
	if buf.Len() != 0 {
		t.Errorf("expected no output before newline, got %q", buf.String())
	}
	w2.Flush()
	if buf.String() != "[srv-2] incomplete line\n" {
		t.Errorf("expected flushed line, got %q", buf.String())
	}
}

func TestLinePrefixWriter_Concurrent(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex

	w1 := NewLinePrefixWriter("s1", "", &buf, &mu)
	w2 := NewLinePrefixWriter("s2", "", &buf, &mu)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(n int) {
			defer wg.Done()
			_, _ = fmt.Fprintf(w1, "msg %d from s1\n", n)
		}(i)
		go func(n int) {
			defer wg.Done()
			_, _ = fmt.Fprintf(w2, "msg %d from s2\n", n)
		}(i)
	}
	wg.Wait()
	w1.Flush()
	w2.Flush()

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 100 {
		t.Fatalf("expected 100 lines, got %d", len(lines))
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "[s1] ") && !strings.HasPrefix(l, "[s2] ") {
			t.Errorf("line does not have valid prefix: %q", l)
		}
	}
}
