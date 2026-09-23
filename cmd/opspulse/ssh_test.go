package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
	_ = testStore.Save(server.Server{Name: "bastion-legacy", Host: "1.1.1.3", User: "root", Port: 22, Tags: []string{"legacy-ssh"}})

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
			want: []string{"ssh",
				"-p", "2222",
				"-o", "IdentitiesOnly=yes",
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
			want: []string{"ssh",
				"-o", "StrictHostKeyChecking=no", "tmux", "admin@1.2.3.4",
			},
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
			want: []string{"ssh",
				"-o", "PubkeyAuthentication=no",
				"-o", "PreferredAuthentications=password,keyboard-interactive",
				"root@1.2.3.5",
			},
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
			want: []string{"ssh",
				"-o", "ProxyCommand=ssh -W %h:%p root@1.1.1.1",
				"ubuntu@vps2",
			},
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
			want: []string{"ssh",
				"-o", fmt.Sprintf("ProxyCommand=ssh -W %%h:%%p -o IdentitiesOnly=yes -i %s -p 2222 root@1.1.1.2", filepath.ToSlash(filepath.Join(home, ".ssh/jump.pem"))),
				"ubuntu@vps2",
			},
		},
		{
			name: "server with legacy-ssh tag enables legacy algorithms",
			srv: server.Server{
				Name: "vps-legacy",
				Host: "192.168.1.50",
				Port: 22,
				User: "root",
				Tags: []string{"legacy-ssh"},
			},
			want: []string{"ssh",
				"-o", "HostKeyAlgorithms=+ssh-rsa,ssh-dss",
				"-o", "PubkeyAcceptedKeyTypes=+ssh-rsa",
				"root@192.168.1.50",
			},
		},
		{
			name: "server with legacy jump host enables legacy algorithms in proxy command",
			srv: server.Server{
				Name:     "vps-internal-via-legacy",
				Host:     "vps3",
				Port:     22,
				User:     "ubuntu",
				JumpHost: "bastion-legacy",
			},
			want: []string{"ssh",
				"-o", "ProxyCommand=ssh -W %h:%p -o HostKeyAlgorithms=+ssh-rsa,ssh-dss -o PubkeyAcceptedKeyTypes=+ssh-rsa root@1.1.1.3",
				"ubuntu@vps3",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildSSHArgs("ssh", tt.srv, tt.extraArgs, testStore)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildSSHArgs() =\n%v\nwant:\n%v", got, tt.want)
			}
		})
	}
}

func TestBuildSSHArgsInjectsControlMaster(t *testing.T) {
	if !server.ControlMasterEnabled() {
		t.Skip("connection multiplexing is disabled on this platform")
	}
	setTestHome(t, t.TempDir())

	srv := server.Server{Name: "vps-01", Host: "192.168.1.10", Port: 22, User: "root"}

	// The socket directory does not exist yet. ssh exits 255 rather than
	// degrading when it cannot bind a ControlPath, so the flags must be absent.
	if got := buildSSHArgs("ssh", srv, nil, nil); strings.Contains(strings.Join(got, " "), "ControlMaster") {
		t.Fatalf("buildSSHArgs() = %v, want no multiplexing before the directory exists", got)
	}

	if err := server.EnsureControlMasterDir(); err != nil {
		t.Fatalf("EnsureControlMasterDir() error: %v", err)
	}

	got := buildSSHArgs("ssh", srv, nil, nil)
	joined := strings.Join(got, " ")
	for _, want := range []string{"-o ControlMaster=auto", "-o ControlPath=", "-o ControlPersist=10m"} {
		if !strings.Contains(joined, want) {
			t.Errorf("buildSSHArgs() = %v, missing %q", got, want)
		}
	}
	if last := got[len(got)-1]; last != "root@192.168.1.10" {
		t.Errorf("buildSSHArgs() last arg = %q, want the destination to stay last", last)
	}

	// A user who passes their own ControlMaster option must win, which means
	// ours has to appear earlier in argv.
	overridden := buildSSHArgs("ssh", srv, []string{"-o", "ControlMaster=no"}, nil)
	mine, theirs := -1, -1
	for i, a := range overridden {
		if a == "ControlMaster=auto" {
			mine = i
		}
		if a == "ControlMaster=no" {
			theirs = i
		}
	}
	if mine == -1 || theirs == -1 || mine > theirs {
		t.Errorf("buildSSHArgs() = %v, want the user's ControlMaster=no to come after ours", overridden)
	}
}

