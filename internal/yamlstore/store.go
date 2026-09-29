// Package yamlstore provides a generic YAML-backed configuration store shared by
// the asset, backup, and notify packages.
package yamlstore

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Schema describes how a concrete configuration type maps onto a YAML file. It
// carries the hooks the generic Read/Write functions need to remain
// type-agnostic:
//
//   - Kind is the noun used in error strings (e.g. "asset", "backup").
//   - NewEmpty constructs an empty configuration for a missing or empty file.
//   - Validate performs per-entry validation and duplicate-key detection.
type Schema[T any] struct {
	Kind     string
	NewEmpty func() *T
	Validate func(cfg *T) error
}

// Read loads and validates a configuration from filePath. A missing or empty
// file yields the schema's empty configuration; otherwise the YAML document is
// decoded with strict field checking, rejected when it contains more than one
// document, and validated via the schema hook.
func Read[T any](filePath string, s Schema[T]) (*T, error) {
	return read[T](filePath, s)
}

func read[T any](filePath string, s Schema[T]) (*T, error) {
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return s.NewEmpty(), nil
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s config from %q: %w", s.Kind, filePath, err)
	}

	if len(data) == 0 {
		return s.NewEmpty(), nil
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var cfg T
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("failed to parse YAML %s config: %w", s.Kind, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("failed to parse YAML %s config: multiple documents are not supported", s.Kind)
		}
		return nil, fmt.Errorf("failed to parse YAML %s config: %w", s.Kind, err)
	}

	if err := s.Validate(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// Cache avoids repeated reads and parses while keeping caller-owned results
// isolated from the cached configuration. The clone function must deep-copy T.
type Cache[T any] struct {
	mu      sync.Mutex
	value   *T
	valid   bool
	exists  bool
	modTime time.Time
	size    int64
	clone   func(*T) *T
}

// NewCache creates a cache using clone to isolate cached values from callers.
func NewCache[T any](clone func(*T) *T) *Cache[T] {
	return &Cache[T]{clone: clone}
}

// Read returns a cached configuration when the file's existence, mtime, and
// size are unchanged; otherwise it reads, validates, and caches the file.
func (c *Cache[T]) Read(filePath string, s Schema[T]) (*T, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	info, err := os.Stat(filePath)
	if os.IsNotExist(err) {
		c.invalidate()
		return s.NewEmpty(), nil
	}
	if err != nil {
		c.invalidate()
		return nil, fmt.Errorf("failed to stat %s config from %q: %w", s.Kind, filePath, err)
	}
	if c.valid && c.exists && c.size == info.Size() && c.modTime.Equal(info.ModTime()) {
		return c.clone(c.value), nil
	}

	cfg, err := read(filePath, s)
	if err != nil {
		c.invalidate()
		return nil, err
	}
	c.value = c.clone(cfg)
	c.valid = true
	c.exists = true
	c.modTime = info.ModTime()
	c.size = info.Size()
	return c.clone(c.value), nil
}

// Write invalidates the cache before attempting an atomic write.
func (c *Cache[T]) Write(filePath string, s Schema[T], cfg *T) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.invalidate()
	return Write(filePath, s, cfg)
}

// Invalidate discards the cached file contents.
func (c *Cache[T]) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.invalidate()
}

func (c *Cache[T]) invalidate() {
	c.value = nil
	c.valid = false
	c.exists = false
	c.modTime = time.Time{}
	c.size = 0
}

// Write atomically persists cfg to filePath: the containing directory is
// created with 0o700, the config is marshalled to YAML, written to a temporary
// file with 0o600 permissions, and renamed over the target.
func Write[T any](filePath string, s Schema[T], cfg *T) error {
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create config directory %q: %w", dir, err)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal %s config to YAML: %w", s.Kind, err)
	}

	tmpFile, err := os.CreateTemp(dir, filepath.Base(filePath)+".tmp.*")
	if err != nil {
		return fmt.Errorf("failed to create temporary %s config file: %w", s.Kind, err)
	}
	tmpPath := tmpFile.Name()
	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
	}()

	if _, err := tmpFile.Write(data); err != nil {
		return fmt.Errorf("failed to write temporary %s config file %q: %w", s.Kind, tmpPath, err)
	}
	if err := tmpFile.Chmod(0o600); err != nil {
		return fmt.Errorf("failed to set permissions on %q: %w", tmpPath, err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temporary %s config file %q: %w", s.Kind, tmpPath, err)
	}

	if err := os.Rename(tmpPath, filePath); err != nil {
		return fmt.Errorf("failed to replace %s config file %q: %w", s.Kind, filePath, err)
	}

	return nil
}
