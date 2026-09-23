package sftp

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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
		// Without this, an ssh-agent offering keys the user never chose can
		// exhaust the server's authentication attempts before the real key.
		if !strings.Contains(argsStr, "-o IdentitiesOnly=yes") {
			t.Errorf("missing IdentitiesOnly in args: %s", argsStr)
		}
		if !strings.Contains(argsStr, "deploy@10.0.0.1:/var/www") {
			t.Errorf("missing target in args: %s", argsStr)
		}
		// Options must precede the destination, otherwise sftp reads them as a
		// remote path.
		if strings.Index(argsStr, "deploy@10.0.0.1") < strings.Index(argsStr, "-i ") {
			t.Errorf("destination precedes options in args: %s", argsStr)
		}
	})
}

func TestBuildLaunchCommandRejectsLegacy1PRefs(t *testing.T) {
	client := ClientInfo{Type: ClientOpenSSH, Name: "OpenSSH sftp", Path: "sftp"}

	t.Run("op:// key path", func(t *testing.T) {
		srv := server.Server{
			Name:    "legacy-key",
			Host:    "10.0.0.3",
			Port:    22,
			User:    "deploy",
			KeyPath: "op://Vault/Item/private_key",
		}
		_, err := BuildLaunchCommand(client, srv, "/var/log")
		if err == nil {
			t.Fatal("expected error for op:// key path, got nil")
		}
		if !strings.Contains(err.Error(), "ops 1p restore") {
			t.Errorf("expected error to point at 'ops 1p restore', got: %v", err)
		}
	})

	t.Run("op:// password", func(t *testing.T) {
		srv := server.Server{
			Name:     "legacy-pass",
			Host:     "10.0.0.4",
			Port:     22,
			User:     "root",
			Password: "op://Vault/Item/password",
		}
		_, err := BuildLaunchCommand(client, srv, "/root")
		if err == nil {
			t.Fatal("expected error for op:// password, got nil")
		}
		if !strings.Contains(err.Error(), "ops 1p restore") {
			t.Errorf("expected error to point at 'ops 1p restore', got: %v", err)
		}
	})
}

