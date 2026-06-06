package main

import (
	"context"
	"errors"
	"testing"

	"lingobridge/internal/core"
	"lingobridge/internal/platform"
	"lingobridge/internal/platform/feishu"
	"lingobridge/internal/store"
)

func TestCmdAccountNewFeishuSavesAccount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	err := cmdAccountNew([]string{
		"feishu",
		"--name", "fsbot",
		"--app-id", "cli_xxx",
		"--app-secret", "secret",
	})
	if err != nil {
		t.Fatalf("cmdAccountNew returned error: %v", err)
	}

	st, err := store.Open()
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
	creds, err := feishu.ParseCredentials(acc)
	if err != nil {
		t.Fatalf("ParseCredentials returned error: %v", err)
	}
	if creds.AppID != "cli_xxx" || creds.AppSecret != "secret" {
		t.Fatalf("credentials = %#v", creds)
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

	err := cmdAccountNew([]string{
		"飞书",
		"--name", "fsbot",
		"--app-id", "cli_alias",
		"--app-secret", "secret",
	})
	if err != nil {
		t.Fatalf("cmdAccountNew returned error: %v", err)
	}

	st, err := store.Open()
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
	registry := newFakeAccountNewRegistry(t)

	err := cmdAccountNewWithRegistry([]string{"weixin", "--name", "wxbot"}, registry)
	if err != nil {
		t.Fatalf("cmdAccountNewWithRegistry returned error: %v", err)
	}

	st, err := store.Open()
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
		CreateAccount: func(ctx platform.AccountNewContext, opts platform.AccountNewOptions) error {
			return ctx.Store.SaveAccount(store.Account{
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
