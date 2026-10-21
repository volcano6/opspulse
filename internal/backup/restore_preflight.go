package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/volcano6/opspulse/internal/docker"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/shellquote"
)

const (
	restorePreflightErrorPrefix = "OPSPULSE_PREFLIGHT_ERROR|"
	restorePreflightEndMarker   = "OPSPULSE_PREFLIGHT_END"
)

// ErrRestorePreflightBlocked identifies a post-file failure caused by the
// target-side preflight. The caller still reports the restored files, but the
// error must not be described as a Compose/auto-start failure.
var ErrRestorePreflightBlocked = errors.New("restore target preflight blocked")

// restorePreflightInput is everything the read-only target check needs before
// the first mutating post-restore step. Artifact paths are already resolved
// against the restored package so the preflight and the execution path cannot
// disagree about which file will be imported. The project directory is
// deliberately not an input: Compose may not have a project file yet, and the
// existing manifest already names the file to check.
type restorePreflightInput struct {
	ProjectName        string
	ComposeFile        string
	VolumeArchives     []string
	DatabaseDump       string
	ExpectedContainers []string
	CrossMachine       bool
}

// restorePreflightReport is the parsed result of the read-only preflight
// script. Container summaries are present only for a cross-machine restore.
type restorePreflightReport struct {
	Errors     []string
	Containers []docker.ContainerSummary
}

// BuildRestorePreflightScript generates the read-only checks that must pass
// before a manifest-driven restore imports a volume, starts Compose or imports
// a database. The script deliberately exits 0 even when checks fail: the Go
// caller needs the complete report, not just the first failed command.
func BuildRestorePreflightScript(in restorePreflightInput) (string, error) {
	if err := validateRestorePreflightInput(in); err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(`set +e
OPSPULSE_DOCKER_READY=0

opspulse_preflight_fail() {
  printf 'OPSPULSE_PREFLIGHT_ERROR|%s|%s\n' "$1" "$2"
}

opspulse_preflight_file() {
  if [ -f "$2" ] && [ -r "$2" ]; then
    printf 'OPSPULSE_PREFLIGHT_FILE|%s|ok\n' "$1"
  else
    opspulse_preflight_fail "path_missing" "$1: $2 is missing or unreadable"
  fi
}
`)

	b.WriteString(`
if ! command -v docker >/dev/null 2>&1; then
  opspulse_preflight_fail "docker_missing" "Docker CLI is not available on the target"
elif ! docker info >/dev/null 2>&1; then
  opspulse_preflight_fail "docker_daemon" "Docker daemon is not reachable on the target"
else
  OPSPULSE_DOCKER_READY=1
fi

if docker compose version >/dev/null 2>&1; then
  printf 'OPSPULSE_PREFLIGHT_COMPOSE|plugin\n'
elif command -v docker-compose >/dev/null 2>&1; then
  printf 'OPSPULSE_PREFLIGHT_COMPOSE|standalone\n'
else
  opspulse_preflight_fail "compose_missing" "neither docker compose nor docker-compose is available"
fi
`)

	fmt.Fprintf(&b, "opspulse_preflight_file 'compose_file' %s\n", shellquote.Quote(in.ComposeFile))
	for i, archive := range in.VolumeArchives {
		fmt.Fprintf(&b, "opspulse_preflight_file 'volume_archive[%d]' %s\n", i, shellquote.Quote(archive))
	}
	if in.DatabaseDump != "" {
		fmt.Fprintf(&b, "opspulse_preflight_file 'database_dump' %s\n", shellquote.Quote(in.DatabaseDump))
	}

	if len(in.VolumeArchives) > 0 {
		b.WriteString(`
if [ "$OPSPULSE_DOCKER_READY" -eq 1 ]; then
  HELPER_IMG="${OPSPULSE_HELPER_IMAGE:-alpine:3.20}"
  if docker image inspect "$HELPER_IMG" >/dev/null 2>&1; then
    printf 'OPSPULSE_PREFLIGHT_HELPER|present\n'
  elif docker image inspect busybox:1.36 >/dev/null 2>&1; then
    printf 'OPSPULSE_PREFLIGHT_HELPER|fallback:busybox:1.36\n'
  elif docker image inspect alpine >/dev/null 2>&1; then
    printf 'OPSPULSE_PREFLIGHT_HELPER|fallback:alpine\n'
  elif docker image inspect busybox >/dev/null 2>&1; then
    printf 'OPSPULSE_PREFLIGHT_HELPER|fallback:busybox\n'
  else
    opspulse_preflight_fail "helper_image_missing" "no configured or fallback helper image is available for volume import"
  fi
fi
`)
	}

	if in.CrossMachine && len(in.ExpectedContainers) > 0 {
		b.WriteString(`
if [ "$OPSPULSE_DOCKER_READY" -eq 1 ]; then
  if ! docker ps -a --format '{{json .}}'; then
    opspulse_preflight_fail "docker_ps_failed" "could not list target containers for conflict checks"
  fi
fi
`)
	}

	b.WriteString("\nprintf '" + restorePreflightEndMarker + "\\n'\nexit 0\n")
	return b.String(), nil
}

