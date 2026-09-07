package doctor

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseProbeOutput_Healthy(t *testing.T) {
	raw := `---PROBE_START---
---DISK---
41943040 20971520 20971520 50%
---DOCKER---
DOCKER_RUNNING
Up 2 days
Up 5 hours
Up 10 minutes
---PROBE_END---`

	var h ServerHealth
	parseProbeOutput(raw, &h)
	computeOverallStatus(&h)

	if h.DiskUsagePct != 50 {
		t.Errorf("DiskUsagePct = %d, want 50", h.DiskUsagePct)
	}
	if h.DiskStatus != StatusHealthy {
		t.Errorf("DiskStatus = %s, want HEALTHY", h.DiskStatus)
	}
	if !h.DockerInstalled || !h.DockerRunning {
		t.Errorf("DockerInstalled=%v, DockerRunning=%v, want both true", h.DockerInstalled, h.DockerRunning)
	}
	if h.ContainersRun != 3 || h.ContainersStop != 0 {
		t.Errorf("ContainersRun=%d, ContainersStop=%d, want 3 and 0", h.ContainersRun, h.ContainersStop)
	}
	if h.OverallStatus != StatusHealthy {
		t.Errorf("OverallStatus = %s, want HEALTHY", h.OverallStatus)
	}
}

func TestParseProbeOutput_DiskThresholds(t *testing.T) {
	tests := []struct {
		name       string
		pct        int
		wantDisk   HealthStatus
		wantStatus HealthStatus
	}{
		{"below warn 84%", 84, StatusHealthy, StatusHealthy},
		{"at warn 85%", 85, StatusWarning, StatusWarning},
		{"above warn 89%", 89, StatusWarning, StatusWarning},
		{"at crit 90%", 90, StatusCritical, StatusCritical},
		{"above crit 95%", 95, StatusCritical, StatusCritical},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := strings.ReplaceAll(`---PROBE_START---
---DISK---
10000000 8000000 2000000 {{PCT}}%
---DOCKER---
DOCKER_NOT_INSTALLED
---PROBE_END---`, "{{PCT}}", string(rune('0'+tt.pct/10))+string(rune('0'+tt.pct%10)))

			var h ServerHealth
			parseProbeOutput(raw, &h)
			computeOverallStatus(&h)

			if h.DiskStatus != tt.wantDisk {
				t.Errorf("DiskStatus = %s, want %s", h.DiskStatus, tt.wantDisk)
			}
			if h.OverallStatus != tt.wantStatus {
				t.Errorf("OverallStatus = %s, want %s", h.OverallStatus, tt.wantStatus)
			}
		})
	}
}

func TestParseProbeOutput_DockerStates(t *testing.T) {
	// 1. Daemon stopped
	rawStopped := `---PROBE_START---
---DISK---
10000000 2000000 8000000 20%
---DOCKER---
DOCKER_DAEMON_STOPPED
---PROBE_END---`

	var h1 ServerHealth
	parseProbeOutput(rawStopped, &h1)
	computeOverallStatus(&h1)

	if !h1.DockerInstalled || h1.DockerRunning {
		t.Errorf("expected docker installed but not running")
	}
	if h1.DockerStatus != StatusWarning {
		t.Errorf("DockerStatus = %s, want WARNING", h1.DockerStatus)
	}
	if h1.OverallStatus != StatusWarning {
		t.Errorf("OverallStatus = %s, want WARNING", h1.OverallStatus)
	}

	// 2. Stopped containers
	rawWithStopped := `---PROBE_START---
---DISK---
10000000 2000000 8000000 20%
---DOCKER---
DOCKER_RUNNING
Up 1 day
Exited (0) 2 hours ago
Exited (1) 5 minutes ago
---PROBE_END---`

	var h2 ServerHealth
	parseProbeOutput(rawWithStopped, &h2)
	computeOverallStatus(&h2)

	if h2.ContainersRun != 1 || h2.ContainersStop != 2 {
		t.Errorf("got %d run, %d stop; want 1 and 2", h2.ContainersRun, h2.ContainersStop)
	}
}

func TestFormatMethods(t *testing.T) {
	h := ServerHealth{
		DiskTotalGB:     40.0,
		DiskUsedGB:      18.5,
		DiskUsagePct:    46,
		DiskStatus:      StatusHealthy,
		DockerInstalled: true,
		DockerRunning:   true,
		ContainersRun:   5,
		ContainersStop:  1,
		Latency:         45 * time.Millisecond,
		OverallStatus:   StatusHealthy,
	}

	if diskStr := h.FormatDisk(); diskStr != "46% (18.5/40.0 GB)" {
		t.Errorf("FormatDisk() = %q, want '46%% (18.5/40.0 GB)'", diskStr)
	}

	if dockerStr := h.FormatDocker(); dockerStr != "5 run, 1 stop" {
		t.Errorf("FormatDocker() = %q, want '5 run, 1 stop'", dockerStr)
	}

	if latStr := h.FormatLatency(); latStr != "45.0ms" {
		t.Errorf("FormatLatency() = %q, want '45.0ms'", latStr)
	}

	if !strings.Contains(h.FormatStatus(), "HEALTHY") {
		t.Errorf("FormatStatus() = %q, want to contain HEALTHY", h.FormatStatus())
	}

	// Unreachable server
	hUnreach := ServerHealth{
		ReachErr:      errors.New("connection refused"),
		OverallStatus: StatusUnreachable,
	}
	computeOverallStatus(&hUnreach)
	if hUnreach.OverallStatus != StatusUnreachable {
		t.Errorf("expected UNREACHABLE, got %s", hUnreach.OverallStatus)
	}
	if !strings.Contains(hUnreach.FormatStatus(), "UNREACHABLE") {
		t.Errorf("FormatStatus() = %q, want UNREACHABLE", hUnreach.FormatStatus())
	}
}
