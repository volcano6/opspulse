package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/volcano6/opspulse/internal/config"
	"gopkg.in/yaml.v3"
)

type backupConfig struct {
	Backups []Job `yaml:"backups"`
}

// Store handles thread-safe persistence and retrieval of backup job configurations in backups.yaml.
type Store struct {
	filePath string
	mu       sync.RWMutex
}

// NewStore creates a new Store pointing to the given YAML file path.
func NewStore(filePath string) *Store {
	return &Store{
		filePath: filePath,
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
	if _, err := os.Stat(s.filePath); os.IsNotExist(err) {
		return &backupConfig{Backups: []Job{}}, nil
	}

	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read backup config from %q: %w", s.filePath, err)
	}

	if len(data) == 0 {
		return &backupConfig{Backups: []Job{}}, nil
	}

	var cfg backupConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse YAML backup config: %w", err)
	}

	return &cfg, nil
}

func (s *Store) writeConfig(cfg *backupConfig) error {
	dir := filepath.Dir(s.filePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create config directory %q: %w", dir, err)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal backup config to YAML: %w", err)
	}

	tmpFile := fmt.Sprintf("%s.tmp.%d", s.filePath, os.Getpid())
	if err := os.WriteFile(tmpFile, data, 0o600); err != nil {
		return fmt.Errorf("failed to write temporary backup config file %q: %w", tmpFile, err)
	}

	if err := os.Rename(tmpFile, s.filePath); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("failed to replace backup config file %q: %w", s.filePath, err)
	}

	return nil
}
