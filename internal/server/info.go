package server

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"
)

// Info holds hardware, OS, and runtime status metrics for a server.
type Info struct {
	ServerName        string `json:"server_name"`
	Host              string `json:"host"`
	OS                string `json:"os"`
	Kernel            string `json:"kernel"`
	CPUModel          string `json:"cpu_model"`
	CPUCores          int    `json:"cpu_cores"`
	MemoryTotalBytes  int64  `json:"memory_total_bytes"`
	MemoryUsedBytes   int64  `json:"memory_used_bytes"`
	DiskTotalBytes    int64  `json:"disk_total_bytes"`
	DiskUsedBytes     int64  `json:"disk_used_bytes"`
	DiskFreeBytes     int64  `json:"disk_free_bytes"`
	SwapTotalBytes    int64  `json:"swap_total_bytes"`
	SwapUsedBytes     int64  `json:"swap_used_bytes"`
	Uptime            string `json:"uptime"`
	DockerInstalled   bool   `json:"docker_installed"`
	DockerVersion     string `json:"docker_version"`
	ContainersRunning int    `json:"containers_running"`
	ContainersStopped int    `json:"containers_stopped"`
	BBREnabled        bool   `json:"bbr_enabled"`
	BBRStatus         string `json:"bbr_status"`
	ProbedAt          time.Time
}

// BuildInfoProbeScript returns a single composite bash script that quickly inspects the host.
func BuildInfoProbeScript() string {
	return `#!/bin/bash
export LC_ALL=C
echo "---OS_RELEASE---"
cat /etc/os-release 2>/dev/null || true
echo "---UNAME---"
uname -r 2>/dev/null || true
echo "---NPROC---"
nproc 2>/dev/null || grep -c ^processor /proc/cpuinfo 2>/dev/null || echo 1
echo "---CPUMODEL---"
grep -m1 'model name' /proc/cpuinfo 2>/dev/null | cut -d: -f2 | sed -e 's/^[ \t]*//' || true
echo "---MEMORY---"
free -b 2>/dev/null || true
echo "---DISK---"
df -B1 -P / 2>/dev/null | tail -n 1 || true
echo "---UPTIME---"
uptime -p 2>/dev/null || uptime 2>/dev/null || true
echo "---BBR---"
sysctl -n net.ipv4.tcp_congestion_control 2>/dev/null || true
echo "---DOCKER---"
if command -v docker >/dev/null 2>&1; then
  echo "INSTALLED"
  docker version --format '{{.Server.Version}}' 2>/dev/null || docker -v 2>/dev/null || true
  docker info --format '{{.ContainersRunning}} {{.ContainersStopped}}' 2>/dev/null || echo "0 0"
else
  echo "NOT_INSTALLED"
fi
echo "---END---"
`
}

// ParseInfo parses raw stdout from the probe script into a structured Info.
func ParseInfo(serverName, host, output string) *Info {
	info := &Info{
		ServerName: serverName,
		Host:       host,
		ProbedAt:   time.Now(),
		CPUCores:   1,
	}

	sections := make(map[string][]string)
	var currentSection string

	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "---") && strings.HasSuffix(line, "---") {
			currentSection = strings.Trim(line, "-")
			continue
		}
		if currentSection != "" {
			sections[currentSection] = append(sections[currentSection], line)
		}
	}

	// 1. OS
	if osLines, ok := sections["OS_RELEASE"]; ok {
		for _, line := range osLines {
			if strings.HasPrefix(line, "PRETTY_NAME=") {
				info.OS = strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), "\"")
				break
			}
		}
	}
	if info.OS == "" {
		info.OS = "Linux"
	}

	// 2. Kernel
	if unameLines, ok := sections["UNAME"]; ok && len(unameLines) > 0 {
		info.Kernel = unameLines[0]
	}

	// 3. CPU Cores
	if nprocLines, ok := sections["NPROC"]; ok && len(nprocLines) > 0 {
		if cores, err := strconv.Atoi(nprocLines[0]); err == nil && cores > 0 {
			info.CPUCores = cores
		}
	}

	// 4. CPU Model
	if cpuLines, ok := sections["CPUMODEL"]; ok && len(cpuLines) > 0 && cpuLines[0] != "" {
		info.CPUModel = cpuLines[0]
	} else {
		info.CPUModel = "Generic CPU"
	}

	// 5. Memory & Swap
	if memLines, ok := sections["MEMORY"]; ok {
		for _, line := range memLines {
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				if strings.HasPrefix(fields[0], "Mem:") {
					total, _ := strconv.ParseInt(fields[1], 10, 64)
					used, _ := strconv.ParseInt(fields[2], 10, 64)
					info.MemoryTotalBytes = total
					info.MemoryUsedBytes = used
				} else if strings.HasPrefix(fields[0], "Swap:") {
					total, _ := strconv.ParseInt(fields[1], 10, 64)
					used, _ := strconv.ParseInt(fields[2], 10, 64)
					info.SwapTotalBytes = total
					info.SwapUsedBytes = used
				}
			}
		}
	}

	// 6. Disk
	if diskLines, ok := sections["DISK"]; ok && len(diskLines) > 0 {
		fields := strings.Fields(diskLines[0])
		if len(fields) >= 4 {
			total, _ := strconv.ParseInt(fields[1], 10, 64)
			used, _ := strconv.ParseInt(fields[2], 10, 64)
			free, _ := strconv.ParseInt(fields[3], 10, 64)
			info.DiskTotalBytes = total
			info.DiskUsedBytes = used
			info.DiskFreeBytes = free
		}
	}

	// 7. Uptime
	if uptimeLines, ok := sections["UPTIME"]; ok && len(uptimeLines) > 0 {
		info.Uptime = strings.TrimPrefix(uptimeLines[0], "up ")
	}

	// 8. BBR
	if bbrLines, ok := sections["BBR"]; ok && len(bbrLines) > 0 {
		info.BBRStatus = bbrLines[0]
		if strings.Contains(bbrLines[0], "bbr") {
			info.BBREnabled = true
		}
	}

	// 9. Docker
	if dockerLines, ok := sections["DOCKER"]; ok && len(dockerLines) > 0 {
		if dockerLines[0] == "INSTALLED" {
			info.DockerInstalled = true
			if len(dockerLines) > 1 {
				info.DockerVersion = dockerLines[1]
			}
			if len(dockerLines) > 2 {
				parts := strings.Fields(dockerLines[2])
				if len(parts) >= 2 {
					info.ContainersRunning, _ = strconv.Atoi(parts[0])
					info.ContainersStopped, _ = strconv.Atoi(parts[1])
				}
			}
		}
	}

	return info
}

