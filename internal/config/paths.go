package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ConfigDir returns the config directory (~/.lingobridge).
func ConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(home, ".lingobridge"), nil
}

// ConfigPath returns the path to config.yaml.
func ConfigPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// ControlSocketPath returns the Unix socket path for the local control API.
func ControlSocketPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "lingobridge.sock"), nil
}

// PlatformDir returns the isolated directory for one platform.
func PlatformDir(platformID string) (string, error) {
	if err := ValidatePlatformID(platformID); err != nil {
		return "", err
	}
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "platforms", platformID), nil
}

// PlatformDataDir returns the isolated data directory for one platform.
func PlatformDataDir(platformID string) (string, error) {
	dir, err := PlatformDir(platformID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "data"), nil
}

// PlatformDBPath returns the SQLite database path for one platform.
func PlatformDBPath(platformID string) (string, error) {
	dir, err := PlatformDataDir(platformID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "lingobridge.db"), nil
}

// ValidatePlatformID accepts only registry-style platform identifiers.
func ValidatePlatformID(platformID string) error {
	if strings.TrimSpace(platformID) != platformID || platformID == "" {
		return fmt.Errorf("platform id %q is invalid", platformID)
	}
	for _, r := range platformID {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return fmt.Errorf("platform id %q is invalid", platformID)
	}
	return nil
}

// ValidatePlatformIDs checks that all platform config keys are safe registry identifiers.
func ValidatePlatformIDs(platforms map[string]yaml.Node) error {
	for platformID := range platforms {
		if err := ValidatePlatformID(platformID); err != nil {
			return err
		}
	}
	return nil
}

// EnsurePlatformDataDir creates a platform data directory if it doesn't exist.
func EnsurePlatformDataDir(platformID string) (string, error) {
	dir, err := PlatformDataDir(platformID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create platform data dir: %w", err)
	}
	return dir, nil
}