func TestBuildLaunchCommandOpenSSHReusesControlMaster(t *testing.T) {
	if !server.ControlMasterEnabled() {
		t.Skip("connection multiplexing is disabled on this platform")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	client := ClientInfo{Type: ClientOpenSSH, Name: "OpenSSH sftp", Path: "sftp"}
	srv := server.Server{Name: "web", Host: "10.0.0.1", Port: 22, User: "deploy"}

	// Before the socket directory exists the flags are withheld: ssh and sftp
	// fail outright on a ControlPath they cannot bind.
	cmd, err := BuildLaunchCommand(client, srv, "/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if argsStr := strings.Join(cmd.Args, " "); strings.Contains(argsStr, "ControlMaster") {
		t.Fatalf("args = %s, want no multiplexing before the directory exists", argsStr)
	}

	if err := server.EnsureControlMasterDir(); err != nil {
		t.Fatalf("EnsureControlMasterDir() error: %v", err)
	}
	cmd, err = BuildLaunchCommand(client, srv, "/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	argsStr := strings.Join(cmd.Args, " ")
	// Same ControlPath pattern the ssh command uses, which is what lets an
	// 'ops ssh' session be reused here instead of authenticating again.
	for _, want := range []string{"-o ControlMaster=auto", "-o ControlPath=", "-o ControlPersist=10m"} {
		if !strings.Contains(argsStr, want) {
			t.Errorf("args = %s, missing %q", argsStr, want)
		}
	}
	if strings.Index(argsStr, "ControlPath") > strings.Index(argsStr, "deploy@10.0.0.1") {
		t.Errorf("destination precedes the multiplexing options: %s", argsStr)
	}
}

func TestBuildLaunchCommandOpenSSHLegacyHost(t *testing.T) {
	client := ClientInfo{Type: ClientOpenSSH, Name: "OpenSSH sftp", Path: "sftp"}

	legacy := server.Server{
		Name: "legacy-1", Host: "192.0.2.10", Port: 22, User: "www",
		Password: "secret", Tags: []string{"legacy-ssh"},
	}
	cmd, err := BuildLaunchCommand(client, legacy, "/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	argsStr := strings.Join(cmd.Args, " ")
	// OpenSSH 9.x refuses ssh-rsa host keys unless they are re-enabled, and a
	// legacy-only daemon offers nothing else - so without this sftp dies at
	// negotiation while ops ssh, which does inject it, connects fine.
	if !strings.Contains(argsStr, "HostKeyAlgorithms=+ssh-rsa,ssh-dss") {
		t.Errorf("args = %s, want the legacy host key algorithms re-enabled", argsStr)
	}

	modern := server.Server{Name: "web", Host: "10.0.0.1", Port: 22, User: "deploy"}
	cmd, err = BuildLaunchCommand(client, modern, "/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if argsStr := strings.Join(cmd.Args, " "); strings.Contains(argsStr, "HostKeyAlgorithms") {
		t.Errorf("args = %s, want no weak algorithms for a modern host", argsStr)
	}
}

func TestBuildLaunchCommandOpenSSHPasswordServer(t *testing.T) {
	client := ClientInfo{Type: ClientOpenSSH, Name: "OpenSSH sftp", Path: "sftp"}
	srv := server.Server{
		Name: "jump-1", Host: "192.168.10.100", Port: 22, User: "root",
		Password: "secret",
	}

	cmd, err := BuildLaunchCommand(client, srv, "/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	argsStr := strings.Join(cmd.Args, " ")
	if !strings.Contains(argsStr, "-o PubkeyAuthentication=no") {
		t.Errorf("args = %s, want pubkey auth disabled for a password-only server", argsStr)
	}
	if !strings.Contains(argsStr, "-o PreferredAuthentications=password,keyboard-interactive") {
		t.Errorf("args = %s, want password auth preferred", argsStr)
	}
	// There is no identity to pin, so pinning one would be wrong.
	if strings.Contains(argsStr, "IdentitiesOnly") {
		t.Errorf("args = %s, want no IdentitiesOnly without a key", argsStr)
	}
	if !strings.HasSuffix(argsStr, "root@192.168.10.100") {
		t.Errorf("args = %s, want the destination last", argsStr)
	}
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

func TestMaterialized1PKeyManagement(t *testing.T) {
	dir, err := Materialized1PKeyDir()
	if err != nil {
		t.Fatalf("Materialized1PKeyDir error: %v", err)
	}
	if !strings.HasSuffix(filepath.ToSlash(dir), ".ssh/opspulse-1p") {
		t.Errorf("expected path to end in .ssh/opspulse-1p, got: %s", dir)
	}

	// Create test directory and dummy keys
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}
	dummyFile1 := filepath.Join(dir, "server-a")
	dummyFile2 := filepath.Join(dir, "server-b")
	_ = os.WriteFile(dummyFile1, []byte("fake-key-1"), 0o600)
	_ = os.WriteFile(dummyFile2, []byte("fake-key-2"), 0o600)

	// Set server-a to 25 hours ago to test staleness detection
	oldTime := time.Now().Add(-25 * time.Hour)
	_ = os.Chtimes(dummyFile1, oldTime, oldTime)

	details, err := ListMaterialized1PKeyDetails()
	if err != nil {
		t.Fatalf("ListMaterialized1PKeyDetails error: %v", err)
	}
	if len(details) < 2 {
		t.Errorf("expected at least 2 key details, got %d", len(details))
	}
	for _, d := range details {
		if d.Name == "server-a" && !d.IsStale {
			t.Errorf("expected server-a to be detected as stale (>24h)")
		}
		if d.Name == "server-b" && d.IsStale {
			t.Errorf("expected server-b not to be stale")
		}
	}

	keys, err := ListMaterialized1PKeys()
	if err != nil {
		t.Fatalf("ListMaterialized1PKeys error: %v", err)
	}
	if len(keys) < 2 {
		t.Errorf("expected at least 2 keys, got %d (%v)", len(keys), keys)
	}

	// 1. Purge single server
	deletedSingle, err := PurgeMaterialized1PKeys("server-a")
	if err != nil {
		t.Fatalf("PurgeMaterialized1PKeys(server-a) error: %v", err)
	}
	if len(deletedSingle) != 1 || deletedSingle[0] != "server-a" {
		t.Errorf("expected [server-a] deleted, got %v", deletedSingle)
	}

	keysMid, err := ListMaterialized1PKeys()
	if err != nil {
		t.Fatalf("ListMaterialized1PKeys after single purge error: %v", err)
	}
	if len(keysMid) != 1 || keysMid[0] != "server-b" {
		t.Errorf("expected only server-b remaining, got: %v", keysMid)
	}

	// 2. Purge nonexistent server
	deletedNone, err := PurgeMaterialized1PKeys("nonexistent")
	if err != nil {
		t.Fatalf("PurgeMaterialized1PKeys(nonexistent) error: %v", err)
	}
	if len(deletedNone) != 0 {
		t.Errorf("expected 0 deleted for nonexistent server, got %v", deletedNone)
	}

	// 3. Purge all remaining keys
	deletedAll, err := PurgeMaterialized1PKeys("")
	if err != nil {
		t.Fatalf("PurgeMaterialized1PKeys(\"\") error: %v", err)
	}
	if len(deletedAll) != 1 || deletedAll[0] != "server-b" {
		t.Errorf("expected [server-b] deleted in purge all, got: %v", deletedAll)
	}

	keysAfter, err := ListMaterialized1PKeys()
	if err != nil {
		t.Fatalf("ListMaterialized1PKeys after purge error: %v", err)
	}
	if len(keysAfter) != 0 {
		t.Errorf("expected 0 keys after purge, got: %v", keysAfter)
	}

	// 4. Purge when already clean
	deletedClean, err := PurgeMaterialized1PKeys("")
	if err != nil {
		t.Fatalf("PurgeMaterialized1PKeys on empty dir error: %v", err)
	}
	if len(deletedClean) != 0 {
		t.Errorf("expected 0 deleted on clean dir, got %v", deletedClean)
	}
}

func TestPurgeMaterialized1PKeys_PathTraversal(t *testing.T) {
	dir, err := Materialized1PKeyDir()
	if err != nil {
		t.Fatalf("Materialized1PKeyDir error: %v", err)
	}
	parentDir := filepath.Dir(dir)
	if err := os.MkdirAll(parentDir, 0o700); err != nil {
		t.Fatalf("failed to create parent dir: %v", err)
	}

	// Create a canary file in the parent directory (~/.ssh)
	canaryFile := filepath.Join(parentDir, "opspulse_test_canary_rsa")
	canaryContent := []byte("DO_NOT_DELETE_CANARY")
	if err := os.WriteFile(canaryFile, canaryContent, 0o600); err != nil {
		t.Fatalf("failed to write canary file: %v", err)
	}
	defer func() { _ = os.Remove(canaryFile) }()

	// Traversal attacks that should be firmly rejected
	attackVectors := []string{
		"..",
		".",
		"../opspulse_test_canary_rsa",
		filepath.Join("..", "opspulse_test_canary_rsa"),
		"../../etc/passwd",
		"/etc/passwd",
		"sub/key",
		"sub\\key",
		"C:\\Windows\\System32",
		".hidden",
		"-flag",
	}

	for _, attack := range attackVectors {
		t.Run(attack, func(t *testing.T) {
			deleted, err := PurgeMaterialized1PKeys(attack)
			if err == nil {
				t.Fatalf("expected error for attack vector %q, but got success with deleted=%v", attack, deleted)
			}
			if len(deleted) != 0 {
				t.Errorf("expected no deleted files for attack vector %q, got: %v", attack, deleted)
			}
		})
	}

	// Verify canary file is still safe and intact
	data, err := os.ReadFile(canaryFile)
	if err != nil {
		t.Fatalf("canary file was damaged or removed! error: %v", err)
	}
	if string(data) != string(canaryContent) {
		t.Errorf("canary file content modified, got: %s", string(data))
	}
}
