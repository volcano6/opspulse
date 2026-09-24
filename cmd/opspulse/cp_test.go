package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/volcano6/opspulse/internal/config"
	"github.com/volcano6/opspulse/internal/server"
	"github.com/volcano6/opspulse/internal/sftp"
)

// newCPTestStore writes the given servers into a private inventory and
// returns a store over it.
func newCPTestStore(t *testing.T, servers ...server.Server) *server.Store {
	t.Helper()
	t.Setenv(config.EnvHome, t.TempDir())

	store := server.NewDefaultStore()
	for _, srv := range servers {
		if err := store.Save(srv); err != nil {
			t.Fatalf("failed to save server %q: %v", srv.Name, err)
		}
	}
	return store
}

func TestResolveJumpServer(t *testing.T) {
	store := newCPTestStore(t,
		server.Server{Name: "bastion", Host: "203.0.113.9", Port: 2222, User: "ops", KeyPath: "~/.ssh/bastion.pem"},
		server.Server{Name: "standalone", Host: "10.0.0.1", User: "root"},
	)

	t.Run("a configured jump host is resolved from its own inventory entry", func(t *testing.T) {
		srv := server.Server{Name: "internal", Host: "10.0.0.5", User: "deploy", JumpHost: "bastion"}

		jump, err := resolveJumpServer(store, &srv)
		if err != nil {
			t.Fatalf("resolveJumpServer() error: %v", err)
		}
		if jump == nil {
			t.Fatal("the jump host was not resolved")
		}
		// The hop's own port, user and key are what the tunnel authenticates
		// with; the target's would connect to the wrong place with the wrong
		// identity.
		if jump.Name != "bastion" || jump.Host != "203.0.113.9" || jump.Port != 2222 || jump.User != "ops" || jump.KeyPath != "~/.ssh/bastion.pem" {
			t.Errorf("jump = %+v, want the bastion inventory entry", jump)
		}
	})

	t.Run("an unknown jump host is an error rather than a direct connection", func(t *testing.T) {
		srv := server.Server{Name: "internal", Host: "10.0.0.5", JumpHost: "ghost"}

		jump, err := resolveJumpServer(store, &srv)
		if err == nil {
			t.Fatal("expected an error for a jump host that is not in the inventory")
		}
		if jump != nil {
			t.Errorf("jump = %+v, want nil so no connection is attempted", jump)
		}
		if !strings.Contains(err.Error(), `"ghost"`) {
			t.Errorf("error %q does not name the missing jump host", err)
		}
	})

	t.Run("a server without a jump host resolves to nil", func(t *testing.T) {
		srv := server.Server{Name: "standalone", Host: "10.0.0.1"}

		jump, err := resolveJumpServer(store, &srv)
		if err != nil {
			t.Fatalf("resolveJumpServer() error: %v", err)
		}
		if jump != nil {
			t.Errorf("jump = %+v, want nil for a direct connection", jump)
		}
	})
}

// sftpStub records what a transfer helper asked the SFTP client for.
type sftpStub struct {
	server *server.Server
	jump   *server.Server
	err    error
}

// stubSFTPClient replaces the client constructor for the duration of a test so
// the resolved jump host can be observed without opening a connection.
func stubSFTPClient(t *testing.T) *sftpStub {
	t.Helper()

	stub := &sftpStub{err: errors.New("stub: stop before opening a connection")}
	restore := newSFTPClient
	newSFTPClient = func(srv server.Server, jump *server.Server, _ time.Duration, _ io.Writer) (*sftp.Client, error) {
		stub.server, stub.jump = &srv, jump
		return nil, stub.err
	}
	t.Cleanup(func() { newSFTPClient = restore })

	return stub
}

