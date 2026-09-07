package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/volcano6/opspulse/internal/doctor"
)

func TestRenderDoctorTable(t *testing.T) {
	results := []doctor.ServerHealth{
		{
			Name:            "vps-healthy",
			Host:            "1.2.3.4",
			Port:            22,
			Latency:         30 * time.Millisecond,
			DiskTotalGB:     50,
			DiskUsedGB:      15,
			DiskUsagePct:    30,
			DiskStatus:      doctor.StatusHealthy,
			DockerInstalled: true,
			DockerRunning:   true,
			ContainersRun:   3,
			ContainersStop:  0,
			DockerStatus:    doctor.StatusHealthy,
			OverallStatus:   doctor.StatusHealthy,
		},
		{
			Name:            "vps-warn",
			Host:            "5.6.7.8",
			Port:            2222,
			Latency:         120 * time.Millisecond,
			DiskTotalGB:     20,
			DiskUsedGB:      18,
			DiskUsagePct:    90,
			DiskStatus:      doctor.StatusCritical,
			DockerInstalled: true,
			DockerRunning:   false,
			DockerStatus:    doctor.StatusWarning,
			OverallStatus:   doctor.StatusCritical,
		},
		{
			Name:          "vps-offline",
			Host:          "9.9.9.9",
			Port:          22,
			OverallStatus: doctor.StatusUnreachable,
		},
	}

	var buf bytes.Buffer
	err := renderDoctorTable(&buf, results)
	if err != nil {
		t.Fatalf("renderDoctorTable error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "SERVER") || !strings.Contains(out, "vps-healthy") || !strings.Contains(out, "vps-warn") || !strings.Contains(out, "vps-offline") {
		t.Errorf("expected table to contain all server names:\n%s", out)
	}
	if !strings.Contains(out, "5.6.7.8:2222") {
		t.Errorf("expected non-standard port 2222 in output:\n%s", out)
	}
	if !strings.Contains(out, "HEALTHY") || !strings.Contains(out, "CRITICAL") || !strings.Contains(out, "UNREACHABLE") {
		t.Errorf("expected all status labels in output:\n%s", out)
	}
}

func TestDoctorCmdFlags(t *testing.T) {
	cmd := doctorCmd
	if cmd.Flags().Lookup("filter") == nil {
		t.Error("expected --filter flag")
	}
	if cmd.Flags().Lookup("parallel") == nil {
		t.Error("expected --parallel flag")
	}
	if cmd.Flags().Lookup("timeout") == nil {
		t.Error("expected --timeout flag")
	}
}
