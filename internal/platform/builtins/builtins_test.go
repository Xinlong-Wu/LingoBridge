package builtins

import (
	"testing"

	"lingobridge/internal/config"
	"lingobridge/internal/core"
	"lingobridge/internal/platform"
	feishuplatform "lingobridge/internal/platform/feishu"
	githubplatform "lingobridge/internal/platform/github"
	wechatplatform "lingobridge/internal/platform/wechat"
	"lingobridge/internal/session"
	"lingobridge/internal/store"
)

func TestDefaultRegistryLookupAliases(t *testing.T) {
	registry, err := NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry returned error: %v", err)
	}

	tests := map[string]string{
		"wechat": store.PlatformWeChat,
		"weixin": store.PlatformWeChat,
		"微信":     store.PlatformWeChat,
		"feishu": store.PlatformFeishu,
		"飞书":     store.PlatformFeishu,
		"github": store.PlatformGitHub,
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
	registry, err := NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry returned error: %v", err)
	}

	if _, ok := registry.LookupAccountPlatform(""); ok {
		t.Fatal("LookupAccountPlatform found empty platform, want no fallback")
	}
}

func TestDefaultDefinitionsSetCoreRuntimeOptions(t *testing.T) {
	registry, err := NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry returned error: %v", err)
	}

	wechat, ok := registry.LookupAccountPlatform(store.PlatformWeChat)
	if !ok {
		t.Fatal("wechat definition not found")
	}
	if wechatplatform.TextChunkLimit != 4000 {
		t.Fatalf("wechat TextChunkLimit = %d, want 4000", wechatplatform.TextChunkLimit)
	}
	if wechat.TextChunkLimit != wechatplatform.TextChunkLimit {
		t.Fatalf("wechat TextChunkLimit = %d, want 4000", wechat.TextChunkLimit)
	}
	if wechat.EnableTextStreaming {
		t.Fatal("wechat EnableTextStreaming = true, want false")
	}

	feishuDef, ok := registry.LookupAccountPlatform(store.PlatformFeishu)
	if !ok {
		t.Fatal("feishu definition not found")
	}
	if feishuplatform.TextChunkLimit != 25*1024 {
		t.Fatalf("feishu TextChunkLimit = %d, want %d", feishuplatform.TextChunkLimit, 25*1024)
	}
	if feishuDef.TextChunkLimit != feishuplatform.TextChunkLimit {
		t.Fatalf("feishu TextChunkLimit = %d, want %d", feishuDef.TextChunkLimit, feishuplatform.TextChunkLimit)
	}
	if !feishuDef.EnableTextStreaming {
		t.Fatal("feishu EnableTextStreaming = false, want true")
	}

	github, ok := registry.LookupAccountPlatform(store.PlatformGitHub)
	if !ok {
		t.Fatal("github definition not found")
	}
	if github.TextChunkLimit != 0 {
		t.Fatalf("github TextChunkLimit = %d, want 0", github.TextChunkLimit)
	}
	if github.EnableTextStreaming {
		t.Fatal("github EnableTextStreaming = true, want false")
	}
}

func TestDefaultDefinitionsCreateRuntimePlatforms(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	registry, err := NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry returned error: %v", err)
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
	githubStore, err := store.Open(store.PlatformGitHub)
	if err != nil {
		t.Fatalf("Open github returned error: %v", err)
	}
	defer githubStore.Close()

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
	if err := feishuplatform.UpsertAccountConfig(feishuCtx, "fsbot", feishuplatform.AccountConfig{AppID: "cli_xxx", AppSecret: "secret"}); err != nil {
		t.Fatalf("UpsertAccountConfig returned error: %v", err)
	}
	githubCtx, err := core.NewPlatformContext(store.PlatformGitHub, &cfg, githubStore, nil)
	if err != nil {
		t.Fatalf("NewPlatformContext github returned error: %v", err)
	}
	if err := githubplatform.UpsertAccountConfig(githubCtx, "ghbot", githubplatform.AccountConfig{
		AppID:          "123",
		InstallationID: "456",
		PrivateKeyPath: "/tmp/github-app.pem",
		Repositories:   []string{"owner/repo"},
	}); err != nil {
		t.Fatalf("UpsertAccountConfig github returned error: %v", err)
	}

	for _, tc := range []struct {
		account store.Account
		st      *store.Store
	}{
		{account: store.Account{ID: "wechat:test", Name: "wxbot", Platform: store.PlatformWeChat}, st: wechatStore},
		{account: store.Account{ID: "feishu:cli_xxx", Name: "fsbot", Platform: store.PlatformFeishu}, st: feishuStore},
		{account: store.Account{ID: "github:456", Name: "ghbot", Platform: store.PlatformGitHub}, st: githubStore},
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
		p, err := def.RuntimePlatform(platform.RuntimeContext{
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
