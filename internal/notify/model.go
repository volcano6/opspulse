// Package notify provides event notification dispatching to external webhook channels.
package notify

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"

	"github.com/volcano6/opspulse/internal/config"
	"github.com/volcano6/opspulse/internal/filelock"
	"github.com/volcano6/opspulse/internal/yamlstore"
)

// Trigger condition constants.
const (
	TriggerFailure = "failure"
	TriggerSuccess = "success"
	TriggerAlways  = "always"
)

// Channel type constants.
const (
	TypeWebhook = "webhook"
)

var (
	// ErrInvalidChannelName is returned when a notification channel name is empty or invalid.
	ErrInvalidChannelName = errors.New("channel name cannot be empty")
	// ErrInvalidChannelType is returned when an unsupported channel type is specified.
	ErrInvalidChannelType = errors.New("unsupported channel type (supported: 'webhook')")
	// ErrInvalidChannelURL is returned when the channel URL is invalid.
	ErrInvalidChannelURL = errors.New("channel URL must be a valid HTTP or HTTPS URL")
	// ErrInvalidTrigger is returned when the trigger condition is not valid.
	ErrInvalidTrigger = errors.New("invalid trigger condition (must be 'failure', 'success', or 'always')")
	// ErrChannelNotFound is returned when a requested channel does not exist.
	ErrChannelNotFound = errors.New("notification channel not found")
)

// Channel defines a notification delivery destination.
type Channel struct {
	Name string `yaml:"name" json:"name"`
	Type string `yaml:"type" json:"type"`
	URL  string `yaml:"url" json:"url"`
	On   string `yaml:"on,omitempty" json:"on,omitempty"` // "failure", "success", "always" (default: "failure")
}

// EffectiveTrigger returns the normalized trigger condition, defaulting to "failure".
func (c *Channel) EffectiveTrigger() string {
	val := strings.TrimSpace(strings.ToLower(c.On))
	if val == "" {
		return TriggerFailure
	}
	return val
}

// Validate checks that the channel definition contains all required fields and valid data.
func (c *Channel) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return ErrInvalidChannelName
	}
	if strings.TrimSpace(strings.ToLower(c.Type)) != TypeWebhook {
		return ErrInvalidChannelType
	}
	rawURL := strings.TrimSpace(c.URL)
	if rawURL == "" {
		return ErrInvalidChannelURL
	}
	parsedURL, err := url.ParseRequestURI(rawURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		return ErrInvalidChannelURL
	}
	trigger := c.EffectiveTrigger()
	if trigger != TriggerFailure && trigger != TriggerSuccess && trigger != TriggerAlways {
		return ErrInvalidTrigger
	}
	return nil
}

// Config represents the top-level notifications.yaml configuration.
type Config struct {
	Channels []Channel `yaml:"channels" json:"channels"`
}

var notifySchema = yamlstore.Schema[Config]{
	Kind: "notifications",
	NewEmpty: func() *Config {
		return &Config{Channels: []Channel{}}
	},
	Validate: func(cfg *Config) error {
		seen := make(map[string]struct{}, len(cfg.Channels))
		for i := range cfg.Channels {
			if err := cfg.Channels[i].Validate(); err != nil {
				return fmt.Errorf("invalid channel entry %d: %w", i+1, err)
			}
			if _, exists := seen[cfg.Channels[i].Name]; exists {
				return fmt.Errorf("duplicate channel name %q", cfg.Channels[i].Name)
			}
			seen[cfg.Channels[i].Name] = struct{}{}
		}
		return nil
	},
}

// Store handles thread-safe persistence and retrieval of notification channels in notifications.yaml.
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

// NewDefaultStore creates a Store pointing to $XDG_CONFIG_HOME/opspulse/notifications.yaml.
func NewDefaultStore() *Store {
	return NewStore(filepath.Join(config.Dir(), "notifications.yaml"))
}

// FilePath returns the configured file path of the notifications storage.
func (s *Store) FilePath() string {
	return s.filePath
}

// List returns all configured notification channels.
func (s *Store) List() ([]Channel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cfg, err := s.readConfig()
	if err != nil {
		return nil, err
	}

	return cfg.Channels, nil
}

// Get finds a specific channel by its name.
func (s *Store) Get(name string) (*Channel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cfg, err := s.readConfig()
	if err != nil {
		return nil, err
	}

	for _, c := range cfg.Channels {
		if c.Name == name {
			channelCopy := c
			return &channelCopy, nil
		}
	}

	return nil, fmt.Errorf("%w: %s", ErrChannelNotFound, name)
}

// Save validates and persists a channel (create or update).
func (s *Store) Save(channel Channel) error {
	if err := channel.Validate(); err != nil {
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
	for i, existing := range cfg.Channels {
		if existing.Name == channel.Name {
			cfg.Channels[i] = channel
			updated = true
			break
		}
	}

	if !updated {
		cfg.Channels = append(cfg.Channels, channel)
	}

	return s.writeConfig(cfg)
}

// Delete removes a channel by name.
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
	for i, existing := range cfg.Channels {
		if existing.Name == name {
			index = i
			break
		}
	}

	if index == -1 {
		return fmt.Errorf("%w: %s", ErrChannelNotFound, name)
	}

	cfg.Channels = append(cfg.Channels[:index], cfg.Channels[index+1:]...)
	return s.writeConfig(cfg)
}

func (s *Store) readConfig() (*Config, error) {
	return yamlstore.Read(s.filePath, notifySchema)
}

func (s *Store) writeConfig(cfg *Config) error {
	return yamlstore.Write(s.filePath, notifySchema, cfg)
}
