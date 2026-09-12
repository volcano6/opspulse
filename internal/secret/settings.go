package secret

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/volcano6/opspulse/internal/config"
	"gopkg.in/yaml.v3"
)

// SettingsFileName is the file inside the OpsPulse config directory that
// remembers the user's 1Password defaults between runs.
const SettingsFileName = "onepassword.yaml"

// Settings holds the 1Password defaults that OpsPulse remembers.
//
// The point is to avoid forcing "--vault X --account Y" onto every invocation:
// most users have exactly one sensible target, and typing it forever is noise.
// Both fields are optional and are overridden by the matching CLI flag and, for
// the account, by the OP_ACCOUNT environment variable.
type Settings struct {
	Vault   string `yaml:"vault,omitempty"`
	Account string `yaml:"account,omitempty"`
}

// IsZero reports whether nothing has been remembered.
func (s Settings) IsZero() bool {
	return strings.TrimSpace(s.Vault) == "" && strings.TrimSpace(s.Account) == ""
}

// SettingsPath returns the default location of the 1Password settings file.
func SettingsPath() string {
	return filepath.Join(config.Dir(), SettingsFileName)
}

// LoadSettings reads the remembered defaults, returning an empty Settings when
// the file does not exist. A malformed file is reported as an error so the user
// can fix it rather than silently losing their configuration.
func LoadSettings() (Settings, error) {
	return LoadSettingsFrom(SettingsPath())
}

// LoadSettingsFrom reads settings from an explicit path. It exists so tests can
// exercise the parsing without touching the real config directory.
func LoadSettingsFrom(path string) (Settings, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is derived from the OpsPulse config dir
	if errors.Is(err, os.ErrNotExist) {
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, fmt.Errorf("read %s: %w", path, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return Settings{}, nil
	}

	var s Settings
	if err := yaml.Unmarshal(data, &s); err != nil {
		return Settings{}, fmt.Errorf("parse %s: %w", path, err)
	}
	s.Vault = strings.TrimSpace(s.Vault)
	s.Account = strings.TrimSpace(s.Account)
	return s, nil
}

// Save writes the settings to the default location, removing the file entirely
// when both fields are empty so a cleared configuration leaves no residue.
func (s Settings) Save() error {
	return s.SaveTo(SettingsPath())
}

// SaveTo writes the settings to an explicit path.
func (s Settings) SaveTo(path string) error {
	s.Vault = strings.TrimSpace(s.Vault)
	s.Account = strings.TrimSpace(s.Account)

	if s.IsZero() {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", path, err)
		}
		return nil
	}

	data, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Errorf("encode 1Password settings: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	tmpFile, err := os.CreateTemp(dir, filepath.Base(path)+".tmp.*")
	if err != nil {
		return fmt.Errorf("create temporary settings file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
	}()

	if _, err := tmpFile.Write(data); err != nil {
		return fmt.Errorf("write temporary settings file: %w", err)
	}
	if err := tmpFile.Chmod(0o600); err != nil {
		return fmt.Errorf("secure temporary settings file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temporary settings file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
