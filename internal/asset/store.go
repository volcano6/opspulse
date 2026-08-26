package asset

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/volcano6/opspulse/internal/config"
	"gopkg.in/yaml.v3"
)

type assetConfig struct {
	Assets []Asset `yaml:"assets"`
}

// Store handles thread-safe persistence and retrieval of asset definitions in assets.yaml.
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

// NewDefaultStore creates a Store pointing to $XDG_CONFIG_HOME/opspulse/assets.yaml.
func NewDefaultStore() *Store {
	return NewStore(filepath.Join(config.Dir(), "assets.yaml"))
}

// FilePath returns the configured file path of the assets YAML storage.
func (s *Store) FilePath() string {
	return s.filePath
}

// List returns all configured assets from storage.
func (s *Store) List() ([]Asset, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cfg, err := s.readConfig()
	if err != nil {
		return nil, err
	}

	return cfg.Assets, nil
}

// Get finds a specific asset by its stable ID.
func (s *Store) Get(id string) (*Asset, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cfg, err := s.readConfig()
	if err != nil {
		return nil, err
	}

	for _, a := range cfg.Assets {
		if a.ID == id {
			assetCopy := a
			return &assetCopy, nil
		}
	}

	return nil, fmt.Errorf("%w: %s", ErrAssetNotFound, id)
}

// GetMultiple retrieves multiple assets by their IDs.
func (s *Store) GetMultiple(ids []string) ([]Asset, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cfg, err := s.readConfig()
	if err != nil {
		return nil, err
	}

	assetMap := make(map[string]Asset)
	for _, a := range cfg.Assets {
		assetMap[a.ID] = a
	}

	var results []Asset
	for _, id := range ids {
		a, ok := assetMap[id]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrAssetNotFound, id)
		}
		results = append(results, a)
	}

	return results, nil
}

// Save validates and persists an asset into storage (creates or updates).
func (s *Store) Save(a Asset) error {
	if err := a.Validate(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, err := s.readConfig()
	if err != nil {
		return err
	}

	updated := false
	for i, existing := range cfg.Assets {
		if existing.ID == a.ID {
			cfg.Assets[i] = a
			updated = true
			break
		}
	}

	if !updated {
		cfg.Assets = append(cfg.Assets, a)
	}

	return s.writeConfig(cfg)
}

// Delete removes an asset by its ID from storage.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, err := s.readConfig()
	if err != nil {
		return err
	}

	index := -1
	for i, existing := range cfg.Assets {
		if existing.ID == id {
			index = i
			break
		}
	}

	if index == -1 {
		return fmt.Errorf("%w: %s", ErrAssetNotFound, id)
	}

	cfg.Assets = append(cfg.Assets[:index], cfg.Assets[index+1:]...)
	return s.writeConfig(cfg)
}

func (s *Store) readConfig() (*assetConfig, error) {
	if _, err := os.Stat(s.filePath); os.IsNotExist(err) {
		return &assetConfig{Assets: []Asset{}}, nil
	}

	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read asset config from %q: %w", s.filePath, err)
	}

	if len(data) == 0 {
		return &assetConfig{Assets: []Asset{}}, nil
	}

	var cfg assetConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse YAML asset config: %w", err)
	}

	return &cfg, nil
}

func (s *Store) writeConfig(cfg *assetConfig) error {
	dir := filepath.Dir(s.filePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create config directory %q: %w", dir, err)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal asset config to YAML: %w", err)
	}

	tmpFile := fmt.Sprintf("%s.tmp.%d", s.filePath, os.Getpid())
	if err := os.WriteFile(tmpFile, data, 0o600); err != nil {
		return fmt.Errorf("failed to write temporary asset config file %q: %w", tmpFile, err)
	}

	if err := os.Rename(tmpFile, s.filePath); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("failed to replace asset config file %q: %w", s.filePath, err)
	}

	return nil
}
