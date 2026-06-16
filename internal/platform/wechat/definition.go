package wechat

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"lingobridge/internal/commands"
	"lingobridge/internal/core"
	"lingobridge/internal/platform"
	"lingobridge/internal/platform/wechat/login"
	wechatmonitor "lingobridge/internal/platform/wechat/monitor"
	"lingobridge/internal/store"
)

const (
	TextChunkLimit  = wechatmonitor.TextChunkLimit
	accountNewUsage = "lingobridge account new weixin [--name <name>]"
)

// Definition returns the built-in WeChat platform registration.
func Definition() platform.Definition {
	return platform.Definition{
		ID:              store.PlatformWeChat,
		Aliases:         []string{"weixin", "微信"},
		AccountNewUsage: accountNewUsage,
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
		TextChunkLimit:      TextChunkLimit,
		EnableTextStreaming: false,
	}
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
