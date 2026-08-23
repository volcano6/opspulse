package backup

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/volcano6/opspulse/internal/server"
	"github.com/volcano6/opspulse/internal/storage"
)

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{-10, "0 B"},
		{500, "500 B"},
		{1024, "1.00 KB"},
		{1048576, "1.00 MB"},
		{52428800, "50.00 MB"},
		{1073741824, "1.00 GB"},
	}

	for _, tt := range tests {
		got := FormatBytes(tt.bytes)
		if got != tt.want {
			t.Errorf("FormatBytes(%d) = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}

func TestPool_DryRun(t *testing.T) {
	pool := NewPool(nil, 2)
	jobs := []Job{
		{
			Name:    "job-1",
			Server:  "local",
			Paths:   []string{"/data"},
			Backend: "/backup",
		},
		{
			Name:    "job-2",
			Server:  "local",
			Paths:   []string{"/data2"},
			Backend: "/backup",
		},
	}

	var buf bytes.Buffer
	res, err := pool.RunAll(context.Background(), jobs, true, &buf)
	if err != nil {
		t.Fatalf("RunAll(dryRun) error: %v", err)
	}

	if !res.IsDryRun {
		t.Error("expected IsDryRun to be true")
	}
	if len(res.Runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(res.Runs))
	}
	if res.SuccessCount != 2 {
		t.Errorf("expected 2 successes, got %d", res.SuccessCount)
	}

	var summaryBuf bytes.Buffer
	res.PrintSummary(&summaryBuf)
	outStr := summaryBuf.String()

	if !strings.Contains(outStr, "DRY-RUN") || !strings.Contains(outStr, "job-1") {
		t.Errorf("expected summary table to contain DRY-RUN and job names, got:\n%s", outStr)
	}
}

func TestPool_ConcurrentExecution(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := storage.Open(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("storage.Open() error: %v", err)
	}
	defer func() { _ = db.Close() }()

	backupRepo := storage.NewBackupRepo(db)
	serverStore := server.NewStore(filepath.Join(tmpDir, "servers.yaml"))
	_ = serverStore.Save(server.Server{
		Name: "vps-01",
		Host: "192.168.1.1",
		User: "root",
	})

	mockOutput := `{"message_type":"summary","files_new":5,"data_added":1024,"total_bytes_processed":2048,"total_duration":1.0,"snapshot_id":"snap-test123"}`
	exec := &mockExecutor{outputToReturn: mockOutput}
	runner := NewRunner(exec, serverStore, backupRepo)

	pool := NewPool(runner, 2)
	jobs := []Job{
		{
			Name:    "web-1",
			Server:  "vps-01",
			Paths:   []string{"/var/www1"},
			Backend: "/backup",
		},
		{
			Name:    "web-2",
			Server:  "vps-01",
			Paths:   []string{"/var/www2"},
			Backend: "/backup",
		},
		{
			Name:    "web-3",
			Server:  "vps-01",
			Paths:   []string{"/var/www3"},
			Backend: "/backup",
		},
	}

	var console bytes.Buffer
	res, err := pool.RunAll(context.Background(), jobs, false, &console)
	if err != nil {
		t.Fatalf("pool.RunAll() error: %v", err)
	}

	if len(res.Runs) != 3 {
		t.Fatalf("expected 3 runs, got %d", len(res.Runs))
	}
	if res.SuccessCount != 3 {
		t.Errorf("expected 3 successes, got %d (failures: %d)", res.SuccessCount, res.FailureCount)
	}

	var summaryBuf bytes.Buffer
	res.PrintSummary(&summaryBuf)
	summaryStr := summaryBuf.String()

	if !strings.Contains(summaryStr, "SUCCESS") || !strings.Contains(summaryStr, "web-1") {
		t.Errorf("expected SUCCESS summary output, got:\n%s", summaryStr)
	}
}
