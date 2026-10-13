// Package config resolves where pinner keeps its files.
package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// EnvConfigDir overrides the config directory. Tests use it so they never touch a real user's pins.
const EnvConfigDir = "PINNER_CONFIG_DIR"

// Dir returns the pinner config directory, creating it with owner-only permissions.
func Dir() (string, error) {
	dir := os.Getenv(EnvConfigDir)
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("error getting home directory: %w", err)
		}
		dir = filepath.Join(home, ".config", "pinner")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("error creating config directory: %w", err)
	}
	// MkdirAll doesn't change the mode of a directory that already exists.
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("error securing config directory: %w", err)
	}
	return dir, nil
}

// DBPath returns the path of the local pins database.
func DBPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "commands.db"), nil
}