func TestNewAskpassFileIsPrivateAndCleanable(t *testing.T) {
	cfg := askpassConfig{DefaultPass: "s3cret", HostPass: map[string]string{"web": "s3cret"}}

	path, cleanup, err := newAskpassFile(cfg)
	if err != nil {
		t.Fatalf("newAskpassFile() error: %v", err)
	}
	dir := filepath.Dir(path)

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat payload: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("payload mode = %o, want 600", perm)
	}

	// The payload must be readable back by the helper that will consume it.
	t.Setenv(askpassDataFile, path)
	got, err := readSSHAskpassPassword("root@web's password: ")
	if err != nil {
		t.Fatalf("readSSHAskpassPassword() error: %v", err)
	}
	if got != "s3cret" {
		t.Errorf("helper returned %q, want %q", got, "s3cret")
	}

	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("cleanup left %s behind (stat err: %v)", dir, err)
	}
}

func TestAskpassEnvPointsAtThisBinary(t *testing.T) {
	env := askpassEnv("/usr/local/bin/ops", "/tmp/x/password")

	if env["SSH_ASKPASS"] != "/usr/local/bin/ops" {
		t.Errorf("SSH_ASKPASS = %q, want the ops binary", env["SSH_ASKPASS"])
	}
	// force is what makes the client consult the helper even though it has an
	// inherited terminal to prompt on.
	if env["SSH_ASKPASS_REQUIRE"] != "force" {
		t.Errorf("SSH_ASKPASS_REQUIRE = %q, want force", env["SSH_ASKPASS_REQUIRE"])
	}
	if env[askpassHelperFlag] != "1" {
		t.Errorf("%s = %q, want 1", askpassHelperFlag, env[askpassHelperFlag])
	}
	if env[askpassDataFile] != "/tmp/x/password" {
		t.Errorf("%s = %q, want the payload path", askpassDataFile, env[askpassDataFile])
	}
}

