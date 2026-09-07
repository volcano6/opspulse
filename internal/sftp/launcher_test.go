package sftp

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/volcano6/opspulse/internal/server"
)

func TestDetectAvailableClients(t *testing.T) {
	clients := DetectAvailableClients()
	// Should not panic, and if sftp or any client is present, return it
	t.Logf("Detected %d clients on %s", len(clients), runtime.GOOS)
	for _, c := range clients {
		t.Logf(" - [%s] %s (%s, GUI=%v)", c.Type, c.Name, c.Path, c.IsGUI)
	}
}

func TestFormatSFTPURL(t *testing.T) {
	tests := []struct {
		name            string
		user            string
		password        string
		host            string
		port            int
		path            string
		includePassword bool
		want            string
	}{
		{
			name:            "Standard user without password",
			user:            "ubuntu",
			password:        "secret",
			host:            "1.2.3.4",
			port:            22,
			path:            "/var/www",
			includePassword: false,
			want:            "sftp://ubuntu@1.2.3.4:22/var/www",
		},
		{
			name:            "Standard user with password",
			user:            "root",
			password:        "p@ss:word",
			host:            "192.168.1.10",
			port:            2222,
			path:            "/home/root",
			includePassword: true,
			want:            "sftp://root:p%40ss%3Aword@192.168.1.10:2222/home/root",
		},
		{
			name:            "IPv6 host",
			user:            "admin",
			password:        "",
			host:            "2001:db8::1",
			port:            22,
			path:            "/etc",
			includePassword: false,
			want:            "sftp://admin@[2001:db8::1]:22/etc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatSFTPURL(tt.user, tt.password, tt.host, tt.port, tt.path, tt.includePassword)
			if got != tt.want {
				t.Errorf("formatSFTPURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildLaunchCommand(t *testing.T) {
	srv := server.Server{
		Name:     "web-prod",
		Host:     "10.0.0.1",
		Port:     2200,
		User:     "deploy",
		Password: "secret-password",
		KeyPath:  "/home/user/.ssh/id_ed25519",
	}

	t.Run("WinSCP with key path", func(t *testing.T) {
		client := ClientInfo{Type: ClientWinSCP, Name: "WinSCP", Path: "winscp.exe"}
		cmd, err := BuildLaunchCommand(client, srv, "/var/log")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(cmd.Args) < 3 {
			t.Fatalf("expected at least 3 args, got %v", cmd.Args)
		}
		if cmd.Args[1] != "sftp://deploy@10.0.0.1:2200/var/log" {
			t.Errorf("unexpected URL arg: %s", cmd.Args[1])
		}
		expectedKeyArg := "/privatekey=" + filepath.Clean(srv.KeyPath)
		if cmd.Args[2] != expectedKeyArg {
			t.Errorf("unexpected key arg: %s, want %s", cmd.Args[2], expectedKeyArg)
		}
	})

	t.Run("Xftp with password", func(t *testing.T) {
		srvPass := server.Server{
			Name:     "web-pass",
			Host:     "10.0.0.2",
			Port:     22,
			User:     "root",
			Password: "mysecret",
		}
		client := ClientInfo{Type: ClientXftp, Name: "Xftp", Path: "xftp.exe"}
		cmd, err := BuildLaunchCommand(client, srvPass, "/root")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(cmd.Args) < 2 {
			t.Fatalf("expected at least 2 args, got %v", cmd.Args)
		}
		if cmd.Args[1] != "sftp://root:mysecret@10.0.0.2:22/root" {
			t.Errorf("unexpected URL arg: %s", cmd.Args[1])
		}
	})

	t.Run("FileZilla", func(t *testing.T) {
		client := ClientInfo{Type: ClientFileZilla, Name: "FileZilla", Path: "filezilla"}
		cmd, err := BuildLaunchCommand(client, srv, "/srv/app")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(cmd.Args) < 2 || !strings.HasPrefix(cmd.Args[1], "sftp://") {
			t.Errorf("unexpected args for FileZilla: %v", cmd.Args)
		}
	})

	t.Run("Cyberduck macOS", func(t *testing.T) {
		client := ClientInfo{Type: ClientCyberduck, Name: "Cyberduck", Path: "/Applications/Cyberduck.app"}
		cmd, err := BuildLaunchCommand(client, srv, "/srv/app")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cmd.Args[0] != "open" || cmd.Args[1] != "-a" || cmd.Args[2] != "Cyberduck" {
			t.Errorf("unexpected args for Cyberduck: %v", cmd.Args)
		}
	})

	t.Run("OpenSSH CLI", func(t *testing.T) {
		client := ClientInfo{Type: ClientOpenSSH, Name: "OpenSSH sftp", Path: "sftp"}
		cmd, err := BuildLaunchCommand(client, srv, "/var/www")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Expect: sftp -P 2200 -i <key> deploy@10.0.0.1:/var/www
		argsStr := strings.Join(cmd.Args, " ")
		if !strings.Contains(argsStr, "-P 2200") {
			t.Errorf("missing -P 2200 in args: %s", argsStr)
		}
		if !strings.Contains(argsStr, "-i ") {
			t.Errorf("missing -i in args: %s", argsStr)
		}
		if !strings.Contains(argsStr, "deploy@10.0.0.1:/var/www") {
			t.Errorf("missing target in args: %s", argsStr)
		}
	})
}

func TestFindClient(t *testing.T) {
	// 1. Non-existent app
	_, err := FindClient("definitely-nonexistent-app-99999", false)
	if err == nil {
		t.Error("expected error for non-existent client preference, got nil")
	}

	// 2. Mock custom file path
	tmpDir := t.TempDir()
	customApp := filepath.Join(tmpDir, "my-winscp.exe")
	if err := os.WriteFile(customApp, []byte("echo"), 0o600); err != nil {
		t.Fatalf("failed to create temp app: %v", err)
	}

	found, err := FindClient(customApp, false)
	if err != nil {
		t.Fatalf("expected to find custom app path, got err: %v", err)
	}
	if found.Type != ClientWinSCP {
		t.Errorf("expected guessed type ClientWinSCP, got %v", found.Type)
	}
}
