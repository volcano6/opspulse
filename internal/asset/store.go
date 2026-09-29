package asset

import (
	"fmt"
	"path/filepath"
	"sync"

	"github.com/volcano6/opspulse/internal/config"
	"github.com/volcano6/opspulse/internal/filelock"
	"github.com/volcano6/opspulse/internal/yamlstore"
)

type assetConfig struct {
	Assets []Asset `yaml:"assets"`
}

var assetSchema = yamlstore.Schema[assetConfig]{
	Kind: "asset",
	NewEmpty: func() *assetConfig {
		return &assetConfig{Assets: []Asset{}}
	},
	Validate: func(cfg *assetConfig) error {
		seen := make(map[string]struct{}, len(cfg.Assets))
		for i := range cfg.Assets {
			if err := cfg.Assets[i].Validate(); err != nil {
				return fmt.Errorf("invalid asset entry %d: %w", i+1, err)
			}
			if _, exists := seen[cfg.Assets[i].ID]; exists {
				return fmt.Errorf("duplicate asset id %q", cfg.Assets[i].ID)
			}
			seen[cfg.Assets[i].ID] = struct{}{}
		}
		return nil
	},
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
	return yamlstore.Read(s.filePath, assetSchema)
}

func (s *Store) writeConfig(cfg *assetConfig) error {
	return yamlstore.Write(s.filePath, assetSchema, cfg)
}
