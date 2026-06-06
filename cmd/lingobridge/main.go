package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	"lingobridge/internal/config"
	"lingobridge/internal/control"
	"lingobridge/internal/core"
	"lingobridge/internal/platform"
	"lingobridge/internal/runner"
	"lingobridge/internal/session"
	"lingobridge/internal/store"
)

var errUsage = errors.New("usage")

func logBuildInfo() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		log.Printf("LingoBridge dev go/%s", runtime.Version()[2:])
		return
	}

	version := info.Main.Version
	if version == "" || version == "(devel)" {
		// Try VCS info
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				version = s.Value
				if len(version) > 8 {
					version = version[:8]
				}
			case "vcs.modified":
				if s.Value == "true" {
					version += "-dirty"
				}
			}
		}
	}
	if version == "" || version == "(devel)" {
		version = "dev"
	}

	var vcsTime string
	for _, s := range info.Settings {
		if s.Key == "vcs.time" {
			vcsTime = s.Value
			break
		}
	}

	if vcsTime != "" {
		log.Printf("LingoBridge %s (%s) go/%s", version, vcsTime, runtime.Version()[2:])
	} else {
		log.Printf("LingoBridge %s go/%s", version, runtime.Version()[2:])
	}
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		if !errors.Is(err, errUsage) {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 1 {
		printUsage()
		return errUsage
	}
	if commandNeedsConfig(args) {
		if err := ensureConfigInitialized(os.Stdin, os.Stdout); err != nil {
			return err
		}
	}

	switch args[0] {
	case "run":
		return cmdRun(args[1:])
	case "account":
		return cmdAccount(args[1:])
	case "model":
		return cmdModel(args[1:])
	default:
		printUsage()
		return errUsage
	}
}

func commandNeedsConfig(args []string) bool {
	switch args[0] {
	case "run":
		return true
	case "account":
		if len(args) < 2 {
			return false
		}
		switch args[1] {
		case "new", "list", "delete":
			return true
		default:
			return false
		}
	case "model":
		return len(args) >= 2 && args[1] == "add"
	default:
		return false
	}
}

func cmdAccount(args []string) error {
	if len(args) < 1 {
		printAccountUsage()
		return errUsage
	}

	switch args[0] {
	case "new":
		return cmdAccountNew(args[1:])
	case "list":
		return cmdAccountList(args[1:])
	case "delete":
		return cmdAccountDelete(args[1:])
	default:
		printAccountUsage()
		return errUsage
	}
}

func cmdModel(args []string) error {
	if len(args) < 1 {
		printModelUsage()
		return errUsage
	}

	switch args[0] {
	case "add":
		return cmdModelAdd(args[1:], os.Stdin, os.Stdout)
	default:
		printModelUsage()
		return errUsage
	}
}

func printUsage() {
	fmt.Println("LingoBridge - WeChat/Feishu Bot → LLM Direct Bridge")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  lingobridge account new <weixin|feishu> [platform options]")
	fmt.Println("                                           Add a bot account")
	fmt.Println("  lingobridge account list                   List all accounts")
	fmt.Println("  lingobridge account delete <name>          Delete an account")
	fmt.Println("  lingobridge model add <name> [model options]")
	fmt.Println("                                           Add an LLM model profile")
	fmt.Println("  lingobridge run [--account <name>]         Start the bot loop")
}

func printAccountUsage() {
	fmt.Println("Usage:")
	fmt.Println("  lingobridge account new <weixin|feishu> [platform options]")
	fmt.Println("  lingobridge account list")
	fmt.Println("  lingobridge account delete <name>")
}

