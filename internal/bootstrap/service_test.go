package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"io"
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

type mockFailingExecutor struct {
	failOnServer string
}

func (m *mockFailingExecutor) Execute(_ context.Context, target executor.Target, taskName string, _ string, _ io.Writer) (*executor.Result, error) {
	if target.Name == m.failOnServer {
		return &executor.Result{
			ServerName: target.Name,
			Template:   taskName,
			Success:    false,
			Error:      errors.New("mock execution error"),
		}, errors.New("mock execution error")
	}
	return &executor.Result{
		ServerName: target.Name,
		Template:   taskName,
		Success:    true,
	}, nil
}

func (m *mockFailingExecutor) Test(_ context.Context, _ executor.Target) (time.Duration, string, error) {
	return 10 * time.Millisecond, "mock Linux", nil
}

func TestBootstrap_StopOnError_MultiServer(t *testing.T) {
	tmpDir := t.TempDir()
	serverStore := server.NewStore(filepath.Join(tmpDir, "servers.yaml"))
	_ = serverStore.Save(server.Server{Name: "srv-01", Host: "1.1.1.1", User: "root"})
	_ = serverStore.Save(server.Server{Name: "srv-02", Host: "1.1.1.2", User: "root"})

	templateLoader := template.NewLoader("")
	exec := &mockFailingExecutor{failOnServer: "srv-01"}
	svc := NewService(serverStore, templateLoader, exec)

	var console bytes.Buffer
	summary, err := svc.Run(context.Background(), RunOptions{
		ServerNames:   []string{"srv-01", "srv-02"},
		TemplateNames: []string{"base", "docker"},
		StopOnError:   true,
	}, &console)

	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if summary.FailureCount != 4 {
		t.Errorf("expected 4 failed/skipped results, got %d", summary.FailureCount)
	}
	if summary.SuccessCount != 0 {
		t.Errorf("expected 0 successes, got %d", summary.SuccessCount)
	}
}

type mockCapturingExecutor struct {
	capturedTasks   []string
	capturedScripts []string
}

func (m *mockCapturingExecutor) Execute(_ context.Context, target executor.Target, taskName string, script string, _ io.Writer) (*executor.Result, error) {
	m.capturedTasks = append(m.capturedTasks, taskName)
	m.capturedScripts = append(m.capturedScripts, script)
	return &executor.Result{
		ServerName: target.Name,
		Template:   taskName,
		Success:    true,
	}, nil
}

func (m *mockCapturingExecutor) Test(_ context.Context, _ executor.Target) (time.Duration, string, error) {
	return 10 * time.Millisecond, "mock Linux", nil
}

func TestBootstrap_TemplateArguments(t *testing.T) {
	tmpDir := t.TempDir()
	serverStore := server.NewStore(filepath.Join(tmpDir, "servers.yaml"))
	_ = serverStore.Save(server.Server{Name: "vps-01", Host: "1.2.3.4", User: "root"})

	templateLoader := template.NewLoader("")
	exec := &mockCapturingExecutor{}
	svc := NewService(serverStore, templateLoader, exec)

	var console bytes.Buffer
	summary, err := svc.Run(context.Background(), RunOptions{
		ServerNames:   []string{"vps-01"},
		TemplateNames: []string{"swap:4", "timezone:UTC"},
	}, &console)

	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if summary.SuccessCount != 2 {
		t.Fatalf("expected 2 successful tasks, got %d", summary.SuccessCount)
	}

	if len(exec.capturedTasks) != 2 {
		t.Fatalf("expected 2 captured tasks, got %d", len(exec.capturedTasks))
	}

	// Verify task names preserved the argument
	if exec.capturedTasks[0] != "swap:4" {
		t.Errorf("expected task 0 to be 'swap:4', got %q", exec.capturedTasks[0])
	}
	if exec.capturedTasks[1] != "timezone:UTC" {
		t.Errorf("expected task 1 to be 'timezone:UTC', got %q", exec.capturedTasks[1])
	}

	// Verify script content had arguments safely escaped via single quotes
	if !strings.Contains(exec.capturedScripts[0], `set -- '4'`) || !strings.Contains(exec.capturedScripts[0], `export SCRIPT_ARG='4'`) {
		t.Errorf("expected script 0 to inject 'set -- \\'4\\'', got:\n%s", exec.capturedScripts[0])
	}
	if !strings.Contains(exec.capturedScripts[1], `set -- 'UTC'`) || !strings.Contains(exec.capturedScripts[1], `export SCRIPT_ARG='UTC'`) {
		t.Errorf("expected script 1 to inject 'set -- \\'UTC\\'', got:\n%s", exec.capturedScripts[1])
	}

	// Verify dangerous metacharacters are safely neutralized with single quotes
	exec2 := &mockCapturingExecutor{}
	svc2 := NewService(serverStore, templateLoader, exec2)
	dangerousArg := `$(id); echo "pwned"`
	_, err = svc2.Run(context.Background(), RunOptions{
		ServerNames:   []string{"vps-01"},
		TemplateNames: []string{"timezone:" + dangerousArg},
	}, &console)
	if err != nil {
		t.Fatalf("Run() with special characters failed: %v", err)
	}
	if !strings.Contains(exec2.capturedScripts[0], "set -- '$(id); echo \"pwned\"'") {
		t.Errorf("expected dangerous argument to be strictly single-quoted, got:\n%s", exec2.capturedScripts[0])
	}
}

func TestBootstrap_ServerEnvironmentVariables(t *testing.T) {
	tmpDir := t.TempDir()
	serverStore := server.NewStore(filepath.Join(tmpDir, "servers.yaml"))
	_ = serverStore.Save(server.Server{
		Name: "custom-vps",
		Host: "192.168.10.100",
		Port: 2222,
		User: "deployer",
	})
	_ = serverStore.Save(server.Server{
		Name: "default-vps",
		Host: "192.168.10.101",
		Port: 0, // Should default to 22
		User: "root",
	})

	templateLoader := template.NewLoader("")
	exec := &mockCapturingExecutor{}
	svc := NewService(serverStore, templateLoader, exec)

	var console bytes.Buffer
	_, err := svc.Run(context.Background(), RunOptions{
		ServerNames:   []string{"custom-vps", "default-vps"},
		TemplateNames: []string{"base"},
	}, &console)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if len(exec.capturedScripts) != 2 {
		t.Fatalf("expected 2 captured scripts, got %d", len(exec.capturedScripts))
	}

	// Verify custom port 2222
	if !strings.Contains(exec.capturedScripts[0], "export OPS_SSH_PORT=2222") {
		t.Errorf("expected custom port script to contain OPS_SSH_PORT=2222, got:\n%s", exec.capturedScripts[0])
	}
	if !strings.Contains(exec.capturedScripts[0], "export OPS_SERVER_NAME='custom-vps'") {
		t.Errorf("expected script to contain OPS_SERVER_NAME='custom-vps', got:\n%s", exec.capturedScripts[0])
	}

	// Verify default port 22
	if !strings.Contains(exec.capturedScripts[1], "export OPS_SSH_PORT=22") {
		t.Errorf("expected default port script to contain OPS_SSH_PORT=22, got:\n%s", exec.capturedScripts[1])
	}
}


