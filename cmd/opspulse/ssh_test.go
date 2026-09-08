package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/volcano6/opspulse/internal/server"
)

func TestBuildSSHArgs(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	testStore := server.NewDefaultStore()
	_ = testStore.Save(server.Server{Name: "bastion", Host: "1.1.1.1", User: "root", Port: 22})
	_ = testStore.Save(server.Server{Name: "bastion-key", Host: "1.1.1.2", User: "root", Port: 2222, KeyPath: "~/.ssh/jump.pem"})

	compatFlags := []string{
		"-o", "HostKeyAlgorithms=+ssh-rsa,ssh-dss",
		"-o", "PubkeyAcceptedKeyTypes=+ssh-rsa",
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
			want:      append(append([]string{"ssh"}, compatFlags...), "root@192.168.1.10"),
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
			want: append(append([]string{"ssh"}, compatFlags...),
				"-p", "2222",
				"-o", "IdentitiesOnly=yes",
				"-i", filepath.Join(home, ".ssh/id_ed25519"),
				"ubuntu@10.0.0.1",
			),
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
			want: append(append([]string{"ssh"}, compatFlags...),
				"-o", "StrictHostKeyChecking=no", "tmux", "admin@1.2.3.4",
			),
		},
		{
			name: "configured password disables public key attempts",
			srv: server.Server{
				Name:     "vps-password",
				Host:     "1.2.3.5",
				Port:     22,
				User:     "root",
				Password: "secret",
			},
			want: append(append([]string{"ssh"}, compatFlags...),
				"-o", "PubkeyAuthentication=no",
				"-o", "PreferredAuthentications=password,keyboard-interactive",
				"root@1.2.3.5",
			),
		},
		{
			name: "server with jump host",
			srv: server.Server{
				Name:     "vps-internal",
				Host:     "vps2",
				Port:     22,
				User:     "ubuntu",
				JumpHost: "bastion",
			},
			want: append(append([]string{"ssh"}, compatFlags...),
				"-o", "ProxyCommand=ssh -W %h:%p -o HostKeyAlgorithms=+ssh-rsa,ssh-dss -o PubkeyAcceptedKeyTypes=+ssh-rsa root@1.1.1.1",
				"ubuntu@vps2",
			),
		},
		{
			name: "server with jump host using private key",
			srv: server.Server{
				Name:     "vps-internal-key",
				Host:     "vps2",
				Port:     22,
				User:     "ubuntu",
				JumpHost: "bastion-key",
			},
			want: append(append([]string{"ssh"}, compatFlags...),
				"-o", fmt.Sprintf("ProxyCommand=ssh -W %%h:%%p -o HostKeyAlgorithms=+ssh-rsa,ssh-dss -o PubkeyAcceptedKeyTypes=+ssh-rsa -o IdentitiesOnly=yes -i %s -p 2222 root@1.1.1.2", filepath.ToSlash(filepath.Join(home, ".ssh/jump.pem"))),
				"ubuntu@vps2",
			),
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

func TestReadSSHAskpassPasswordPreservesBytes(t *testing.T) {
	want := "p@ss word\nwith-special-$'chars"
	passwordPath := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(passwordPath, []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(askpassDataFile, passwordPath)

	got, err := readSSHAskpassPassword("")
	if err != nil {
		t.Fatalf("readSSHAskpassPassword() error: %v", err)
	}
	if got != want {
		t.Fatalf("readSSHAskpassPassword() = %q, want %q", got, want)
	}
}

func TestReadSSHAskpassPasswordMultiHost(t *testing.T) {
	cfg := askpassConfig{
		DefaultPass: "fallback-pass",
		HostPass: map[string]string{
			"116.62.16.170": "jump-pass",
			"hb170":         "jump-pass",
			"huobaworker":   "worker-pass",
			"hb-worker":     "worker-pass",
		},
	}
	payload, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	passwordPath := filepath.Join(t.TempDir(), "password.json")
	if err := os.WriteFile(passwordPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(askpassDataFile, passwordPath)

	// 1. Jump host prompt matches jump-pass
	jumpPass, err := readSSHAskpassPassword("www@116.62.16.170's password: ")
	if err != nil || jumpPass != "jump-pass" {
		t.Fatalf("expected 'jump-pass', got %q, err: %v", jumpPass, err)
	}

	// 2. Target host prompt matches worker-pass
	targetPass, err := readSSHAskpassPassword("www@huobaworker's password: ")
	if err != nil || targetPass != "worker-pass" {
		t.Fatalf("expected 'worker-pass', got %q, err: %v", targetPass, err)
	}

	// 3. Unknown prompt returns fallback default
	defaultPass, err := readSSHAskpassPassword("root@unknown's password: ")
	if err != nil || defaultPass != "fallback-pass" {
		t.Fatalf("expected 'fallback-pass', got %q, err: %v", defaultPass, err)
	}
}

func TestSelectServerInteractively(t *testing.T) {
	servers := []server.Server{
		{Name: "web-prod", Host: "10.0.0.1", User: "root", Tags: []string{"prod"}, Description: "Web server"},
		{Name: "db-prod", Host: "10.0.0.2", User: "postgres", Description: "Database server"},
	}

	// 1. Single server connects directly
	single := []server.Server{servers[0]}
	got, err := selectServerInteractively(nil, nil, single)
	if err != nil || got.Name != "web-prod" {
		t.Fatalf("single server auto-select failed: %v", err)
	}

	// 2. Empty Enter selects default [1]
	inEnter := strings.NewReader("\n")
	got, err = selectServerInteractively(inEnter, nil, servers)
	if err != nil || got.Name != "web-prod" {
		t.Fatalf("empty enter select failed: %v", err)
	}

	// 3. Numeric choice "2"
	inTwo := strings.NewReader("2\n")
	got, err = selectServerInteractively(inTwo, nil, servers)
	if err != nil || got.Name != "db-prod" {
		t.Fatalf("numeric choice 2 select failed: %v", err)
	}

	// 4. Name prefix choice "db"
	inPrefix := strings.NewReader("db\n")
	got, err = selectServerInteractively(inPrefix, nil, servers)
	if err != nil || got.Name != "db-prod" {
		t.Fatalf("name prefix choice select failed: %v", err)
	}

	// 5. Cancel "q"
	inCancel := strings.NewReader("q\n")
	_, err = selectServerInteractively(inCancel, nil, servers)
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("expected cancel error, got: %v", err)
	}

	// 6. Invalid choice
	inInvalid := strings.NewReader("99\n")
	_, err = selectServerInteractively(inInvalid, nil, servers)
	if err == nil || !strings.Contains(err.Error(), "invalid server number") {
		t.Fatalf("expected invalid error, got: %v", err)
	}
}