func printModelUsage() {
	fmt.Println("Usage:")
	fmt.Println("  lingobridge model add <name> [--provider <openai|anthropic>] [--base-url <url>] [--api-key <key>] [--id <model-id>] [--endpoint <mode>] [--default]")
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func openStore(platformID string) (*store.Store, error) {
	st, err := store.Open(platformID)
	if err != nil {
		return nil, fmt.Errorf("open %s store: %w", platformID, err)
	}
	return st, nil
}

type accountCatalog struct {
	stores   map[string]*store.Store
	accounts []store.Account
}

func openAccountCatalog(registry *platform.Registry) (*accountCatalog, error) {
	catalog := &accountCatalog{stores: map[string]*store.Store{}}
	for _, platformID := range registry.PlatformNames() {
		st, err := openStore(platformID)
		if err != nil {
			catalog.Close()
			return nil, err
		}
		catalog.stores[platformID] = st
		accounts, err := st.ListAccounts()
		if err != nil {
			catalog.Close()
			return nil, fmt.Errorf("list %s accounts: %w", platformID, err)
		}
		catalog.accounts = append(catalog.accounts, accounts...)
	}
	return catalog, nil
}

func (c *accountCatalog) Close() {
	for _, st := range c.stores {
		_ = st.Close()
	}
}

func (c *accountCatalog) ListAccounts() ([]store.Account, error) {
	accounts := make([]store.Account, 0)
	for platformID, st := range c.stores {
		platformAccounts, err := st.ListAccounts()
		if err != nil {
			return nil, fmt.Errorf("list %s accounts: %w", platformID, err)
		}
		accounts = append(accounts, platformAccounts...)
	}
	return accounts, nil
}

func ensureConfigInitialized(in io.Reader, out io.Writer) error {
	if _, err := config.Load(); err == nil {
		return nil
	} else if !errors.Is(err, config.ErrConfigNotFound) {
		return fmt.Errorf("load config: %w", err)
	}

	fmt.Fprintln(out, "未找到 config.yaml，开始创建初始配置。")
	cfg := config.DefaultConfig()
	name, model, err := promptModelProfile("", config.LLMModelConfig{}, in, out)
	if err != nil {
		return fmt.Errorf("initialize config: %w", err)
	}
	if err := config.AddModel(&cfg, name, model, true); err != nil {
		return fmt.Errorf("initialize config: %w", err)
	}
	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	path, _ := config.ConfigPath()
	fmt.Fprintf(out, "已创建配置文件：%s\n", path)
	return nil
}

func cmdModelAdd(args []string, in io.Reader, out io.Writer) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		printModelUsage()
		return errUsage
	}
	name := args[0]
	fs := newFlagSet("model add")
	provider := fs.String("provider", "", "model provider")
	baseURL := fs.String("base-url", "", "model API base URL")
	apiKey := fs.String("api-key", "", "model API key")
	modelID := fs.String("id", "", "provider model ID")
	endpoint := fs.String("endpoint", "", "endpoint mode")
	makeDefault := fs.Bool("default", false, "set as default model")
	if err := fs.Parse(args[1:]); err != nil {
		printModelUsage()
		return errUsage
	}
	if fs.NArg() > 0 {
		printModelUsage()
		return errUsage
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.LLM.HasModel(name) {
		return fmt.Errorf("%w: %s", config.ErrModelExists, name)
	}
	model := config.LLMModelConfig{
		Provider: *provider,
		BaseURL:  *baseURL,
		APIKey:   *apiKey,
		ID:       *modelID,
		Endpoint: *endpoint,
	}
	name, model, err = promptModelProfile(name, model, in, out)
	if err != nil {
		return err
	}
	if err := config.AddModel(&cfg, name, model, *makeDefault); err != nil {
		return err
	}
	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	if *makeDefault || cfg.LLM.DefaultModel == name {
		fmt.Fprintf(out, "✅ 已添加模型: %s (default)\n", name)
	} else {
		fmt.Fprintf(out, "✅ 已添加模型: %s\n", name)
	}
	notifyRunningProcess()
	return nil
}

func promptModelProfile(name string, model config.LLMModelConfig, in io.Reader, out io.Writer) (string, config.LLMModelConfig, error) {
	reader := bufio.NewReader(in)
	var err error
	if strings.TrimSpace(name) == "" {
		name, err = promptCLIValue(reader, out, "模型名称: ", true, "")
		if err != nil {
			return "", model, err
		}
	}
	if strings.TrimSpace(model.Provider) == "" {
		model.Provider, err = promptCLIValue(reader, out, "Provider (openai/anthropic，默认 openai): ", true, "openai")
		if err != nil {
			return "", model, err
		}
	}
	if strings.TrimSpace(model.BaseURL) == "" {
		model.BaseURL, err = promptCLIValue(reader, out, "API Base URL: ", true, "")
		if err != nil {
			return "", model, err
		}
	}
	if strings.TrimSpace(model.APIKey) == "" {
		model.APIKey, err = promptCLIValue(reader, out, "API Key: ", true, "")
		if err != nil {
			return "", model, err
		}
	}
	if strings.TrimSpace(model.ID) == "" {
		model.ID, err = promptCLIValue(reader, out, "模型 ID: ", true, "")
		if err != nil {
			return "", model, err
		}
	}
	if strings.TrimSpace(model.Endpoint) == "" {
		defaultEndpoint := config.DefaultEndpointForProvider(strings.TrimSpace(model.Provider))
		prompt := fmt.Sprintf("Endpoint（直接回车使用 %s）: ", defaultEndpoint)
		model.Endpoint, err = promptCLIValue(reader, out, prompt, false, defaultEndpoint)
		if err != nil {
			return "", model, err
		}
	}
	return strings.TrimSpace(name), model, nil
}

