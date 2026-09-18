package server

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/volcano6/opspulse/internal/secret"
)

// MergeDecision says which side of a conflict wins.
type MergeDecision int

const (
	// KeepLocal keeps the definition already in the file being merged into.
	KeepLocal MergeDecision = iota
	// TakeRemote takes the definition from the backed-up inventory.
	TakeRemote
)

// FieldDiff is one field whose value differs between the two inventories.
type FieldDiff struct {
	Field  string
	Local  string
	Remote string
}

// MergeConflict describes a server where the two inventories disagree and
// neither side is automatically right, so the user has to choose.
type MergeConflict struct {
	Name   string
	Local  Server
	Remote Server
	// Diffs lists the fields that differ, in a stable order. It includes a
	// credential field only when both sides hold an op:// reference: a local
	// path on one side and a reference on the other is resolved automatically
	// (the reference wins), and two local paths are not a choice worth making.
	Diffs []FieldDiff
}

// Summary renders the conflict as a one-line, field-level diff.
func (c MergeConflict) Summary() string {
	parts := make([]string, 0, len(c.Diffs))
	for _, d := range c.Diffs {
		parts = append(parts, fmt.Sprintf("%s: %s -> %s", d.Field, d.Local, d.Remote))
	}
	return fmt.Sprintf("%s (%s)", c.Name, strings.Join(parts, ", "))
}

// MergeInventories unions remote into local, matching by exact server name.
//
// The merge never deletes: a server that only the local file knows about is
// kept. That is what makes this safe to run on a machine that holds servers the
// backup has never seen.
//
// Servers that differ only in credential fields are not conflicts. A credential
// field is either a machine-local path or a portable op:// reference, so two
// machines legitimately disagree, and pickCredential decides between them. A
// conflict is raised when the inventory fields disagree, or when both sides
// hold op:// references that point at different items.
//
// decide is consulted once per conflict. Passing nil makes any conflict an
// error, which is what a non-interactive caller wants: the conflicts are
// returned so the caller can list them and tell the user what to pass instead.
func MergeInventories(local, remote []Server, decide func(MergeConflict) MergeDecision) (merged []Server, added, updated int, conflicts []MergeConflict, err error) {
	byName := make(map[string]int, len(local))
	merged = make([]Server, len(local))
	copy(merged, local)
	for i, srv := range merged {
		byName[srv.Name] = i
	}

	// Collect every conflict before consulting decide, so an interactive caller
	// can show the whole picture and then ask once.
	type pending struct {
		index    int
		conflict MergeConflict
	}
	var choices []pending

	for _, incoming := range remote {
		idx, exists := byName[incoming.Name]
		if !exists {
			byName[incoming.Name] = len(merged)
			merged = append(merged, incoming)
			added++
			continue
		}

		current := merged[idx]
		diffs := diffServers(current, incoming)
		if len(diffs) == 0 {
			resolved, changed := resolveServer(current, incoming, false)
			if changed {
				merged[idx] = resolved
				updated++
			}
			continue
		}

		conflicts = append(conflicts, MergeConflict{Name: incoming.Name, Local: current, Remote: incoming, Diffs: diffs})
		choices = append(choices, pending{index: idx, conflict: conflicts[len(conflicts)-1]})
	}

	if len(conflicts) > 0 && decide == nil {
		names := make([]string, 0, len(conflicts))
		for _, c := range conflicts {
			names = append(names, c.Summary())
		}
		sort.Strings(names)
		return nil, added, updated, conflicts, fmt.Errorf("the backup and this machine disagree about %d server(s): %s", len(conflicts), strings.Join(names, "; "))
	}

	for _, choice := range choices {
		incoming := choice.conflict.Remote

		takeRemote := false
		if decide != nil {
			takeRemote = decide(choice.conflict) == TakeRemote
		}

		current := merged[choice.index]
		resolved, changed := resolveServer(current, incoming, takeRemote)
		if changed {
			merged[choice.index] = resolved
			updated++
		}
	}

	return merged, added, updated, conflicts, nil
}

// resolveServer produces the winning definition for one conflicting or
// credential-only-differing server, and reports whether anything actually
// changed. Credential fields are resolved separately from the inventory
// choice: a portable reference is better than a local path regardless of which
// host definition won.
func resolveServer(current, incoming Server, takeRemote bool) (Server, bool) {
	resolved := current
	if takeRemote {
		resolved = incoming
	}
	resolved.KeyPath = pickCredential(current.KeyPath, incoming.KeyPath, takeRemote)
	resolved.Password = pickCredential(current.Password, incoming.Password, takeRemote)

	changed := takeRemote || resolved.KeyPath != current.KeyPath || resolved.Password != current.Password
	return resolved, changed
}

