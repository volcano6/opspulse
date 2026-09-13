package sftp

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/volcano6/opspulse/internal/server"
)

func TestNewClient_InvalidHost(t *testing.T) {
	srv := server.Server{
		Name:     "invalid-server",
		Host:     "192.0.2.1", // Test-Net-1 unrouteable
		Port:     2222,
		User:     "root",
		Password: "fake-password",
	}

	// Should fail connection quickly
	_, err := NewClient(srv, 50*time.Millisecond)
	if err == nil {
		t.Error("expected connection error for unrouteable host, got nil")
	}
}

func TestNewClient_NoAuth(t *testing.T) {
	srv := server.Server{
		Name:    "no-auth-server",
		Host:    "127.0.0.1",
		Port:    22,
		KeyPath: "/non/existent/key/path",
	}

	_, err := NewClient(srv, 100*time.Millisecond)
	if err == nil {
		t.Error("expected auth error for non-existent key path, got nil")
	}
}

func TestDownloadDir_PathTraversalValidation(t *testing.T) {
	localDir := filepath.Join(t.TempDir(), "dest")
	cleanBase := filepath.Clean(localDir)

	testCases := []struct {
		relPath string
		valid   bool
	}{
		{"safe/file.txt", true},
		{"safe/subdir/subfile.txt", true},
		{"../escape.txt", false},
		{"../../etc/passwd", false},
		{"safe/../../escape.txt", false},
	}

	for _, tc := range testCases {
		target := filepath.Join(localDir, filepath.FromSlash(tc.relPath))
		cleanTarget := filepath.Clean(target)
		isSafe := cleanTarget == cleanBase || (len(cleanTarget) > len(cleanBase) && cleanTarget[:len(cleanBase)+1] == cleanBase+string(filepath.Separator))
		if isSafe != tc.valid {
			t.Errorf("path %q: expected valid=%v, got %v", tc.relPath, tc.valid, isSafe)
		}
	}
}
