package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestBuildDockerPsScript(t *testing.T) {
	scriptRunning := buildDockerPsScript(false)
	if strings.Contains(scriptRunning, "-a ") {
		t.Errorf("expected running script to not contain '-a ', got: %s", scriptRunning)
	}
	if !strings.Contains(scriptRunning, "docker ps --format '{{json .}}'") {
		t.Errorf("unexpected script: %s", scriptRunning)
	}

	scriptAll := buildDockerPsScript(true)
	if !strings.Contains(scriptAll, "docker ps -a --format '{{json .}}'") {
		t.Errorf("expected all script to contain '-a ', got: %s", scriptAll)
	}
}

func TestParseDockerPsOutput(t *testing.T) {
	sampleJSON := `{"ID":"1a2b3c4d5e6f7g8h","Names":"web-nginx","Image":"nginx:alpine","Command":"/docker-entrypoint.sh nginx -g 'daemon off;'","CreatedAt":"2026-09-01 10:00:00","RunningFor":"2 days ago","Status":"Up 2 days","Ports":"0.0.0.0:80->80/tcp","State":"running"}
{"ID":"9z8y7x6w5v4u3t2s","Names":"db-postgres","Image":"postgres:16","Command":"docker-entrypoint.sh postgres","CreatedAt":"2026-09-01 10:01:00","RunningFor":"2 days ago","Status":"Up 2 days (healthy)","Ports":"127.0.0.1:5432->5432/tcp","State":"running"}
`
	items, err := parseDockerPsOutput(sampleJSON)
	if err != nil {
		t.Fatalf("unexpected error parsing docker ps output: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 containers, got %d", len(items))
	}

	if items[0].Names != "web-nginx" || items[0].Image != "nginx:alpine" {
		t.Errorf("unexpected item[0]: %+v", items[0])
	}
	if items[1].Names != "db-postgres" {
		t.Errorf("unexpected item[1]: %+v", items[1])
	}

	// Empty input
	emptyItems, err := parseDockerPsOutput("   \n\n ")
	if err != nil {
		t.Fatalf("unexpected error on empty output: %v", err)
	}
	if len(emptyItems) != 0 {
		t.Errorf("expected 0 items, got %d", len(emptyItems))
	}

	// Malformed input
	_, err = parseDockerPsOutput("not-json")
	if err == nil {
		t.Error("expected error for malformed json")
	}
}

func TestRenderDockerPsTable(t *testing.T) {
	containers := []remoteContainerItem{
		{
			ID:         "1a2b3c4d5e6f7g8h",
			Names:      "web-nginx",
			Image:      "nginx:alpine",
			Command:    "nginx -g 'daemon off;'",
			RunningFor: "2 days ago",
			Status:     "Up 2 days",
			Ports:      "0.0.0.0:80->80/tcp",
			State:      "running",
		},
		{
			ID:         "9z8y7x",
			Names:      "redis",
			Image:      "redis:latest",
			Command:    "redis-server --very-long-argument-here-exceeding-standard-width",
			CreatedAt:  "yesterday",
			Status:     "Exited (0)",
			Ports:      "",
			State:      "exited",
		},
	}

	var buf bytes.Buffer
	err := renderDockerPsTable(&buf, containers)
	if err != nil {
		t.Fatalf("renderDockerPsTable error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "CONTAINER ID") || !strings.Contains(out, "web-nginx") || !strings.Contains(out, "redis") {
		t.Errorf("unexpected table output:\n%s", out)
	}
	if !strings.Contains(out, "1a2b3c4d5e6f") { // Truncated to 12 chars
		t.Errorf("expected truncated ID 1a2b3c4d5e6f in output:\n%s", out)
	}
	if !strings.Contains(out, "...") { // Long command truncated
		t.Errorf("expected truncated command with '...' in output:\n%s", out)
	}
}

func TestBuildDockerLogsScript(t *testing.T) {
	s1 := buildDockerLogsScript("my-app", "50", false, false)
	if s1 != "docker logs --tail 50 my-app\n" {
		t.Errorf("unexpected script: %q", s1)
	}

	s2 := buildDockerLogsScript("my-app", "200", true, true)
	if !strings.Contains(s2, "--tail 200") || !strings.Contains(s2, "-t") || !strings.Contains(s2, "-f") || !strings.Contains(s2, "my-app") {
		t.Errorf("unexpected script with all flags: %q", s2)
	}

	s3 := buildDockerLogsScript("my-app", "", false, false)
	if s3 != "docker logs my-app\n" {
		t.Errorf("unexpected script with empty tail: %q", s3)
	}
}
