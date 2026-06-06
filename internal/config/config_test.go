package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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

func TestPathsUseLingoBridgeHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	configDir, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir returned error: %v", err)
	}
	if configDir != filepath.Join(home, ".lingobridge") {
		t.Fatalf("ConfigDir = %q, want ~/.lingobridge", configDir)
	}

	socketPath, err := ControlSocketPath()
	if err != nil {
		t.Fatalf("ControlSocketPath returned error: %v", err)
	}
	if socketPath != filepath.Join(home, ".lingobridge", "lingobridge.sock") {
		t.Fatalf("ControlSocketPath = %q, want lingobridge socket", socketPath)
	}

	wechatDataDir, err := PlatformDataDir("wechat")
	if err != nil {
		t.Fatalf("PlatformDataDir returned error: %v", err)
	}
	if wechatDataDir != filepath.Join(home, ".lingobridge", "platforms", "wechat", "data") {
		t.Fatalf("PlatformDataDir = %q, want wechat platform data", wechatDataDir)
	}
	feishuDataDir, err := PlatformDataDir("feishu")
	if err != nil {
		t.Fatalf("PlatformDataDir feishu returned error: %v", err)
	}
	if feishuDataDir == wechatDataDir {
		t.Fatalf("platform data dirs should differ: %q", feishuDataDir)
	}
	for _, platformID := range []string{"", "we/chat", ".wechat", " wechat"} {
		if _, err := PlatformDataDir(platformID); err == nil {
			t.Fatalf("PlatformDataDir(%q) returned nil error, want invalid platform error", platformID)
		}
	}
}

func TestLoadMissingConfigReturnsErrConfigNotFound(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	_, err := Load()
	if !errors.Is(err, ErrConfigNotFound) {
		t.Fatalf("Load error = %v, want ErrConfigNotFound", err)
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

func TestLLMConfigValidateOpenAIEndpointGuidesResponses(t *testing.T) {
	cfg := validLLMConfig()
	model := cfg.Models["deepseek"]
	model.Endpoint = "response"
	cfg.Models["deepseek"] = model

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "use responses, not response") {
		t.Fatalf("Validate error = %v, want responses guidance", err)
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

func TestLoadDefaultsAnthropicEndpointToMessages(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, err := ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath returned error: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	data := []byte(`llm:
  default_model: "claude"
  models:
    claude:
      provider: "anthropic"
      base_url: "https://api.anthropic.com"
      api_key: "key"
      id: "claude"
`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	resolved, err := cfg.LLM.ResolveModel("claude")
	if err != nil {
		t.Fatalf("ResolveModel returned error: %v", err)
	}
	if resolved.Endpoint != "messages" {
		t.Fatalf("resolved endpoint = %q, want messages", resolved.Endpoint)
	}
}

func TestAddModelSetsFirstModelAsDefault(t *testing.T) {
	cfg := DefaultConfig()

	err := AddModel(&cfg, "gpt", LLMModelConfig{
		Provider: "openai",
		BaseURL:  "https://api.openai.com/v1",
		APIKey:   "key",
		ID:       "gpt-4o",
	}, false)
	if err != nil {
		t.Fatalf("AddModel returned error: %v", err)
	}
	if cfg.LLM.DefaultModel != "gpt" {
		t.Fatalf("default_model = %q, want gpt", cfg.LLM.DefaultModel)
	}
	if cfg.LLM.Models["gpt"].Endpoint != "chat" {
		t.Fatalf("endpoint = %q, want chat", cfg.LLM.Models["gpt"].Endpoint)
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

func TestLoadPreservesUnknownPlatformConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, err := ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath returned error: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	data := []byte(`llm:
  default_model: "deepseek"
  models:
    deepseek:
      provider: "openai"
      base_url: "https://api.deepseek.com/v1"
      api_key: "key"
      id: "deepseek-chat"
platforms:
  custom:
    nested:
      value: "kept"
`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	var custom struct {
		Nested struct {
			Value string `yaml:"value"`
		} `yaml:"nested"`
	}
	customNode := cfg.Platforms["custom"]
	if err := customNode.Decode(&custom); err != nil {
		t.Fatalf("Decode custom platform returned error: %v", err)
	}
	if custom.Nested.Value != "kept" {
		t.Fatalf("custom platform config = %#v", custom)
	}
}

func TestLoadRejectsInvalidPlatformConfigID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, err := ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath returned error: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	data := []byte(`llm:
  default_model: "deepseek"
  models:
    deepseek:
      provider: "openai"
      base_url: "https://api.deepseek.com/v1"
      api_key: "key"
      id: "deepseek-chat"
platforms:
  bad/platform: {}
`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	_, err = Load()
	if err == nil || !strings.Contains(err.Error(), "platform id") {
		t.Fatalf("Load error = %v, want invalid platform id error", err)
	}
}

func TestSaveRejectsInvalidPlatformConfigID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := DefaultConfig()
	cfg.Platforms = map[string]yaml.Node{
		"bad/platform": {Kind: yaml.MappingNode},
	}
	if err := AddModel(&cfg, "deepseek", LLMModelConfig{
		Provider: "openai",
		BaseURL:  "https://api.deepseek.com/v1",
		APIKey:   "key",
		ID:       "deepseek-chat",
	}, true); err != nil {
		t.Fatalf("AddModel returned error: %v", err)
	}
	err := Save(cfg)
	if err == nil || !strings.Contains(err.Error(), "platform id") {
		t.Fatalf("Save error = %v, want invalid platform id error", err)
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
