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

	sb.WriteString("restic snapshots --json\n")
	return sb.String()
}
