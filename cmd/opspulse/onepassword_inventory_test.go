package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/volcano6/opspulse/internal/server"
)

// TestValidatePreferFlags pins the one combination that means nothing: asking
// for both sides to win. backup and restore both carry the inventory, so there
// is no longer an "--inventory" prerequisite to check for.
func TestValidatePreferFlags(t *testing.T) {
	tests := []struct {
		name         string
		preferLocal  bool
		preferRemote bool
		wantErr      string
	}{
		{name: "no preference is fine"},
		{name: "prefer-local is fine", preferLocal: true},
		{name: "prefer-remote is fine", preferRemote: true},
		{name: "contradictory preferences are rejected", preferLocal: true, preferRemote: true, wantErr: "contradict"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePreferFlags(tt.preferLocal, tt.preferRemote)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q should contain %q", err, tt.wantErr)
			}
		})
	}
}

// TestInventoryConflictPromptNonInteractive pins the rule that makes the
// command safe to run from a script: without --prefer-* there is no decider at
// all, so MergeInventories reports the conflicts instead of a default silently
// choosing a host definition.
func TestInventoryConflictPromptNonInteractive(t *testing.T) {
	p := newInventoryConflictPrompt(strings.NewReader(""), &bytes.Buffer{}, false, false, false)
	if p.decider() != nil {
		t.Error("a non-interactive shell without --prefer-* must not get a decider")
	}
}

func TestInventoryConflictPromptPreferFlags(t *testing.T) {
	conflict := server.MergeConflict{Name: "box"}

	local := newInventoryConflictPrompt(strings.NewReader(""), &bytes.Buffer{}, true, true, false)
	if got := local.decide(conflict); got != server.KeepLocal {
		t.Errorf("--prefer-local chose %v, want KeepLocal", got)
	}

	remote := newInventoryConflictPrompt(strings.NewReader(""), &bytes.Buffer{}, false, false, true)
	if got := remote.decide(conflict); got != server.TakeRemote {
		t.Errorf("--prefer-remote chose %v, want TakeRemote", got)
	}
}

func TestInventoryConflictPromptInteractive(t *testing.T) {
	conflict := server.MergeConflict{
		Name:  "box",
		Diffs: []server.FieldDiff{{Field: "host", Local: "10.0.0.1", Remote: "10.0.0.9"}},
	}

	tests := []struct {
		name    string
		input   string
		want    []server.MergeDecision
		aborted bool
	}{
		{name: "l keeps local", input: "l\n", want: []server.MergeDecision{server.KeepLocal}},
		{name: "blank line defaults to local", input: "\n", want: []server.MergeDecision{server.KeepLocal}},
		{name: "r takes remote", input: "r\n", want: []server.MergeDecision{server.TakeRemote}},
		{name: "a is sticky remote", input: "a\n", want: []server.MergeDecision{server.TakeRemote, server.TakeRemote}},
		{name: "A is sticky local", input: "A\n", want: []server.MergeDecision{server.KeepLocal, server.KeepLocal}},
		{name: "garbage is re-asked", input: "maybe\nr\n", want: []server.MergeDecision{server.TakeRemote}},
		{name: "q aborts", input: "q\n", want: []server.MergeDecision{server.KeepLocal}, aborted: true},
		{name: "EOF aborts", input: "", want: []server.MergeDecision{server.KeepLocal}, aborted: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			p := newInventoryConflictPrompt(strings.NewReader(tt.input), &out, true, false, false)
			for i, want := range tt.want {
				if got := p.decide(conflict); got != want {
					t.Fatalf("decision %d = %v, want %v", i, got, want)
				}
			}
			if p.aborted != tt.aborted {
				t.Errorf("aborted = %v, want %v", p.aborted, tt.aborted)
			}
			if !strings.Contains(out.String(), "10.0.0.1 -> 10.0.0.9") {
				t.Errorf("the prompt should show the field-level diff, got:\n%s", out.String())
			}
		})
	}
}

// TestInventoryConflictPromptEOFDoesNotConsumeLaterAnswers guards the bug a
// per-prompt bufio.Scanner would introduce: over-reading would swallow the
// answer meant for the next conflict.
func TestInventoryConflictPromptReadsEachAnswerOnce(t *testing.T) {
	conflict := server.MergeConflict{Name: "box"}
	var out bytes.Buffer
	p := newInventoryConflictPrompt(strings.NewReader("r\nl\n"), &out, true, false, false)

	if got := p.decide(conflict); got != server.TakeRemote {
		t.Fatalf("first decision = %v, want TakeRemote", got)
	}
	if got := p.decide(conflict); got != server.KeepLocal {
		t.Fatalf("second decision = %v, want KeepLocal", got)
	}
}

