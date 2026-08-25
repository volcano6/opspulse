package server

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/volcano6/opspulse/internal/config"
	"gopkg.in/yaml.v3"
)

// Store handles persistence of server configurations to a YAML file.
type Store struct {
	filePath string
	mu       sync.RWMutex
}

// NewStore creates a new Store instance with a custom file path.
func NewStore(filePath string) *Store {
	return &Store{
		filePath: filePath,
	}
}

// NewDefaultStore creates a Store using the default servers.yaml path in the config directory.
func NewDefaultStore() *Store {
	path := filepath.Join(config.Dir(), "servers.yaml")
	return NewStore(path)
}

// FilePath returns the file path used by this store.
func (s *Store) FilePath() string {
	return s.filePath
}

// List returns all configured servers.
func (s *Store) List() ([]Server, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cf, err := s.read()
	if err != nil {
		return nil, err
	}
	return cf.Servers, nil
}

// Get retrieves a server by its unique name.
func (s *Store) Get(name string) (*Server, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cf, err := s.read()
	if err != nil {
		return nil, err
	}
	for i := range cf.Servers {
		if cf.Servers[i].Name == name {
			return &cf.Servers[i], nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrServerNotFound, name)
}

// Save adds a new server or updates an existing one by name.
func (s *Store) Save(srv Server) error {
	if err := srv.Validate(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	cf, err := s.read()
	if err != nil {
		return err
	}

	found := false
	for i := range cf.Servers {
		if cf.Servers[i].Name == srv.Name {
			cf.Servers[i] = srv
			found = true
			break
		}
	}
	if !found {
		cf.Servers = append(cf.Servers, srv)
	}

	return s.write(cf)
}

// Delete removes a server by its name.
func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cf, err := s.read()
	if err != nil {
		return err
	}

	found := false
	filtered := make([]Server, 0, len(cf.Servers))
	for _, srv := range cf.Servers {
		if srv.Name == name {
			found = true
			continue
		}
		filtered = append(filtered, srv)
	}

	if !found {
		return fmt.Errorf("%w: %s", ErrServerNotFound, name)
	}

	cf.Servers = filtered
	return s.write(cf)
}

// Validate checks a complete inventory document without writing it.
func (s *Store) Validate(data []byte) error {
	_, err := parseAndValidateConfig(data)
	return err
}

// Replace validates and replaces the complete inventory while preserving the
// caller-provided YAML formatting and comments.
func (s *Store) Replace(data []byte) error {
	if _, err := parseAndValidateConfig(data); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Dir(s.filePath)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	tmpFile := fmt.Sprintf("%s.tmp.%d", s.filePath, os.Getpid())
	if err := os.WriteFile(tmpFile, data, 0o600); err != nil {
		return fmt.Errorf("failed to write temporary servers file: %w", err)
	}

	if err := os.Rename(tmpFile, s.filePath); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("failed to replace servers file: %w", err)
	}
	return nil
}

func parseAndValidateConfig(data []byte) (*ConfigFile, error) {
	var cf ConfigFile
	if err := yaml.Unmarshal(data, &cf); err != nil {
		return nil, fmt.Errorf("failed to parse servers YAML: %w", err)
	}
	seen := make(map[string]struct{}, len(cf.Servers))
	for i := range cf.Servers {
		if err := cf.Servers[i].Validate(); err != nil {
			return nil, fmt.Errorf("invalid server entry %d: %w", i+1, err)
		}
		if _, exists := seen[cf.Servers[i].Name]; exists {
			return nil, fmt.Errorf("duplicate server name %q", cf.Servers[i].Name)
		}
		seen[cf.Servers[i].Name] = struct{}{}
	}
	return &cf, nil
}

func (s *Store) read() (*ConfigFile, error) {
	if _, err := os.Stat(s.filePath); os.IsNotExist(err) {
		return &ConfigFile{Servers: []Server{}}, nil
	}

	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read servers file: %w", err)
	}

	if len(data) == 0 {
		return &ConfigFile{Servers: []Server{}}, nil
	}

	var cf ConfigFile
	if err := yaml.Unmarshal(data, &cf); err != nil {
		return nil, fmt.Errorf("failed to parse servers YAML: %w", err)
	}

	return &cf, nil
}

func (s *Store) write(cf *ConfigFile) error {
	dir := filepath.Dir(s.filePath)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	data, err := yaml.Marshal(cf)
	if err != nil {
		return fmt.Errorf("failed to marshal servers YAML: %w", err)
	}

	tmpFile := fmt.Sprintf("%s.tmp.%d", s.filePath, os.Getpid())
	if err := os.WriteFile(tmpFile, data, 0o600); err != nil {
		return fmt.Errorf("failed to write temporary servers file: %w", err)
	}

	if err := os.Rename(tmpFile, s.filePath); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("failed to replace servers file: %w", err)
	}

	return nil
}
