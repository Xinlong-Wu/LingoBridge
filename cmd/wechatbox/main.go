package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"

	"wechatbox/internal/config"
	"wechatbox/internal/llm"
	"wechatbox/internal/session"
	"wechatbox/internal/store"
	"wechatbox/internal/wechat/login"
	"wechatbox/internal/wechat/monitor"
)

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
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]

	switch cmd {
	case "run":
		cmdRun()
	case "account":
		if len(os.Args) < 3 {
			fmt.Println("Usage: wechatbox account <new|list|delete> [--name <name>]")
			os.Exit(1)
		}
		switch os.Args[2] {
		case "new":
			cmdAccountNew()
		case "list":
			cmdAccountList()
		case "delete":
			cmdAccountDelete()
		default:
			fmt.Println("Usage: wechatbox account <new|list|delete> [--name <name>]")
			os.Exit(1)
		}
	default:
		printUsage()
		os.Exit(1)
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

func cmdAccountNew() {
	// Parse optional --name flag.
	accountName := ""
	args := os.Args
	argStart := 3 // skip program + "account" + "new"

	for i := argStart; i < len(args); i++ {
		if args[i] == "--name" && i+1 < len(args) {
			accountName = args[i+1]
			i++
		}
	}

	if accountName == "" {
		accountName = "default"
	}

	st, err := store.Open()
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	// Auto-suffix if name already exists
	accounts, err := st.ListAccounts()
	if err != nil {
		log.Fatalf("list accounts: %v", err)
	}
	exists := func(name string) bool {
		for _, a := range accounts {
			if a.Name == name {
				return true
			}
		}
		return false
	}

	originalName := accountName
	suffix := 2
	for exists(accountName) {
		accountName = fmt.Sprintf("%s-%d", originalName, suffix)
		suffix++
	}
	if accountName != originalName {
		fmt.Printf("Name %q already exists, using %q instead.\n", originalName, accountName)
	}

	if err := login.Login(st, accountName); err != nil {
		log.Fatalf("login failed: %v", err)
	}
}

func cmdRun() {
	// Parse optional --account flag
	targetAccount := ""
	for i := 2; i < len(os.Args); i++ {
		if os.Args[i] == "--account" && i+1 < len(os.Args) {
			targetAccount = os.Args[i+1]
			i++
		}
	}

	// Load config
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	logBuildInfo()

	// Open store
	st, err := store.Open()
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	// List accounts
	accounts, err := st.ListAccounts()
	if err != nil {
		log.Fatalf("list accounts: %v", err)
	}

	enabledAccounts := make([]store.Account, 0)
	for _, a := range accounts {
		if a.Enabled {
			enabledAccounts = append(enabledAccounts, a)
		}
	}

	if len(enabledAccounts) == 0 {
		log.Fatal("No enabled accounts. Run 'wechatbox account new' first.")
	}

	// Filter by --account if specified
	if targetAccount != "" {
		filtered := make([]store.Account, 0)
		for _, a := range enabledAccounts {
			if a.Name == targetAccount {
				filtered = append(filtered, a)
			}
		}
		if len(filtered) == 0 {
			log.Fatalf("Account %q not found. Use 'wechatbox account list' to see available accounts.", targetAccount)
		}
		enabledAccounts = filtered
	}

	// Create LLM client
	llmClient := llm.NewClient(llm.Config{
		Provider: cfg.LLM.Provider,
		BaseURL:  cfg.LLM.BaseURL,
		APIKey:   cfg.LLM.APIKey,
		Model:    cfg.LLM.Model,
		Endpoint: cfg.LLM.Endpoint,
	})

	// Create session manager
	sm := session.NewManager(st)

	log.Printf("LLM provider: %s", cfg.LLM.Provider)
	log.Printf("LLM base_url: %s", cfg.LLM.BaseURL)
	log.Printf("LLM model: %s", cfg.LLM.Model)
	log.Printf("LLM max_history: %d", cfg.LLM.MaxHistory)
	log.Printf("LLM system_prompt: %s", cfg.LLM.SystemPrompt)
	log.Printf("LLM endpoint: %s", cfg.LLM.Endpoint)

	// Run all enabled accounts concurrently
	var wg sync.WaitGroup
	for _, acc := range enabledAccounts {
		wg.Add(1)
		go func(a store.Account) {
			defer wg.Done()
			log.Printf("Starting bot for account: %s (%s)", a.Name, a.ID)
			if err := monitor.Run(st, sm, llmClient, cfg.LLM, a); err != nil {
				log.Printf("monitor for %s exited: %v", a.Name, err)
			}
		}(acc)
	}

	// Handle signals for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("Shutting down...")
		os.Exit(0)
	}()

	log.Printf("Listening on %d account(s)...", len(enabledAccounts))
	wg.Wait()
}

func cmdAccountList() {
	st, err := store.Open()
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	accounts, err := st.ListAccounts()
	if err != nil {
		log.Fatalf("list accounts: %v", err)
	}

	if len(accounts) == 0 {
		fmt.Println("No accounts. Run 'wechatbox account new' to add one.")
		return
	}

	fmt.Println("Accounts:")
	for _, a := range accounts {
		status := "✓"
		if !a.Enabled {
			status = "✗"
		}
		fmt.Printf("  %s %s (id: %s)\n", status, a.Name, a.ID)
	}
}

func cmdAccountDelete() {
	if len(os.Args) < 4 {
		fmt.Println("Usage: wechatbox account delete <name>")
		os.Exit(1)
	}
	name := strings.Join(os.Args[3:], " ")

	st, err := store.Open()
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	// Find account by name
	accounts, err := st.ListAccounts()
	if err != nil {
		log.Fatalf("list accounts: %v", err)
	}

	var targetID string
	for _, a := range accounts {
		if a.Name == name {
			targetID = a.ID
			break
		}
	}
	if targetID == "" {
		log.Fatalf("Account %q not found.", name)
	}

	if err := st.DeleteAccount(targetID); err != nil {
		log.Fatalf("delete account: %v", err)
	}

	fmt.Printf("Deleted account: %s\n", name)
}