// FormatBox renders a stylized, readable terminal box presenting the server metrics.
func (s *Info) FormatBox(w io.Writer) {
	bbrStr := fmt.Sprintf("%s (disabled)", s.BBRStatus)
	if s.BBREnabled {
		bbrStr = fmt.Sprintf("%s ✓", s.BBRStatus)
	}

	dockerStr := "not installed"
	if s.DockerInstalled {
		dockerStr = fmt.Sprintf("%s ✓", s.DockerVersion)
	}

	cpuStr := fmt.Sprintf("%d Cores (%s)", s.CPUCores, s.CPUModel)
	memStr := fmt.Sprintf("%s (%s used)", formatBytes(s.MemoryTotalBytes), formatBytes(s.MemoryUsedBytes))
	diskStr := fmt.Sprintf("%s (%s used / %s free)",
		formatBytes(s.DiskTotalBytes), formatBytes(s.DiskUsedBytes), formatBytes(s.DiskFreeBytes))
	swapStr := fmt.Sprintf("%s (%s used)", formatBytes(s.SwapTotalBytes), formatBytes(s.SwapUsedBytes))

	var metrics bytes.Buffer
	tw := tabwriter.NewWriter(&metrics, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "OS\t: %s\n", s.OS)
	_, _ = fmt.Fprintf(tw, "Kernel\t: %s\n", s.Kernel)
	_, _ = fmt.Fprintf(tw, "CPU\t: %s\n", cpuStr)
	_, _ = fmt.Fprintf(tw, "Memory\t: %s\n", memStr)
	_, _ = fmt.Fprintf(tw, "Disk\t: %s\n", diskStr)
	_, _ = fmt.Fprintf(tw, "Swap\t: %s\n", swapStr)
	_, _ = fmt.Fprintf(tw, "Uptime\t: %s\n", s.Uptime)
	_, _ = fmt.Fprintf(tw, "Docker\t: %s\n", dockerStr)
	if s.DockerInstalled {
		_, _ = fmt.Fprintf(tw, "Containers\t: %d running / %d stopped\n", s.ContainersRunning, s.ContainersStopped)
	}
	_, _ = fmt.Fprintf(tw, "BBR\t: %s\n", bbrStr)
	_ = tw.Flush()

	headerRows := []string{
		fmt.Sprintf("Server : %s", s.ServerName),
		fmt.Sprintf("Host   : %s", s.Host),
	}
	metricRows := strings.Split(strings.TrimSuffix(metrics.String(), "\n"), "\n")
	contentWidth := 59
	for _, row := range append(headerRows, metricRows...) {
		contentWidth = max(contentWidth, utf8.RuneCountInString(row))
	}
	borderWidth := contentWidth + 4

	_, _ = fmt.Fprintf(w, "\n╔%s╗\n", strings.Repeat("═", borderWidth))
	for _, row := range headerRows {
		formatBoxRow(w, row, contentWidth)
	}
	_, _ = fmt.Fprintf(w, "╠%s╣\n", strings.Repeat("═", borderWidth))
	for _, row := range metricRows[:7] {
		formatBoxRow(w, row, contentWidth)
	}
	_, _ = fmt.Fprintf(w, "╟%s╢\n", strings.Repeat("─", borderWidth))
	for _, row := range metricRows[7:] {
		formatBoxRow(w, row, contentWidth)
	}
	_, _ = fmt.Fprintf(w, "╚%s╝\n", strings.Repeat("═", borderWidth))
}

func formatBoxRow(w io.Writer, row string, contentWidth int) {
	padding := contentWidth - utf8.RuneCountInString(row)
	_, _ = fmt.Fprintf(w, "║  %s%s  ║\n", row, strings.Repeat(" ", padding))
}

func formatBytes(b int64) string {
	if b <= 0 {
		return "0 B"
	}
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	return fmt.Sprintf("%.2f %s", float64(b)/float64(div), units[exp])
}
