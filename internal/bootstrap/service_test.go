package bootstrap

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/server"
	"github.com/volcano6/opspulse/internal/template"
)

func TestService_Validation(t *testing.T) {
	tmpDir := t.TempDir()
	serverStore := server.NewStore(filepath.Join(tmpDir, "servers.yaml"))
	templateLoader := template.NewLoader(filepath.Join(tmpDir, "templates"))
	exec := executor.NewSSHExecutor()

	svc := NewService(serverStore, templateLoader, exec)
	ctx := context.Background()

	// 1. No servers
	_, err := svc.Run(ctx, RunOptions{TemplateNames: []string{"docker"}}, nil)
	if err != ErrNoServersSpecified {
		t.Errorf("expected ErrNoServersSpecified, got %v", err)
	}

	// 2. No templates
	_, err = svc.Run(ctx, RunOptions{ServerNames: []string{"vps-01"}}, nil)
	if err != ErrNoTemplatesSpecified {
		t.Errorf("expected ErrNoTemplatesSpecified, got %v", err)
	}

	// 3. Server not in inventory
	_, err = svc.Run(ctx, RunOptions{
		ServerNames:   []string{"non-existent"},
		TemplateNames: []string{"docker"},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "not found in inventory") {
		t.Errorf("expected server not found error, got %v", err)
	}
}

func TestService_DryRun(t *testing.T) {
	tmpDir := t.TempDir()
	serverStore := server.NewStore(filepath.Join(tmpDir, "servers.yaml"))
	_ = serverStore.Save(server.Server{
		Name: "vps-test",
		Host: "192.168.1.50",
		User: "root",
	})

	templateLoader := template.NewLoader("") // Built-in templates
	exec := executor.NewSSHExecutor()
	svc := NewService(serverStore, templateLoader, exec)

	var console bytes.Buffer
	summary, err := svc.Run(context.Background(), RunOptions{
		ServerNames:   []string{"vps-test"},
		TemplateNames: []string{"base", "docker"},
		DryRun:        true,
	}, &console)

	if err != nil {
		t.Fatalf("DryRun failed: %v", err)
	}

	if !summary.IsDryRun {
		t.Error("expected summary.IsDryRun to be true")
	}
	if len(summary.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(summary.Results))
	}
	if summary.SuccessCount != 2 {
		t.Errorf("expected 2 successes, got %d", summary.SuccessCount)
	}

	// Test PrintTable output
	var tableOut bytes.Buffer
	summary.PrintTable(&tableOut)
	outStr := tableOut.String()

	if !strings.Contains(outStr, "vps-test") || !strings.Contains(outStr, "DRY-RUN") {
		t.Errorf("expected table to contain server name and DRY-RUN status, got:\n%s", outStr)
	}
}

func TestSummary_PrintTable(t *testing.T) {
	summary := Summary{
		Results: []executor.Result{
			{
				ServerName: "web-01",
				Template:   "docker",
				Success:    true,
				Duration:   12 * time.Second,
				LogPath:    "/tmp/logs/bootstrap-web-01.log",
			},
			{
				ServerName: "web-02",
				Template:   "base",
				Success:    false,
				Duration:   3 * time.Second,
				LogPath:    "/tmp/logs/bootstrap-web-02.log",
			},
		},
		TotalDuration: 15 * time.Second,
		SuccessCount:  1,
		FailureCount:  1,
	}

	var buf bytes.Buffer
	summary.PrintTable(&buf)
	output := buf.String()

	if !strings.Contains(output, "SUCCESS") || !strings.Contains(output, "FAILED") {
		t.Errorf("expected SUCCESS and FAILED in summary table, got:\n%s", output)
	}
	if !strings.Contains(output, "Total: 2") {
		t.Errorf("expected Total: 2 in summary, got:\n%s", output)
	}
}
