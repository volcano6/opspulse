package doctor

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/server"
)

// ProbeScript is the bash script executed remotely to collect health metrics.
const ProbeScript = `echo "---PROBE_START---"
echo "---DISK---"
df -k / 2>/dev/null | awk 'NR==2 {print $2, $3, $4, $5}'
echo "---DOCKER---"
if command -v docker >/dev/null 2>&1; then
  if docker info >/dev/null 2>&1; then
    echo "DOCKER_RUNNING"
    docker ps -a --format '{{.Status}}'
  else
    echo "DOCKER_DAEMON_STOPPED"
  fi
else
  echo "DOCKER_NOT_INSTALLED"
fi
echo "---PROBE_END---"
`

// InspectServer conducts health probes on a single remote server.
func InspectServer(ctx context.Context, srv server.Server, exec *executor.SSHExecutor) ServerHealth {
	health := ServerHealth{
		Name:          srv.Name,
		Host:          srv.Host,
		Port:          srv.Port,
		DiskStatus:    StatusNA,
		DockerStatus:  StatusNA,
		OverallStatus: StatusHealthy,
	}

	start := time.Now()
	var buf bytes.Buffer
	target := executor.NewServerTarget(srv)

	res, err := exec.Execute(ctx, target, "doctor-probe", ProbeScript, &buf)
	health.Latency = time.Since(start)

	if err != nil || (res != nil && !res.Success) {
		health.ReachErr = err
		if health.ReachErr == nil && res != nil && res.Error != nil {
			health.ReachErr = res.Error
		}
		health.OverallStatus = StatusUnreachable
		return health
	}

	parseProbeOutput(buf.String(), &health)
	computeOverallStatus(&health)
	return health
}

func parseProbeOutput(output string, health *ServerHealth) {
	diskSection := extractSection(output, "---DISK---", "---DOCKER---")
	parseDiskSection(diskSection, health)

	dockerSection := extractSection(output, "---DOCKER---", "---PROBE_END---")
	parseDockerSection(dockerSection, health)
}

func extractSection(output, startMarker, endMarker string) string {
	start := strings.Index(output, startMarker)
	if start == -1 {
		return ""
	}
	start += len(startMarker)

	end := strings.Index(output[start:], endMarker)
	if end == -1 {
		return strings.TrimSpace(output[start:])
	}
	return strings.TrimSpace(output[start : start+end])
}

func parseDiskSection(section string, health *ServerHealth) {
	fields := strings.Fields(section)
	if len(fields) < 4 {
		return
	}

	totalKB, err1 := strconv.ParseFloat(fields[0], 64)
	usedKB, err2 := strconv.ParseFloat(fields[1], 64)
	pctStr := strings.TrimSuffix(fields[3], "%")
	pct, err3 := strconv.Atoi(pctStr)

	if err1 != nil || err2 != nil || err3 != nil {
		return
	}

	health.DiskTotalGB = totalKB / (1024 * 1024)
	health.DiskUsedGB = usedKB / (1024 * 1024)
	health.DiskUsagePct = pct

	if pct >= CritDiskUsage {
		health.DiskStatus = StatusCritical
		health.Notes = append(health.Notes, fmt.Sprintf("disk critical (%d%% >= %d%%)", pct, CritDiskUsage))
	} else if pct >= WarnDiskUsage {
		health.DiskStatus = StatusWarning
		health.Notes = append(health.Notes, fmt.Sprintf("disk high (%d%% >= %d%%)", pct, WarnDiskUsage))
	} else {
		health.DiskStatus = StatusHealthy
	}
}

func parseDockerSection(section string, health *ServerHealth) {
	lines := strings.Split(section, "\n")
	if len(lines) == 0 {
		return
	}

	firstLine := strings.TrimSpace(lines[0])
	switch firstLine {
	case "DOCKER_NOT_INSTALLED":
		health.DockerInstalled = false
		health.DockerRunning = false
		health.DockerStatus = StatusNA
		return

	case "DOCKER_DAEMON_STOPPED":
		health.DockerInstalled = true
		health.DockerRunning = false
		health.DockerStatus = StatusWarning
		health.Notes = append(health.Notes, "docker daemon stopped")
		return

	case "DOCKER_RUNNING":
		health.DockerInstalled = true
		health.DockerRunning = true
		health.DockerStatus = StatusHealthy

		for _, line := range lines[1:] {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			if strings.HasPrefix(trimmed, "Up ") {
				health.ContainersRun++
			} else {
				health.ContainersStop++
			}
		}
	}
}

func computeOverallStatus(health *ServerHealth) {
	if health.ReachErr != nil {
		health.OverallStatus = StatusUnreachable
		return
	}

	if health.DiskStatus == StatusCritical {
		health.OverallStatus = StatusCritical
		return
	}

	if health.DiskStatus == StatusWarning || health.DockerStatus == StatusWarning {
		health.OverallStatus = StatusWarning
		return
	}

	health.OverallStatus = StatusHealthy
}
