package builtins

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"lingobridge/internal/commands"
	"lingobridge/internal/core"
	"lingobridge/internal/platform"
	"lingobridge/internal/platform/feishu"
	feishumonitor "lingobridge/internal/platform/feishu/monitor"
	"lingobridge/internal/platform/wechat/login"
	wechatmonitor "lingobridge/internal/platform/wechat/monitor"
	"lingobridge/internal/store"
)

const (
	WeChatTextChunkLimit = 4000
	FeishuTextChunkLimit = 25 * 1024
)

func NewRegistry() (*platform.Registry, error) {
	r := platform.NewRegistry()
	for _, def := range []platform.Definition{wechatDefinition(), feishuDefinition()} {
		if err := r.Register(def); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func newAccountFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func normalizeAccountName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "default"
	}
	return name
}

func normalizeAccountNewIO(io platform.AccountNewIO) platform.AccountNewIO {
	if io.In == nil {
		io.In = os.Stdin
	}
	if io.Out == nil {
		io.Out = os.Stdout
	}
	return io
}

func wechatDefinition() platform.Definition {
	return platform.Definition{
		ID:              store.PlatformWeChat,
		Aliases:         []string{"weixin", "微信"},
		AccountNewUsage: "lingobridge account new weixin [--name <name>]",
		ParseAccountNewFlags: func(args []string, io platform.AccountNewIO) (platform.AccountNewOptions, error) {
			fs := newAccountFlagSet("account new weixin")
			name := fs.String("name", "default", "account name")
			if err := fs.Parse(args); err != nil {
				return platform.AccountNewOptions{}, err
			}
			if fs.NArg() > 0 {
				return platform.AccountNewOptions{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
			}
			return platform.AccountNewOptions{Name: normalizeAccountName(*name)}, nil
		},
		CreateOrUpdateAccount: func(ctx platform.AccountNewContext, opts platform.AccountNewOptions) error {
			if err := login.Login(ctx.Platform.DataStore(), opts.Name); err != nil {
				return fmt.Errorf("login failed: %w", err)
			}
			return nil
		},
		NewRuntimePlatform: func(ctx platform.RuntimeContext) (core.Platform, error) {
			return wechatmonitor.NewPlatform(ctx.Store, ctx.Sessions, ctx.LLMConfig, ctx.Account), nil
		},
		CommandPolicy:       commands.DefaultPolicy(),
		TextChunkLimit:      WeChatTextChunkLimit,
		EnableTextStreaming: false,
	}
}

func feishuDefinition() platform.Definition {
	return platform.Definition{
		ID:              store.PlatformFeishu,
		Aliases:         []string{"飞书"},
		AccountNewUsage: "lingobridge account new feishu [--name <name>] [--app-id <id>] [--app-secret <secret>] [--base-url <url>]",
		ParseAccountNewFlags: func(args []string, io platform.AccountNewIO) (platform.AccountNewOptions, error) {
			accountIO := normalizeAccountNewIO(io)
			values, err := feishu.ParseAccountNewFlags(args, accountIO.In, accountIO.Out)
			if err != nil {
				return platform.AccountNewOptions{}, err
			}
			return platform.AccountNewOptions{
				Name:   normalizeAccountName(values.Name),
				Values: values,
			}, nil
		},
		CreateOrUpdateAccount: func(ctx platform.AccountNewContext, opts platform.AccountNewOptions) error {
			values, ok := opts.Values.(feishu.AccountNewOptions)
			if !ok {
				return fmt.Errorf("invalid feishu account options")
			}
			if err := feishu.UpsertAccountConfig(ctx.Platform, opts.Name, feishu.AccountConfig{
				AppID:     values.AppID,
				AppSecret: values.AppSecret,
				BaseURL:   values.BaseURL,
			}); err != nil {
				return err
			}
			acc, err := feishu.NewAccount(opts.Name, values.AppID, values.AppSecret, values.BaseURL)
			if err != nil {
				return err
			}
			if err := ctx.Platform.DataStore().SaveAccount(acc); err != nil {
				return fmt.Errorf("save account: %w", err)
			}
			fmt.Printf("✅ 已添加飞书账户: %s (%s)\n", acc.Name, acc.ID)
			return nil
		},
		DeleteAccount: func(ctx platform.AccountDeleteContext) error {
			return feishu.DeleteAccountConfig(ctx.Platform, ctx.Account.Name)
		},
		NewRuntimePlatform: func(ctx platform.RuntimeContext) (core.Platform, error) {
			feishuConfig, err := feishu.LoadConfig(ctx.Platform)
			if err != nil {
				return nil, err
			}
			return feishumonitor.NewPlatform(ctx.Account, feishuConfig, ctx.LogLevel), nil
		},
		CommandPolicy:       commands.DefaultPolicy(),
		TextChunkLimit:      FeishuTextChunkLimit,
		EnableTextStreaming: true,
	}
}
