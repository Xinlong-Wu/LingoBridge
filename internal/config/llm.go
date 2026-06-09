package config

import (
	"fmt"
	"sort"
	"strings"
)

// ApplyDefaults fills optional LLM profile fields.
func (c *LLMConfig) ApplyDefaults() {
	if c.Models == nil {
		c.Models = map[string]LLMModelConfig{}
	}
	for name, model := range c.Models {
		if model.Endpoint == "" {
			model.Endpoint = DefaultEndpointForProvider(model.Provider)
		}
		model.Compact = defaultCompactConfig(model.Compact)
		c.Models[name] = model
	}
}

// DefaultEndpointForProvider returns the endpoint default for a provider.
func DefaultEndpointForProvider(provider string) string {
	if provider == "anthropic" {
		return DefaultAnthropicEndpoint
	}
	return DefaultModelEndpoint
}

// EndpointChoicesForProvider returns valid endpoint modes for a provider.
func EndpointChoicesForProvider(provider string) []string {
	if provider == "anthropic" {
		return []string{DefaultAnthropicEndpoint}
	}
	return []string{DefaultModelEndpoint, "responses"}
}

// IsValidEndpointForProvider reports whether endpoint is valid for provider.
func IsValidEndpointForProvider(provider, endpoint string) bool {
	for _, choice := range EndpointChoicesForProvider(provider) {
		if endpoint == choice {
			return true
		}
	}
	return false
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
	compact := defaultCompactConfig(model.Compact)
	if model.ContextWindow < 0 {
		return fmt.Errorf("llm.models.%s.context_window must be >= 0", name)
	}
	if compact.Threshold <= 0 || compact.Threshold > 1 {
		return fmt.Errorf("llm.models.%s.compact.threshold must be > 0 and <= 1", name)
	}
	switch model.Provider {
	case "openai":
		if !IsValidEndpointForProvider(model.Provider, endpoint) {
			return fmt.Errorf("llm.models.%s.endpoint must be chat or responses for openai; use responses, not response", name)
		}
		if compact.Mode == CompactModeTrue && !SupportsNativeCompact(model.Provider, endpoint) {
			return fmt.Errorf("llm.models.%s.compact requires endpoint responses for openai", name)
		}
	case "anthropic":
		if !IsValidEndpointForProvider(model.Provider, endpoint) {
			return fmt.Errorf("llm.models.%s.endpoint must be messages for anthropic", name)
		}
		if compact.Mode == CompactModeTrue && !SupportsNativeCompact(model.Provider, endpoint) {
			return fmt.Errorf("llm.models.%s.compact requires endpoint messages for anthropic", name)
		}
	}
	if compact.Mode != CompactModeFalse && SupportsNativeCompact(model.Provider, endpoint) && model.ContextWindow <= 0 {
		return fmt.Errorf("llm.models.%s.context_window is required when compact is enabled or auto for this provider endpoint", name)
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
	model.Compact = defaultCompactConfig(model.Compact)
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
		Name:          name,
		Provider:      model.Provider,
		BaseURL:       model.BaseURL,
		APIKey:        model.APIKey,
		ID:            model.ID,
		Endpoint:      endpoint,
		ContextWindow: model.ContextWindow,
		Compact:       defaultCompactConfig(model.Compact),
	}, nil
}

func defaultCompactConfig(cfg LLMCompactConfig) LLMCompactConfig {
	if cfg.Mode == "" {
		cfg.Mode = CompactModeAuto
	}
	if !cfg.thresholdSet && cfg.Threshold == 0 {
		cfg.Threshold = DefaultCompactThreshold
	}
	return cfg
}

// SupportsNativeCompact reports whether a provider endpoint has native compact support.
func SupportsNativeCompact(provider, endpoint string) bool {
	if endpoint == "" {
		endpoint = DefaultEndpointForProvider(provider)
	}
	switch provider {
	case "openai":
		return endpoint == "responses"
	case "anthropic":
		return endpoint == DefaultAnthropicEndpoint
	default:
		return false
	}
}
