package backup

import (
	"fmt"
	"maps"
	"path/filepath"
	"sync"

	"github.com/volcano6/opspulse/internal/config"
	"github.com/volcano6/opspulse/internal/filelock"
	"github.com/volcano6/opspulse/internal/yamlstore"
)

type backupConfig struct {
	Backups []Job `yaml:"backups"`
}

var backupSchema = yamlstore.Schema[backupConfig]{
	Kind: "backup",
	NewEmpty: func() *backupConfig {
		return &backupConfig{Backups: []Job{}}
	},
	Validate: func(cfg *backupConfig) error {
		seen := make(map[string]struct{}, len(cfg.Backups))
		for i := range cfg.Backups {
			if err := cfg.Backups[i].Validate(); err != nil {
				return fmt.Errorf("invalid backup entry %d: %w", i+1, err)
			}
			if _, exists := seen[cfg.Backups[i].Name]; exists {
				return fmt.Errorf("duplicate backup name %q", cfg.Backups[i].Name)
			}
			seen[cfg.Backups[i].Name] = struct{}{}
		}
		return nil
	},
}

func cloneBackupConfig(cfg *backupConfig) *backupConfig {
	clone := &backupConfig{Backups: make([]Job, len(cfg.Backups))}
	for i, job := range cfg.Backups {
		clone.Backups[i] = job
		clone.Backups[i].Paths = append([]string(nil), job.Paths...)
		clone.Backups[i].Assets = append([]string(nil), job.Assets...)
		clone.Backups[i].Excludes = append([]string(nil), job.Excludes...)
		clone.Backups[i].Tags = append([]string(nil), job.Tags...)
		if job.Env != nil {
			clone.Backups[i].Env = maps.Clone(job.Env)
		}
		if job.Remap != nil {
			clone.Backups[i].Remap = maps.Clone(job.Remap)
		}
		if job.Retention != nil {
			retention := *job.Retention
			retention.KeepTags = append([]string(nil), job.Retention.KeepTags...)
			clone.Backups[i].Retention = &retention
		}
	}
	return clone
}

type Store struct {
	filePath string
	mu       sync.RWMutex
	cache    *yamlstore.Cache[backupConfig]
}

func NewStore(filePath string) *Store {
	return &Store{
		filePath: filePath,
		cache:    yamlstore.NewCache(cloneBackupConfig),
	}
}

// NewDefaultStore creates a Store pointing to the default backups.yaml location under config.Dir().
func NewDefaultStore() *Store {
	return NewStore(filepath.Join(config.Dir(), "backups.yaml"))
}

// FilePath returns the configured file path of the backups YAML storage.
func (s *Store) FilePath() string {
	return s.filePath
}

// List returns all configured backup jobs from storage.
func (s *Store) List() ([]Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cfg, err := s.readConfig()
	if err != nil {
		return nil, err
	}

	return cfg.Backups, nil
}

// Get finds a specific backup job by name.
func (s *Store) Get(name string) (*Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cfg, err := s.readConfig()
	if err != nil {
		return nil, err
	}

	for _, j := range cfg.Backups {
		if j.Name == name {
			jobCopy := j
			return &jobCopy, nil
		}
	}

	return nil, fmt.Errorf("%w: %s", ErrJobNotFound, name)
}

// Save validates and persists a backup job into storage (creates or updates).
func (s *Store) Save(job Job) error {
	if err := job.Validate(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	unlock, err := filelock.Lock(s.filePath)
	if err != nil {
		return fmt.Errorf("failed to acquire file lock: %w", err)
	}
	defer unlock()

	cfg, err := s.readConfig()
	if err != nil {
		return err
	}

	updated := false
	for i, existing := range cfg.Backups {
		if existing.Name == job.Name {
			cfg.Backups[i] = job
			updated = true
			break
		}
	}

	if !updated {
		cfg.Backups = append(cfg.Backups, job)
	}

	return s.writeConfig(cfg)
}

// Delete removes a backup job by name from storage.
func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	unlock, err := filelock.Lock(s.filePath)
	if err != nil {
		return fmt.Errorf("failed to acquire file lock: %w", err)
	}
	defer unlock()

	cfg, err := s.readConfig()
	if err != nil {
		return err
	}

	index := -1
	for i, existing := range cfg.Backups {
		if existing.Name == name {
			index = i
			break
		}
	}

	if index == -1 {
		return fmt.Errorf("%w: %s", ErrJobNotFound, name)
	}

	cfg.Backups = append(cfg.Backups[:index], cfg.Backups[index+1:]...)
	return s.writeConfig(cfg)
}

func (s *Store) readConfig() (*backupConfig, error) {
	return s.cache.Read(s.filePath, backupSchema)
}

func (s *Store) writeConfig(cfg *backupConfig) error {
	return s.cache.Write(s.filePath, backupSchema, cfg)
}
