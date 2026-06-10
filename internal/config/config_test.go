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
				Provider:      "anthropic",
				BaseURL:       "https://api.anthropic.com",
				APIKey:        "key",
				ID:            "claude-sonnet-4-20250514",
				Endpoint:      "messages",
				ContextWindow: 200000,
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

func TestLLMConfigDefaultsCompactModeAndThreshold(t *testing.T) {
	cfg := validLLMConfig()
	model := cfg.Models["deepseek"]
	model.Endpoint = "responses"
	model.ContextWindow = 128000
	cfg.Models["deepseek"] = model

	resolved, err := cfg.ResolveModel("deepseek")
	if err != nil {
		t.Fatalf("ResolveModel returned error: %v", err)
	}
	if resolved.Compact.Mode != CompactModeAuto {
		t.Fatalf("compact mode = %q, want auto", resolved.Compact.Mode)
	}
	if resolved.Compact.Threshold != DefaultCompactThreshold {
		t.Fatalf("compact threshold = %v, want %v", resolved.Compact.Threshold, DefaultCompactThreshold)
	}
	if resolved.Compact.Instructions != "" {
		t.Fatalf("compact instructions = %q, want empty", resolved.Compact.Instructions)
	}
}

func TestLoadParsesCompactModes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, err := ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath returned error: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	data := []byte(`llm:
  default_model: "gpt"
  models:
    gpt:
      provider: "openai"
      base_url: "https://api.openai.com/v1"
      api_key: "key"
      id: "gpt-4o"
      endpoint: "responses"
      context_window: 128000
      compact:
        mode: true
        threshold: 0.75
    claude:
      provider: "anthropic"
      base_url: "https://api.anthropic.com"
      api_key: "key"
      id: "claude"
      context_window: 200000
      compact:
        mode: auto
    chat:
      provider: "openai"
      base_url: "https://api.example.com/v1"
      api_key: "key"
      id: "chat"
      compact:
        mode: false
`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := cfg.LLM.Models["gpt"].Compact.Mode; got != CompactModeTrue {
		t.Fatalf("gpt compact mode = %q, want true", got)
	}
	if got := cfg.LLM.Models["gpt"].Compact.Threshold; got != 0.75 {
		t.Fatalf("gpt compact threshold = %v, want 0.75", got)
	}
	if got := cfg.LLM.Models["claude"].Compact.Mode; got != CompactModeAuto {
		t.Fatalf("claude compact mode = %q, want auto", got)
	}
	if got := cfg.LLM.Models["claude"].Compact.Threshold; got != DefaultCompactThreshold {
		t.Fatalf("claude compact threshold = %v, want default", got)
	}
	if got := cfg.LLM.Models["chat"].Compact.Mode; got != CompactModeFalse {
		t.Fatalf("chat compact mode = %q, want false", got)
	}
}

func TestLLMConfigValidateTrueCompactRequiresSupportedEndpoint(t *testing.T) {
	cfg := validLLMConfig()
	model := cfg.Models["deepseek"]
	model.Endpoint = "chat"
	model.Compact.Mode = CompactModeTrue
	cfg.Models["deepseek"] = model

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "compact requires endpoint responses") {
		t.Fatalf("Validate error = %v, want compact endpoint error", err)
	}
}

func TestLLMConfigValidateAutoAllowsUnsupportedEndpoint(t *testing.T) {
	cfg := validLLMConfig()
	model := cfg.Models["deepseek"]
	model.Endpoint = "chat"
	model.Compact.Mode = CompactModeAuto
	cfg.Models["deepseek"] = model

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
}

func TestLLMConfigValidateCompactRequiresContextWindowForSupportedEndpoint(t *testing.T) {
	cfg := validLLMConfig()
	model := cfg.Models["deepseek"]
	model.Endpoint = "responses"
	model.Compact.Mode = CompactModeAuto
	cfg.Models["deepseek"] = model

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "context_window") {
		t.Fatalf("Validate error = %v, want context_window error", err)
	}
}

