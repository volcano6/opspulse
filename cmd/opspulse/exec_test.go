package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/volcano6/opspulse/internal/cliutil"
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

// TestCommandExitCodePropagatesChildStatus guards the contract that
// 'ops ssh <name> --exec ...' exits with the remote command's status: a failing
// child reaches commandExitCode as a bare *exec.ExitError rather than as an
// executor.ExecutionError, and its status must survive.
func TestCommandExitCodePropagatesChildStatus(t *testing.T) {
	if os.Getenv("OPSPULSE_TEST_EXIT_CHILD") == "1" {
		os.Exit(42)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestCommandExitCodePropagatesChildStatus")
	cmd.Env = append(os.Environ(), "OPSPULSE_TEST_EXIT_CHILD=1")
	err := cmd.Run()
	if err == nil {
		t.Fatal("helper child process unexpectedly succeeded")
	}

	if got := commandExitCode(err); got != 42 {
		t.Fatalf("commandExitCode(%v) = %d, want 42", err, got)
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

// batchStubExecutor is a batch executor whose per-server behaviour is scripted,
// so executeFiltered can be exercised without an SSH connection.
type batchStubExecutor struct {
	outputs map[string]string
	errs    map[string]error
}

func (f *batchStubExecutor) Execute(_ context.Context, target executor.Target, _ string, _ string, outputWriter io.Writer) (*executor.Result, error) {
	if out := f.outputs[target.Name]; out != "" && outputWriter != nil {
		_, _ = io.WriteString(outputWriter, out)
	}
	if err := f.errs[target.Name]; err != nil {
		return &executor.Result{ServerName: target.Name, Success: false, Error: err}, err
	}
	return &executor.Result{ServerName: target.Name, Success: true}, nil
}

func (f *batchStubExecutor) Test(context.Context, executor.Target) (time.Duration, string, error) {
	return 0, "", nil
}

// newBatchTestStore writes a servers.yaml with the given servers and returns a
// store pointing at it.
func newBatchTestStore(t *testing.T, servers ...server.Server) *server.Store {
	t.Helper()

	store := server.NewStore(filepath.Join(t.TempDir(), "servers.yaml"))
	for _, srv := range servers {
		if err := store.Save(srv); err != nil {
			t.Fatalf("failed to seed server %q: %v", srv.Name, err)
		}
	}
	return store
}

// TestExecuteFilteredKeepsStdoutForCommandOutput guards the contract that a
// redirect such as 'ops exec -f all "ss -lnt" > ports.txt' captures the remote
// commands' output only: OpsPulse's own diagnostics (skipped-servers notice,
// per-server failure lines, summary) must all land on stderr.
func TestExecuteFilteredKeepsStdoutForCommandOutput(t *testing.T) {
	store := newBatchTestStore(t,
		server.Server{Name: "web-1", Host: "10.0.0.1"},
		server.Server{Name: "web-2", Host: "10.0.0.2"},
		server.Server{Name: "batch-skipped", Host: "10.0.0.3", SkipBatch: true},
	)

	stub := &batchStubExecutor{
		outputs: map[string]string{"web-1": "LISTEN 0 128 0.0.0.0:22\n"},
		errs:    map[string]error{"web-2": errors.New("ssh: handshake failed")},
	}

	var stdout, stderr bytes.Buffer
	err := executeFiltered(store, stub, &stdout, &stderr, "all", "ss -lnt", 2, 0, false)
	if err == nil {
		t.Fatal("expected a failure once one server returned an error")
	}

	if !strings.Contains(stdout.String(), "LISTEN 0 128 0.0.0.0:22") {
		t.Errorf("stdout lost the remote command output: %q", stdout.String())
	}
	for _, leaked := range []string{"Command failed", "Summary", "Skipped", "❌"} {
		if strings.Contains(stdout.String(), leaked) {
			t.Errorf("stdout carries the diagnostic %q: %q", leaked, stdout.String())
		}
	}

	for _, want := range []string{"[web-2]", "Command failed", "ssh: handshake failed", "Skipped 1 server(s)"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr is missing %q: %q", want, stderr.String())
		}
	}
	if !strings.Contains(stderr.String(), "Summary: 1 succeeded, 1 failed across 2 servers") {
		t.Errorf("stderr is missing the summary: %q", stderr.String())
	}
}

// TestExecuteFilteredStdoutIsEmptyOnFailure pins the same contract from the other
// side: when no remote command prints anything, stdout stays empty even though
// every server failed.
func TestExecuteFilteredStdoutIsEmptyOnFailure(t *testing.T) {
	store := newBatchTestStore(t, server.Server{Name: "web-1", Host: "10.0.0.1"})

	stub := &batchStubExecutor{errs: map[string]error{"web-1": errors.New("connection refused")}}

	var stdout, stderr bytes.Buffer
	if err := executeFiltered(store, stub, &stdout, &stderr, "all", "false", 1, 0, false); err == nil {
		t.Fatal("expected an error for the failed server")
	}

	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "connection refused") {
		t.Errorf("stderr is missing the failure: %q", stderr.String())
	}
}

// TestExecuteFilteredParallelismIsNeverZero drives the semaphore with every
// resolved --parallel value, including the one that used to mean "unlimited".
// A zero-sized semaphore would block forever, so returning at all is the
// assertion.
func TestExecuteFilteredParallelismIsNeverZero(t *testing.T) {
	store := newBatchTestStore(t,
		server.Server{Name: "web-1", Host: "10.0.0.1"},
		server.Server{Name: "web-2", Host: "10.0.0.2"},
	)

	for _, parallel := range []int{cliutil.Unlimited, 0, 1, 3} {
		t.Run(strconv.Itoa(parallel), func(t *testing.T) {
			stub := &batchStubExecutor{}
			var stdout, stderr bytes.Buffer

			if err := executeFiltered(store, stub, &stdout, &stderr, "all", "true", parallel, 0, false); err != nil {
				t.Fatalf("parallel=%d returned error: %v", parallel, err)
			}
			if !strings.Contains(stderr.String(), "2 succeeded") {
				t.Errorf("parallel=%d: stderr = %q, want 2 succeeded", parallel, stderr.String())
			}
		})
	}
}

// TestExecuteFilteredNoMatchGoesToStderr keeps the empty-result path usable in
// pipelines: the "nothing matched" notice is a diagnostic, not command output.
func TestExecuteFilteredNoMatchGoesToStderr(t *testing.T) {
	store := newBatchTestStore(t, server.Server{Name: "web-1", Host: "10.0.0.1"})

	var stdout, stderr bytes.Buffer
	if err := executeFiltered(store, &batchStubExecutor{}, &stdout, &stderr, "nonexistent", "true", 1, 0, false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), `No servers matched filter "nonexistent"`) {
		t.Errorf("stderr is missing the no-match notice: %q", stderr.String())
	}
}
