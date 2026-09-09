package main

import (
	"bytes"
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

	// 4. Corrupted JSON file returns error instead of leaking content
	corruptedPath := filepath.Join(t.TempDir(), "corrupted.json")
	_ = os.WriteFile(corruptedPath, []byte("{\"corrupted\": true, invalid"), 0o600)
	t.Setenv(askpassDataFile, corruptedPath)
	leaked, err := readSSHAskpassPassword("any prompt")
	if err == nil {
		t.Fatalf("expected error for corrupted askpass file, got nil, returned content: %q", leaked)
	}
	if leaked != "" {
		t.Fatalf("expected empty string on error, got %q", leaked)
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

func TestTitleFilterWriter(t *testing.T) {
	tests := []struct {
		name     string
		chunks   []string
		expected string
	}{
		{
			name:     "simple OSC 0 with BEL",
			chunks:   []string{"prompt \033]0;user@host:dir\007$ "},
			expected: "prompt $ ",
		},
		{
			name:     "simple OSC 2 with BEL",
			chunks:   []string{"prompt \033]2;user@host:dir\007$ "},
			expected: "prompt $ ",
		},
		{
			name:     "OSC 0 with ST terminator",
			chunks:   []string{"prompt \033]0;user@host:dir\033\\$ "},
			expected: "prompt $ ",
		},
		{
			name:     "OSC 2 with ST terminator",
			chunks:   []string{"prompt \033]2;user@host:dir\033\\$ "},
			expected: "prompt $ ",
		},
		{
			name:     "preserve ANSI color sequences",
			chunks:   []string{"\033[32m[user@host ~]$\033[0m "},
			expected: "\033[32m[user@host ~]$\033[0m ",
		},
		{
			name:     "chunk split right at ESC",
			chunks:   []string{"hello \033", "]0;title\007world"},
			expected: "hello world",
		},
		{
			name:     "chunk split inside OSC header",
			chunks:   []string{"hello \033]0", ";title\007world"},
			expected: "hello world",
		},
		{
			name:     "chunk split at ST terminator",
			chunks:   []string{"hello \033]0;title\033", "\\world"},
			expected: "hello world",
		},
		{
			name:     "preserve non-title OSC sequence",
			chunks:   []string{"\033]10;rgb:12/34/56\007text"},
			expected: "\033]10;rgb:12/34/56\007text",
		},
		{
			name:     "preserve UTF-8 multibyte characters",
			chunks:   []string{"你好世界\033]0;火把开发机:/var/www\007！欢迎使用"},
			expected: "你好世界！欢迎使用",
		},
		{
			name:     "multiple title sequences in single stream",
			chunks:   []string{"\033]0;title1\007A\033]2;title2\007B\033]0;title3\033\\C"},
			expected: "ABC",
		},
		{
			name:     "flush pending byte when EOF without sequence completion",
			chunks:   []string{"unfinished \033"},
			expected: "unfinished \033",
		},
		{
			name:     "bare LF converted to CRLF to prevent staircase effect",
			chunks:   []string{"Welcome\nUpdates available\n  Notice\n"},
			expected: "Welcome\r\nUpdates available\r\n  Notice\r\n",
		},
		{
			name:     "existing CRLF preserved without double CR",
			chunks:   []string{"Prompt line\r\nNext line\r\n"},
			expected: "Prompt line\r\nNext line\r\n",
		},
		{
			name:     "CR and LF split across chunks",
			chunks:   []string{"Prompt line\r", "\nNext line\n"},
			expected: "Prompt line\r\nNext line\r\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := &bytes.Buffer{}
			w := newTitleFilterWriter(buf)
			for _, chunk := range tt.chunks {
				n, err := w.Write([]byte(chunk))
				if err != nil {
					t.Fatalf("Write error: %v", err)
				}
				if n != len(chunk) {
					t.Fatalf("Write returned %d, expected %d", n, len(chunk))
				}
			}
			if err := w.Flush(); err != nil {
				t.Fatalf("Flush error: %v", err)
			}
			if got := buf.String(); got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}
