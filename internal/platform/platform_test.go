package platform

import (
	"testing"

	"lingobridge/internal/config"
	"lingobridge/internal/core"
	"lingobridge/internal/platform/feishu"
	"lingobridge/internal/session"
	"lingobridge/internal/store"
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

func TestLookupAccountPlatformDoesNotFallback(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry returned error: %v", err)
	}

	if _, ok := registry.LookupAccountPlatform(""); ok {
		t.Fatal("LookupAccountPlatform found empty platform, want no fallback")
	}
}

func TestDefaultDefinitionsSetCoreRuntimeOptions(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry returned error: %v", err)
	}

	wechat, ok := registry.LookupAccountPlatform(store.PlatformWeChat)
	if !ok {
		t.Fatal("wechat definition not found")
	}
	if WeChatTextChunkLimit != 4000 {
		t.Fatalf("WeChatTextChunkLimit = %d, want 4000", WeChatTextChunkLimit)
	}
	if wechat.TextChunkLimit != WeChatTextChunkLimit {
		t.Fatalf("wechat TextChunkLimit = %d, want 4000", wechat.TextChunkLimit)
	}
	if wechat.EnableTextStreaming {
		t.Fatal("wechat EnableTextStreaming = true, want false")
	}

	feishu, ok := registry.LookupAccountPlatform(store.PlatformFeishu)
	if !ok {
		t.Fatal("feishu definition not found")
	}
	if FeishuTextChunkLimit != 30*1024 {
		t.Fatalf("FeishuTextChunkLimit = %d, want %d", FeishuTextChunkLimit, 30*1024)
	}
	if feishu.TextChunkLimit != FeishuTextChunkLimit {
		t.Fatalf("feishu TextChunkLimit = %d, want %d", feishu.TextChunkLimit, 30*1024)
	}
	if !feishu.EnableTextStreaming {
		t.Fatal("feishu EnableTextStreaming = false, want true")
	}
}

func TestDefaultDefinitionsCreateRuntimePlatforms(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatalf("NewDefaultRegistry returned error: %v", err)
	}
	wechatStore, err := store.Open(store.PlatformWeChat)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer wechatStore.Close()
	feishuStore, err := store.Open(store.PlatformFeishu)
	if err != nil {
		t.Fatalf("Open feishu returned error: %v", err)
	}
	defer feishuStore.Close()

	cfg := config.DefaultConfig()
	if err := config.AddModel(&cfg, "deepseek", config.LLMModelConfig{
		Provider: "openai",
		BaseURL:  "https://llm.test",
		APIKey:   "key",
		ID:       "model",
	}, true); err != nil {
		t.Fatalf("AddModel returned error: %v", err)
	}
	feishuCtx, err := core.NewPlatformContext(store.PlatformFeishu, &cfg, feishuStore, nil)
	if err != nil {
		t.Fatalf("NewPlatformContext feishu returned error: %v", err)
	}
	if err := feishu.UpsertAccountConfig(feishuCtx, "fsbot", feishu.AccountConfig{AppID: "cli_xxx", AppSecret: "secret"}); err != nil {
		t.Fatalf("UpsertAccountConfig returned error: %v", err)
	}

	for _, tc := range []struct {
		account store.Account
		st      *store.Store
	}{
		{account: store.Account{ID: "wechat:test", Name: "wxbot", Platform: store.PlatformWeChat}, st: wechatStore},
		{account: store.Account{ID: "feishu:cli_xxx", Name: "fsbot", Platform: store.PlatformFeishu}, st: feishuStore},
	} {
		account := tc.account
		def, ok := registry.LookupAccountPlatform(account.Platform)
		if !ok {
			t.Fatalf("LookupAccountPlatform(%q) did not find platform", account.Platform)
		}
		sm := session.NewManager(tc.st, cfg.LLM)
		platformCtx, err := core.NewPlatformContext(account.Platform, &cfg, tc.st, nil)
		if err != nil {
			t.Fatalf("NewPlatformContext(%q) returned error: %v", account.Platform, err)
		}
		p, err := def.RuntimePlatform(RuntimeContext{
			Store:     tc.st,
			Sessions:  sm,
			Platform:  platformCtx,
			Config:    cfg,
			LLMConfig: cfg.LLM,
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
