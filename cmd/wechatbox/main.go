package main

import (
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
	"syscall"
	"time"

	"wechatbox/internal/config"
	"wechatbox/internal/control"
	"wechatbox/internal/runner"
	"wechatbox/internal/session"
	"wechatbox/internal/store"
	"wechatbox/internal/wechat/login"
	"wechatbox/internal/wechat/monitor"
)

var errUsage = errors.New("usage")

func logBuildInfo() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		log.Printf("WeChatBox dev go/%s", runtime.Version()[2:])
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
		log.Printf("WeChatBox %s (%s) go/%s", version, vcsTime, runtime.Version()[2:])
	} else {
		log.Printf("WeChatBox %s go/%s", version, runtime.Version()[2:])
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

	switch args[0] {
	case "run":
		return cmdRun(args[1:])
	case "account":
		return cmdAccount(args[1:])
	default:
		printUsage()
		return errUsage
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

func printUsage() {
	fmt.Println("WeChatBox - WeChat Bot → LLM Direct Bridge")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  wechatbox account new [--name <name>]   Add a WeChat bot account via QR login")
	fmt.Println("  wechatbox account list                   List all accounts")
	fmt.Println("  wechatbox account delete <name>          Delete an account")
	fmt.Println("  wechatbox run [--account <name>]         Start the bot loop")
}

func printAccountUsage() {
	fmt.Println("Usage: wechatbox account <new|list|delete> [--name <name>]")
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func openStore() (*store.Store, error) {
	st, err := store.Open()
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	return st, nil
}

func cmdAccountNew(args []string) error {
	fs := newFlagSet("account new")
	accountName := fs.String("name", "default", "account name")
	if err := fs.Parse(args); err != nil {
		fmt.Println("Usage: wechatbox account new [--name <name>]")
		return errUsage
	}
	if *accountName == "" {
		*accountName = "default"
	}

	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	accounts, err := st.ListAccounts()
	if err != nil {
		return fmt.Errorf("list accounts: %w", err)
	}
	exists := func(name string) bool {
		for _, a := range accounts {
			if a.Name == name {
				return true
			}
		}
		return false
	}

	originalName := *accountName
	suffix := 2
	for exists(*accountName) {
		*accountName = fmt.Sprintf("%s-%d", originalName, suffix)
		suffix++
	}
	if *accountName != originalName {
		fmt.Printf("Name %q already exists, using %q instead.\n", originalName, *accountName)
	}

	if err := login.Login(st, *accountName); err != nil {
		return fmt.Errorf("login failed: %w", err)
	}
	notifyRunningProcess()
	return nil
}

func cmdRun(args []string) error {
	fs := newFlagSet("run")
	targetAccount := fs.String("account", "", "account name")
	if err := fs.Parse(args); err != nil {
		fmt.Println("Usage: wechatbox run [--account <name>]")
		return errUsage
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := cfg.LLM.Validate(); err != nil {
		return fmt.Errorf("validate llm config: %w", err)
	}

	logBuildInfo()

	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	resetCount, err := st.ResetUnavailableUserModels(cfg.LLM.DefaultModel, cfg.LLM.ModelNames())
	if err != nil {
		return fmt.Errorf("reset unavailable user models: %w", err)
	}
	if resetCount > 0 {
		log.Printf("Reset %d user model preference(s) to default model %q", resetCount, cfg.LLM.DefaultModel)
	}

	sm := session.NewManager(st, cfg.LLM)

	log.Printf("LLM default_model: %s", cfg.LLM.DefaultModel)
	log.Printf("LLM models: %s", strings.Join(cfg.LLM.ModelNames(), ", "))
	log.Printf("LLM max_history: %d", cfg.LLM.MaxHistory)
	log.Printf("LLM system_prompt: %s", cfg.LLM.SystemPrompt)

	runCtx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	supervisor := runner.NewSupervisor(st, func(ctx context.Context, acc store.Account) error {
		return monitor.RunContext(ctx, st, sm, cfg.LLM, acc)
	}, *targetAccount)

	controlServer, err := control.StartServer(runCtx, func(context.Context) error {
		return supervisor.Reconcile(runCtx)
	})
	if err != nil {
		if errors.Is(err, control.ErrAlreadyRunning) {
			return fmt.Errorf("another wechatbox run process is already active")
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
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	accounts, err := st.ListAccounts()
	if err != nil {
		return fmt.Errorf("list accounts: %w", err)
	}

	if len(accounts) == 0 {
		fmt.Println("No accounts. Run 'wechatbox account new' to add one.")
		return nil
	}

	fmt.Println("Accounts:")
	for _, a := range accounts {
		status := "✓"
		if !a.Enabled {
			status = "✗"
		}
		fmt.Printf("  %s %s (id: %s)\n", status, a.Name, a.ID)
	}
	return nil
}

func cmdAccountDelete(args []string) error {
	if len(args) < 1 {
		fmt.Println("Usage: wechatbox account delete <name>")
		return errUsage
	}
	name := strings.Join(args, " ")

	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	accounts, err := st.ListAccounts()
	if err != nil {
		return fmt.Errorf("list accounts: %w", err)
	}

	var targetID string
	for _, a := range accounts {
		if a.Name == name {
			targetID = a.ID
			break
		}
	}
	if targetID == "" {
		return fmt.Errorf("account %q not found", name)
	}

	if err := st.DeleteAccount(targetID); err != nil {
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
		fmt.Println("Reloaded running wechatbox process.")
	case errors.Is(err, control.ErrUnavailable):
		fmt.Println("No running wechatbox process found; start or restart 'wechatbox run' to pick up changes.")
	default:
		fmt.Printf("Warning: notify running process failed: %v\n", err)
	}
}
