package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validLLMConfig() LLMConfig {
	return LLMConfig{
		DefaultModel: "deepseek",
		Models: map[string]LLMModelConfig{
			"deepseek": {
				Provider: "openai",
				BaseURL:  "https://api.deepseek.com/v1",
				APIKey:   "key",
				ID:       "deepseek-chat",
				Endpoint: "chat",
			},
			"claude": {
				Provider: "anthropic",
				BaseURL:  "https://api.anthropic.com",
				APIKey:   "key",
				ID:       "claude-sonnet-4-20250514",
				Endpoint: "messages",
			},
		},
	}
}

func TestLLMConfigValidateFullProfiles(t *testing.T) {
	cfg := validLLMConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}

	resolved, err := cfg.ResolveModel("claude")
	if err != nil {
		t.Fatalf("ResolveModel returned error: %v", err)
	}
	if resolved.Name != "claude" || resolved.Provider != "anthropic" || resolved.ID == "" {
		t.Fatalf("resolved model = %#v", resolved)
	}
}

func TestLLMConfigDefaultsMissingEndpointToChat(t *testing.T) {
	cfg := validLLMConfig()
	model := cfg.Models["deepseek"]
	model.Endpoint = ""
	cfg.Models["deepseek"] = model

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	resolved, err := cfg.ResolveModel("deepseek")
	if err != nil {
		t.Fatalf("ResolveModel returned error: %v", err)
	}
	if resolved.Endpoint != "chat" {
		t.Fatalf("resolved endpoint = %q, want chat", resolved.Endpoint)
	}
}

func TestLLMConfigValidateMissingRequiredField(t *testing.T) {
	cfg := validLLMConfig()
	model := cfg.Models["deepseek"]
	model.APIKey = ""
	cfg.Models["deepseek"] = model

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("Validate error = %v, want api_key error", err)
	}
}

func TestLoadDefaultsMissingEndpointToChat(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, err := ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath returned error: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	data := []byte(`llm:
  default_model: "mimo-v2.5"
  models:
    mimo-v2.5:
      provider: "openai"
      base_url: "https://api.example.com/v1"
      api_key: "key"
      id: "mimo-v2.5"
`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if err := cfg.LLM.Validate(); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	resolved, err := cfg.LLM.ResolveModel("mimo-v2.5")
	if err != nil {
		t.Fatalf("ResolveModel returned error: %v", err)
	}
	if resolved.Endpoint != "chat" {
		t.Fatalf("resolved endpoint = %q, want chat", resolved.Endpoint)
	}
}

func TestLLMConfigValidateUnknownDefault(t *testing.T) {
	cfg := validLLMConfig()
	cfg.DefaultModel = "missing"

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "default_model") {
		t.Fatalf("Validate error = %v, want default_model error", err)
	}
}

func TestLoadRejectsLegacyTopLevelLLMFields(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, err := ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath returned error: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	data := []byte(`llm:
  provider: "openai"
  default_model: "deepseek"
  models:
    deepseek:
      provider: "openai"
      base_url: "https://api.deepseek.com/v1"
      api_key: "key"
      id: "deepseek-chat"
      endpoint: "chat"
`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	_, err = Load()
	if err == nil || !strings.Contains(err.Error(), "llm.provider has been removed") {
		t.Fatalf("Load error = %v, want legacy field error", err)
	}
}
