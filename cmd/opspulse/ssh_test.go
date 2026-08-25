package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/volcano6/opspulse/internal/server"
)

func TestBuildSSHArgs(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/home/user"
	}

	tests := []struct {
		name      string
		srv       server.Server
		extraArgs []string
		want      []string
	}{
		{
			name: "default port 22 without key",
			srv: server.Server{
				Name: "vps-01",
				Host: "192.168.1.10",
				Port: 22,
				User: "root",
			},
			extraArgs: nil,
			want:      []string{"ssh", "root@192.168.1.10"},
		},
		{
			name: "custom port and key path",
			srv: server.Server{
				Name:    "vps-custom",
				Host:    "10.0.0.1",
				Port:    2222,
				User:    "ubuntu",
				KeyPath: "~/.ssh/id_ed25519",
			},
			extraArgs: nil,
			want: []string{
				"ssh",
				"-p", "2222",
				"-i", filepath.Join(home, ".ssh/id_ed25519"),
				"ubuntu@10.0.0.1",
			},
		},
		{
			name: "with extra passthrough args",
			srv: server.Server{
				Name: "vps-extra",
				Host: "1.2.3.4",
				Port: 22,
				User: "admin",
			},
			extraArgs: []string{"-o", "StrictHostKeyChecking=no", "tmux"},
			want:      []string{"ssh", "-o", "StrictHostKeyChecking=no", "tmux", "admin@1.2.3.4"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildSSHArgs("ssh", tt.srv, tt.extraArgs)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildSSHArgs() =\n%v\nwant:\n%v", got, tt.want)
			}
		})
	}
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/home/user"
	}

	if got := expandHome("~"); got != home {
		t.Errorf("expandHome('~') = %q, want %q", got, home)
	}

	if got := expandHome("~/test.key"); got != filepath.Join(home, "test.key") {
		t.Errorf("expandHome('~/test.key') = %q, want %q", got, filepath.Join(home, "test.key"))
	}

	if got := expandHome("/absolute/path"); got != "/absolute/path" {
		t.Errorf("expandHome('/absolute/path') = %q, want /absolute/path", got)
	}
}