func TestInventoryConflictErrorNamesTheWayOut(t *testing.T) {
	cause := errors.New("the backup and this machine disagree about 1 server(s): box (host: 10.0.0.1 -> 10.0.0.9)")
	err := inventoryConflictError(cause)
	for _, want := range []string{"--prefer-local", "--prefer-remote", "opspulse_inventory", cause.Error()} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}

func TestMarshalInventoryRoundTripsThroughParseConfig(t *testing.T) {
	servers := []server.Server{
		{Name: "web", Host: "10.0.0.1", Port: 2222, User: "deploy", KeyPath: "op://Personal/opspulse_web_key/opspulse_private_key", Tags: []string{"prod"}, Labels: map[string]string{"env": "prod"}},
		{Name: "db", Host: "10.0.0.2", Port: 22, User: "root", Password: "op://Personal/opspulse_db_password/password", JumpHost: "web", SkipBatch: true},
	}

	payload, err := marshalInventory(servers)
	if err != nil {
		t.Fatalf("marshalInventory: %v", err)
	}
	parsed, err := server.ParseConfig(payload)
	if err != nil {
		t.Fatalf("the marshalled inventory must be a valid servers.yaml: %v", err)
	}
	if len(parsed) != len(servers) {
		t.Fatalf("round trip produced %d servers, want %d", len(parsed), len(servers))
	}
	for i, want := range servers {
		got := parsed[i]
		if got.Name != want.Name || got.Host != want.Host || got.Port != want.Port ||
			got.User != want.User || got.KeyPath != want.KeyPath || got.Password != want.Password ||
			got.JumpHost != want.JumpHost || got.SkipBatch != want.SkipBatch {
			t.Errorf("server %d round-tripped as %+v, want %+v", i, got, want)
		}
	}
}

func TestCollectBackupCredentials(t *testing.T) {
	blobs := []backupBlob{
		{
			title: "opspulse_inventory_laptop",
			file: server.BackupFile{
				Servers: []server.Server{
					{Name: "web", Host: "10.0.0.1", Password: "web-pass"},
					{Name: "shared", Host: "10.0.0.2"},
				},
				Keys: map[string]string{"web": "laptop-key", "shared": "laptop-shared-key"},
			},
		},
		{
			title: "opspulse_inventory_desktop",
			file: server.BackupFile{
				Servers: []server.Server{
					{Name: "shared", Host: "10.0.0.2"},
					{Name: "db", Host: "10.0.0.3"},
				},
				Keys: map[string]string{"shared": "desktop-shared-key", "db": "desktop-db-key"},
			},
		},
	}

	creds := collectBackupCredentials(blobs)

	if got := string(creds["web"].key); got != "laptop-key" {
		t.Errorf("web key = %q, want the laptop's", got)
	}
	if got := creds["web"].password; got != "web-pass" {
		t.Errorf("web password = %q, want the laptop's", got)
	}
	// The first blob in sorted order wins, so which machine's key a restore
	// uses does not depend on map iteration order.
	if got := string(creds["shared"].key); got != "laptop-shared-key" {
		t.Errorf("shared key = %q, want the first blob's", got)
	}
	if got := string(creds["db"].key); got != "desktop-db-key" {
		t.Errorf("db key = %q, want the desktop's", got)
	}
	// A server nobody backed up a password for simply has none.
	if got := creds["db"].password; got != "" {
		t.Errorf("db password = %q, want none", got)
	}
	if got := collectBackupCredentials(nil); len(got) != 0 {
		t.Errorf("collectBackupCredentials(nil) = %v, want empty", got)
	}
}

func TestWarnMissingLocalKeyFiles(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "present.key")
	if err := os.WriteFile(present, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	absent := filepath.Join(dir, "absent.key")

	var out bytes.Buffer
	warnMissingLocalKeyFiles(&out, []server.Server{
		{Name: "managed", KeyPath: "op://Personal/opspulse_managed_key/opspulse_private_key"},
		{Name: "password-only"},
		{Name: "here", KeyPath: present},
		{Name: "gone", KeyPath: absent},
	})
	got := out.String()
	if !strings.Contains(got, "gone") {
		t.Errorf("a server with a missing key file should be reported:\n%s", got)
	}
	for _, unexpected := range []string{"managed", "password-only", "here"} {
		if strings.Contains(got, unexpected) {
			t.Errorf("server %q should not be reported as missing a key:\n%s", unexpected, got)
		}
	}

	out.Reset()
	warnMissingLocalKeyFiles(&out, []server.Server{{Name: "managed", KeyPath: "op://Personal/x/y"}})
	if out.Len() != 0 {
		t.Errorf("no warning should be printed when nothing is missing, got %q", out.String())
	}
}
