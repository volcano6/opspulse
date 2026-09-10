package backup

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/volcano6/opspulse/internal/shellquote"
)

// JobTag returns the canonical restic tag for isolating snapshots by backup job.
func JobTag(jobName string) string {
	return "job:" + jobName
}

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
if ! restic snapshots --retry-lock 1m >/dev/null 2>&1; then
  echo "Repository not initialized. Running restic init..."
  restic init
fi
` + "\n")

	// 4. Build restic backup command
	sb.WriteString("echo \"Starting restic backup for job: \" " + shellquote.Quote(job.Name) + " \"...\"\n")
	sb.WriteString("restic backup --retry-lock 2m --json")

	for _, tag := range job.Tags {
		sb.WriteString(" --tag " + shellquote.Quote(tag))
	}
	// Add job name tag by default for strict snapshot isolation
	sb.WriteString(" --tag " + shellquote.Quote(JobTag(job.Name)))

	for _, excl := range job.Excludes {
		sb.WriteString(" --exclude " + shellquote.Quote(excl))
	}

	for _, p := range job.Paths {
		sb.WriteString(" " + shellquote.Quote(p))
	}
	sb.WriteString("\n\n")

	// 5. Build restic forget & prune if retention policy is defined
	if job.Retention != nil {
		ret := job.Retention
		var forgetArgs []string

		// Isolate retention strictly to this job's snapshots
		forgetArgs = append(forgetArgs, "--tag "+shellquote.Quote(JobTag(job.Name)))

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
			forgetArgs = append(forgetArgs, "--keep-tag "+shellquote.Quote(tag))
		}

		if len(forgetArgs) > 1 {
			sb.WriteString("echo \"Applying retention policy (restic forget --prune)...\"\n")
			sb.WriteString("restic forget --retry-lock 5m --prune " + strings.Join(forgetArgs, " ") + "\n")
		}
	}

	return sb.String(), nil
}

// BuildSnapshotsScript generates a shell script to list all snapshots in JSON format,
// filtered strictly to the current job's tag for shared repository isolation.
func BuildSnapshotsScript(job Job) string {
	var sb strings.Builder
	sb.WriteString("#!/bin/bash\n")
	sb.WriteString("set -euo pipefail\n\n")

	writeEnvBlock(&sb, job)

	sb.WriteString("restic snapshots --retry-lock 30s --json --tag " + shellquote.Quote(JobTag(job.Name)) + "\n")
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

	if len(job.Remap) > 0 {
		// Use staging directory for remapping
		sb.WriteString("echo \"Starting restic restore with path remapping (snapshot: \" " + shellquote.Quote(snapshotID) + " \")...\"\n")
		sb.WriteString("STAGING=$(mktemp -d -t opspulse-restore-XXXXXX)\n")
		sb.WriteString("trap 'rm -rf \"$STAGING\"' EXIT\n\n")
		
		sb.WriteString("restic restore --retry-lock 2m " + shellquote.Quote(snapshotID) + " --target \"$STAGING\"")
		for _, pattern := range includePatterns {
			sb.WriteString(" --include " + shellquote.Quote(pattern))
		}
		sb.WriteString(" --verbose\n\n")

		remapper := NewPathRemapper(job.Remap)
		sb.WriteString("echo \"Applying path remapping...\"\n")
		
		// If includePatterns are specified, only remap those. Otherwise remap job.Paths
		pathsToRemap := job.Paths
		if len(includePatterns) > 0 {
			pathsToRemap = includePatterns
		}
		
		for _, p := range pathsToRemap {
			origClean := path.Clean(p)
			if !strings.HasPrefix(origClean, "/") {
				origClean = "/" + origClean
			}
			targetRemap := remapper.Remap(origClean)
			
			// Adjust target if it's absolute
			targetPathAbs := targetPath
			if targetPathAbs == "" {
				targetPathAbs = "/"
			}
			
			finalTarget := path.Join(targetPathAbs, strings.TrimPrefix(targetRemap, "/"))
			
			// We cannot shellquote $STAGING entirely because we need the shell to expand it
			// We shellquote the relative path and concatenate.
			relPath := strings.TrimPrefix(origClean, "/")
			
			sb.WriteString(fmt.Sprintf("if [ -e \"$STAGING\"/%s ]; then\n", shellquote.Quote(relPath)))
			sb.WriteString(fmt.Sprintf("  mkdir -p %s\n", shellquote.Quote(path.Dir(finalTarget))))
			sb.WriteString(fmt.Sprintf("  echo \"Moving \"$STAGING\"/%s -> %s\"\n", shellquote.Quote(relPath), shellquote.Quote(finalTarget)))
			sb.WriteString(fmt.Sprintf("  cp -a \"$STAGING\"/%s %s\n", shellquote.Quote(relPath), shellquote.Quote(path.Dir(finalTarget))))
			sb.WriteString("fi\n")
		}
	} else {
		// Standard restore
		sb.WriteString("echo \"Starting restic restore (snapshot: \" " + shellquote.Quote(snapshotID) + " \", target: \" " + shellquote.Quote(targetPath) + " \")...\"\n")
		sb.WriteString("restic restore --retry-lock 2m " + shellquote.Quote(snapshotID) + " --target " + shellquote.Quote(targetPath))

		for _, pattern := range includePatterns {
			sb.WriteString(" --include " + shellquote.Quote(pattern))
		}
		sb.WriteString(" --verbose\n")
	}

	sb.WriteString("\necho \"Restore completed successfully.\"\n")
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

	sb.WriteString("echo \"[DRY-RUN] Listing files in snapshot \" " + shellquote.Quote(snapshotID) + " \"...\"\n")
	sb.WriteString("restic ls --retry-lock 30s " + shellquote.Quote(snapshotID))

	for _, pattern := range includePatterns {
		sb.WriteString(" --include " + shellquote.Quote(pattern))
	}

	sb.WriteString("\n")
	return sb.String()
}

// writeEnvBlock writes the common environment variable export block for a backup job,
// validating variable names and single-quoting all values to prevent shell injection and variable expansion.
func writeEnvBlock(sb *strings.Builder, job Job) {
	envKeys := make([]string, 0, len(job.Env))
	for k := range job.Env {
		if shellquote.ValidEnvName(k) {
			envKeys = append(envKeys, k)
		}
	}
	sort.Strings(envKeys)

	hasRepo := false
	for _, k := range envKeys {
		if k == "RESTIC_REPOSITORY" {
			hasRepo = true
		}
		sb.WriteString(fmt.Sprintf("export %s=%s\n", k, shellquote.Quote(job.Env[k])))
	}

	if !hasRepo && job.Backend != "" {
		sb.WriteString(fmt.Sprintf("export RESTIC_REPOSITORY=%s\n", shellquote.Quote(job.Backend)))
	}
	sb.WriteString("\n")
}
