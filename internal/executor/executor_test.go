package executor

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/volcano6/opspulse/internal/server"
)

func TestTarget(t *testing.T) {
	srv := server.Server{
		Name: "vps-target",
		Host: "192.168.1.10",
		User: "root",
	}

	serverTarget := NewServerTarget(srv)
	if serverTarget.IsLocal {
		t.Error("expected server target IsLocal to be false")
	}
	if serverTarget.Name != "vps-target" {
		t.Errorf("expected server target name 'vps-target', got %q", serverTarget.Name)
	}
	if serverTarget.Server == nil || serverTarget.Server.Host != "192.168.1.10" {
		t.Errorf("expected server target server host '192.168.1.10', got %v", serverTarget.Server)
	}

	localTarget := NewLocalTarget()
	if !localTarget.IsLocal {
		t.Error("expected local target IsLocal to be true")
	}
	if localTarget.Name != "local" {
		t.Errorf("expected local target name 'local', got %q", localTarget.Name)
	}
	if localTarget.Server != nil {
		t.Errorf("expected local target Server to be nil, got %v", localTarget.Server)
	}
}

func TestLocalExecutor_Execute(t *testing.T) {
	exec := NewLocalExecutor()
	target := NewLocalTarget()
	ctx := context.Background()

	// 1. Success execution
	var buf bytes.Buffer
	res, err := exec.Execute(ctx, target, "echo-test", "echo 'hello from local executor'", &buf)
	if err != nil {
		t.Fatalf("Execute() unexpected error: %v", err)
	}
	if !res.Success {
		t.Error("expected execution to be successful")
	}
	if res.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", res.ExitCode)
	}
	if !strings.Contains(buf.String(), "hello from local executor") {
		t.Errorf("expected output to contain 'hello from local executor', got %q", buf.String())
	}

	// 2. Failure execution with non-zero exit code
	buf.Reset()
	failRes, failErr := exec.Execute(ctx, target, "fail-test", "exit 42", &buf)
	if failErr == nil {
		t.Error("expected error for non-zero exit code, got nil")
	}
	if failRes.Success {
		t.Error("expected failRes.Success to be false")
	}
	if failRes.ExitCode != 42 {
		t.Errorf("expected exit code 42, got %d", failRes.ExitCode)
	}

	// 3. Context cancellation timeout
	timeoutCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()

	_, timeoutErr := exec.Execute(timeoutCtx, target, "sleep-test", "sleep 2", nil)
	if timeoutErr == nil {
		t.Error("expected timeout error, got nil")
	}
}

func TestLocalExecutor_Test(t *testing.T) {
	exec := NewLocalExecutor()
	target := NewLocalTarget()
	ctx := context.Background()

	rtt, banner, err := exec.Test(ctx, target)
	if err != nil {
		t.Fatalf("Test() unexpected error: %v", err)
	}
	if rtt <= 0 {
		t.Errorf("expected positive RTT, got %v", rtt)
	}
	if banner == "" {
		t.Error("expected non-empty banner")
	}
}

func TestSSHExecutor_InvalidTarget(t *testing.T) {
	exec := NewSSHExecutor()
	ctx := context.Background()

	// Local target passed to SSHExecutor
	localTarget := NewLocalTarget()
	_, _, err := exec.Test(ctx, localTarget)
	if err == nil {
		t.Error("expected error when passing local target to SSHExecutor, got nil")
	}

	_, execErr := exec.Execute(ctx, localTarget, "test", "echo 1", nil)
	if execErr == nil {
		t.Error("expected error when passing local target to SSHExecutor Execute, got nil")
	}
}
