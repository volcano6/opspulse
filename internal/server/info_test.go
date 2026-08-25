package server

import (
	"bytes"
	"strings"
	"testing"
)

func TestBuildInfoProbeScript(t *testing.T) {
	script := BuildInfoProbeScript()
	if !strings.Contains(script, "---OS_RELEASE---") || !strings.Contains(script, "---MEMORY---") {
		t.Errorf("BuildInfoProbeScript() missing expected sections, got:\n%s", script)
	}
}

func TestParseInfo_Full(t *testing.T) {
	rawOutput := `
---OS_RELEASE---
NAME="Ubuntu"
VERSION="24.04 LTS (Noble Numbat)"
ID=ubuntu
PRETTY_NAME="Ubuntu 24.04 LTS"
---UNAME---
6.8.0-45-generic
---NPROC---
2
---CPUMODEL---
Ampere Altra (ARM64)
---MEMORY---
              total        used        free      shared  buff/cache   available
Mem:    12884901888  2254857830  8589934592    10485760  2040109466 10630044058
Swap:    2147483648           0  2147483648
---DISK---
/dev/sda1 85899345920 24696061952 61203283968 29% /
---UPTIME---
up 42 days, 5 hours, 10 minutes
---BBR---
bbr
---DOCKER---
INSTALLED
27.5.1
5 2
---END---
`

	info := ParseInfo("oracle-sg", "168.138.1.1:22", rawOutput)
	if info.ServerName != "oracle-sg" || info.Host != "168.138.1.1:22" {
		t.Errorf("unexpected server name/host: %s / %s", info.ServerName, info.Host)
	}
	if info.OS != "Ubuntu 24.04 LTS" {
		t.Errorf("expected OS 'Ubuntu 24.04 LTS', got %q", info.OS)
	}
	if info.Kernel != "6.8.0-45-generic" {
		t.Errorf("expected Kernel '6.8.0-45-generic', got %q", info.Kernel)
	}
	if info.CPUCores != 2 {
		t.Errorf("expected 2 cores, got %d", info.CPUCores)
	}
	if info.CPUModel != "Ampere Altra (ARM64)" {
		t.Errorf("expected CPUModel 'Ampere Altra (ARM64)', got %q", info.CPUModel)
	}
	if info.MemoryTotalBytes != 12884901888 || info.MemoryUsedBytes != 2254857830 {
		t.Errorf("unexpected memory: total=%d, used=%d", info.MemoryTotalBytes, info.MemoryUsedBytes)
	}
	if info.DiskTotalBytes != 85899345920 || info.DiskUsedBytes != 24696061952 || info.DiskFreeBytes != 61203283968 {
		t.Errorf("unexpected disk: total=%d, used=%d, free=%d", info.DiskTotalBytes, info.DiskUsedBytes, info.DiskFreeBytes)
	}
	if info.SwapTotalBytes != 2147483648 || info.SwapUsedBytes != 0 {
		t.Errorf("unexpected swap: total=%d, used=%d", info.SwapTotalBytes, info.SwapUsedBytes)
	}
	if info.Uptime != "42 days, 5 hours, 10 minutes" {
		t.Errorf("unexpected uptime: %q", info.Uptime)
	}
	if !info.BBREnabled || info.BBRStatus != "bbr" {
		t.Errorf("expected BBR enabled, got enabled=%v, status=%s", info.BBREnabled, info.BBRStatus)
	}
	if !info.DockerInstalled || info.DockerVersion != "27.5.1" {
		t.Errorf("expected Docker installed with 27.5.1, got installed=%v, ver=%s", info.DockerInstalled, info.DockerVersion)
	}
	if info.ContainersRunning != 5 || info.ContainersStopped != 2 {
		t.Errorf("expected 5 running, 2 stopped containers, got %d / %d", info.ContainersRunning, info.ContainersStopped)
	}

	var buf bytes.Buffer
	info.FormatBox(&buf)
	boxStr := buf.String()
	if !strings.Contains(boxStr, "oracle-sg") || !strings.Contains(boxStr, "Ubuntu 24.04 LTS") || !strings.Contains(boxStr, "27.5.1 ✓") {
		t.Errorf("FormatBox missing expected content:\n%s", boxStr)
	}
}

func TestParseInfo_NoDockerNoBBR(t *testing.T) {
	rawOutput := `
---OS_RELEASE---
PRETTY_NAME="Debian GNU/Linux 12 (bookworm)"
---UNAME---
6.1.0-22-amd64
---NPROC---
4
---CPUMODEL---
Intel Xeon E5-2680
---MEMORY---
Mem:    8388608000  4194304000  4194304000 0 0 0
Swap:            0           0           0
---DISK---
/dev/vda1 42949672960 10737418240 32212254720 25% /
---UPTIME---
up 12 hours
---BBR---
cubic
---DOCKER---
NOT_INSTALLED
---END---
`

	info := ParseInfo("debian-node", "10.0.0.1:22", rawOutput)
	if info.OS != "Debian GNU/Linux 12 (bookworm)" {
		t.Errorf("expected Debian OS, got %q", info.OS)
	}
	if info.CPUCores != 4 {
		t.Errorf("expected 4 cores, got %d", info.CPUCores)
	}
	if info.BBREnabled {
		t.Error("expected BBR disabled for cubic")
	}
	if info.DockerInstalled {
		t.Error("expected Docker not installed")
	}

	var buf bytes.Buffer
	info.FormatBox(&buf)
	boxStr := buf.String()
	if !strings.Contains(boxStr, "not installed") || !strings.Contains(boxStr, "cubic (disabled)") {
		t.Errorf("FormatBox missing expected non-installed docker info:\n%s", boxStr)
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{500, "500 B"},
		{1024, "1.00 KB"},
		{1048576, "1.00 MB"},
		{1073741824, "1.00 GB"},
		{12884901888, "12.00 GB"},
	}

	for _, tt := range tests {
		got := formatBytes(tt.bytes)
		if got != tt.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}