func promptCLIValue(reader *bufio.Reader, out io.Writer, prompt string, required bool, defaultValue string) (string, error) {
	for {
		fmt.Fprint(out, prompt)
		value, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		value = strings.TrimSpace(value)
		if value == "" {
			value = defaultValue
		}
		if value != "" || !required {
			return value, nil
		}
		fmt.Fprintln(out, "此项必填，请重新输入。")
		if err == io.EOF {
			return "", fmt.Errorf("missing required input")
		}
	}
}

type runtimeState struct {
	stores   map[string]*store.Store
	mu       sync.RWMutex
	cfg      config.Config
	runtimes map[string]platformRuntime
	digest   string
}

type platformRuntime struct {
	store   *store.Store
	sm      *session.Manager
	handler *core.Bot
}

func newRuntimeState(stores map[string]*store.Store, cfg config.Config) (*runtimeState, error) {
	rs := &runtimeState{stores: stores}
	if err := rs.updateConfig(cfg); err != nil {
		return nil, err
	}
	return rs, nil
}

func (r *runtimeState) updateConfig(cfg config.Config) error {
	if err := cfg.LLM.Validate(); err != nil {
		return fmt.Errorf("validate llm config: %w", err)
	}
	runtimes := make(map[string]platformRuntime, len(r.stores))
	for platformID, st := range r.stores {
		resetCount, err := st.ResetUnavailableUserModels(cfg.LLM.DefaultModel, cfg.LLM.ModelNames())
		if err != nil {
			return fmt.Errorf("reset %s user models: %w", platformID, err)
		}
		if resetCount > 0 {
			log.Printf("Reset %d %s user model preference(s) to default model %q", resetCount, platformID, cfg.LLM.DefaultModel)
		}
		sm := session.NewManager(st, cfg.LLM)
		runtimes[platformID] = platformRuntime{
			store:   st,
			sm:      sm,
			handler: core.New(sm, cfg.LLM),
		}
	}
	digest, err := config.Digest(cfg)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.cfg = cfg
	r.runtimes = runtimes
	r.digest = digest
	r.mu.Unlock()
	logConfig(cfg)
	return nil
}

func (r *runtimeState) snapshot(platformID string) (config.Config, platformRuntime, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rt, ok := r.runtimes[platformID]
	return r.cfg, rt, ok
}

func (r *runtimeState) signatureExtra(acc store.Account) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if acc.Platform == store.PlatformFeishu {
		return r.digest
	}
	return r.digest
}

func logConfig(cfg config.Config) {
	log.Printf("LLM default_model: %s", cfg.LLM.DefaultModel)
	log.Printf("LLM models: %s", strings.Join(cfg.LLM.ModelNames(), ", "))
	log.Printf("LLM max_history: %d", cfg.LLM.MaxHistory)
	log.Printf("LLM system_prompt: %s", cfg.LLM.SystemPrompt)
}

func cmdAccountNew(args []string) error {
	registry, err := platform.NewDefaultRegistry()
	if err != nil {
		return err
	}
	return cmdAccountNewWithRegistry(args, registry)
}

func cmdAccountNewWithRegistry(args []string, registry *platform.Registry) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Println("Usage: lingobridge account new <weixin|feishu> [platform options]")
		return errUsage
	}
	def, ok := registry.Lookup(args[0])
	if !ok {
		return fmt.Errorf("unsupported platform %q; use one of: %s", args[0], strings.Join(registry.PlatformNames(), ", "))
	}
	opts, err := def.ParseAccountNewFlags(args[1:], platform.DefaultAccountNewIO())
	if err != nil {
		fmt.Println("Usage:", def.AccountNewUsage)
		return errUsage
	}

	catalog, err := openAccountCatalog(registry)
	if err != nil {
		return err
	}
	defer catalog.Close()

	st, ok := catalog.stores[def.ID]
	if !ok {
		return fmt.Errorf("store for platform %q is not open", def.ID)
	}
	exists := func(name string) bool {
		for _, a := range catalog.accounts {
			if a.Name == name {
				return true
			}
		}
		return false
	}

	originalName := opts.Name
	suffix := 2
	for exists(opts.Name) {
		opts.Name = fmt.Sprintf("%s-%d", originalName, suffix)
		suffix++
	}
	if opts.Name != originalName {
		fmt.Printf("Name %q already exists, using %q instead.\n", originalName, opts.Name)
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	platformCtx, err := core.NewPlatformContext(def.ID, &cfg, st, config.Save)
	if err != nil {
		return err
	}
	if err := def.CreateOrUpdateAccount(platform.AccountNewContext{Platform: platformCtx}, opts); err != nil {
		return err
	}
	notifyRunningProcess()
	return nil
}

