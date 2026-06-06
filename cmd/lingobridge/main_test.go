package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"lingobridge/internal/config"
	"lingobridge/internal/core"
	"lingobridge/internal/logging"
	"lingobridge/internal/platform"
	"lingobridge/internal/platform/feishu"
	"lingobridge/internal/store"
)

func TestCmdAccountNewFeishuSavesAccount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeTestConfig(t)

	err := cmdAccountNew([]string{
		"feishu",
		"--name", "fsbot",
		"--app-id", "cli_xxx",
		"--app-secret", "secret",
	})
	if err != nil {
		t.Fatalf("cmdAccountNew returned error: %v", err)
	}

	st, err := store.Open(store.PlatformFeishu)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer st.Close()

	acc, err := st.GetAccount("feishu:cli_xxx")
	if err != nil {
		t.Fatalf("GetAccount returned error: %v", err)
	}
	if acc.Name != "fsbot" || acc.Platform != store.PlatformFeishu || acc.BaseURL != feishu.DefaultBaseURL {
		t.Fatalf("account = %#v", acc)
	}
	if acc.CredentialsJSON != "{}" {
		t.Fatalf("credentials_json = %q, want {}", acc.CredentialsJSON)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	platformCtx, err := core.NewPlatformContext(store.PlatformFeishu, &cfg, st, nil)
	if err != nil {
		t.Fatalf("NewPlatformContext returned error: %v", err)
	}
	feishuAccount, ok, err := feishu.ResolveAccountConfig(platformCtx, "fsbot")
	if err != nil {
		t.Fatalf("ResolveAccountConfig returned error: %v", err)
	}
	if !ok || feishuAccount.AppID != "cli_xxx" || feishuAccount.AppSecret != "secret" {
		t.Fatalf("feishu account = %#v ok=%v", feishuAccount, ok)
	}
}

func TestCmdAccountNewFeishuRequiresCredentials(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	err := cmdAccountNew([]string{"feishu", "--name", "fsbot"})
	if err == nil {
		t.Fatal("cmdAccountNew returned nil error, want missing credentials error")
	}
}

func TestCmdAccountNewFeishuAliasSavesAccount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeTestConfig(t)

	err := cmdAccountNew([]string{
		"飞书",
		"--name", "fsbot",
		"--app-id", "cli_alias",
		"--app-secret", "secret",
	})
	if err != nil {
		t.Fatalf("cmdAccountNew returned error: %v", err)
	}

	st, err := store.Open(store.PlatformFeishu)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer st.Close()

	acc, err := st.GetAccount("feishu:cli_alias")
	if err != nil {
		t.Fatalf("GetAccount returned error: %v", err)
	}
	if acc.Platform != store.PlatformFeishu {
		t.Fatalf("platform = %q, want feishu", acc.Platform)
	}
}

func TestCmdAccountNewWeixinAliasUsesPlatformDefinition(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeTestConfig(t)
	registry := newFakeAccountNewRegistry(t)

	err := cmdAccountNewWithRegistry([]string{"weixin", "--name", "wxbot"}, registry)
	if err != nil {
		t.Fatalf("cmdAccountNewWithRegistry returned error: %v", err)
	}

	st, err := store.Open(store.PlatformWeChat)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer st.Close()

	acc, err := st.GetAccount("wechat:test")
	if err != nil {
		t.Fatalf("GetAccount returned error: %v", err)
	}
	if acc.Name != "wxbot" || acc.Platform != store.PlatformWeChat {
		t.Fatalf("account = %#v", acc)
	}
}

func TestCmdAccountNewRejectsOldPlatformFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	err := cmdAccountNew([]string{"--platform", "feishu", "--name", "fsbot"})
	if !errors.Is(err, errUsage) {
		t.Fatalf("cmdAccountNew error = %v, want errUsage", err)
	}
}

func TestCmdAccountNewUnknownPlatform(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	err := cmdAccountNew([]string{"unknown"})
	if err == nil {
		t.Fatal("cmdAccountNew returned nil error, want unsupported platform error")
	}
}

func TestParseRunOptionsDefaultsLogLevelToInfo(t *testing.T) {
	opts, err := parseRunOptions(nil)
	if err != nil {
		t.Fatalf("parseRunOptions returned error: %v", err)
	}
	if opts.logLevel != logging.Info {
		t.Fatalf("logLevel = %v, want info", opts.logLevel)
	}
}

