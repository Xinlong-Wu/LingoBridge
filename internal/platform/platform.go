package platform

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"lingobridge/internal/commands"
	"lingobridge/internal/config"
	"lingobridge/internal/core"
	"lingobridge/internal/logging"
	"lingobridge/internal/platform/feishu"
	feishumonitor "lingobridge/internal/platform/feishu/monitor"
	"lingobridge/internal/platform/wechat/login"
	wechatmonitor "lingobridge/internal/platform/wechat/monitor"
	"lingobridge/internal/session"
	"lingobridge/internal/store"
)

const (
	WeChatTextChunkLimit = 4000
	FeishuTextChunkLimit = 25 * 1024
)

type AccountNewOptions struct {
	Name   string
	Values any
}

type AccountNewIO struct {
	In  io.Reader
	Out io.Writer
}

type AccountNewContext struct {
	Platform *core.PlatformContext
}

type RuntimeContext struct {
	Store     *store.Store
	Sessions  *session.Manager
	Platform  *core.PlatformContext
	Config    config.Config
	LLMConfig config.LLMConfig
	Account   store.Account
	LogLevel  logging.Level
}

type Definition struct {
	ID                    string
	Aliases               []string
	AccountNewUsage       string
	ParseAccountNewFlags  func(args []string, io AccountNewIO) (AccountNewOptions, error)
	CreateOrUpdateAccount func(ctx AccountNewContext, opts AccountNewOptions) error
	NewRuntimePlatform    func(ctx RuntimeContext) (core.Platform, error)
	CommandPolicy         commands.Policy
	TextChunkLimit        int
	EnableTextStreaming   bool
}

func DefaultAccountNewIO() AccountNewIO {
	return AccountNewIO{In: os.Stdin, Out: os.Stdout}
}

type Registry struct {
	definitions map[string]Definition
	aliases     map[string]string
}

type policyPlatform struct {
	inner  core.Platform
	policy commands.Policy
}

type policyHandler struct {
	next   core.Handler
	policy commands.Policy
}

func (p policyPlatform) Run(ctx context.Context, handler core.Handler) error {
	return p.inner.Run(ctx, policyHandler{next: handler, policy: p.policy})
}

func (h policyHandler) Handle(ctx context.Context, msg core.InboundMessage, sender core.Sender) error {
	msg.CommandPolicy = h.policy
	return h.next.Handle(ctx, msg, sender)
}

func NewDefaultRegistry() (*Registry, error) {
	r := NewRegistry()
	for _, def := range []Definition{wechatDefinition(), feishuDefinition()} {
		if err := r.Register(def); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func NewRegistry() *Registry {
	return &Registry{
		definitions: map[string]Definition{},
		aliases:     map[string]string{},
	}
}

func (r *Registry) Register(def Definition) error {
	id := normalizePlatformName(def.ID)
	if id == "" {
		return fmt.Errorf("platform id is required")
	}
	if _, ok := r.definitions[id]; ok {
		return fmt.Errorf("platform %q is already registered", id)
	}
	def.ID = id
	if def.ParseAccountNewFlags == nil {
		return fmt.Errorf("platform %q missing account new parser", id)
	}
	if def.CreateOrUpdateAccount == nil {
		return fmt.Errorf("platform %q missing account creator", id)
	}
	if def.NewRuntimePlatform == nil {
		return fmt.Errorf("platform %q missing runtime factory", id)
	}

	r.definitions[id] = def
	for _, name := range append([]string{id}, def.Aliases...) {
		alias := normalizePlatformName(name)
		if alias == "" {
			continue
		}
		if existing, ok := r.aliases[alias]; ok {
			return fmt.Errorf("platform alias %q already maps to %q", alias, existing)
		}
		r.aliases[alias] = id
	}
	return nil
}

func (r *Registry) Lookup(name string) (Definition, bool) {
	id, ok := r.aliases[normalizePlatformName(name)]
	if !ok {
		return Definition{}, false
	}
	def, ok := r.definitions[id]
	return def, ok
}

func (r *Registry) LookupAccountPlatform(platform string) (Definition, bool) {
	platform = normalizePlatformName(platform)
	return r.Lookup(platform)
}

func (r *Registry) PlatformNames() []string {
	names := make([]string, 0, len(r.definitions))
	for id := range r.definitions {
		names = append(names, id)
	}
	sort.Strings(names)
	return names
}

func (d Definition) RuntimePlatform(ctx RuntimeContext) (core.Platform, error) {
	p, err := d.NewRuntimePlatform(ctx)
	if err != nil {
		return nil, err
	}
	return policyPlatform{inner: p, policy: d.CommandPolicy}, nil
}

func normalizePlatformName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
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

func normalizeAccountNewIO(io AccountNewIO) AccountNewIO {
	if io.In == nil {
		io.In = os.Stdin
	}
	if io.Out == nil {
		io.Out = os.Stdout
	}
	return io
}

func wechatDefinition() Definition {
	return Definition{
		ID:              store.PlatformWeChat,
		Aliases:         []string{"weixin", "微信"},
		AccountNewUsage: "lingobridge account new weixin [--name <name>]",
		ParseAccountNewFlags: func(args []string, io AccountNewIO) (AccountNewOptions, error) {
			fs := newAccountFlagSet("account new weixin")
			name := fs.String("name", "default", "account name")
			if err := fs.Parse(args); err != nil {
				return AccountNewOptions{}, err
			}
			if fs.NArg() > 0 {
				return AccountNewOptions{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
			}
			return AccountNewOptions{Name: normalizeAccountName(*name)}, nil
		},
		CreateOrUpdateAccount: func(ctx AccountNewContext, opts AccountNewOptions) error {
			if err := login.Login(ctx.Platform.DataStore(), opts.Name); err != nil {
				return fmt.Errorf("login failed: %w", err)
			}
			return nil
		},
		NewRuntimePlatform: func(ctx RuntimeContext) (core.Platform, error) {
			return wechatmonitor.NewPlatform(ctx.Store, ctx.Sessions, ctx.LLMConfig, ctx.Account), nil
		},
		CommandPolicy:       commands.DefaultPolicy(),
		TextChunkLimit:      WeChatTextChunkLimit,
		EnableTextStreaming: false,
	}
}

func feishuDefinition() Definition {
	return Definition{
		ID:              store.PlatformFeishu,
		Aliases:         []string{"飞书"},
		AccountNewUsage: "lingobridge account new feishu [--name <name>] [--app-id <id>] [--app-secret <secret>] [--base-url <url>]",
		ParseAccountNewFlags: func(args []string, io AccountNewIO) (AccountNewOptions, error) {
			accountIO := normalizeAccountNewIO(io)
			values, err := feishu.ParseAccountNewFlags(args, accountIO.In, accountIO.Out)
			if err != nil {
				return AccountNewOptions{}, err
			}
			return AccountNewOptions{
				Name:   normalizeAccountName(values.Name),
				Values: values,
			}, nil
		},
		CreateOrUpdateAccount: func(ctx AccountNewContext, opts AccountNewOptions) error {
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
		NewRuntimePlatform: func(ctx RuntimeContext) (core.Platform, error) {
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
