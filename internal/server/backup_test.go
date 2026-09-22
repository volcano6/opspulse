package server

import (
	"reflect"
	"strings"
	"testing"
)

func TestMarshalBackupRoundTrip(t *testing.T) {
	in := BackupFile{
		Machine: "workstation",
		Servers: []Server{
			srv("web", "10.0.0.1", "~/.ssh/opspulse_web"),
			{Name: "db", Host: "10.0.0.2", Port: 2222, User: "admin", Password: "hunter2"},
		},
		Keys: map[string]string{
			"web": "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----\n",
		},
	}

	data, err := MarshalBackup(in)
	if err != nil {
		t.Fatalf("MarshalBackup() error: %v", err)
	}

	out, err := ParseBackup(data)
	if err != nil {
		t.Fatalf("ParseBackup() error: %v", err)
	}

	if out.Version != BackupVersion {
		t.Errorf("version = %d, want %d", out.Version, BackupVersion)
	}
	if out.Machine != in.Machine {
		t.Errorf("machine = %q, want %q", out.Machine, in.Machine)
	}
	if !reflect.DeepEqual(out.Servers, in.Servers) {
		t.Errorf("servers round-tripped as %+v, want %+v", out.Servers, in.Servers)
	}
	if !reflect.DeepEqual(out.Keys, in.Keys) {
		t.Errorf("keys round-tripped as %+v, want %+v", out.Keys, in.Keys)
	}
}

func TestMarshalBackupFillsInVersion(t *testing.T) {
	data, err := MarshalBackup(BackupFile{Servers: []Server{srv("web", "10.0.0.1", "")}})
	if err != nil {
		t.Fatalf("MarshalBackup() error: %v", err)
	}
	if !strings.Contains(string(data), "version: 1") {
		t.Errorf("the marshalled backup does not carry a version:\n%s", data)
	}
}

// A backup of servers with no keys must not carry an empty keys map: the field is
// what a reader uses to tell "this machine holds no keys" from "this blob predates
// keys", and an explicit null muddies that.
func TestMarshalBackupOmitsEmptyKeys(t *testing.T) {
	data, err := MarshalBackup(BackupFile{Servers: []Server{srv("web", "10.0.0.1", "")}})
	if err != nil {
		t.Fatalf("MarshalBackup() error: %v", err)
	}
	if strings.Contains(string(data), "keys:") {
		t.Errorf("a keyless backup still mentions keys:\n%s", data)
	}
}

// MarshalBackup validates the servers, but only after copying them: Validate
// normalises Port and User in place, and the caller's inventory must come back
// untouched.
func TestMarshalBackupDoesNotMutateCaller(t *testing.T) {
	servers := []Server{{Name: "web", Host: "10.0.0.1"}}
	if _, err := MarshalBackup(BackupFile{Servers: servers}); err != nil {
		t.Fatalf("MarshalBackup() error: %v", err)
	}
	if servers[0].Port != 0 || servers[0].User != "" {
		t.Errorf("MarshalBackup() rewrote the caller's server: %+v", servers[0])
	}
}

func TestMarshalBackupRefusesUnreadableBackup(t *testing.T) {
	_, err := MarshalBackup(BackupFile{Servers: []Server{
		srv("dup", "10.0.0.1", ""),
		srv("dup", "10.0.0.2", ""),
	}})
	if err == nil {
		t.Fatal("MarshalBackup() accepted a backup with a duplicate server name")
	}
	if !strings.Contains(err.Error(), "duplicate server name") {
		t.Errorf("error does not name the problem: %v", err)
	}
}

func TestParseBackupRejects(t *testing.T) {
	valid := `version: 1
machine: box
servers:
  - name: web
    host: 10.0.0.1
    port: 22
    user: root
`

	tests := []struct {
		name    string
		data    string
		wantErr string
	}{
		{
			name:    "not yaml",
			data:    "\tthis: is not: yaml",
			wantErr: "failed to parse the backup",
		},
		{
			name:    "several documents",
			data:    valid + "---\nversion: 1\n",
			wantErr: "multiple documents",
		},
		{
			name:    "no version at all",
			data:    "servers:\n  - name: web\n    host: 10.0.0.1\n    port: 22\n    user: root\n",
			wantErr: "version 0",
		},
		{
			name:    "a version from the future",
			data:    strings.Replace(valid, "version: 1", "version: 99", 1),
			wantErr: "version 99",
		},
		{
			name:    "an unknown field",
			data:    valid + "surprise: true\n",
			wantErr: "failed to parse the backup",
		},
		{
			name:    "a server with no host",
			data:    "version: 1\nservers:\n  - name: web\n    host: \"\"\n    port: 22\n    user: root\n",
			wantErr: "invalid server list",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseBackup([]byte(tt.data))
			if err == nil {
				t.Fatal("ParseBackup() accepted a document it should have refused")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

// A version this build understands must keep parsing, so that the check above
// cannot pass by refusing everything.
func TestParseBackupAcceptsCurrentVersion(t *testing.T) {
	data, err := MarshalBackup(BackupFile{Servers: []Server{srv("web", "10.0.0.1", "")}})
	if err != nil {
		t.Fatalf("MarshalBackup() error: %v", err)
	}
	if _, err := ParseBackup(data); err != nil {
		t.Fatalf("ParseBackup() rejected a backup MarshalBackup produced: %v", err)
	}
}