func TestExecuteUpload_PassesResolvedJumpHostToClient(t *testing.T) {
	store := newCPTestStore(t,
		server.Server{Name: "bastion", Host: "203.0.113.9", Port: 2222, User: "ops"},
		server.Server{Name: "internal", Host: "10.0.0.5", User: "deploy", JumpHost: "bastion"},
	)

	localPath := filepath.Join(t.TempDir(), "upload.txt")
	if err := os.WriteFile(localPath, []byte("payload"), 0o600); err != nil {
		t.Fatalf("write local file: %v", err)
	}

	stub := stubSFTPClient(t)

	err := executeUpload(store, "internal", localPath, "/tmp/upload.txt")
	if !errors.Is(err, stub.err) {
		t.Fatalf("executeUpload() error = %v, want the stubbed failure", err)
	}
	if stub.server.Name != "internal" {
		t.Errorf("client server = %q, want internal", stub.server.Name)
	}
	if stub.jump == nil {
		t.Fatal("upload dropped the jump host and would have connected directly")
	}
	if stub.jump.Name != "bastion" || stub.jump.Port != 2222 || stub.jump.User != "ops" {
		t.Errorf("jump = %+v, want the bastion inventory entry", stub.jump)
	}
}

func TestExecuteDownload_PassesResolvedJumpHostToClient(t *testing.T) {
	store := newCPTestStore(t,
		server.Server{Name: "bastion", Host: "203.0.113.9", Port: 2222, User: "ops"},
		server.Server{Name: "internal", Host: "10.0.0.5", User: "deploy", JumpHost: "bastion"},
	)

	stub := stubSFTPClient(t)

	err := executeDownload(store, "internal", "/var/log/app.log", filepath.Join(t.TempDir(), "app.log"))
	if !errors.Is(err, stub.err) {
		t.Fatalf("executeDownload() error = %v, want the stubbed failure", err)
	}
	if stub.server.Name != "internal" {
		t.Errorf("client server = %q, want internal", stub.server.Name)
	}
	if stub.jump == nil {
		t.Fatal("download dropped the jump host and would have connected directly")
	}
	if stub.jump.Name != "bastion" {
		t.Errorf("jump = %+v, want the bastion inventory entry", stub.jump)
	}
}

func TestExecuteUpload_UnknownJumpHostFailsBeforeConnecting(t *testing.T) {
	store := newCPTestStore(t, server.Server{Name: "internal", Host: "10.0.0.5", JumpHost: "ghost"})

	localPath := filepath.Join(t.TempDir(), "upload.txt")
	if err := os.WriteFile(localPath, []byte("payload"), 0o600); err != nil {
		t.Fatalf("write local file: %v", err)
	}

	stub := stubSFTPClient(t)

	err := executeUpload(store, "internal", localPath, "/tmp/upload.txt")
	if errors.Is(err, stub.err) {
		t.Fatal("a connection was attempted despite the unknown jump host")
	}
	if err == nil || !strings.Contains(err.Error(), `"ghost"`) {
		t.Errorf("error = %v, want the unresolved jump host to be named", err)
	}
}

func TestParseRemotePath(t *testing.T) {
	tests := []struct {
		input      string
		wantServer string
		wantPath   string
		wantRemote bool
	}{
		{"vps-1:/var/log/nginx.log", "vps-1", "/var/log/nginx.log", true},
		{"web-01:config.yaml", "web-01", "config.yaml", true},
		{"./local/path/file.txt", "", "./local/path/file.txt", false},
		{"/var/log/nginx.log", "", "/var/log/nginx.log", false},
		{`C:\Users\test\file.txt`, "", `C:\Users\test\file.txt`, false},
		{"C:/Users/test/file.txt", "", "C:/Users/test/file.txt", false},
		{"D:\\", "", "D:\\", false},
		{":invalid", "", ":invalid", false},
		{"", "", "", false},
	}

	for _, tt := range tests {
		server, path, isRemote := parseRemotePath(tt.input)
		if isRemote != tt.wantRemote || server != tt.wantServer || path != tt.wantPath {
			t.Errorf("parseRemotePath(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.input, server, path, isRemote, tt.wantServer, tt.wantPath, tt.wantRemote)
		}
	}
}

func TestFormatTransferBytes(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.00 KB"},
		{1048576, "1.00 MB"},
		{1073741824, "1.00 GB"},
		{5368709120, "5.00 GB"},
	}

	for _, tt := range tests {
		got := formatTransferBytes(tt.bytes)
		if got != tt.want {
			t.Errorf("formatTransferBytes(%d) = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}
