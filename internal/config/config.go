package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	Provider string `yaml:"provider"` // "openai" or "anthropic"
	BaseURL  string `yaml:"base_url"` // API base URL
	APIKey   string `yaml:"api_key"`  // API key
	ID       string `yaml:"id"`       // Provider model identifier
	Endpoint string `yaml:"endpoint"` // Provider endpoint mode
}

// ResolvedModel is an effective LLM profile selected for a user.
type ResolvedModel struct {
	Name     string
	Provider string
	BaseURL  string
	APIKey   string
	ID       string
	Endpoint string
}

// Config is the top-level configuration.
type Config struct {
	LLM       LLMConfig            `yaml:"llm"`
	Platforms map[string]yaml.Node `yaml:"platforms,omitempty"`
}

const DefaultSystemPrompt = "You are a helpful assistant."
const DefaultModelEndpoint = "chat"
const DefaultAnthropicEndpoint = "messages"

var (
	// ErrConfigNotFound is returned when ~/.lingobridge/config.yaml does not exist.
	ErrConfigNotFound = errors.New("config file not found")
	// ErrModelExists is returned when adding a duplicate model profile.
	ErrModelExists = errors.New("model profile already exists")
)

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

	return cfg, nil
}

// Save writes the config to disk, creating directories as needed.
func Save(cfg Config) error {
	cfg.LLM.ApplyDefaults()
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

// ApplyDefaults fills optional LLM profile fields.
func (c *LLMConfig) ApplyDefaults() {
	if c.Models == nil {
		c.Models = map[string]LLMModelConfig{}
	}
	for name, model := range c.Models {
		if model.Endpoint == "" {
			model.Endpoint = DefaultEndpointForProvider(model.Provider)
			c.Models[name] = model
		}
	}
}

// DefaultEndpointForProvider returns the endpoint default for a provider.
func DefaultEndpointForProvider(provider string) string {
	if provider == "anthropic" {
		return DefaultAnthropicEndpoint
	}
	return DefaultModelEndpoint
}

// Validate checks that the configured model profiles are complete and usable.
func (c LLMConfig) Validate() error {
	if c.DefaultModel == "" {
		return fmt.Errorf("llm.default_model is required")
	}
	if len(c.Models) == 0 {
		return fmt.Errorf("llm.models must define at least one model profile")
	}
	if _, ok := c.Models[c.DefaultModel]; !ok {
		return fmt.Errorf("llm.default_model %q is not defined in llm.models", c.DefaultModel)
	}
	for name, model := range c.Models {
		if name == "" {
			return fmt.Errorf("llm.models contains an empty profile name")
		}
		if err := validateModelProfile(name, model); err != nil {
			return err
		}
	}
	return nil
}

func validateModelProfile(name string, model LLMModelConfig) error {
	endpoint := model.Endpoint
	if endpoint == "" {
		endpoint = DefaultEndpointForProvider(model.Provider)
	}
	if model.Provider == "" {
		return fmt.Errorf("llm.models.%s.provider is required", name)
	}
	if model.Provider != "openai" && model.Provider != "anthropic" {
		return fmt.Errorf("llm.models.%s.provider must be openai or anthropic", name)
	}
	if model.BaseURL == "" {
		return fmt.Errorf("llm.models.%s.base_url is required", name)
	}
	if model.APIKey == "" {
		return fmt.Errorf("llm.models.%s.api_key is required", name)
	}
	if model.ID == "" {
		return fmt.Errorf("llm.models.%s.id is required", name)
	}
	switch model.Provider {
	case "openai":
		if endpoint != "chat" && endpoint != "responses" {
			return fmt.Errorf("llm.models.%s.endpoint must be chat or responses for openai", name)
		}
	case "anthropic":
		if endpoint != "messages" {
			return fmt.Errorf("llm.models.%s.endpoint must be messages for anthropic", name)
		}
	}
	return nil
}

// AddModel adds a named model profile to the config.
func AddModel(cfg *Config, name string, model LLMModelConfig, makeDefault bool) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("model profile name is required")
	}
	if cfg.LLM.Models == nil {
		cfg.LLM.Models = map[string]LLMModelConfig{}
	}
	if _, ok := cfg.LLM.Models[name]; ok {
		return fmt.Errorf("%w: %s", ErrModelExists, name)
	}
	model.Provider = strings.TrimSpace(model.Provider)
	model.BaseURL = strings.TrimRight(strings.TrimSpace(model.BaseURL), "/")
	model.APIKey = strings.TrimSpace(model.APIKey)
	model.ID = strings.TrimSpace(model.ID)
	model.Endpoint = strings.TrimSpace(model.Endpoint)
	if model.Endpoint == "" {
		model.Endpoint = DefaultEndpointForProvider(model.Provider)
	}
	if err := validateModelProfile(name, model); err != nil {
		return err
	}
	cfg.LLM.Models[name] = model
	if cfg.LLM.DefaultModel == "" || makeDefault {
		cfg.LLM.DefaultModel = name
	}
	if cfg.LLM.SystemPrompt == "" {
		cfg.LLM.SystemPrompt = DefaultSystemPrompt
	}
	if cfg.LLM.MaxHistory < 0 {
		cfg.LLM.MaxHistory = 0
	}
	return nil
}

// ModelNames returns sorted model profile names.
func (c LLMConfig) ModelNames() []string {
	names := make([]string, 0, len(c.Models))
	for name := range c.Models {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// HasModel reports whether a named model profile exists.
func (c LLMConfig) HasModel(name string) bool {
	_, ok := c.Models[name]
	return ok
}

// ResolveModel returns a complete model profile, falling back to default for empty or unknown names.
func (c LLMConfig) ResolveModel(name string) (ResolvedModel, error) {
	if name == "" || !c.HasModel(name) {
		name = c.DefaultModel
	}
	model, ok := c.Models[name]
	if !ok {
		return ResolvedModel{}, fmt.Errorf("llm model profile %q is not defined", name)
	}
	endpoint := model.Endpoint
	if endpoint == "" {
		endpoint = DefaultEndpointForProvider(model.Provider)
	}
	return ResolvedModel{
		Name:     name,
		Provider: model.Provider,
		BaseURL:  model.BaseURL,
		APIKey:   model.APIKey,
		ID:       model.ID,
		Endpoint: endpoint,
	}, nil
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
