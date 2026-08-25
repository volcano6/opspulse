package sftp

import (
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
