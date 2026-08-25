package backup

import (
	"testing"
)

func TestParseResticSummary(t *testing.T) {
	rawOutput := `
{"message_type":"status","percent_done":0.5,"total_files":100,"files_done":50,"total_bytes":1000000,"bytes_done":500000}
{"message_type":"status","percent_done":1.0,"total_files":100,"files_done":100,"total_bytes":1000000,"bytes_done":1000000}
{"message_type":"summary","files_new":5,"files_changed":2,"files_unmodified":93,"dirs_new":1,"dirs_changed":0,"dirs_unmodified":10,"data_blobs":7,"tree_blobs":2,"data_added":2048576,"total_files_processed":100,"total_bytes_processed":1000000,"total_duration":4.32,"snapshot_id":"c1a2b3d4"}
`

	summary := ParseResticSummary(rawOutput)
	if summary == nil {
		t.Fatal("expected non-nil summary")
	}

	if summary.SnapshotID != "c1a2b3d4" {
		t.Errorf("expected snapshot_id 'c1a2b3d4', got %q", summary.SnapshotID)
	}
	if summary.FilesNew != 5 || summary.FilesChanged != 2 || summary.FilesUnmodified != 93 {
		t.Errorf("unexpected files counts: new=%d, changed=%d, unmodified=%d",
			summary.FilesNew, summary.FilesChanged, summary.FilesUnmodified)
	}
	if summary.DataAdded != 2048576 || summary.TotalBytes != 1000000 {
		t.Errorf("unexpected bytes: added=%d, total=%d", summary.DataAdded, summary.TotalBytes)
	}
	if summary.TotalDuration != 4.32 {
		t.Errorf("expected duration 4.32, got %f", summary.TotalDuration)
	}

	// Test with no summary line
	noSummary := `
some raw text output without json
{"message_type":"status","percent_done":0.1}
`
	if got := ParseResticSummary(noSummary); got != nil {
		t.Errorf("expected nil for output without summary, got %+v", got)
	}
}

func TestParseSnapshotsJSON(t *testing.T) {
	rawJSON := `[
		{
			"id": "1234567890abcdef",
			"time": "2026-08-20T12:00:00Z",
			"tree": "tree-hash",
			"paths": ["/var/www"],
			"hostname": "vps-01",
			"username": "root",
			"tags": ["prod"]
		}
	]`

	snapshots, err := ParseSnapshotsJSON(rawJSON)
	if err != nil {
		t.Fatalf("ParseSnapshotsJSON() error: %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snapshots))
	}
	if snapshots[0].ID != "1234567890abcdef" {
		t.Errorf("expected id '1234567890abcdef', got %q", snapshots[0].ID)
	}
	if snapshots[0].ShortID != "12345678" {
		t.Errorf("expected short_id '12345678', got %q", snapshots[0].ShortID)
	}
	if snapshots[0].Hostname != "vps-01" {
		t.Errorf("expected hostname 'vps-01', got %q", snapshots[0].Hostname)
	}

	// Empty string
	empty, err := ParseSnapshotsJSON("")
	if err != nil || len(empty) != 0 {
		t.Errorf("expected empty slice for empty string, got %v (err: %v)", empty, err)
	}
}

func TestBuildSnapshotsScript_DeterministicEnv(t *testing.T) {
	job := Job{
		Name:    "test-job",
		Server:  "vps-01",
		Backend: "s3:bucket/path",
		Env: map[string]string{
			"ZZZ_KEY": "val3",
			"AAA_KEY": "val1",
			"MMM_KEY": "val2",
		},
	}

	first := BuildSnapshotsScript(job)
	for i := 0; i < 10; i++ {
		got := BuildSnapshotsScript(job)
		if got != first {
			t.Fatalf("BuildSnapshotsScript produced non-deterministic output at iteration %d:\nfirst:\n%s\ngot:\n%s", i, first, got)
		}
	}
}

