package server

import (
	"strings"
	"testing"
)

func srv(name, host, keyPath string) Server {
	return Server{Name: name, Host: host, Port: 22, User: "root", KeyPath: keyPath}
}

func names(servers []Server) []string {
	out := make([]string, 0, len(servers))
	for _, s := range servers {
		out = append(out, s.Name)
	}
	return out
}

func findServer(t *testing.T, servers []Server, name string) Server {
	t.Helper()
	for _, s := range servers {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("server %q not found in %v", name, names(servers))
	return Server{}
}

func TestMergeInventoriesUnions(t *testing.T) {
	tests := []struct {
		name          string
		local         []Server
		remote        []Server
		wantNames     []string
		wantAdded     int
		wantUpdated   int
		wantConflicts int
		wantKey       map[string]string
	}{
		{
			name:      "remote-only server is appended",
			local:     []Server{srv("keep", "10.0.0.1", "~/.ssh/keep.key")},
			remote:    []Server{srv("fresh", "10.0.0.2", "~/.ssh/fresh.key")},
			wantNames: []string{"keep", "fresh"},
			wantAdded: 1,
		},
		{
			name:      "local-only server is never deleted",
			local:     []Server{srv("only-local", "10.0.0.1", "~/.ssh/a.key")},
			remote:    nil,
			wantNames: []string{"only-local"},
		},
		{
			name:      "identical server is left alone",
			local:     []Server{srv("same", "10.0.0.1", "op://Personal/opspulse_same_key/password")},
			remote:    []Server{srv("same", "10.0.0.1", "op://Personal/opspulse_same_key/password")},
			wantNames: []string{"same"},
		},
		{
			name:          "host difference is a conflict and keep-local wins",
			local:         []Server{srv("box", "10.0.0.1", "~/.ssh/a.key")},
			remote:        []Server{srv("box", "10.0.0.9", "~/.ssh/a.key")},
			wantNames:     []string{"box"},
			wantConflicts: 1,
		},
		{
			name:        "op reference beats local path without a conflict",
			local:       []Server{srv("box", "10.0.0.1", "~/.ssh/box.key")},
			remote:      []Server{srv("box", "10.0.0.1", "op://Personal/opspulse_box_key/password")},
			wantNames:   []string{"box"},
			wantUpdated: 1,
			wantKey:     map[string]string{"box": "op://Personal/opspulse_box_key/password"},
		},
		{
			name:      "two local paths are not churned",
			local:     []Server{srv("box", "10.0.0.1", "~/.ssh/box.key")},
			remote:    []Server{srv("box", "10.0.0.1", "~/.ssh/other.key")},
			wantNames: []string{"box"},
			wantKey:   map[string]string{"box": "~/.ssh/box.key"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			merged, added, updated, conflicts, err := MergeInventories(tt.local, tt.remote, func(MergeConflict) MergeDecision {
				return KeepLocal
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := names(merged); strings.Join(got, ",") != strings.Join(tt.wantNames, ",") {
				t.Errorf("names = %v, want %v", got, tt.wantNames)
			}
			if added != tt.wantAdded {
				t.Errorf("added = %d, want %d", added, tt.wantAdded)
			}
			if updated != tt.wantUpdated {
				t.Errorf("updated = %d, want %d", updated, tt.wantUpdated)
			}
			if len(conflicts) != tt.wantConflicts {
				t.Errorf("conflicts = %v, want %d", conflicts, tt.wantConflicts)
			}
			for name, wantKey := range tt.wantKey {
				if got := findServer(t, merged, name).KeyPath; got != wantKey {
					t.Errorf("%s key_path = %q, want %q", name, got, wantKey)
				}
			}
		})
	}
}

func TestMergeInventoriesCredentialOnlyDifferenceIsNotAConflict(t *testing.T) {
	local := []Server{srv("box", "10.0.0.1", "~/.ssh/box.key")}
	remote := []Server{srv("box", "10.0.0.1", "op://Personal/opspulse_box_key/password")}

	merged, added, updated, conflicts, err := MergeInventories(local, remote, nil)
	if err != nil {
		t.Fatalf("expected no error when only credentials differ, got: %v", err)
	}
	if added != 0 {
		t.Errorf("added = %d, want 0", added)
	}
	if updated != 1 {
		t.Errorf("updated = %d, want 1", updated)
	}
	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %v, want none", conflicts)
	}
	if got := merged[0].KeyPath; got != "op://Personal/opspulse_box_key/password" {
		t.Errorf("key_path = %q, want the op reference", got)
	}
}

func TestMergeInventoriesReportsConflictsWithoutDecider(t *testing.T) {
	local := []Server{srv("box", "10.0.0.1", "~/.ssh/a.key")}
	remote := []Server{srv("box", "10.0.0.9", "~/.ssh/a.key")}

	merged, _, _, conflicts, err := MergeInventories(local, remote, nil)
	if err == nil {
		t.Fatal("expected an error when a conflict cannot be decided")
	}
	if merged != nil {
		t.Errorf("merged = %v, want nil on undecided conflict", merged)
	}
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %v, want exactly one", conflicts)
	}
	if conflicts[0].Name != "box" {
		t.Errorf("conflict name = %q, want box", conflicts[0].Name)
	}
	if !strings.Contains(err.Error(), "box") || !strings.Contains(err.Error(), "host") {
		t.Errorf("error %q should name the server and the differing field", err)
	}
}

func TestMergeInventoriesTakeRemoteReplacesInventoryFields(t *testing.T) {
	local := []Server{srv("box", "10.0.0.1", "~/.ssh/a.key")}
	remote := []Server{srv("box", "10.0.0.9", "~/.ssh/a.key")}

	merged, _, updated, _, err := MergeInventories(local, remote, func(MergeConflict) MergeDecision {
		return TakeRemote
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated != 1 {
		t.Errorf("updated = %d, want 1", updated)
	}
	if got := merged[0].Host; got != "10.0.0.9" {
		t.Errorf("host = %q, want the remote host", got)
	}
}

func TestMergeInventoriesKeepsLocalCredentialWhenBothAreReferences(t *testing.T) {
	local := []Server{srv("box", "10.0.0.1", "op://Personal/opspulse_box_key/password")}
	remote := []Server{srv("box", "10.0.0.1", "op://Work/opspulse_box_key/password")}

	merged, _, _, conflicts, err := MergeInventories(local, remote, func(MergeConflict) MergeDecision {
		return KeepLocal
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %v, want one (competing references)", conflicts)
	}
	if got := merged[0].KeyPath; got != "op://Personal/opspulse_box_key/password" {
		t.Errorf("key_path = %q, want the local reference", got)
	}
}

func TestMergeInventoriesTakesRemoteCredentialWhenBothAreReferences(t *testing.T) {
	local := []Server{srv("box", "10.0.0.1", "op://Personal/opspulse_box_key/password")}
	remote := []Server{srv("box", "10.0.0.1", "op://Work/opspulse_box_key/password")}

	merged, _, _, _, err := MergeInventories(local, remote, func(MergeConflict) MergeDecision {
		return TakeRemote
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := merged[0].KeyPath; got != "op://Work/opspulse_box_key/password" {
		t.Errorf("key_path = %q, want the remote reference", got)
	}
}

func TestMergeInventoriesDoesNotChurnReorderedMetadata(t *testing.T) {
	local := []Server{{
		Name: "box", Host: "10.0.0.1", Port: 22, User: "root",
		Tags:   []string{"prod", "web"},
		Labels: map[string]string{"env": "prod", "team": "infra"},
	}}
	remote := []Server{{
		Name: "box", Host: "10.0.0.1", Port: 22, User: "root",
		Tags:   []string{"web", "prod"},
		Labels: map[string]string{"team": "infra", "env": "prod"},
	}}

	_, _, updated, conflicts, err := MergeInventories(local, remote, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated != 0 {
		t.Errorf("updated = %d, want 0 for reordered metadata", updated)
	}
	if len(conflicts) != 0 {
		t.Errorf("conflicts = %v, want none", conflicts)
	}
}

func TestMergeInventoriesNormalisesDefaults(t *testing.T) {
	local := []Server{{Name: "box", Host: "10.0.0.1"}}
	remote := []Server{{Name: "box", Host: "10.0.0.1", Port: 22, User: "root"}}

	_, _, updated, conflicts, err := MergeInventories(local, remote, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated != 0 {
		t.Errorf("updated = %d, want 0 when implicit defaults match explicit ones", updated)
	}
	if len(conflicts) != 0 {
		t.Errorf("conflicts = %v, want none", conflicts)
	}
}

// TestMergeInventoriesLocalOnlyServerIsNotCounted pins the trap that let a push
// skip a backup which was missing a server.
//
// A server only the local file knows about changes the union without moving
// either counter: it was never "added" from the backup's point of view, and
// nothing about it was "updated". A caller that decides whether to write from
// the counters alone therefore concludes there is nothing to do, and the backup
// stays behind forever. The result has to be compared against the side being
// written instead.
func TestMergeInventoriesLocalOnlyServerIsNotCounted(t *testing.T) {
	local := []Server{srv("a", "10.0.0.1", ""), srv("b", "10.0.0.2", "")}
	remote := []Server{srv("a", "10.0.0.1", "")}

	merged, added, updated, _, err := MergeInventories(local, remote, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if added != 0 || updated != 0 {
		t.Fatalf("added = %d, updated = %d, want 0 and 0", added, updated)
	}
	if SameInventory(merged, remote) {
		t.Error("the union must not compare equal to a backup that lacks a local server")
	}
	if !SameInventory(merged, local) {
		t.Error("the union of a superset with its own subset is the superset itself")
	}
}

func TestSameInventory(t *testing.T) {
	tagged := func(tags ...string) []Server {
		return []Server{{Name: "a", Host: "10.0.0.1", Port: 22, User: "root", Tags: tags}}
	}

	tests := []struct {
		name string
		a, b []Server
		want bool
	}{
		{name: "both empty", a: nil, b: []Server{}, want: true},
		{name: "identical", a: []Server{srv("a", "10.0.0.1", "")}, b: []Server{srv("a", "10.0.0.1", "")}, want: true},
		{
			name: "order does not matter",
			a:    []Server{srv("a", "10.0.0.1", ""), srv("b", "10.0.0.2", "")},
			b:    []Server{srv("b", "10.0.0.2", ""), srv("a", "10.0.0.1", "")},
			want: true,
		},
		{
			name: "an extra server is a difference",
			a:    []Server{srv("a", "10.0.0.1", ""), srv("b", "10.0.0.2", "")},
			b:    []Server{srv("a", "10.0.0.1", "")},
			want: false,
		},
		{name: "a different host is a difference", a: []Server{srv("a", "10.0.0.1", "")}, b: []Server{srv("a", "10.0.0.9", "")}, want: false},
		{
			name: "a different credential is a difference",
			a:    []Server{srv("a", "10.0.0.1", "op://Personal/one/password")},
			b:    []Server{srv("a", "10.0.0.1", "op://Personal/two/password")},
			want: false,
		},
		{name: "reordered tags are equal", a: tagged("prod", "web"), b: tagged("web", "prod"), want: true},
		{name: "different tags are not", a: tagged("prod"), b: tagged("dev"), want: false},
		{
			name: "implicit defaults equal explicit ones",
			a:    []Server{{Name: "a", Host: "10.0.0.1"}},
			b:    []Server{{Name: "a", Host: "10.0.0.1", Port: 22, User: "root"}},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SameInventory(tt.a, tt.b); got != tt.want {
				t.Errorf("SameInventory() = %v, want %v", got, tt.want)
			}
			if got := SameInventory(tt.b, tt.a); got != tt.want {
				t.Errorf("SameInventory() reversed = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMergeConflictSummaryListsEveryField(t *testing.T) {
	c := MergeConflict{
		Name: "box",
		Diffs: []FieldDiff{
			{Field: "host", Local: "10.0.0.1", Remote: "10.0.0.9"},
			{Field: "user", Local: "root", Remote: "deploy"},
		},
	}
	got := c.Summary()
	for _, want := range []string{"box", "host", "10.0.0.1", "10.0.0.9", "user", "deploy"} {
		if !strings.Contains(got, want) {
			t.Errorf("Summary() = %q, missing %q", got, want)
		}
	}
}

func TestPickCredential(t *testing.T) {
	const (
		refA  = "op://Personal/opspulse_box_key/password"
		refB  = "op://Work/opspulse_box_key/password"
		pathA = "~/.ssh/box.key"
		pathB = "~/.ssh/other.key"
	)
	tests := []struct {
		name       string
		local      string
		remote     string
		takeRemote bool
		want       string
	}{
		{name: "equal values stay put", local: refA, remote: refA, want: refA},
		{name: "local ref beats remote path", local: refA, remote: pathA, want: refA},
		{name: "remote ref beats local path", local: pathA, remote: refA, want: refA},
		{name: "two local paths keep local", local: pathA, remote: pathB, want: pathA},
		{name: "two refs keep local by default", local: refA, remote: refB, want: refA},
		{name: "two refs honour takeRemote", local: refA, remote: refB, takeRemote: true, want: refB},
		{name: "remote wins when local is empty", local: "", remote: pathA, want: pathA},
		{name: "local wins when remote is empty", local: pathA, remote: "", want: pathA},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pickCredential(tt.local, tt.remote, tt.takeRemote); got != tt.want {
				t.Errorf("pickCredential(%q, %q, %v) = %q, want %q", tt.local, tt.remote, tt.takeRemote, got, tt.want)
			}
		})
	}
}
