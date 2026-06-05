package platform

import (
	"context"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"wechatbox/internal/commands"
	"wechatbox/internal/config"
	"wechatbox/internal/core"
	"wechatbox/internal/platform/feishu"
	feishumonitor "wechatbox/internal/platform/feishu/monitor"
	"wechatbox/internal/platform/wechat/login"
	wechatmonitor "wechatbox/internal/platform/wechat/monitor"
	"wechatbox/internal/session"
	"wechatbox/internal/store"
)

type AccountNewOptions struct {
	Name   string
	Values any
}

type AccountNewContext struct {
	Store *store.Store
}

type RuntimeContext struct {
	Store     *store.Store
	Sessions  *session.Manager
	LLMConfig config.LLMConfig
	Account   store.Account
}

type Definition struct {
	ID                   string
	Aliases              []string
	AccountNewUsage      string
	ParseAccountNewFlags func(args []string) (AccountNewOptions, error)
	CreateAccount        func(ctx AccountNewContext, opts AccountNewOptions) error
	NewRuntimePlatform   func(ctx RuntimeContext) (core.Platform, error)
	CommandPolicy        commands.Policy
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
	if def.CreateAccount == nil {
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
	if platform == "" {
		platform = store.PlatformWeChat
	}
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

func wechatDefinition() Definition {
	return Definition{
		ID:              store.PlatformWeChat,
		Aliases:         []string{"weixin", "微信"},
		AccountNewUsage: "wechatbox account new weixin [--name <name>]",
		ParseAccountNewFlags: func(args []string) (AccountNewOptions, error) {
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
		CreateAccount: func(ctx AccountNewContext, opts AccountNewOptions) error {
			if err := login.Login(ctx.Store, opts.Name); err != nil {
				return fmt.Errorf("login failed: %w", err)
			}
			return nil
		},
		NewRuntimePlatform: func(ctx RuntimeContext) (core.Platform, error) {
			return wechatmonitor.NewPlatform(ctx.Store, ctx.Sessions, ctx.LLMConfig, ctx.Account), nil
		},
		CommandPolicy: commands.DefaultPolicy(),
	}
}

type feishuAccountNewOptions struct {
	AppID     string
	AppSecret string
	BaseURL   string
}

func feishuDefinition() Definition {
	return Definition{
		ID:              store.PlatformFeishu,
		Aliases:         []string{"飞书"},
		AccountNewUsage: "wechatbox account new feishu --name <name> --app-id <id> --app-secret <secret> [--base-url <url>]",
		ParseAccountNewFlags: func(args []string) (AccountNewOptions, error) {
			fs := newAccountFlagSet("account new feishu")
			name := fs.String("name", "default", "account name")
			appID := fs.String("app-id", "", "Feishu app ID")
			appSecret := fs.String("app-secret", "", "Feishu app secret")
			baseURL := fs.String("base-url", "", "platform API base URL")
			if err := fs.Parse(args); err != nil {
				return AccountNewOptions{}, err
			}
			if fs.NArg() > 0 {
				return AccountNewOptions{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
			}
			return AccountNewOptions{
				Name: normalizeAccountName(*name),
				Values: feishuAccountNewOptions{
					AppID:     *appID,
					AppSecret: *appSecret,
					BaseURL:   *baseURL,
				},
			}, nil
		},
		CreateAccount: func(ctx AccountNewContext, opts AccountNewOptions) error {
			values, ok := opts.Values.(feishuAccountNewOptions)
			if !ok {
				return fmt.Errorf("invalid feishu account options")
			}
			acc, err := feishu.NewAccount(opts.Name, values.AppID, values.AppSecret, values.BaseURL)
			if err != nil {
				return err
			}
			if err := ctx.Store.SaveAccount(acc); err != nil {
				return fmt.Errorf("save account: %w", err)
			}
			fmt.Printf("✅ 已添加飞书账户: %s (%s)\n", acc.Name, acc.ID)
			return nil
		},
		NewRuntimePlatform: func(ctx RuntimeContext) (core.Platform, error) {
			return feishumonitor.NewPlatform(ctx.Account), nil
		},
		CommandPolicy: commands.DefaultPolicy(),
	}
}
