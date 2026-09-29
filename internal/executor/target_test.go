package executor

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/volcano6/opspulse/internal/server"
)

func TestResolveTarget(t *testing.T) {
	tests := []struct {
		name       string
		store      *server.Store
		serverName string
		wantLocal  bool
		wantName   string
		wantErr    string
	}{
		{
			name:       "local keyword",
			store:      nil,
			serverName: "local",
			wantLocal:  true,
			wantName:   "local",
		},
		{
			name:       "empty name",
			store:      nil,
			serverName: "",
			wantLocal:  true,
			wantName:   "local",
		},
		{
			name:       "nil store",
			store:      nil,
			serverName: "db-1",
			wantErr:    `serverStore is nil, cannot resolve "db-1"`,
		},
		{
			name:       "unknown server",
			store:      testResolveStore(t, server.Server{Name: "known", Host: "10.0.0.1"}),
			serverName: "missing",
			wantErr:    `server "missing" not found in inventory:`,
		},
		{
			name:       "success",
			store:      testResolveStore(t, server.Server{Name: "db-1", Host: "10.0.0.1"}),
			serverName: "db-1",
			wantName:   "db-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveTarget(tt.store, tt.serverName)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.HasPrefix(err.Error(), tt.wantErr) {
					t.Fatalf("expected error prefix %q, got %q", tt.wantErr, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.IsLocal != tt.wantLocal {
				t.Errorf("IsLocal = %v, want %v", got.IsLocal, tt.wantLocal)
			}
			if got.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", got.Name, tt.wantName)
			}
		})
	}
}

func testResolveStore(t *testing.T, srv server.Server) *server.Store {
	t.Helper()
	store := server.NewStore(filepath.Join(t.TempDir(), "servers.yaml"))
	if err := store.Save(srv); err != nil {
		t.Fatalf("failed to seed store: %v", err)
	}
	return store
}