// ParseRestorePreflightOutput parses the marker and docker ps output from the
// preflight script. A missing end marker is treated as a broken probe, never
// as an empty success.
func ParseRestorePreflightOutput(raw string) (*restorePreflightReport, error) {
	report := &restorePreflightReport{}
	sawEnd := false

	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if trimmed == restorePreflightEndMarker {
			sawEnd = true
			continue
		}
		if strings.HasPrefix(trimmed, restorePreflightErrorPrefix) {
			parts := strings.SplitN(trimmed, "|", 3)
			if len(parts) < 3 {
				return nil, fmt.Errorf("malformed restore preflight error line %q", trimmed)
			}
			report.Errors = append(report.Errors, fmt.Sprintf("%s: %s", parts[1], parts[2]))
			continue
		}
		if strings.HasPrefix(trimmed, "{") {
			containers, err := docker.ParsePsJSON(trimmed)
			if err != nil {
				return nil, fmt.Errorf("parse target container list from restore preflight: %w", err)
			}
			report.Containers = append(report.Containers, containers...)
		}
	}

	if !sawEnd {
		return nil, fmt.Errorf("restore preflight did not report completion; refusing to run post-restore steps")
	}
	return report, nil
}

// runRestorePreflight executes the read-only probe and turns any prerequisite
// failure or cross-machine conflict into one error before mutation starts.
func runRestorePreflight(
	ctx context.Context,
	execToUse executor.Executor,
	target executor.Target,
	jobName string,
	in restorePreflightInput,
) error {
	script, err := BuildRestorePreflightScript(in)
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	res, execErr := execToUse.Execute(ctx, target, "restore-preflight-"+jobName, script, &buf)
	if execErr != nil {
		return fmt.Errorf("restore preflight could not run: %w", execErr)
	}
	if res != nil && !res.Success {
		return fmt.Errorf("restore preflight exited with code %d: %v", res.ExitCode, res.Error)
	}

	report, err := ParseRestorePreflightOutput(buf.String())
	if err != nil {
		return err
	}
	if len(report.Errors) > 0 {
		return fmt.Errorf("restore preflight failed: %s", strings.Join(report.Errors, "; "))
	}
	if in.CrossMachine {
		if conflicts := detectRestoreTargetConflicts(report.Containers, in.ProjectName, in.ExpectedContainers); len(conflicts) > 0 {
			return fmt.Errorf("restore preflight found target conflicts: %s", strings.Join(conflicts, "; "))
		}
	}
	return nil
}

// detectRestoreTargetConflicts blocks a cross-machine restore when the target
// already owns the Compose project or a container name the restore is about to
// create. Existing named volumes are intentionally not conflicts: importing a
// volume is defined to update its contents.
func detectRestoreTargetConflicts(containers []docker.ContainerSummary, projectName string, expectedContainers []string) []string {
	expected := make(map[string]bool, len(expectedContainers))
	for _, name := range expectedContainers {
		name = strings.TrimSpace(name)
		if name != "" {
			expected[name] = true
		}
	}

	var conflicts []string
	seen := make(map[string]bool)
	add := func(msg string) {
		if msg == "" || seen[msg] {
			return
		}
		seen[msg] = true
		conflicts = append(conflicts, msg)
	}

	for _, container := range containers {
		if expected[container.Name] {
			add(fmt.Sprintf("target already has container %q", container.Name))
		}
		if projectName != "" && container.ComposeProject() == projectName {
			add(fmt.Sprintf("target already has Compose project %q (container %q)", projectName, container.Name))
		}
	}
	sort.Strings(conflicts)
	return conflicts
}

func validateRestorePreflightInput(in restorePreflightInput) error {
	if strings.TrimSpace(in.ComposeFile) == "" {
		return fmt.Errorf("restore preflight: compose file is required")
	}
	paths := append([]string{in.ProjectName, in.ComposeFile, in.DatabaseDump}, in.VolumeArchives...)
	paths = append(paths, in.ExpectedContainers...)
	for _, value := range paths {
		if hasRestorePreflightControlCharacter(value) {
			return fmt.Errorf("restore preflight: value %q contains a control character", value)
		}
	}
	return nil
}

func hasRestorePreflightControlCharacter(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
