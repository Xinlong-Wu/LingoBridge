package github

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"lingobridge/internal/core"
	"lingobridge/internal/store"
)

const (
	DefaultBaseURL      = "https://api.github.com"
	DefaultWebURL       = "https://github.com"
	DefaultPollInterval = 2 * time.Minute
	DefaultToolTimeout  = 30 * time.Second
	DefaultMaxToolCalls = 30
	DefaultResultLimit  = 60000

	reviewInstructionsPath = ".github/review_instructions.md"
)

type Config struct {
	Accounts map[string]AccountConfig `yaml:"accounts"`
}

type AccountConfig struct {
	AppID          string       `yaml:"app_id"`
	InstallationID string       `yaml:"installation_id"`
	PrivateKeyPath string       `yaml:"private_key_path"`
	BaseURL        string       `yaml:"base_url,omitempty"`
	WebURL         string       `yaml:"web_url,omitempty"`
	PollInterval   Duration     `yaml:"poll_interval,omitempty"`
	Repositories   []string     `yaml:"repositories,omitempty"`
	Review         ReviewConfig `yaml:"review,omitempty"`
	MCP            MCPConfig    `yaml:"mcp,omitempty"`
}

type ReviewConfig struct {
	MaxToolCalls        int      `yaml:"max_tool_calls,omitempty"`
	ToolTimeout         Duration `yaml:"tool_timeout,omitempty"`
	ToolResultLimit     int      `yaml:"tool_result_limit,omitempty"`
	DefaultInstructions string   `yaml:"default_instructions,omitempty"`
}

type MCPConfig struct {
	Command string            `yaml:"command,omitempty"`
	Args    []string          `yaml:"args,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`
	CWD     string            `yaml:"cwd,omitempty"`
}

type Repository struct {
	Owner string
	Name  string
}

func LoadConfig(api core.PlatformConfigAPI) (Config, error) {
	var cfg Config
	if err := api.GetPlatformConfig(store.PlatformGitHub, &cfg); err != nil {
		if errors.Is(err, core.ErrPlatformConfigNotFound) {
			cfg.ApplyDefaults()
			return cfg, nil
		}
		return Config{}, err
	}
	cfg.ApplyDefaults()
	return cfg, nil
}

func UpsertAccountConfig(api core.PlatformConfigAPI, name string, account AccountConfig) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("github account name is required")
	}
	account = normalizeAccountConfig(account)
	if err := validateAccountSetup(name, account); err != nil {
		return err
	}
	cfg, err := LoadConfig(api)
	if err != nil {
		return err
	}
	if cfg.Accounts == nil {
		cfg.Accounts = map[string]AccountConfig{}
	}
	cfg.Accounts[name] = account
	return api.SetPlatformConfig(store.PlatformGitHub, cfg)
}

func DeleteAccountConfig(api core.PlatformConfigAPI, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("github account name is required")
	}
	var cfg Config
	if err := api.GetPlatformConfig(store.PlatformGitHub, &cfg); err != nil {
		if errors.Is(err, core.ErrPlatformConfigNotFound) {
			return nil
		}
		return err
	}
	if cfg.Accounts == nil {
		return nil
	}
	if _, ok := cfg.Accounts[name]; !ok {
		return nil
	}
	delete(cfg.Accounts, name)
	cfg.ApplyDefaults()
	return api.SetPlatformConfig(store.PlatformGitHub, cfg)
}

func ResolveAccountConfig(api core.PlatformConfigAPI, name string) (AccountConfig, bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return AccountConfig{}, false, nil
	}
	cfg, err := LoadConfig(api)
	if err != nil {
		return AccountConfig{}, false, err
	}
	account, ok := cfg.Accounts[name]
	if !ok {
		return AccountConfig{}, false, nil
	}
	account = normalizeAccountConfig(account)
	if err := validateAccountSetup(name, account); err != nil {
		return account, true, err
	}
	return account, true, nil
}

func NewAccount(name, appID, installationID, privateKeyPath, baseURL string) (store.Account, error) {
	account := normalizeAccountConfig(AccountConfig{
		AppID:          appID,
		InstallationID: installationID,
		PrivateKeyPath: privateKeyPath,
		BaseURL:        baseURL,
	})
	if account.AppID == "" {
		return store.Account{}, fmt.Errorf("github app_id is required")
	}
	if account.InstallationID == "" {
		return store.Account{}, fmt.Errorf("github installation_id is required")
	}
	if account.PrivateKeyPath == "" {
		return store.Account{}, fmt.Errorf("github private_key_path is required")
	}
	return store.Account{
		ID:              "github:" + account.InstallationID,
		Name:            strings.TrimSpace(name),
		Platform:        store.PlatformGitHub,
		BaseURL:         account.BaseURL,
		UserID:          account.InstallationID,
		CredentialsJSON: "{}",
		Enabled:         true,
	}, nil
}

