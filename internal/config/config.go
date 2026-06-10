package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// LLMConfig holds the LLM API configuration.
type LLMConfig struct {
	DefaultModel string                    `yaml:"default_model"` // Default model profile name
	Models       map[string]LLMModelConfig `yaml:"models"`        // Named model profiles
	SystemPrompt string                    `yaml:"system_prompt"` // System prompt for the AI
	MaxHistory   int                       `yaml:"max_history"`   // Max messages to include from history
}

// LLMModelConfig holds one complete LLM profile.
type LLMModelConfig struct {
	Provider      string           `yaml:"provider"`                 // "openai" or "anthropic"
	BaseURL       string           `yaml:"base_url"`                 // API base URL
	APIKey        string           `yaml:"api_key"`                  // API key
	ID            string           `yaml:"id"`                       // Provider model identifier
	Endpoint      string           `yaml:"endpoint"`                 // Provider endpoint mode
	ContextWindow int              `yaml:"context_window,omitempty"` // Model context window in tokens
	Compact       LLMCompactConfig `yaml:"compact,omitempty"`        // Provider-native context compaction
}

// LLMCompactConfig enables provider-native context compaction for one model.
type LLMCompactConfig struct {
	Mode         CompactMode `yaml:"mode,omitempty"`
	Threshold    float64     `yaml:"threshold,omitempty"`
	Instructions string      `yaml:"instructions,omitempty"`
	thresholdSet bool
}

// CompactMode controls whether provider-native compaction is enabled.
type CompactMode string

const (
	CompactModeAuto  CompactMode = "auto"
	CompactModeTrue  CompactMode = "true"
	CompactModeFalse CompactMode = "false"
)

// ResolvedModel is an effective LLM profile selected for a user.
type ResolvedModel struct {
	Name          string
	Provider      string
	BaseURL       string
	APIKey        string
	ID            string
	Endpoint      string
	ContextWindow int
	Compact       LLMCompactConfig
}

// Config is the top-level configuration.
type Config struct {
	LLM       LLMConfig            `yaml:"llm"`
	MCP       MCPConfig            `yaml:"mcp,omitempty"`
	Platforms map[string]yaml.Node `yaml:"platforms,omitempty"`
}

const DefaultSystemPrompt = "You are a helpful assistant."
const DefaultModelEndpoint = "chat"
const DefaultAnthropicEndpoint = "messages"
const DefaultCompactThreshold = 0.9

var (
	// ErrConfigNotFound is returned when ~/.lingobridge/config.yaml does not exist.
	ErrConfigNotFound = errors.New("config file not found")
	// ErrModelExists is returned when adding a duplicate model profile.
	ErrModelExists = errors.New("model profile already exists")
)

// UnmarshalYAML accepts compact.mode as YAML bool true/false or the string "auto".
func (m *CompactMode) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		switch value.Tag {
		case "!!bool":
			if strings.EqualFold(value.Value, "true") {
				*m = CompactModeTrue
				return nil
			}
			if strings.EqualFold(value.Value, "false") {
				*m = CompactModeFalse
				return nil
			}
		default:
			mode := CompactMode(strings.ToLower(strings.TrimSpace(value.Value)))
			switch mode {
			case "", CompactModeAuto, CompactModeTrue, CompactModeFalse:
				*m = mode
				return nil
			}
		}
	}
	return fmt.Errorf("compact.mode must be true, false, or auto")
}

// MarshalYAML emits true/false modes as YAML booleans and auto as a string.
func (m CompactMode) MarshalYAML() (any, error) {
	switch m {
	case CompactModeTrue:
		return true, nil
	case CompactModeFalse:
		return false, nil
	case "", CompactModeAuto:
		return string(CompactModeAuto), nil
	default:
		return nil, fmt.Errorf("compact.mode must be true, false, or auto")
	}
}

// ParseCompactMode parses a CLI/config string compact mode.
func ParseCompactMode(value string) (CompactMode, error) {
	mode := CompactMode(strings.ToLower(strings.TrimSpace(value)))
	switch mode {
	case CompactModeAuto, CompactModeTrue, CompactModeFalse:
		return mode, nil
	default:
		return "", fmt.Errorf("compact mode must be true, false, or auto")
	}
}

