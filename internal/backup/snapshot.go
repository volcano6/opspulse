package backup

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Snapshot represents a restic backup snapshot.
type Snapshot struct {
	ID        string    `json:"id"`
	ShortID   string    `json:"short_id"`
	Time      time.Time `json:"time"`
	Parent    string    `json:"parent,omitempty"`
	Tree      string    `json:"tree"`
	Paths     []string  `json:"paths"`
	Hostname  string    `json:"hostname"`
	Username  string    `json:"username"`
	Tags      []string  `json:"tags,omitempty"`
}

// ParseSnapshotsJSON parses the JSON array output of `restic snapshots --json`.
func ParseSnapshotsJSON(rawJSON string) ([]Snapshot, error) {
	trimmed := strings.TrimSpace(rawJSON)
	if trimmed == "" || trimmed == "null" {
		return []Snapshot{}, nil
	}

	// Find the start of the JSON array '[' if there's header noise
	startIdx := strings.Index(trimmed, "[")
	if startIdx == -1 {
		return []Snapshot{}, nil
	}
	endIdx := strings.LastIndex(trimmed, "]")
	if endIdx == -1 || endIdx < startIdx {
		return nil, fmt.Errorf("invalid snapshots JSON output")
	}

	cleanJSON := trimmed[startIdx : endIdx+1]
	var snapshots []Snapshot
	if err := json.Unmarshal([]byte(cleanJSON), &snapshots); err != nil {
		return nil, fmt.Errorf("failed to unmarshal snapshots JSON: %w", err)
	}

	// Fill ShortID if missing
	for i := range snapshots {
		if snapshots[i].ShortID == "" && len(snapshots[i].ID) >= 8 {
			snapshots[i].ShortID = snapshots[i].ID[:8]
		}
	}

	return snapshots, nil
}
