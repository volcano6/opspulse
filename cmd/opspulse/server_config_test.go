package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/volcano6/opspulse/internal/server"
)

func TestSetServerFieldsPreservesUnchangedFields(t *testing.T) {
	store := server.NewStore(filepath.Join(t.TempDir(), "servers.yaml"))
	original := server.Server{
		Name:        "web-01",
		Host:        "192.0.2.10",
		Port:        22,
		User:        "ubuntu",
		KeyPath:     "~/.ssh/old",
		Password:    "recovery-password",
		Tags:        []string{"prod"},
		Labels:      map[string]string{"region": "sg"},
		Description: "primary",
	}
	if err := store.Save(original); err != nil {
		t.Fatal(err)
	}

	port := 2222
	key := "~/.ssh/new"
	if err := setServerFields(store, original.Name, nil, &port, &key); err != nil {
		t.Fatalf("setServerFields() error: %v", err)
	}
	got, err := store.Get(original.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.Port != port || got.KeyPath != key {
		t.Fatalf("updated fields = port %d, key %q", got.Port, got.KeyPath)
	}
	if got.Host != original.Host || got.User != original.User || got.Password != original.Password ||
		got.Description != original.Description || len(got.Tags) != 1 || got.Labels["region"] != "sg" {
		t.Fatalf("unchanged fields were modified: %+v", got)
	}
}

func TestSetServerFieldsRejectsNoChangesAndInvalidPort(t *testing.T) {
	store := server.NewStore(filepath.Join(t.TempDir(), "servers.yaml"))
	if err := store.Save(server.Server{Name: "web-01", Host: "192.0.2.10", Port: 22}); err != nil {
		t.Fatal(err)
	}
	if err := setServerFields(store, "web-01", nil, nil, nil); err == nil {
		t.Fatal("expected no-change update to fail")
	}
	invalidPort := 70000
	if err := setServerFields(store, "web-01", nil, &invalidPort, nil); err == nil {
		t.Fatal("expected invalid port update to fail")
	}
}

func TestEditServerConfigValidatesBeforeReplacing(t *testing.T) {
	tests := []struct {
		name       string
		editedYAML string
		wantErr    bool
		wantHost   string
	}{
		{
			name: "valid update",
			editedYAML: `servers:
  - name: web-01
    host: 192.0.2.20
    port: 22
    user: root
`,
			wantHost: "192.0.2.20",
		},
		{
			name:       "invalid yaml",
			editedYAML: "servers: [\n",
			wantErr:    true,
			wantHost:   "192.0.2.10",
		},
		{
			name:       "target removed",
			editedYAML: "servers: []\n",
			wantErr:    true,
			wantHost:   "192.0.2.10",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			store := server.NewStore(filepath.Join(dir, "servers.yaml"))
			if err := store.Save(server.Server{Name: "web-01", Host: "192.0.2.10", Port: 22}); err != nil {
				t.Fatal(err)
			}
			var editor string
			if runtime.GOOS == "windows" {
				editor = filepath.Join(dir, "editor.cmd")
				b64 := base64.StdEncoding.EncodeToString([]byte(tt.editedYAML))
				script := fmt.Sprintf(`@echo off
set "LAST=%%~1"
for %%%%a in (%%*) do set "LAST=%%%%~a"
powershell -NoProfile -Command "[System.IO.File]::WriteAllBytes($env:LAST, [System.Convert]::FromBase64String('%s'))"
`, b64)
				if err := os.WriteFile(editor, []byte(script), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				editor = filepath.Join(dir, "editor.sh")
				script := "#!/bin/sh\nfor last do :; done\nprintf '%s' '" + strings.ReplaceAll(tt.editedYAML, "'", "'\\''") + "' > \"$last\"\n"
				if err := os.WriteFile(editor, []byte(script), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(editor, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("VISUAL", editor)
			t.Setenv("EDITOR", "")

			err := editServerConfig(store, "web-01")
			if (err != nil) != tt.wantErr {
				t.Fatalf("editServerConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
			got, getErr := store.Get("web-01")
			if getErr != nil {
				t.Fatalf("original server unavailable after edit: %v", getErr)
			}
			if got.Host != tt.wantHost {
				t.Fatalf("host = %q, want %q", got.Host, tt.wantHost)
			}
		})
	}
}

func TestServerYAMLLine(t *testing.T) {
	data := []byte("servers:\n  - name: web-01\n    host: 192.0.2.10\n  - name: db-01\n")
	if got := serverYAMLLine(data, "db-01"); got != 4 {
		t.Fatalf("serverYAMLLine() = %d, want 4", got)
	}
}
