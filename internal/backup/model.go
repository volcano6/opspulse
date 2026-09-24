// Package backup manages backup job definitions, restic execution, and snapshot lifecycle.
package backup

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/volcano6/opspulse/internal/asset"
	"github.com/volcano6/opspulse/internal/shellquote"
)

var (
	// ErrInvalidJobName is returned when a backup job name is empty or invalid.
	ErrInvalidJobName = errors.New("job name cannot be empty")
	// ErrInvalidJobServer is returned when a backup job has no associated server.
	ErrInvalidJobServer = errors.New("job server cannot be empty")
	// ErrInvalidJobPaths is returned when a backup job has no paths specified.
	ErrInvalidJobPaths = errors.New("job must specify at least one backup path")
	// ErrInvalidJobBackend is returned when a backup job has no backend repository URL/path.
	ErrInvalidJobBackend = errors.New("job backend repository cannot be empty")
	// ErrInvalidJobEnv is returned when a backup job's env contains a variable name that is not a valid POSIX identifier.
	ErrInvalidJobEnv = errors.New("job env contains an invalid variable name")
	// ErrInvalidJobSchedule is returned when a backup job's schedule is not a single printable line.
	ErrInvalidJobSchedule = errors.New("job schedule is malformed")
	// ErrJobNotFound is returned when a requested backup job does not exist.
	ErrJobNotFound = errors.New("backup job not found")
)

// MaxJobNameLength bounds backup job names. A job name is used verbatim as a
// single path segment, a restic tag value, and a log-file component, so an
// unbounded name is a portability hazard.
const MaxJobNameLength = 64

// maxScheduleLength bounds cron expressions to keep a malformed multi-line or
// runaway value out of the generated scripts.
const maxScheduleLength = 128

// RetentionPolicy specifies snapshot pruning rules for restic forget.
type RetentionPolicy struct {
	KeepDaily   int      `yaml:"keep_daily,omitempty" json:"keep_daily,omitempty"`
	KeepWeekly  int      `yaml:"keep_weekly,omitempty" json:"keep_weekly,omitempty"`
	KeepMonthly int      `yaml:"keep_monthly,omitempty" json:"keep_monthly,omitempty"`
	KeepYearly  int      `yaml:"keep_yearly,omitempty" json:"keep_yearly,omitempty"`
	KeepLast    int      `yaml:"keep_last,omitempty" json:"keep_last,omitempty"`
	KeepTags    []string `yaml:"keep_tags,omitempty" json:"keep_tags,omitempty"`
}

// Job defines a declarative backup task.
type Job struct {
	Name        string            `yaml:"name" json:"name"`
	Server      string            `yaml:"server" json:"server"`
	Paths       []string          `yaml:"paths,omitempty" json:"paths,omitempty"`
	Assets      []string          `yaml:"assets,omitempty" json:"assets,omitempty"`
	Backend     string            `yaml:"backend" json:"backend"`
	Env         map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	Retention   *RetentionPolicy  `yaml:"retention,omitempty" json:"retention,omitempty"`
	Excludes    []string          `yaml:"excludes,omitempty" json:"excludes,omitempty"`
	Tags        []string          `yaml:"tags,omitempty" json:"tags,omitempty"`
	Description string            `yaml:"description,omitempty" json:"description,omitempty"`
	Schedule    string            `yaml:"schedule,omitempty" json:"schedule,omitempty"` // Cron expression (e.g. "0 2 * * *", "@daily")
	Remap       map[string]string `yaml:"remap,omitempty" json:"remap,omitempty"`       // Source prefix -> Target prefix mapping for cross-machine restore
}

// ValidateJobName verifies that a backup job name is safe as a single filesystem
// path segment, a restic tag value, and a shell argument.
//
// It allows Unicode letters and digits plus '.', '_', '@' and '-'. It rejects
// names that could change path or shell semantics: path separators, "..",
// control characters, whitespace, a leading '.' or '-', and names longer than
// MaxJobNameLength runes. An empty (or whitespace-only) name returns
// ErrInvalidJobName unwrapped.
//
// This is the single source of truth for the job-name contract; container alias
// validation (internal/backup/container_runner.go) delegates to it.
func ValidateJobName(name string) error {
	if strings.TrimSpace(name) == "" {
		return ErrInvalidJobName
	}
	if utf8.RuneCountInString(name) > MaxJobNameLength {
		return fmt.Errorf("%w: job name %q is longer than %d characters", ErrInvalidJobName, name, MaxJobNameLength)
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("%w: job name %q must not contain %q", ErrInvalidJobName, name, "..")
	}
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "-") {
		return fmt.Errorf("%w: job name %q must not start with '.' or '-'", ErrInvalidJobName, name)
	}
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		switch r {
		case '.', '_', '@', '-':
			continue
		}
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return fmt.Errorf("%w: job name %q must not contain whitespace or control characters", ErrInvalidJobName, name)
		}
		return fmt.Errorf("%w: job name %q may only contain letters, digits and '._@-'", ErrInvalidJobName, name)
	}
	return nil
}