// SameInventory reports whether two inventories describe exactly the same
// servers, ignoring order.
//
// It exists because added/updated describe how the merge treated the local side,
// which is not the same question as "is the other side already up to date". A
// server only the local file knows about is neither added nor updated - it was
// already there - yet a backup that lacks it is out of date and has to be
// written. Comparing the merge result against the side being written answers the
// real question directly.
func SameInventory(a, b []Server) bool {
	if len(a) != len(b) {
		return false
	}
	byName := make(map[string]Server, len(a))
	for _, srv := range a {
		byName[srv.Name] = srv
	}
	for _, srv := range b {
		other, ok := byName[srv.Name]
		if !ok {
			return false
		}
		if !sameServer(other, srv) {
			return false
		}
	}
	return true
}

// sameServer compares every field, using the same normalisation as the diff so
// that a reordered tag list or an implicit default is not mistaken for a change.
func sameServer(a, b Server) bool {
	return a.Host == b.Host &&
		normalisedPort(a.Port) == normalisedPort(b.Port) &&
		normalisedUser(a.User) == normalisedUser(b.User) &&
		a.KeyPath == b.KeyPath &&
		a.Password == b.Password &&
		a.JumpHost == b.JumpHost &&
		a.SkipBatch == b.SkipBatch &&
		a.Description == b.Description &&
		renderTags(a.Tags) == renderTags(b.Tags) &&
		renderLabels(a.Labels) == renderLabels(b.Labels)
}

// diffServers lists the fields that genuinely need a user decision.
//
// Credential fields are excluded unless both sides are op:// references: a
// local path and a reference are not competing claims, they are the same
// credential seen from a managed machine and an unmanaged one.
func diffServers(local, remote Server) []FieldDiff {
	var diffs []FieldDiff
	add := func(field, l, r string) {
		if l != r {
			diffs = append(diffs, FieldDiff{Field: field, Local: l, Remote: r})
		}
	}

	add("host", local.Host, remote.Host)
	add("port", strconv.Itoa(normalisedPort(local.Port)), strconv.Itoa(normalisedPort(remote.Port)))
	add("user", normalisedUser(local.User), normalisedUser(remote.User))
	add("jump_host", local.JumpHost, remote.JumpHost)
	add("skip_batch", strconv.FormatBool(local.SkipBatch), strconv.FormatBool(remote.SkipBatch))
	add("tags", renderTags(local.Tags), renderTags(remote.Tags))
	add("labels", renderLabels(local.Labels), renderLabels(remote.Labels))
	add("description", local.Description, remote.Description)

	if local.KeyPath != remote.KeyPath && secret.Is1PRef(local.KeyPath) && secret.Is1PRef(remote.KeyPath) {
		diffs = append(diffs, FieldDiff{Field: "key_path", Local: local.KeyPath, Remote: remote.KeyPath})
	}
	if local.Password != remote.Password && secret.Is1PRef(local.Password) && secret.Is1PRef(remote.Password) {
		diffs = append(diffs, FieldDiff{Field: "password", Local: local.Password, Remote: remote.Password})
	}

	return diffs
}

// pickCredential decides between two values for one credential field.
//
// A portable op:// reference beats a machine-local path, because only the
// reference still means something on the machine being bootstrapped. When
// neither side is a reference both are local paths belonging to different
// machines, so the local value is kept rather than churned back and forth.
// takeRemote only matters when the caller already asked the user about two
// competing references.
func pickCredential(local, remote string, takeRemote bool) string {
	if local == remote {
		return local
	}
	localRef, remoteRef := secret.Is1PRef(local), secret.Is1PRef(remote)
	switch {
	case localRef && remoteRef:
		if takeRemote {
			return remote
		}
		return local
	case localRef:
		return local
	case remoteRef:
		return remote
	case local == "":
		return remote
	case remote == "":
		return local
	default:
		return local
	}
}

func normalisedPort(port int) int {
	if port <= 0 || port > 65535 {
		return 22
	}
	return port
}

func normalisedUser(user string) string {
	if strings.TrimSpace(user) == "" {
		return "root"
	}
	return user
}

// renderTags sorts before joining so that a reordered tag list is not mistaken
// for a change.
func renderTags(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	sorted := make([]string, len(tags))
	copy(sorted, tags)
	sort.Strings(sorted)
	return strings.Join(sorted, ",")
}

func renderLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+labels[k])
	}
	return strings.Join(parts, ",")
}
