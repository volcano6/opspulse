// Package config handles configuration directories and paths.
package config

import (
	"os"
	"path/filepath"

	"github.com/adrg/xdg"
)

const (
	appName = "opspulse"
	// EnvHome is the environment variable for overriding the OpsPulse home directory.
	EnvHome = "OPSPULSE_HOME"
)

// Dir returns the configuration directory.
// Priority: OPSPULSE_HOME > ~/.opspulse (legacy) > $XDG_CONFIG_HOME/opspulse
func Dir() string {
	if env := os.Getenv(EnvHome); env != "" {
		return env
	}
	if legacy := legacyDir(); legacy != "" && dirExists(legacy) {
		return legacy
	}
	return filepath.Join(xdg.ConfigHome, appName)
}

// DataDir returns the data directory (SQLite, logs, etc.).
// Priority: OPSPULSE_HOME/data > ~/.opspulse/data (legacy) > $XDG_DATA_HOME/opspulse
func DataDir() string {
	if env := os.Getenv(EnvHome); env != "" {
		return filepath.Join(env, "data")
	}
	if legacy := legacyDir(); legacy != "" && dirExists(legacy) {
		return filepath.Join(legacy, "data")
	}
	return filepath.Join(xdg.DataHome, appName)
}

func legacyDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".opspulse")
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