// UnmarshalYAML tracks whether threshold was explicitly configured.
func (c *LLMCompactConfig) UnmarshalYAML(value *yaml.Node) error {
	type rawCompact struct {
		Mode         CompactMode `yaml:"mode"`
		Threshold    *float64    `yaml:"threshold"`
		Instructions string      `yaml:"instructions"`
	}
	var raw rawCompact
	if err := value.Decode(&raw); err != nil {
		return err
	}
	c.Mode = raw.Mode
	c.Instructions = raw.Instructions
	c.thresholdSet = raw.Threshold != nil
	if raw.Threshold != nil {
		c.Threshold = *raw.Threshold
	}
	return nil
}

// DefaultConfig returns a config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		LLM: LLMConfig{
			Models:       map[string]LLMModelConfig{},
			SystemPrompt: DefaultSystemPrompt,
			MaxHistory:   0, // 0 = no limit
		},
	}
}

// Load reads and parses the config file.
func Load() (Config, error) {
	cfg := DefaultConfig()

	path, err := ConfigPath()
	if err != nil {
		return cfg, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, fmt.Errorf("%w: %s", ErrConfigNotFound, path)
		}
		return cfg, fmt.Errorf("read config: %w", err)
	}

	if err := rejectLegacyLLMFields(data); err != nil {
		return cfg, err
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
	if cfg.LLM.Models == nil {
		cfg.LLM.Models = map[string]LLMModelConfig{}
	}
	if cfg.Platforms == nil {
		cfg.Platforms = map[string]yaml.Node{}
	}
	if err := ValidatePlatformIDs(cfg.Platforms); err != nil {
		return cfg, err
	}
	cfg.LLM.ApplyDefaults()
	cfg.MCP.ApplyDefaults()
	if err := cfg.MCP.Validate(); err != nil {
		return cfg, err
	}

	return cfg, nil
}

// Save writes the config to disk, creating directories as needed.
func Save(cfg Config) error {
	cfg.LLM.ApplyDefaults()
	cfg.MCP.ApplyDefaults()
	if err := cfg.MCP.Validate(); err != nil {
		return err
	}
	if cfg.Platforms == nil {
		cfg.Platforms = map[string]yaml.Node{}
	}
	if err := ValidatePlatformIDs(cfg.Platforms); err != nil {
		return err
	}

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

// Digest returns a stable hash of the effective config.
func Digest(cfg Config) (string, error) {
	cfg.LLM.ApplyDefaults()
	cfg.MCP.ApplyDefaults()
	if err := cfg.MCP.Validate(); err != nil {
		return "", err
	}
	if cfg.Platforms == nil {
		cfg.Platforms = map[string]yaml.Node{}
	}
	if err := ValidatePlatformIDs(cfg.Platforms); err != nil {
		return "", err
	}
	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return "", fmt.Errorf("marshal config: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func rejectLegacyLLMFields(data []byte) error {
	for _, field := range []string{"provider", "base_url", "api_key", "model", "endpoint"} {
		found, err := hasLLMField(data, field)
		if err != nil {
			return fmt.Errorf("parse config: %w", err)
		}
		if found {
			return fmt.Errorf("llm.%s has been removed; define it under llm.models.<name>.%s", field, legacyFieldReplacement(field))
		}
	}
	return nil
}

func legacyFieldReplacement(field string) string {
	if field == "model" {
		return "id"
	}
	return field
}

func hasLLMField(data []byte, field string) (bool, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return false, err
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return false, nil
	}
	for i := 0; i+1 < len(root.Content[0].Content); i += 2 {
		key := root.Content[0].Content[i]
		value := root.Content[0].Content[i+1]
		if key.Value != "llm" || value.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(value.Content); j += 2 {
			if strings.EqualFold(value.Content[j].Value, field) {
				return true, nil
			}
		}
	}
	return false, nil
}
