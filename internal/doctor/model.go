// Package doctor provides lightweight remote server health inspection, including SSH latency, disk utilization, and Docker status.
package doctor

import (
	"fmt"
	"time"
)

// HealthStatus represents the health level of a server or component.
type HealthStatus string

// Health status levels.
const (
	// StatusHealthy indicates all probed components are operating normally.
	StatusHealthy HealthStatus = "HEALTHY"
	// StatusWarning indicates a metric exceeded warning threshold or has minor issues.
	StatusWarning HealthStatus = "WARNING"
	// StatusCritical indicates a metric exceeded critical threshold.
	StatusCritical HealthStatus = "CRITICAL"
	// StatusUnreachable indicates the server could not be reached via SSH.
	StatusUnreachable HealthStatus = "UNREACHABLE"
	// StatusNA indicates the component is not applicable or not installed.
	StatusNA HealthStatus = "N/A"
)

const (
	// WarnDiskUsage is the disk space usage percentage that triggers a warning.
	WarnDiskUsage = 85
	// CritDiskUsage is the disk space usage percentage that triggers a critical alert.
	CritDiskUsage = 90
)

// ServerHealth captures all health metrics gathered from a single server.
type ServerHealth struct {
	Name     string
	Host     string
	Port     int
	Latency  time.Duration
	ReachErr error

	DiskTotalGB  float64
	DiskUsedGB   float64
	DiskUsagePct int
	DiskStatus   HealthStatus

	DockerInstalled bool
	DockerRunning   bool
	ContainersRun   int
	ContainersStop  int
	DockerStatus    HealthStatus

	OverallStatus HealthStatus
	Notes         []string
}

// FormatStatus returns a colored string with an emoji icon for the overall status.
func (s *ServerHealth) FormatStatus() string {
	switch s.OverallStatus {
	case StatusHealthy:
		return "\033[32m🟢 HEALTHY\033[0m"
	case StatusWarning:
		return "\033[33m🟡 WARNING\033[0m"
	case StatusCritical:
		return "\033[31m🔴 CRITICAL\033[0m"
	case StatusUnreachable:
		return "\033[90m⚪ UNREACHABLE\033[0m"
	default:
		return string(s.OverallStatus)
	}
}

// FormatDisk returns a human-readable disk summary (e.g. "45% (18/40 GB)").
func (s *ServerHealth) FormatDisk() string {
	if s.DiskStatus == StatusNA || s.DiskTotalGB == 0 {
		return "-"
	}
	return fmt.Sprintf("%d%% (%.1f/%.1f GB)", s.DiskUsagePct, s.DiskUsedGB, s.DiskTotalGB)
}

// FormatDocker returns a human-readable Docker summary (e.g. "4 run, 1 stop").
func (s *ServerHealth) FormatDocker() string {
	if !s.DockerInstalled {
		return "not installed"
	}
	if !s.DockerRunning {
		return "\033[33mdaemon stopped\033[0m"
	}
	if s.ContainersStop > 0 {
		return fmt.Sprintf("%d run, %d stop", s.ContainersRun, s.ContainersStop)
	}
	return fmt.Sprintf("%d run", s.ContainersRun)
}

// FormatLatency returns the RTT in milliseconds.
func (s *ServerHealth) FormatLatency() string {
	if s.Latency <= 0 {
		return "-"
	}
	ms := float64(s.Latency.Microseconds()) / 1000.0
	return fmt.Sprintf("%.1fms", ms)
}
