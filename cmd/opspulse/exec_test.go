package main

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/server"
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

func TestSelectBatchServers(t *testing.T) {
	servers := []server.Server{
		{Name: "vps-personal", Host: "1.1.1.1", Tags: []string{"web", "dev"}},
		{Name: "vps-company", Host: "2.2.2.2", SkipBatch: true, Tags: []string{"prod", "web"}},
		{Name: "vps-backup", Host: "3.3.3.3", Tags: []string{"backup"}},
	}

	t.Run("filter all without includeSkipped excludes SkipBatch servers", func(t *testing.T) {
		targets, skipped := selectBatchServers(servers, "all", false)
		if len(targets) != 2 {
			t.Fatalf("expected 2 targets, got %d", len(targets))
		}
		if targets[0].Name != "vps-personal" || targets[1].Name != "vps-backup" {
			t.Errorf("unexpected targets: %+v", targets)
		}
		if len(skipped) != 1 || skipped[0] != "vps-company" {
			t.Errorf("expected skipped=[vps-company], got %+v", skipped)
		}
	})

	t.Run("filter all with includeSkipped includes all servers", func(t *testing.T) {
		targets, skipped := selectBatchServers(servers, "all", true)
		if len(targets) != 3 {
			t.Fatalf("expected 3 targets, got %d", len(targets))
		}
		if len(skipped) != 0 {
			t.Errorf("expected empty skipped, got %+v", skipped)
		}
	})

	t.Run("tag filter matching SkipBatch server without includeSkipped excludes it", func(t *testing.T) {
		targets, skipped := selectBatchServers(servers, "prod", false)
		if len(targets) != 0 {
			t.Fatalf("expected 0 targets, got %d", len(targets))
		}
		if len(skipped) != 1 || skipped[0] != "vps-company" {
			t.Errorf("expected skipped=[vps-company], got %+v", skipped)
		}
	})

	t.Run("tag filter matching SkipBatch server with includeSkipped includes it", func(t *testing.T) {
		targets, skipped := selectBatchServers(servers, "prod", true)
		if len(targets) != 1 || targets[0].Name != "vps-company" {
			t.Fatalf("expected target vps-company, got %+v", targets)
		}
		if len(skipped) != 0 {
			t.Errorf("expected empty skipped, got %+v", skipped)
		}
	})

	t.Run("filter by server name in batch mode without includeSkipped respects SkipBatch", func(t *testing.T) {
		targets, skipped := selectBatchServers(servers, "vps-company", false)
		if len(targets) != 0 {
			t.Fatalf("expected 0 targets, got %d", len(targets))
		}
		if len(skipped) != 1 || skipped[0] != "vps-company" {
			t.Errorf("expected skipped=[vps-company], got %+v", skipped)
		}
	})

	t.Run("filter with no matches", func(t *testing.T) {
		targets, skipped := selectBatchServers(servers, "nonexistent", false)
		if len(targets) != 0 || len(skipped) != 0 {
			t.Errorf("expected 0 targets and 0 skipped, got %d targets, %d skipped", len(targets), len(skipped))
		}
	})
}