func cmdRun(args []string) error {
	fs := newFlagSet("run")
	targetAccount := fs.String("account", "", "account name")
	if err := fs.Parse(args); err != nil {
		fmt.Println("Usage: lingobridge run [--account <name>]")
		return errUsage
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logBuildInfo()

	registry, err := platform.NewDefaultRegistry()
	if err != nil {
		return err
	}
	catalog, err := openAccountCatalog(registry)
	if err != nil {
		return err
	}
	defer catalog.Close()

	state, err := newRuntimeState(catalog.stores, cfg)
	if err != nil {
		return err
	}

	runCtx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	supervisor := runner.NewSupervisor(catalog, func(ctx context.Context, acc store.Account) error {
		cfg, rt, ok := state.snapshot(acc.Platform)
		if !ok {
			return fmt.Errorf("runtime for platform %q is not available", acc.Platform)
		}
		def, ok := registry.LookupAccountPlatform(acc.Platform)
		if !ok {
			return fmt.Errorf("unsupported account platform %q for account %s", acc.Platform, acc.Name)
		}
		platformCtx, err := core.NewPlatformContext(acc.Platform, &cfg, rt.store, nil)
		if err != nil {
			return err
		}
		runtimePlatform, err := def.RuntimePlatform(platform.RuntimeContext{
			Store:     rt.store,
			Sessions:  rt.sm,
			Platform:  platformCtx,
			Config:    cfg,
			LLMConfig: cfg.LLM,
			Account:   acc,
		})
		if err != nil {
			return err
		}
		return runtimePlatform.Run(ctx, rt.handler)
	}, *targetAccount)
	supervisor.SetSignatureExtra(state.signatureExtra)

	controlServer, err := control.StartServer(runCtx, func(context.Context) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		if err := state.updateConfig(cfg); err != nil {
			return err
		}
		return supervisor.Reconcile(runCtx)
	})
	if err != nil {
		if errors.Is(err, control.ErrAlreadyRunning) {
			return fmt.Errorf("another lingobridge run process is already active")
		}
		return fmt.Errorf("start control server: %w", err)
	}

	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := controlServer.Close(shutdownCtx); err != nil {
			log.Printf("control server shutdown: %v", err)
		}
	}()

	if err := supervisor.Reconcile(runCtx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("reconcile accounts: %w", err)
	}

	log.Printf("Listening on %d account(s)...", supervisor.RunningCount())
	<-runCtx.Done()
	log.Println("Shutting down...")
	supervisor.Stop()
	return nil
}

func cmdAccountList(args []string) error {
	registry, err := platform.NewDefaultRegistry()
	if err != nil {
		return err
	}
	catalog, err := openAccountCatalog(registry)
	if err != nil {
		return err
	}
	defer catalog.Close()

	accounts, err := catalog.ListAccounts()
	if err != nil {
		return fmt.Errorf("list accounts: %w", err)
	}

	if len(accounts) == 0 {
		fmt.Println("No accounts. Run 'lingobridge account new' to add one.")
		return nil
	}

	fmt.Println("Accounts:")
	for _, a := range accounts {
		status := "✓"
		if !a.Enabled {
			status = "✗"
		}
		fmt.Printf("  %s %s [%s] (id: %s)\n", status, a.Name, a.Platform, a.ID)
	}
	return nil
}

func cmdAccountDelete(args []string) error {
	if len(args) < 1 {
		fmt.Println("Usage: lingobridge account delete <name>")
		return errUsage
	}
	name := strings.Join(args, " ")

	registry, err := platform.NewDefaultRegistry()
	if err != nil {
		return err
	}
	catalog, err := openAccountCatalog(registry)
	if err != nil {
		return err
	}
	defer catalog.Close()

	accounts, err := catalog.ListAccounts()
	if err != nil {
		return fmt.Errorf("list accounts: %w", err)
	}

	var target store.Account
	for _, a := range accounts {
		if a.Name == name {
			target = a
			break
		}
	}
	if target.ID == "" {
		return fmt.Errorf("account %q not found", name)
	}

	st, ok := catalog.stores[target.Platform]
	if !ok {
		return fmt.Errorf("store for platform %q is not open", target.Platform)
	}
	if err := st.DeleteAccount(target.ID); err != nil {
		return fmt.Errorf("delete account: %w", err)
	}

	fmt.Printf("Deleted account: %s\n", name)
	notifyRunningProcess()
	return nil
}

func notifyRunningProcess() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := control.NotifyReload(ctx)
	switch {
	case err == nil:
		fmt.Println("Reloaded running lingobridge process.")
	case errors.Is(err, control.ErrUnavailable):
		fmt.Println("No running lingobridge process found; start or restart 'lingobridge run' to pick up changes.")
	default:
		fmt.Printf("Warning: notify running process failed: %v\n", err)
	}
}