// validateScheduleShape rejects schedules that are not a single printable line.
// Full cron parsing lives in internal/scheduler (importing it here would create
// an import cycle), so this only guards the shape and length.
func validateScheduleShape(spec string) error {
	trimmed := strings.TrimSpace(spec)
	if utf8.RuneCountInString(trimmed) > maxScheduleLength {
		return fmt.Errorf("schedule expression is longer than %d characters", maxScheduleLength)
	}
	for _, r := range trimmed {
		if unicode.IsControl(r) {
			return errors.New("schedule expression must be a single line without control characters")
		}
	}
	return nil
}

// Validate checks that the backup job contains all required fields and valid data.
//
// It rejects only genuinely unsafe input: a name that could escape its path
// segment or break a shell, a relative backup path (which would operate on the
// SSH session's working directory), an invalid env identifier (previously
// dropped silently from the generated script), and a multi-line/oversized
// schedule. Full cron parsing stays in internal/scheduler.
func (j *Job) Validate() error {
	if err := ValidateJobName(j.Name); err != nil {
		return err
	}
	if strings.TrimSpace(j.Server) == "" {
		return ErrInvalidJobServer
	}
	if len(j.Paths) == 0 && len(j.Assets) == 0 {
		return ErrInvalidJobPaths
	}
	for _, p := range j.Paths {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			return ErrInvalidJobPaths
		}
		if !strings.HasPrefix(trimmed, "/") {
			// A leading "~" is the common case: the path reaches restic
			// shell-quoted inside single quotes, so the remote shell never
			// expands it and restic resolves it against the session's working
			// directory instead of the user's home. Say that outright rather
			// than let the user hunt for why "/var/www" worked and "~" did not.
			hint := ""
			if strings.HasPrefix(trimmed, "~") || strings.HasPrefix(trimmed, ".") {
				hint = " (note: '~' and relative paths are never expanded here; write the full path, e.g. /home/user/.config)"
			}
			return fmt.Errorf("%w: job %q path %q must be an absolute path (e.g. /var/www)%s", ErrInvalidJobPaths, j.Name, p, hint)
		}
	}
	if strings.TrimSpace(j.Backend) == "" {
		return ErrInvalidJobBackend
	}
	for key := range j.Env {
		if !shellquote.ValidEnvName(key) {
			return fmt.Errorf("%w: job %q env key %q must match [A-Za-z_][A-Za-z0-9_]*", ErrInvalidJobEnv, j.Name, key)
		}
	}
	if strings.TrimSpace(j.Schedule) != "" {
		if err := validateScheduleShape(j.Schedule); err != nil {
			return fmt.Errorf("%w: job %q: %v", ErrInvalidJobSchedule, j.Name, err)
		}
	}
	return nil
}

// ParseContainerTarget inspects an argument string for the <server>:<container> format.
func ParseContainerTarget(arg string) (serverName, containerName string, isContainer bool) {
	trimmed := strings.TrimSpace(arg)
	if !strings.Contains(trimmed, ":") {
		return "", "", false
	}
	parts := strings.SplitN(trimmed, ":", 2)
	srv := strings.TrimSpace(parts[0])
	ctr := strings.TrimSpace(parts[1])
	if srv == "" || ctr == "" {
		return "", "", false
	}
	return srv, ctr, true
}

// ResolveAllPaths merges explicit Paths with sources resolved from referenced Assets.
func (j *Job) ResolveAllPaths(assetStore *asset.Store) ([]string, error) {
	seen := make(map[string]bool)
	var result []string

	for _, p := range j.Paths {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" && !seen[trimmed] {
			seen[trimmed] = true
			result = append(result, trimmed)
		}
	}

	if assetStore != nil && len(j.Assets) > 0 {
		for _, assetID := range j.Assets {
			a, err := assetStore.Get(assetID)
			if err != nil {
				return nil, fmt.Errorf("asset %q referenced by job %q not found: %w", assetID, j.Name, err)
			}
			src := strings.TrimSpace(a.Source)
			if src != "" && !seen[src] {
				seen[src] = true
				result = append(result, src)
			}
		}
	}

	return result, nil
}
