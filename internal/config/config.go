package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// LLMConfig holds the LLM API configuration.
type LLMConfig struct {
	Provider     string `yaml:"provider"`      // "openai" or "anthropic"
	BaseURL      string `yaml:"base_url"`      // API base URL
	APIKey       string `yaml:"api_key"`       // API key
	Model        string `yaml:"model"`         // Model name
	SystemPrompt string `yaml:"system_prompt"` // System prompt for the AI
	MaxHistory   int    `yaml:"max_history"`   // Max messages to include from history
	Endpoint     string `yaml:"endpoint"`      // "chat" (default) or "responses"
}

// Config is the top-level configuration.
type Config struct {
	LLM LLMConfig `yaml:"llm"`
}

const DefaultSystemPrompt = "You are a helpful assistant."

// DefaultConfig returns a config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		LLM: LLMConfig{
			Provider:     "openai",
			BaseURL:      "https://api.deepseek.com/v1",
			Model:        "deepseek-chat",
			SystemPrompt: DefaultSystemPrompt,
			MaxHistory:   0, // 0 = no limit
		},
	}
}

// ConfigDir returns the config directory (~/.wechatbox).
func ConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(home, ".wechatbox"), nil
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
	return filepath.Join(dir, "wechatbox.sock"), nil
}

// DataDir returns the data directory (~/.wechatbox/data).
func DataDir() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "data"), nil
}

// SessionsDir returns the sessions directory (~/.wechatbox/data/sessions).
func SessionsDir() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sessions"), nil
}

// Load reads and parses the config file, falling back to defaults if not found.
func Load() (Config, error) {
	cfg := DefaultConfig()

	path, err := ConfigPath()
	if err != nil {
		return cfg, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil // return defaults
		}
		return cfg, fmt.Errorf("read config: %w", err)
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}

	// Apply defaults for empty fields
	if cfg.LLM.SystemPrompt == "" {
		cfg.LLM.SystemPrompt = DefaultSystemPrompt
	}
	if cfg.LLM.MaxHistory < 0 {
		cfg.LLM.MaxHistory = 0
	}
	if cfg.LLM.Endpoint == "" {
		cfg.LLM.Endpoint = "chat"
	}

	return cfg, nil
}

// Save writes the config to disk, creating directories as needed.
func Save(cfg Config) error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	return nil
}

// EnsureDataDir creates the data directory if it doesn't exist.
func EnsureDataDir() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create data dir: %w", err)
	}
	return dir, nil
}

// EnsureSessionsDir creates the sessions directory if it doesn't exist.
func EnsureSessionsDir() (string, error) {
	dir, err := SessionsDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create sessions dir: %w", err)
	}
	return dir, nil
}
