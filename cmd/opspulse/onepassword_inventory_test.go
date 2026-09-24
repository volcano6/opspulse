package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

func TestCountLocalOnly(t *testing.T) {
	names := func(list ...string) map[string]struct{} {
		set := make(map[string]struct{}, len(list))
		for _, name := range list {
			set[name] = struct{}{}
		}
		return set
	}

	tests := []struct {
		name     string
		local    []server.Server
		incoming map[string]struct{}
		want     int
	}{
		{
			// The bug this helper exists for: on a fresh machine nothing is
			// local, so the count must be zero however large the backup is.
			name:     "a fresh machine keeps nothing",
			local:    nil,
			incoming: names("a", "b", "c"),
			want:     0,
		},
		{
			name:     "a local-only server is counted",
			local:    []server.Server{{Name: "a"}, {Name: "mine"}},
			incoming: names("a", "b"),
			want:     1,
		},
		{
			name:     "a shared server is not local-only even when the merge updated it",
			local:    []server.Server{{Name: "a"}, {Name: "b"}},
			incoming: names("a", "b", "c"),
			want:     0,
		},
		{
			name:     "an empty backup leaves every local server local-only",
			local:    []server.Server{{Name: "a"}, {Name: "b"}},
			incoming: names(),
			want:     2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := countLocalOnly(tt.local, tt.incoming); got != tt.want {
				t.Errorf("countLocalOnly = %d, want %d", got, tt.want)
			}
		})
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

// TestCountPlaintextPasswordsToWrite pins the count the confirmation gate is
// taken on. The bug it guards: counting the credential plan instead, which is
// built after the merge has already written the passwords to disk, reports zero
// and skips the question entirely.
func TestCountPlaintextPasswordsToWrite(t *testing.T) {
	ref := "op://Personal/opspulse_web_password/password"

	tests := []struct {
		name   string
		local  []server.Server
		merged []server.Server
		want   int
	}{
		{
			name:   "a password on a server this machine does not have counts",
			merged: []server.Server{{Name: "web", Password: "pw"}},
			want:   1,
		},
		{
			name:   "a password filling a local empty field counts",
			local:  []server.Server{{Name: "web"}},
			merged: []server.Server{{Name: "web", Password: "pw"}},
			want:   1,
		},
		{
			name:   "a plaintext password already on disk does not count",
			local:  []server.Server{{Name: "web", Password: "pw"}},
			merged: []server.Server{{Name: "web", Password: "pw"}},
			want:   0,
		},
		{
			name:   "a changed plaintext password counts",
			local:  []server.Server{{Name: "web", Password: "old"}},
			merged: []server.Server{{Name: "web", Password: "new"}},
			want:   1,
		},
		{
			name:   "an op:// reference is not a plaintext password",
			local:  nil,
			merged: []server.Server{{Name: "web", Password: ref}},
			want:   0,
		},
		{
			name:   "no password at all counts nothing",
			local:  nil,
			merged: []server.Server{{Name: "web"}, {Name: "db"}},
			want:   0,
		},
		{
			name:   "a legacy reference replaced by plaintext counts",
			local:  []server.Server{{Name: "web", Password: ref}},
			merged: []server.Server{{Name: "web", Password: "pw"}},
			want:   1,
		},
		{
			name:   "only the servers whose password would be new are counted",
			local:  []server.Server{{Name: "keep", Password: "pw"}, {Name: "legacy", Password: ref}},
			merged: []server.Server{{Name: "keep", Password: "pw"}, {Name: "legacy", Password: "pw"}, {Name: "fresh", Password: "pw"}, {Name: "nopass"}},
			want:   2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := countPlaintextPasswordsToWrite(tt.local, tt.merged); got != tt.want {
				t.Errorf("countPlaintextPasswordsToWrite = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestInheritMissingKeys pins the rule that keeps a local read failure from
// deleting the off-machine copy of a key: the previous document is the one thing
// that still has the key, and the write about to happen replaces it.
func TestInheritMissingKeys(t *testing.T) {
	tests := []struct {
		name        string
		payload     server.BackupFile
		previous    *server.BackupFile
		unreadable  []string
		wantKeys    map[string]string
		wantInherit []string
		wantDropped []string
	}{
		{
			name:       "a first backup has nothing to inherit from",
			payload:    server.BackupFile{Servers: []server.Server{{Name: "web"}}},
			unreadable: []string{"web"},
		},
		{
			name:        "an unreadable key is carried over from the previous document",
			payload:     server.BackupFile{Servers: []server.Server{{Name: "web"}}},
			previous:    &server.BackupFile{Keys: map[string]string{"web": "PEM"}},
			unreadable:  []string{"web"},
			wantKeys:    map[string]string{"web": "PEM"},
			wantInherit: []string{"web"},
		},
		{
			name:       "a previous document with no keys has nothing to give",
			payload:    server.BackupFile{Servers: []server.Server{{Name: "web"}, {Name: "db"}}},
			previous:   &server.BackupFile{},
			unreadable: []string{"web"},
		},
		{
			name:       "a key that was read off disk wins over the stored copy",
			payload:    server.BackupFile{Servers: []server.Server{{Name: "web"}}, Keys: map[string]string{"web": "FRESH"}},
			previous:   &server.BackupFile{Keys: map[string]string{"web": "STALE"}},
			unreadable: []string{"web"},
			wantKeys:   map[string]string{"web": "FRESH"},
		},
		{
			name:        "a server removed from servers.yaml is reported as dropped",
			payload:     server.BackupFile{Servers: []server.Server{{Name: "web"}}, Keys: map[string]string{"web": "PEM"}},
			previous:    &server.BackupFile{Keys: map[string]string{"web": "PEM", "gone": "OLD", "also": "OLD"}},
			wantKeys:    map[string]string{"web": "PEM"},
			wantDropped: []string{"also", "gone"},
		},
		{
			name:        "a key reported unreadable and dropped is never both",
			payload:     server.BackupFile{Servers: []server.Server{{Name: "web"}, {Name: "db"}}},
			previous:    &server.BackupFile{Keys: map[string]string{"db": "PEM", "gone": "OLD"}},
			unreadable:  []string{"db"},
			wantKeys:    map[string]string{"db": "PEM"},
			wantInherit: []string{"db"},
			wantDropped: []string{"gone"},
		},
		{
			name:       "servers without keys are never reported as dropped",
			payload:    server.BackupFile{Servers: []server.Server{{Name: "bare"}}},
			previous:   &server.BackupFile{Keys: nil},
			unreadable: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, inherited, dropped := inheritMissingKeys(tt.payload, tt.previous, tt.unreadable)
			if !reflect.DeepEqual(got.Keys, tt.wantKeys) {
				t.Errorf("keys = %v, want %v", got.Keys, tt.wantKeys)
			}
			if !reflect.DeepEqual(inherited, tt.wantInherit) {
				t.Errorf("inherited = %v, want %v", inherited, tt.wantInherit)
			}
			if !reflect.DeepEqual(dropped, tt.wantDropped) {
				t.Errorf("dropped = %v, want %v", dropped, tt.wantDropped)
			}
		})
	}
}

// TestReadLocalKeysNamesUnreadableFiles pins what the inheritance depends on:
// a key that could not be read has to come back named, not just printed. The
// warning alone is what let the backup drop the key and overwrite the only copy.
func TestReadLocalKeysNamesUnreadableFiles(t *testing.T) {
	dir := t.TempDir()
	readable := filepath.Join(dir, "id_here")
	if err := os.WriteFile(readable, []byte("PEM"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	keys, unreadable := readLocalKeys([]server.Server{
		{Name: "here", KeyPath: readable},
		{Name: "gone", KeyPath: filepath.Join(dir, "id_gone")},
		{Name: "password-only"},
		{Name: "managed", KeyPath: "op://Personal/opspulse_managed_key/opspulse_private_key"},
	}, &out)

	if !reflect.DeepEqual(keys, map[string]string{"here": "PEM"}) {
		t.Errorf("keys = %v, want only the readable one", keys)
	}
	if !reflect.DeepEqual(unreadable, []string{"gone"}) {
		t.Errorf("unreadable = %v, want [gone]", unreadable)
	}
	if !strings.Contains(out.String(), "gone") {
		t.Errorf("the unreadable key should be reported to the user:\n%s", out.String())
	}
}

// TestBackupItemAbsent pins the fail-closed reading of a failed `op read`: only
// the CLI's "the item is not there" wording counts as a first backup. Anything
// else must stop the write, because the alternative is replacing a document that
// was never read.
func TestBackupItemAbsent(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "the edit wording is accepted", err: errors.New(`unable to process line 1: could not find item to edit`), want: true},
		{name: "the read wording is accepted", err: errors.New(`[ERROR] could not find item "opspulse_inventory_host"`), want: true},
		{name: "the alternate wording is accepted", err: errors.New("item not found"), want: true},
		{name: "a locked vault is not a first backup", err: errors.New("[ERROR] error initializing client: vault is locked"), want: false},
		{name: "a missing vault is not a first backup", err: errors.New(`vault "Personal" was not found`), want: false},
		{name: "a stalled call is not a first backup", err: errors.New("context deadline exceeded"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := backupItemAbsent(tt.err); got != tt.want {
				t.Errorf("backupItemAbsent(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
