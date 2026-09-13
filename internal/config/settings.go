// Package config reads a repo's .slopgate/ directory: settings.json,
// repo-id, and rules/*.md. It imports nothing else from this repo.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Dir is the name of the slopgate config directory inside a repo.
const Dir = ".slopgate"

// Settings holds the tunables read from .slopgate/settings.json.
type Settings struct {
	MaxConcurrentAgents int `json:"maxConcurrentAgents"`
}

// DefaultSettings returns the settings applied when settings.json is absent
// or omits a key.
func DefaultSettings() Settings {
	return Settings{MaxConcurrentAgents: 4}
}

// LoadSettings reads settings.json from slopgateDir (the .slopgate
// directory itself, not the repo root). A missing file is normal and
// yields DefaultSettings with a nil error. A present file that omits
// maxConcurrentAgents, or sets it to 0, also gets the default. A negative
// value is an error.
func LoadSettings(slopgateDir string) (Settings, error) {
	path := filepath.Join(slopgateDir, "settings.json")

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DefaultSettings(), nil
		}
		return Settings{}, fmt.Errorf("reading settings file %s: %w", path, err)
	}

	var raw struct {
		MaxConcurrentAgents *int `json:"maxConcurrentAgents"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Settings{}, fmt.Errorf("parsing settings file %s: %w", path, err)
	}

	settings := DefaultSettings()
	if raw.MaxConcurrentAgents != nil {
		switch {
		case *raw.MaxConcurrentAgents < 0:
			return Settings{}, fmt.Errorf("settings file %s: maxConcurrentAgents must not be negative, got %d", path, *raw.MaxConcurrentAgents)
		case *raw.MaxConcurrentAgents > 0:
			settings.MaxConcurrentAgents = *raw.MaxConcurrentAgents
		default:
			// Explicit zero: keep the default.
		}
	}

	return settings, nil
}
