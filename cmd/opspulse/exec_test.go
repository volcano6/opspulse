package main

import (
	"strings"
	"testing"
)

func TestParseExecArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantServer  string
		wantCommand string
	}{
		{
			name:        "single quoted command",
			args:        []string{"oracle-sg", "docker ps --format 'table {{.Names}}\t{{.Status}}'"},
			wantServer:  "oracle-sg",
			wantCommand: "docker ps --format 'table {{.Names}}\t{{.Status}}'",
		},
		{
			name:        "multiple arguments command",
			args:        []string{"racknerd-la", "df", "-h", "/"},
			wantServer:  "racknerd-la",
			wantCommand: "df -h /",
		},
		{
			name:        "complex piped command",
			args:        []string{"vps-01", "cat /var/log/nginx/access.log | grep 404 | wc -l"},
			wantServer:  "vps-01",
			wantCommand: "cat /var/log/nginx/access.log | grep 404 | wc -l",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serverName := tt.args[0]
			commandStr := strings.Join(tt.args[1:], " ")

			if serverName != tt.wantServer {
				t.Errorf("serverName = %q, want %q", serverName, tt.wantServer)
			}
			if commandStr != tt.wantCommand {
				t.Errorf("commandStr = %q, want %q", commandStr, tt.wantCommand)
			}
		})
	}
}
