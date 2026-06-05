package platform

import (
	"testing"

	"wechatbox/internal/config"
	"wechatbox/internal/core"
	"wechatbox/internal/session"
	"wechatbox/internal/store"
)

func TestDefaultRegistryLookupAliases(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry returned error: %v", err)
	}

	tests := map[string]string{
		"wechat": store.PlatformWeChat,
		"weixin": store.PlatformWeChat,
		"微信":     store.PlatformWeChat,
		"feishu": store.PlatformFeishu,
		"飞书":     store.PlatformFeishu,
	}
	for name, want := range tests {
		def, ok := registry.Lookup(name)
		if !ok {
			t.Fatalf("Lookup(%q) did not find platform", name)
		}
		if def.ID != want {
			t.Fatalf("Lookup(%q).ID = %q, want %q", name, def.ID, want)
		}
	}
}

func TestLookupAccountPlatformFallbacksToWechat(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry returned error: %v", err)
	}

	def, ok := registry.LookupAccountPlatform("")
	if !ok {
		t.Fatal("LookupAccountPlatform did not find default platform")
	}
	if def.ID != store.PlatformWeChat {
		t.Fatalf("default platform = %q, want wechat", def.ID)
	}
}

func TestDefaultDefinitionsCreateRuntimePlatforms(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry returned error: %v", err)
	}
	st, err := store.Open()
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer st.Close()

	cfg := config.LLMConfig{
		DefaultModel: "deepseek",
		Models: map[string]config.LLMModelConfig{
			"deepseek": {Provider: "openai", BaseURL: "https://llm.test", APIKey: "key", ID: "model"},
		},
	}
	sm := session.NewManager(st, cfg)
	for _, account := range []store.Account{
		{ID: "wechat:test", Platform: store.PlatformWeChat},
		{ID: "feishu:test", Platform: store.PlatformFeishu, CredentialsJSON: `{"app_id":"cli_xxx","app_secret":"secret"}`},
	} {
		def, ok := registry.LookupAccountPlatform(account.Platform)
		if !ok {
			t.Fatalf("LookupAccountPlatform(%q) did not find platform", account.Platform)
		}
		p, err := def.RuntimePlatform(RuntimeContext{
			Store:     st,
			Sessions:  sm,
			LLMConfig: cfg,
			Account:   account,
		})
		if err != nil {
			t.Fatalf("RuntimePlatform(%q) returned error: %v", account.Platform, err)
		}
		if _, ok := p.(core.Platform); !ok {
			t.Fatalf("runtime platform for %q does not implement core.Platform", account.Platform)
		}
		if !def.CommandPolicy.Allows("/help") || !def.CommandPolicy.Allows("/model") {
			t.Fatalf("default command policy for %q should allow shared commands", account.Platform)
		}
	}
}