func TestLLMConfigValidateCompactThresholdRange(t *testing.T) {
	cfg := validLLMConfig()
	model := cfg.Models["claude"]
	model.Compact.Threshold = 1.1
	model.Compact.thresholdSet = true
	cfg.Models["claude"] = model

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "compact.threshold") {
		t.Fatalf("Validate error = %v, want compact.threshold error", err)
	}

	model.Compact.Threshold = 0
	cfg.Models["claude"] = model
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "compact.threshold") {
		t.Fatalf("Validate error = %v, want compact.threshold error", err)
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

func TestMCPConfigDefaultsAndValidation(t *testing.T) {
	cfg := MCPConfig{
		Servers: map[string]MCPServerConfig{
			"local": {
				Transport: MCPTransportStdio,
				Command:   "node",
				Env:       map[string]string{" TOKEN ": " secret "},
			},
			"remote": {
				Transport: MCPTransportStreamableHTTP,
				URL:       "https://example.com/mcp",
			},
		},
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	if !cfg.Servers["local"].IsEnabled() {
		t.Fatal("local server disabled by default, want enabled")
	}
	if _, ok := cfg.Servers["local"].Env["TOKEN"]; !ok {
		t.Fatalf("env keys = %#v, want trimmed TOKEN", cfg.Servers["local"].Env)
	}

	disabled := false
	cfg.Servers["staged"] = MCPServerConfig{Enabled: &disabled}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate with disabled staged server returned error: %v", err)
	}

	cfg.Servers["missing_command"] = MCPServerConfig{Transport: MCPTransportStdio}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "command is required") {
		t.Fatalf("Validate error = %v, want missing command", err)
	}
}

func TestMCPConfigValidateHTTPURL(t *testing.T) {
	cfg := MCPConfig{Servers: map[string]MCPServerConfig{
		"remote": {Transport: MCPTransportStreamableHTTP, URL: "ftp://example.com/mcp"},
	}}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "http or https") {
		t.Fatalf("Validate error = %v, want http url error", err)
	}
}

func TestMCPServerScopeEmptyIsGlobal(t *testing.T) {
	var scope MCPServerScope
	if !scope.IsZero() {
		t.Fatalf("empty scope IsZero = false, want true")
	}
	if err := scope.Validate(); err != nil {
		t.Fatalf("empty scope Validate returned error: %v", err)
	}
}

func TestMCPConfigScopeDefaultsAndValidation(t *testing.T) {
	cfg := MCPConfig{Servers: map[string]MCPServerConfig{
		"scoped": {
			Transport: MCPTransportStdio,
			Command:   "server",
			Scope: MCPServerScope{
				Platforms: []string{" feishu "},
				Accounts:  []string{" feishu/admin-bot ", "feishu:cli_xxx"},
			},
		},
	}}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	scope := cfg.Servers["scoped"].Scope
	if scope.Platforms[0] != "feishu" || scope.Accounts[0] != "feishu/admin-bot" {
		t.Fatalf("scope = %#v, want trimmed selectors", scope)
	}
}

func TestMCPConfigScopeRejectsBlankSelectors(t *testing.T) {
	cfg := MCPConfig{Servers: map[string]MCPServerConfig{
		"scoped": {
			Transport: MCPTransportStdio,
			Command:   "server",
			Scope:     MCPServerScope{Accounts: []string{" "}},
		},
	}}
	cfg.ApplyDefaults()

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "accounts[0] must not be empty") {
		t.Fatalf("Validate error = %v, want blank account selector error", err)
	}
}

func TestMCPConfigScopeValidatesPlatformSelectors(t *testing.T) {
	cfg := MCPConfig{Servers: map[string]MCPServerConfig{
		"scoped": {
			Transport: MCPTransportStdio,
			Command:   "server",
			Scope:     MCPServerScope{Platforms: []string{"bad id"}},
		},
	}}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "platforms[0]") {
		t.Fatalf("Validate error = %v, want invalid platform selector error", err)
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
