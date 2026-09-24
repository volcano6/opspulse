package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/volcano6/opspulse/internal/secret"
	"github.com/volcano6/opspulse/internal/server"
)

// TestRenderDoctorSteps_Verdict pins the rule the exit status depends on: a
// single failed step fails the command, warnings alone do not, and a hint is
// printed one line per line so a multi-line hint (install instructions, WSL
// notes) cannot swallow its own second line.
func TestRenderDoctorSteps_Verdict(t *testing.T) {
	tests := []struct {
		name       string
		steps      []doctorStep
		wantFailed bool
		wantOut    []string
	}{
		{
			name: "all ok",
			steps: []doctorStep{
				{status: doctorOK, label: "op executable", detail: "/usr/bin/op (Linux/Unix build)"},
			},
			wantFailed: false,
			wantOut:    []string{"✅ op executable: /usr/bin/op (Linux/Unix build)", "healthy"},
		},
		{
			name: "a warning is not a failure",
			steps: []doctorStep{
				{status: doctorOK, label: "account", detail: "1 account(s)"},
				{status: doctorWarn, label: "backup item", detail: "\"opspulse_inventory_host\" does not exist yet", hint: "run 'ops 1p backup'"},
			},
			wantFailed: false,
			wantOut:    []string{"⚠️  backup item:", "💡 run 'ops 1p backup'", "caveats"},
		},
		{
			name: "a failure fails the command",
			steps: []doctorStep{
				{status: doctorFail, label: "vault", detail: "authorisation timeout", hint: "line one\nline two"},
			},
			wantFailed: true,
			wantOut:    []string{"❌ vault: authorisation timeout", "💡 line one", "💡 line two", "Some checks failed"},
		},
		{
			name:       "skipped steps still render",
			steps:      []doctorStep{{status: doctorSkip, label: "1Password round trips", detail: "skipped by --offline"}},
			wantFailed: false,
			wantOut:    []string{"⏭️  1Password round trips: skipped by --offline", "healthy"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if got := renderDoctorSteps(&buf, tc.steps); got != tc.wantFailed {
				t.Errorf("renderDoctorSteps() = %v, want %v", got, tc.wantFailed)
			}
			out := buf.String()
			for _, want := range tc.wantOut {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q:\n%s", want, out)
				}
			}
		})
	}
}

// TestDescribeBackupPayload_CountsWithoutEchoingSecrets guards the promise the
// doctor makes: it describes the backup document, it does not leak it.
func TestDescribeBackupPayload_CountsWithoutEchoingSecrets(t *testing.T) {
	payload, err := server.MarshalBackup(server.BackupFile{
		Machine: "test-host",
		Servers: []server.Server{
			{Name: "plain", Host: "10.0.0.1", User: "root", Password: "hunter2-plaintext"},
			{Name: "referenced", Host: "10.0.0.2", User: "root", Password: "op://Personal/item/password", KeyPath: "/home/u/.ssh/opspulse_referenced"},
			{Name: "keyless", Host: "10.0.0.3", User: "root"},
		},
		Keys: map[string]string{"referenced": "-----BEGIN OPENSSH PRIVATE KEY-----\nsecret\n-----END OPENSSH PRIVATE KEY-----"},
	})
	if err != nil {
		t.Fatalf("MarshalBackup() error: %v", err)
	}

	detail, err := describeBackupPayload(payload)
	if err != nil {
		t.Fatalf("describeBackupPayload() error: %v", err)
	}

	for _, want := range []string{
		"3 server(s)",
		"1 private key(s)",
		"1 plaintext password(s)",
		"1 server(s) naming a key",
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("description %q missing %q", detail, want)
		}
	}
	for _, leak := range []string{"hunter2-plaintext", "-----BEGIN", "op://Personal"} {
		if strings.Contains(detail, leak) {
			t.Errorf("description leaked %q: %q", leak, detail)
		}
	}

	if _, err := describeBackupPayload([]byte("not a backup document")); err == nil {
		t.Error("describeBackupPayload() accepted a document it cannot parse")
	}
}

// TestLocalCredentialGaps covers the two states a fresh machine lands in: a key
// path that no longer exists locally, and an unresolved op:// reference.
func TestLocalCredentialGaps(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "opspulse_present")
	if err := os.WriteFile(present, []byte("key"), 0o600); err != nil {
		t.Fatalf("preparing fixture: %v", err)
	}

	servers := []server.Server{
		{Name: "fine", Host: "10.0.0.1", KeyPath: present},
		{Name: "gone", Host: "10.0.0.2", KeyPath: filepath.Join(dir, "opspulse_gone")},
		{Name: "ref-key", Host: "10.0.0.3", KeyPath: "op://Personal/item/private_key"},
		{Name: "ref-pw", Host: "10.0.0.4", Password: "op://Personal/item/password"},
	}

	refs, missingKeys := localCredentialGaps(servers)

	if got, want := strings.Join(refs, ","), "ref-key,ref-pw"; got != want {
		t.Errorf("refs = %q, want %q", got, want)
	}
	if got, want := strings.Join(missingKeys, ","), "gone"; got != want {
		t.Errorf("missingKeys = %q, want %q", got, want)
	}
}

// TestDescribeVaultSource answers "which vault did that come from", so each of
// the three sources has to be distinguishable in the output.
func TestDescribeVaultSource(t *testing.T) {
	tests := []struct {
		name     string
		explicit string
		settings secret.Settings
		want     string
	}{
		{name: "explicit flag wins", explicit: "Named", settings: secret.Settings{Vault: "Remembered"}, want: "--vault"},
		{name: "remembered setting", settings: secret.Settings{Vault: "Remembered"}, want: "remembered setting"},
		{name: "nothing named", want: "inferred"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := describeVaultSource(tc.explicit, tc.settings); !strings.Contains(got, tc.want) {
				t.Errorf("describeVaultSource() = %q, want it to mention %q", got, tc.want)
			}
		})
	}
}