func TestParseRunOptionsAcceptsVerboseDebug(t *testing.T) {
	opts, err := parseRunOptions([]string{"--verbose", "debug", "--account", "fsbot"})
	if err != nil {
		t.Fatalf("parseRunOptions returned error: %v", err)
	}
	if opts.logLevel != logging.Debug || opts.targetAccount != "fsbot" {
		t.Fatalf("options = %#v, want debug level and target account", opts)
	}
}

func TestParseRunOptionsRejectsInvalidVerbose(t *testing.T) {
	if _, err := parseRunOptions([]string{"--verbose", "noisy"}); err == nil {
		t.Fatal("parseRunOptions returned nil error, want invalid verbose error")
	}
}

func TestEnsureConfigInitializedCreatesFirstModelAsDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	input := strings.NewReader("first\nopenai\nhttps://api.example.com/v1\nkey\nmodel\n\n")
	var out bytes.Buffer

	if err := ensureConfigInitialized(input, &out); err != nil {
		t.Fatalf("ensureConfigInitialized returned error: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.LLM.DefaultModel != "first" || !cfg.LLM.HasModel("first") {
		t.Fatalf("llm config = %#v", cfg.LLM)
	}
}

func TestEnsureConfigInitializedRepromptsInvalidEndpoint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	input := strings.NewReader("first\nopenai\nhttps://api.example.com/v1\nkey\nmodel\nresponse\nresponses\n")
	var out bytes.Buffer

	if err := ensureConfigInitialized(input, &out); err != nil {
		t.Fatalf("ensureConfigInitialized returned error: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.LLM.Models["first"].Endpoint != "responses" {
		t.Fatalf("endpoint = %q, want responses", cfg.LLM.Models["first"].Endpoint)
	}
	if !strings.Contains(out.String(), "注意不是 response") || !strings.Contains(out.String(), "复数 responses") {
		t.Fatalf("prompt output = %q, want responses guidance", out.String())
	}
}

func TestCmdModelAddWritesConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeTestConfig(t)
	var out bytes.Buffer

	err := cmdModelAdd([]string{
		"gpt4o",
		"--provider", "openai",
		"--base-url", "https://api.openai.com/v1",
		"--api-key", "key",
		"--id", "gpt-4o",
		"--endpoint", "responses",
		"--default",
	}, strings.NewReader(""), &out)
	if err != nil {
		t.Fatalf("cmdModelAdd returned error: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.LLM.DefaultModel != "gpt4o" || cfg.LLM.Models["gpt4o"].Endpoint != "responses" {
		t.Fatalf("llm config = %#v", cfg.LLM)
	}
}

type fakeRuntimePlatform struct{}

func (fakeRuntimePlatform) Run(ctx context.Context, handler core.Handler) error {
	return nil
}

func newFakeAccountNewRegistry(t *testing.T) *platform.Registry {
	t.Helper()
	registry := platform.NewRegistry()
	err := registry.Register(platform.Definition{
		ID:              store.PlatformWeChat,
		Aliases:         []string{"weixin", "微信"},
		AccountNewUsage: "lingobridge account new weixin [--name <name>]",
		ParseAccountNewFlags: func(args []string, io platform.AccountNewIO) (platform.AccountNewOptions, error) {
			name := "default"
			for i := 0; i < len(args); i++ {
				if args[i] == "--name" && i+1 < len(args) {
					name = args[i+1]
					i++
				}
			}
			return platform.AccountNewOptions{Name: name}, nil
		},
		CreateOrUpdateAccount: func(ctx platform.AccountNewContext, opts platform.AccountNewOptions) error {
			return ctx.Platform.DataStore().SaveAccount(store.Account{
				ID:              "wechat:test",
				Name:            opts.Name,
				Platform:        store.PlatformWeChat,
				CredentialsJSON: "{}",
				Enabled:         true,
			})
		},
		NewRuntimePlatform: func(ctx platform.RuntimeContext) (core.Platform, error) {
			return fakeRuntimePlatform{}, nil
		},
	})
	if err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	return registry
}

func writeTestConfig(t *testing.T) {
	t.Helper()
	cfg := config.DefaultConfig()
	if err := config.AddModel(&cfg, "deepseek", config.LLMModelConfig{
		Provider: "openai",
		BaseURL:  "https://api.deepseek.com/v1",
		APIKey:   "key",
		ID:       "deepseek-chat",
	}, true); err != nil {
		t.Fatalf("AddModel returned error: %v", err)
	}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
}
