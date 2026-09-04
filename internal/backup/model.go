// Package backup manages backup job definitions, restic execution, and snapshot lifecycle.
package backup

import (
	"errors"
	"fmt"
	"strings"

	"github.com/volcano6/opspulse/internal/asset"
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
	Paths       []string          `yaml:"paths,omitempty" json:"paths,omitempty"`
	Assets      []string          `yaml:"assets,omitempty" json:"assets,omitempty"`
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
	if len(j.Paths) == 0 && len(j.Assets) == 0 {
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


