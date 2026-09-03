// Package backup manages backup job definitions, restic execution, and snapshot lifecycle.
package backup

import (
	"errors"
	"strings"
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
	// ErrJobNotFound is returned when a requested backup job does not exist.
	ErrJobNotFound = errors.New("backup job not found")
)

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
	Paths       []string          `yaml:"paths" json:"paths"`
	Backend     string            `yaml:"backend" json:"backend"`
	Env         map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	Retention   *RetentionPolicy  `yaml:"retention,omitempty" json:"retention,omitempty"`
	Excludes    []string          `yaml:"excludes,omitempty" json:"excludes,omitempty"`
	Tags        []string          `yaml:"tags,omitempty" json:"tags,omitempty"`
	Description string            `yaml:"description,omitempty" json:"description,omitempty"`
	Schedule    string            `yaml:"schedule,omitempty" json:"schedule,omitempty"` // Cron expression (e.g. "0 2 * * *", "@daily")
}

// Validate checks that the backup job contains all required fields and valid data.
func (j *Job) Validate() error {
	if strings.TrimSpace(j.Name) == "" {
		return ErrInvalidJobName
	}
	if strings.TrimSpace(j.Server) == "" {
		return ErrInvalidJobServer
	}
	if len(j.Paths) == 0 {
		return ErrInvalidJobPaths
	}
	for _, p := range j.Paths {
		if strings.TrimSpace(p) == "" {
			return ErrInvalidJobPaths
		}
	}
	if strings.TrimSpace(j.Backend) == "" {
		return ErrInvalidJobBackend
	}
	return nil
}
