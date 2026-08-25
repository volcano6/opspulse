package backup

import (
	"bufio"
	"encoding/json"
	"strings"
)

// ResticSummary captures the structured summary metrics from restic backup --json output.
type ResticSummary struct {
	MessageType        string  `json:"message_type"`
	FilesNew           int64   `json:"files_new"`
	FilesChanged       int64   `json:"files_changed"`
	FilesUnmodified    int64   `json:"files_unmodified"`
	DirsNew            int64   `json:"dirs_new"`
	DirsChanged        int64   `json:"dirs_changed"`
	DirsUnmodified     int64   `json:"dirs_unmodified"`
	DataBlobs          int64   `json:"data_blobs"`
	TreeBlobs          int64   `json:"tree_blobs"`
	DataAdded          int64   `json:"data_added"`
	TotalFiles         int64   `json:"total_files_processed"`
	TotalBytes         int64   `json:"total_bytes_processed"`
	TotalDuration      float64 `json:"total_duration"`
	SnapshotID         string  `json:"snapshot_id"`
}

// ParseResticSummary scans the lines of restic output to find and parse the JSON summary event.
func ParseResticSummary(output string) *ResticSummary {
	scanner := bufio.NewScanner(strings.NewReader(output))
	const maxLineLength = 1024 * 1024 // 1MB buffer for large restic status lines
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, maxLineLength)

	var latestSummary *ResticSummary

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "{") || !strings.HasSuffix(line, "}") {
			continue
		}

		var summary ResticSummary
		if err := json.Unmarshal([]byte(line), &summary); err == nil && summary.MessageType == "summary" {
			latestSummary = &summary
		}
	}

	return latestSummary
}