func TestReadSSHAskpassRefusesHostKeyPrompt(t *testing.T) {
	// A host-key confirmation routed to the askpass helper expects "yes" or
	// "no". Answering it with a password makes ssh's confirm loop ask again
	// forever, which turned an unknown host into a hang instead of an error.
	prompt := "The authenticity of host '10.0.0.1 (10.0.0.1)' can't be established.\n" +
		"ED25519 key fingerprint is SHA256:abc.\n" +
		"Are you sure you want to continue connecting (yes/no/[fingerprint])? "

	cfg := askpassConfig{DefaultPass: "s3cret", HostPass: map[string]string{"10.0.0.1": "s3cret"}}
	payload, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	passwordPath := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(passwordPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(askpassDataFile, passwordPath)

	got, err := readSSHAskpassPassword(prompt)
	if err == nil {
		t.Fatalf("readSSHAskpassPassword() = %q, want an error for the host key prompt", got)
	}
	if got != "" {
		t.Errorf("readSSHAskpassPassword() leaked %q while refusing", got)
	}

	// The ordinary credential prompt for the very same host must still be
	// answered, or the guard would have broken password login.
	pass, err := readSSHAskpassPassword("root@10.0.0.1's password: ")
	if err != nil {
		t.Fatalf("password prompt was refused: %v", err)
	}
	if pass != "s3cret" {
		t.Errorf("password prompt returned %q, want %q", pass, "s3cret")
	}
}

func TestIsHostKeyConfirmation(t *testing.T) {
	yes := []string{
		"The authenticity of host 'x' can't be established.\nAre you sure you want to continue connecting (yes/no/[fingerprint])? ",
		"Are you sure you want to continue connecting (yes/no)? ",
	}
	for _, p := range yes {
		if !isHostKeyConfirmation(p) {
			t.Errorf("isHostKeyConfirmation(%q) = false, want true", p)
		}
	}

	no := []string{
		"root@10.0.0.1's password: ",
		"Password: ",
		"Enter passphrase for key '/home/u/.ssh/id_ed25519': ",
		"",
	}
	for _, p := range no {
		if isHostKeyConfirmation(p) {
			t.Errorf("isHostKeyConfirmation(%q) = true, want false", p)
		}
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
			"192.0.2.10": "jump-pass",
			"bastion-1":         "jump-pass",
			"worker-1.example.com":   "worker-pass",
			"worker-1":     "worker-pass",
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
	jumpPass, err := readSSHAskpassPassword("www@192.0.2.10's password: ")
	if err != nil || jumpPass != "jump-pass" {
		t.Fatalf("expected 'jump-pass', got %q, err: %v", jumpPass, err)
	}

	// 2. Target host prompt matches worker-pass
	targetPass, err := readSSHAskpassPassword("www@worker-1.example.com's password: ")
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

func TestSelectServerInteractively_PointerIntegrity(t *testing.T) {
	servers := []server.Server{
		{Name: "server-a", Host: "1.1.1.1"},
		{Name: "server-b", Host: "2.2.2.2"},
	}

	// Match server-a by exact name
	inA := strings.NewReader("server-a\n")
	resA, err := selectServerInteractively(inA, io.Discard, servers)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resA != &servers[0] {
		t.Errorf("expected pointer to servers[0], got %p vs %p", resA, &servers[0])
	}

	// Match server-b by prefix
	inB := strings.NewReader("server-b\n")
	resB, err := selectServerInteractively(inB, io.Discard, servers)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resB != &servers[1] {
		t.Errorf("expected pointer to servers[1], got %p vs %p", resB, &servers[1])
	}
}

func TestBuildAskpassConfig_Security(t *testing.T) {
	target := server.Server{Name: "web", Host: "192.168.1.10"}
	jump := &server.Server{Name: "bastion", Host: "10.0.0.1"}

	// Scenario 1: Target uses key auth (no password), jump host uses password.
	// CRITICAL: DefaultPass MUST be empty so jump password never leaks to target!
	cfgKeyTarget := buildAskpassConfig(target, jump, "", "jump-secret")
	if cfgKeyTarget.DefaultPass != "" {
		t.Errorf("expected DefaultPass to be empty for key-auth target, got %q", cfgKeyTarget.DefaultPass)
	}
	if cfgKeyTarget.HostPass["10.0.0.1"] != "jump-secret" || cfgKeyTarget.HostPass["bastion"] != "jump-secret" {
		t.Errorf("jump host password missing in HostPass: %+v", cfgKeyTarget.HostPass)
	}
	if _, exists := cfgKeyTarget.HostPass["192.168.1.10"]; exists {
		t.Error("target host should not be in HostPass when targetPassword is empty")
	}

	// Scenario 2: Both target and jump host use passwords.
	cfgBoth := buildAskpassConfig(target, jump, "target-secret", "jump-secret")
	if cfgBoth.DefaultPass != "target-secret" {
		t.Errorf("expected DefaultPass to be 'target-secret', got %q", cfgBoth.DefaultPass)
	}
	if cfgBoth.HostPass["192.168.1.10"] != "target-secret" || cfgBoth.HostPass["web"] != "target-secret" {
		t.Errorf("target host password missing in HostPass: %+v", cfgBoth.HostPass)
	}
	if cfgBoth.HostPass["10.0.0.1"] != "jump-secret" || cfgBoth.HostPass["bastion"] != "jump-secret" {
		t.Errorf("jump host password missing in HostPass: %+v", cfgBoth.HostPass)
	}
}

func TestMatchHostPassword_DeterministicLongestMatch(t *testing.T) {
	hostPass := map[string]string{
		"bastion":        "pass-short",
		"bastion-legacy": "pass-long",
		"web":            "pass-web",
		"web.corp.local": "pass-web-fqdn",
	}

	// Should match the longer, more specific key first
	if got := matchHostPassword(hostPass, "root@bastion-legacy's password:"); got != "pass-long" {
		t.Errorf("expected 'pass-long', got %q", got)
	}

	// Should match the shorter key when only it matches
	if got := matchHostPassword(hostPass, "root@bastion's password:"); got != "pass-short" {
		t.Errorf("expected 'pass-short', got %q", got)
	}

	// FQDN longest prefix match
	if got := matchHostPassword(hostPass, "ubuntu@web.corp.local's password:"); got != "pass-web-fqdn" {
		t.Errorf("expected 'pass-web-fqdn', got %q", got)
	}

	// Unknown host
	if got := matchHostPassword(hostPass, "root@unknown's password:"); got != "" {
		t.Errorf("expected empty string for unknown host, got %q", got)
	}
}

func TestResolveTargetPassword(t *testing.T) {
	tests := []struct {
		name     string
		srv      server.Server
		wantPass string
	}{
		{
			name:     "empty password returns empty string",
			srv:      server.Server{Name: "web", Password: ""},
			wantPass: "",
		},
		{
			name:     "plaintext password with empty KeyPath returns plaintext",
			srv:      server.Server{Name: "web", Password: "plain-secret", KeyPath: ""},
			wantPass: "plain-secret",
		},
		{
			name:     "plaintext password with KeyPath set returns plaintext",
			srv:      server.Server{Name: "web", Password: "plain-secret", KeyPath: "/id_rsa"},
			wantPass: "plain-secret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveTargetPassword(tt.srv); got != tt.wantPass {
				t.Errorf("resolveTargetPassword() = %q, want %q", got, tt.wantPass)
			}
		})
	}
}
