package backup

import (
	"fmt"
	"sort"
	"strings"
)

// BuildBackupScript generates a self-contained shell script that checks, initializes,
// executes a restic backup, and optionally prunes old snapshots according to the retention policy.
func BuildBackupScript(job Job) (string, error) {
	if err := job.Validate(); err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString("#!/bin/bash\n")
	sb.WriteString("set -euo pipefail\n\n")

	// 1. Export environment variables
	writeEnvBlock(&sb, job)

	// 2. Check restic installation
	sb.WriteString(`if ! command -v restic >/dev/null 2>&1; then
  echo "Error: restic is not installed on target host. Please install it first or run: opspulse bootstrap <server> -t restic" >&2
  exit 127
fi
` + "\n")

	// 3. Auto-initialize repository if not initialized
	sb.WriteString(`# Check if repository is initialized, if not initialize it
if ! restic snapshots >/dev/null 2>&1; then
  echo "Repository not initialized. Running restic init..."
  restic init
fi
` + "\n")

	// 4. Build restic backup command
	sb.WriteString("echo \"Starting restic backup for job: " + job.Name + "...\"\n")
	sb.WriteString("restic backup --json")

	for _, tag := range job.Tags {
		sb.WriteString(fmt.Sprintf(" --tag %q", tag))
	}
	// Add job name tag by default
	sb.WriteString(fmt.Sprintf(" --tag %q", "job:"+job.Name))

	for _, excl := range job.Excludes {
		sb.WriteString(fmt.Sprintf(" --exclude %q", excl))
	}

	for _, p := range job.Paths {
		sb.WriteString(fmt.Sprintf(" %q", p))
	}
	sb.WriteString("\n\n")

	// 5. Build restic forget & prune if retention policy is defined
	if job.Retention != nil {
		ret := job.Retention
		var forgetArgs []string

		if ret.KeepLast > 0 {
			forgetArgs = append(forgetArgs, fmt.Sprintf("--keep-last %d", ret.KeepLast))
		}
		if ret.KeepDaily > 0 {
			forgetArgs = append(forgetArgs, fmt.Sprintf("--keep-daily %d", ret.KeepDaily))
		}
		if ret.KeepWeekly > 0 {
			forgetArgs = append(forgetArgs, fmt.Sprintf("--keep-weekly %d", ret.KeepWeekly))
		}
		if ret.KeepMonthly > 0 {
			forgetArgs = append(forgetArgs, fmt.Sprintf("--keep-monthly %d", ret.KeepMonthly))
		}
		if ret.KeepYearly > 0 {
			forgetArgs = append(forgetArgs, fmt.Sprintf("--keep-yearly %d", ret.KeepYearly))
		}
		for _, tag := range ret.KeepTags {
			forgetArgs = append(forgetArgs, fmt.Sprintf("--keep-tag %q", tag))
		}

		if len(forgetArgs) > 0 {
			sb.WriteString("echo \"Applying retention policy (restic forget --prune)...\"\n")
			sb.WriteString("restic forget --prune " + strings.Join(forgetArgs, " ") + "\n")
		}
	}

	return sb.String(), nil
}

// BuildSnapshotsScript generates a shell script to list all snapshots in JSON format.
func BuildSnapshotsScript(job Job) string {
	var sb strings.Builder
	sb.WriteString("#!/bin/bash\n")
	sb.WriteString("set -euo pipefail\n\n")

	writeEnvBlock(&sb, job)

	sb.WriteString("restic snapshots --json\n")
	return sb.String()
}

// BuildRestoreScript generates a shell script that executes `restic restore` to restore
// files from a specific snapshot. Optionally filters by include patterns for targeted asset restore.
func BuildRestoreScript(job Job, snapshotID string, targetPath string, includePatterns []string) string {
	var sb strings.Builder
	sb.WriteString("#!/bin/bash\n")
	sb.WriteString("set -euo pipefail\n\n")

	writeEnvBlock(&sb, job)

	// Check restic installation
	sb.WriteString(`if ! command -v restic >/dev/null 2>&1; then
  echo "Error: restic is not installed on target host." >&2
  exit 127
fi
`)
	sb.WriteString("\n")

	sb.WriteString(fmt.Sprintf("echo \"Starting restic restore (snapshot: %s, target: %s)...\"\n", snapshotID, targetPath))
	sb.WriteString(fmt.Sprintf("restic restore %q --target %q", snapshotID, targetPath))

	for _, pattern := range includePatterns {
		sb.WriteString(fmt.Sprintf(" --include %q", pattern))
	}

	sb.WriteString(" --verbose\n")

	sb.WriteString("echo \"Restore completed successfully.\"\n")
	return sb.String()
}

// BuildRestoreDryRunScript generates a shell script that uses `restic ls` to preview
// which files would be restored from a snapshot, without actually writing any data.
func BuildRestoreDryRunScript(job Job, snapshotID string, includePatterns []string) string {
	var sb strings.Builder
	sb.WriteString("#!/bin/bash\n")
	sb.WriteString("set -euo pipefail\n\n")

	writeEnvBlock(&sb, job)

	// Check restic installation
	sb.WriteString(`if ! command -v restic >/dev/null 2>&1; then
  echo "Error: restic is not installed on target host." >&2
  exit 127
fi
`)
	sb.WriteString("\n")

	sb.WriteString(fmt.Sprintf("echo \"[DRY-RUN] Listing files in snapshot %s...\"\n", snapshotID))
	sb.WriteString(fmt.Sprintf("restic ls %q", snapshotID))

	for _, pattern := range includePatterns {
		sb.WriteString(fmt.Sprintf(" --include %q", pattern))
	}

	sb.WriteString("\n")
	return sb.String()
}

// writeEnvBlock writes the common environment variable export block for a backup job,
// including RESTIC_REPOSITORY if not already set via Env map.
func writeEnvBlock(sb *strings.Builder, job Job) {
	envKeys := make([]string, 0, len(job.Env))
	for k := range job.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)

	hasRepo := false
	for _, k := range envKeys {
		if k == "RESTIC_REPOSITORY" {
			hasRepo = true
		}
		sb.WriteString(fmt.Sprintf("export %s=%q\n", k, job.Env[k]))
	}

	if !hasRepo && job.Backend != "" {
		sb.WriteString(fmt.Sprintf("export RESTIC_REPOSITORY=%q\n", job.Backend))
	}
	sb.WriteString("\n")
}

