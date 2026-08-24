// Package asset defines structured stateful infrastructure assets (Docker Compose, Volumes, Databases, Directories, Files).
package asset

import (
	"errors"
	"regexp"
	"strings"
)

// Type represents the classification of a stateful asset.
type Type string

const (
	// TypeDockerCompose represents a Docker Compose project directory.
	TypeDockerCompose Type = "docker_compose"
	// TypeVolume represents a Docker Named Volume or storage mount.
	TypeVolume Type = "volume"
	// TypeDatabase represents a database logical backup asset (e.g. MySQL, PostgreSQL).
	TypeDatabase Type = "database"
	// TypeDirectory represents a generic filesystem directory (e.g. Nginx sites, static files).
	TypeDirectory Type = "directory"
	// TypeFile represents an individual file or file group (e.g. SSL certificates).
	TypeFile Type = "file"
)

var (
	// ErrInvalidAssetID is returned when an asset ID is empty or contains invalid characters.
	ErrInvalidAssetID = errors.New("asset id must be non-empty and contain only letters, numbers, hyphens, and underscores")
	// ErrInvalidAssetType is returned when an asset type is unknown or unsupported.
	ErrInvalidAssetType = errors.New("unsupported asset type")
	// ErrInvalidAssetSource is returned when an asset source path is empty.
	ErrInvalidAssetSource = errors.New("asset source path cannot be empty")
	// ErrAssetNotFound is returned when a requested asset does not exist.
	ErrAssetNotFound = errors.New("asset not found")

	validIDRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
)

// Asset represents a structured stateful resource on a server with a stable ID.
type Asset struct {
	ID          string   `yaml:"id" json:"id"`
	Type        Type     `yaml:"type" json:"type"`
	Source      string   `yaml:"source" json:"source"`
	Engine      string   `yaml:"engine,omitempty" json:"engine,omitempty"`
	Container   string   `yaml:"container,omitempty" json:"container,omitempty"`
	Excludes    []string `yaml:"excludes,omitempty" json:"excludes,omitempty"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
}

// Validate checks that the asset fields meet the required constraints.
func (a *Asset) Validate() error {
	trimmedID := strings.TrimSpace(a.ID)
	if trimmedID == "" || !validIDRegex.MatchString(trimmedID) {
		return ErrInvalidAssetID
	}

	switch a.Type {
	case TypeDockerCompose, TypeVolume, TypeDatabase, TypeDirectory, TypeFile:
		// Valid types
	default:
		return ErrInvalidAssetType
	}

	if strings.TrimSpace(a.Source) == "" {
		return ErrInvalidAssetSource
	}

	return nil
}
