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
	githubplatform "lingobridge/internal/platform/github"
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
	for _, def := range []platform.Definition{wechatDefinition(), feishuDefinition(), githubDefinition()} {
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

func githubDefinition() platform.Definition {
	return platform.Definition{
		ID:              store.PlatformGitHub,
		AccountNewUsage: "lingobridge account new github [--name <name>] [--app-id <id>] [--installation-id <id>] [--private-key-path <pem>] --repo owner/repo [--repo owner/other] [--poll-interval 2m] [--base-url <url>] [--web-url <url>]",
		ParseAccountNewFlags: func(args []string, io platform.AccountNewIO) (platform.AccountNewOptions, error) {
			accountIO := normalizeAccountNewIO(io)
			values, err := githubplatform.ParseAccountNewFlags(args, accountIO.In, accountIO.Out)
			if err != nil {
				return platform.AccountNewOptions{}, err
			}
			return platform.AccountNewOptions{
				Name:   normalizeAccountName(values.Name),
				Values: values,
			}, nil
		},
		CreateOrUpdateAccount: func(ctx platform.AccountNewContext, opts platform.AccountNewOptions) error {
			values, ok := opts.Values.(githubplatform.AccountNewOptions)
			if !ok {
				return fmt.Errorf("invalid github account options")
			}
			accountConfig := githubplatform.AccountConfig{
				AppID:          values.AppID,
				InstallationID: values.InstallationID,
				PrivateKeyPath: values.PrivateKeyPath,
				BaseURL:        values.BaseURL,
				WebURL:         values.WebURL,
				Repositories:   values.Repositories,
			}
			if values.PollInterval > 0 {
				accountConfig.PollInterval = githubplatform.NewDuration(values.PollInterval)
			}
			if err := githubplatform.UpsertAccountConfig(ctx.Platform, opts.Name, accountConfig); err != nil {
				return err
			}
			acc, err := githubplatform.NewAccount(opts.Name, values.AppID, values.InstallationID, values.PrivateKeyPath, values.BaseURL)
			if err != nil {
				return err
			}
			if err := ctx.Platform.DataStore().SaveAccount(acc); err != nil {
				return fmt.Errorf("save account: %w", err)
			}
			fmt.Printf("✅ 已添加 GitHub 账户: %s (%s)\n", acc.Name, acc.ID)
			fmt.Println("Note: Configure platforms.github.accounts.<name>.mcp.command and .mcp.args before running this account.")
			return nil
		},
		DeleteAccount: func(ctx platform.AccountDeleteContext) error {
			return githubplatform.DeleteAccountConfig(ctx.Platform, ctx.Account.Name)
		},
		NewRuntimePlatform: func(ctx platform.RuntimeContext) (core.Platform, error) {
			githubConfig, err := githubplatform.LoadConfig(ctx.Platform)
			if err != nil {
				return nil, err
			}
			return githubplatform.NewPlatform(ctx.Account, githubConfig, ctx.Store), nil
		},
		CommandPolicy:       commands.DefaultPolicy(),
		TextChunkLimit:      0,
		EnableTextStreaming: false,
	}
}
