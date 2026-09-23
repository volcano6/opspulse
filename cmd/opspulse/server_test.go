package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/volcano6/opspulse/internal/server"
)

func TestFormatKeyDisplay(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/home/user"
	}

	tests := []struct {
		input string
		want  string
	}{
		{"~/.ssh/id_rsa", "~/.ssh/id_rsa"},
		{filepath.Join(home, ".ssh", "id_ed25519"), "~/.ssh/id_ed25519"},
		{"/opt/keys/custom.pem", "custom.pem"},
	}

	for _, tt := range tests {
		got := formatKeyDisplay(tt.input)
		if got != tt.want {
			t.Errorf("formatKeyDisplay(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFormatTagsAndLabels(t *testing.T) {
	s1 := server.Server{Tags: []string{"web", "prod"}}
	if got := formatTagsAndLabels(s1); got != "web,prod" {
		t.Errorf("expected 'web,prod', got %q", got)
	}

	s2 := server.Server{Labels: map[string]string{"env": "staging"}}
	if got := formatTagsAndLabels(s2); got != "env=staging" {
		t.Errorf("expected 'env=staging', got %q", got)
	}

	s3 := server.Server{Tags: []string{"api"}, Labels: map[string]string{"env": "prod"}}
	if got := formatTagsAndLabels(s3); got != "api env=prod" {
		t.Errorf("expected 'api env=prod', got %q", got)
	}

	s4 := server.Server{}
	if got := formatTagsAndLabels(s4); got != "-" {
		t.Errorf("expected '-', got %q", got)
	}

	s5 := server.Server{SkipBatch: true}
	if got := formatTagsAndLabels(s5); got != "[skip-batch]" {
		t.Errorf("expected '[skip-batch]', got %q", got)
	}

	s6 := server.Server{Tags: []string{"prod"}, SkipBatch: true}
	if got := formatTagsAndLabels(s6); got != "prod [skip-batch]" {
		t.Errorf("expected 'prod [skip-batch]', got %q", got)
	}
}

func TestRenderServerTable(t *testing.T) {
	servers := []server.Server{
		{
			Name: "bastion-1",
			Host: "192.0.2.10",
			Port: 22,
			User: "root",
		},
		{
			Name:     "worker-1",
			Host:     "worker-1.example.com",
			Port:     22,
			User:     "root",
			JumpHost: "bastion-1",
		},
		{
			Name:        "db-1",
			Host:        "198.51.100.20",
			Port:        22,
			User:        "ubuntu",
			KeyPath:     "~/.ssh/id_rsa",
			Description: "Tencent VPS",
			Tags:        []string{"prod"},
		},
	}

	var buf bytes.Buffer
	if err := renderServerTable(&buf, servers); err != nil {
		t.Fatalf("renderServerTable error: %v", err)
	}

	out := buf.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 { // Header + 3 servers
		t.Fatalf("expected 4 lines, got %d:\n%s", len(lines), out)
	}

	header := lines[0]
	for _, col := range []string{"NAME", "TARGET", "VIA JUMP", "AUTH", "TAGS", "DESCRIPTION"} {
		if !strings.Contains(header, col) {
			t.Errorf("header missing column %q: %s", col, header)
		}
	}

	if !strings.Contains(lines[1], "root@192.0.2.10") {
		t.Errorf("line 1 missing target root@192.0.2.10: %s", lines[1])
	}
	if !strings.Contains(lines[2], "root@worker-1.example.com") || !strings.Contains(lines[2], "bastion-1") {
		t.Errorf("line 2 missing target or jump host: %s", lines[2])
	}
	if !strings.Contains(lines[3], "ubuntu@198.51.100.20") || !strings.Contains(lines[3], "Tencent VPS") {
		t.Errorf("line 3 missing target or description: %s", lines[3])
	}
}

func TestTopLevelShortcuts(t *testing.T) {
	if testCmd == nil || testCmd.Use != "test <name>" {
		t.Errorf("testCmd not configured properly")
	}
	if infoCmd == nil || infoCmd.Use != "info <name>" {
		t.Errorf("infoCmd not configured properly")
	}
	if testCmd.ValidArgsFunction == nil {
		t.Errorf("testCmd missing server name auto-completion")
	}
	if infoCmd.ValidArgsFunction == nil {
		t.Errorf("infoCmd missing server name auto-completion")
	}
}
