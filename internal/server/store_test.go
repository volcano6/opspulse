package server

import (
	"path/filepath"
	"testing"
)

func TestServer_Validate(t *testing.T) {
	tests := []struct {
		name    string
		srv     Server
		wantErr bool
	}{
		{
			name: "valid server",
			srv: Server{
				Name: "vps-01",
				Host: "192.168.1.10",
				Port: 22,
				User: "root",
			},
			wantErr: false,
		},
		{
			name: "missing name",
			srv: Server{
				Host: "192.168.1.10",
			},
			wantErr: true,
		},
		{
			name: "missing host",
			srv: Server{
				Name: "vps-01",
			},
			wantErr: true,
		},
		{
			name: "default port and user fallback",
			srv: Server{
				Name: "vps-02",
				Host: "1.1.1.1",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.srv.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr {
				if tt.srv.Port != 22 && tt.srv.Name == "vps-02" {
					t.Errorf("expected default port 22, got %d", tt.srv.Port)
				}
				if tt.srv.User != "root" && tt.srv.Name == "vps-02" {
					t.Errorf("expected default user 'root', got %s", tt.srv.User)
				}
			}
		})
	}
}

func TestServer_Address(t *testing.T) {
	srv := Server{Host: "10.0.0.1", Port: 2222}
	if got := srv.Address(); got != "10.0.0.1:2222" {
		t.Errorf("Address() = %q, want %q", got, "10.0.0.1:2222")
	}

	srvDefault := Server{Host: "10.0.0.2"}
	if got := srvDefault.Address(); got != "10.0.0.2:22" {
		t.Errorf("Address() = %q, want %q", got, "10.0.0.2:22")
	}
}

func TestServer_LabelsAndFilter(t *testing.T) {
	srv := Server{
		Name: "oracle-sg",
		Host: "1.1.1.1",
		Tags: []string{"prod", "web"},
		Labels: map[string]string{
			"provider": "oracle",
			"region":   "singapore",
		},
	}

	// 1. FormatLabels
	formatted := srv.FormatLabels()
	if formatted != "provider=oracle,region=singapore" {
		t.Errorf("FormatLabels() = %q, want %q", formatted, "provider=oracle,region=singapore")
	}

	srvEmpty := Server{Name: "empty"}
	if srvEmpty.FormatLabels() != "-" {
		t.Errorf("FormatLabels() on empty = %q, want '-'", srvEmpty.FormatLabels())
	}

	// 2. MatchFilter
	if !srv.MatchFilter("") {
		t.Error("expected empty filter to match")
	}
	if !srv.MatchFilter("provider=oracle") {
		t.Error("expected provider=oracle to match")
	}
	if srv.MatchFilter("provider=aws") {
		t.Error("expected provider=aws to NOT match")
	}
	if !srv.MatchFilter("singapore") {
		t.Error("expected value singapore to match")
	}
	if !srv.MatchFilter("prod") {
		t.Error("expected tag prod to match")
	}
	if !srv.MatchFilter("oracle-sg") {
		t.Error("expected name oracle-sg to match")
	}
}

func TestStore_Lifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "servers.yaml")
	store := NewStore(filePath)

	if store.FilePath() != filePath {
		t.Errorf("FilePath() = %q, want %q", store.FilePath(), filePath)
	}

	// 1. Initial list should be empty
	list, err := store.List()
	if err != nil {
		t.Fatalf("List() unexpected error: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected 0 servers, got %d", len(list))
	}

	// 2. Save a server
	s1 := Server{
		Name:        "web-prod",
		Host:        "1.2.3.4",
		Port:        22,
		User:        "ubuntu",
		KeyPath:     "~/.ssh/id_rsa",
		Tags:        []string{"prod", "web"},
		Labels:      map[string]string{"provider": "oracle"},
		Description: "Production web server",
	}
	if err := store.Save(s1); err != nil {
		t.Fatalf("Save(s1) error: %v", err)
	}

	// 3. Get server
	got, err := store.Get("web-prod")
	if err != nil {
		t.Fatalf("Get('web-prod') error: %v", err)
	}
	if got.Host != "1.2.3.4" || got.User != "ubuntu" || got.Labels["provider"] != "oracle" {
		t.Errorf("Get() returned unexpected server: %+v", got)
	}

	// 4. Update server
	s1Updated := s1
	s1Updated.Host = "1.2.3.5"
	if err := store.Save(s1Updated); err != nil {
		t.Fatalf("Save(updated) error: %v", err)
	}

	gotUpdated, err := store.Get("web-prod")
	if err != nil {
		t.Fatalf("Get('web-prod') error: %v", err)
	}
	if gotUpdated.Host != "1.2.3.5" {
		t.Errorf("expected updated host '1.2.3.5', got %q", gotUpdated.Host)
	}

	// 5. Save second server
	s2 := Server{
		Name: "db-prod",
		Host: "1.2.3.10",
		User: "root",
	}
	if err := store.Save(s2); err != nil {
		t.Fatalf("Save(s2) error: %v", err)
	}

	list, err = store.List()
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(list))
	}

	// 6. Delete server
	if err := store.Delete("web-prod"); err != nil {
		t.Fatalf("Delete('web-prod') error: %v", err)
	}

	_, err = store.Get("web-prod")
	if err == nil {
		t.Error("expected error getting deleted server, got nil")
	}

	// 7. Delete non-existent server
	if err := store.Delete("non-existent"); err == nil {
		t.Error("expected error deleting non-existent server, got nil")
	}
}