func (c *Config) ApplyDefaults() {
	if c.Accounts == nil {
		c.Accounts = map[string]AccountConfig{}
	}
	for name, account := range c.Accounts {
		c.Accounts[name] = normalizeAccountConfig(account)
	}
}

func normalizeAccountConfig(account AccountConfig) AccountConfig {
	account.AppID = strings.TrimSpace(account.AppID)
	account.InstallationID = strings.TrimSpace(account.InstallationID)
	account.PrivateKeyPath = strings.TrimSpace(account.PrivateKeyPath)
	account.BaseURL = strings.TrimRight(strings.TrimSpace(account.BaseURL), "/")
	if account.BaseURL == "" {
		account.BaseURL = DefaultBaseURL
	}
	account.WebURL = strings.TrimRight(strings.TrimSpace(account.WebURL), "/")
	if account.WebURL == "" {
		account.WebURL = DefaultWebURL
	}
	if account.PollInterval.Duration <= 0 {
		account.PollInterval = NewDuration(DefaultPollInterval)
	}
	account.Repositories = normalizeRepositoryList(account.Repositories)
	account.Review = normalizeReviewConfig(account.Review)
	account.MCP.Command = strings.TrimSpace(account.MCP.Command)
	account.MCP.Args = normalizeStringList(account.MCP.Args)
	account.MCP.CWD = strings.TrimSpace(account.MCP.CWD)
	account.MCP.Env = normalizeStringMap(account.MCP.Env)
	return account
}

func normalizeReviewConfig(review ReviewConfig) ReviewConfig {
	review.DefaultInstructions = strings.TrimSpace(review.DefaultInstructions)
	if review.MaxToolCalls <= 0 {
		review.MaxToolCalls = DefaultMaxToolCalls
	}
	if review.ToolTimeout.Duration <= 0 {
		review.ToolTimeout = NewDuration(DefaultToolTimeout)
	}
	if review.ToolResultLimit <= 0 {
		review.ToolResultLimit = DefaultResultLimit
	}
	return review
}

func validateAccountSetup(name string, account AccountConfig) error {
	if account.AppID == "" {
		return fmt.Errorf("platforms.github.accounts.%s.app_id is required", name)
	}
	if account.InstallationID == "" {
		return fmt.Errorf("platforms.github.accounts.%s.installation_id is required", name)
	}
	if account.PrivateKeyPath == "" {
		return fmt.Errorf("platforms.github.accounts.%s.private_key_path is required", name)
	}
	if len(account.Repositories) == 0 {
		return fmt.Errorf("platforms.github.accounts.%s.repositories must include at least one owner/repo", name)
	}
	for i, repo := range account.Repositories {
		if _, err := ParseRepository(repo); err != nil {
			return fmt.Errorf("platforms.github.accounts.%s.repositories[%d]: %w", name, i, err)
		}
	}
	return nil
}

func validateAccountRuntime(name string, account AccountConfig) error {
	if err := validateAccountSetup(name, account); err != nil {
		return err
	}
	if account.MCP.Command == "" {
		return fmt.Errorf("platforms.github.accounts.%s.mcp.command is required", name)
	}
	if len(account.MCP.Args) == 0 {
		return fmt.Errorf("platforms.github.accounts.%s.mcp.args is required", name)
	}
	return nil
}

func ParseRepository(value string) (Repository, error) {
	value = strings.TrimSpace(value)
	owner, repo, ok := strings.Cut(value, "/")
	if !ok || strings.TrimSpace(owner) == "" || strings.TrimSpace(repo) == "" || strings.Contains(repo, "/") {
		return Repository{}, fmt.Errorf("repository must be owner/repo")
	}
	return Repository{Owner: strings.TrimSpace(owner), Name: strings.TrimSpace(repo)}, nil
}

func (r Repository) FullName() string {
	if r.Owner == "" || r.Name == "" {
		return ""
	}
	return r.Owner + "/" + r.Name
}

func normalizeRepositoryList(values []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		repo, err := ParseRepository(value)
		if err != nil {
			value = strings.TrimSpace(value)
		} else {
			value = repo.FullName()
		}
		if value == "" || seen[value] {
			continue
		}
		out = append(out, value)
		seen[value] = true
	}
	return out
}

func normalizeStringList(values []string) []string {
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func normalizeStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := map[string]string{}
	for key, value := range values {
		key = strings.TrimSpace(key)
		if key != "" {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
