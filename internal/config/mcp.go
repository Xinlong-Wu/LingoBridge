package config

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
)

const (
	MCPTransportStdio          = "stdio"
	MCPTransportStreamableHTTP = "streamable_http"
)

// MCPConfig holds global Model Context Protocol server configuration.
type MCPConfig struct {
	Servers map[string]MCPServerConfig `yaml:"servers,omitempty"`
}

func (c MCPConfig) IsZero() bool {
	return len(c.Servers) == 0
}

// MCPServerConfig describes one configured MCP server.
type MCPServerConfig struct {
	Enabled   *bool             `yaml:"enabled,omitempty"`
	Transport string            `yaml:"transport,omitempty"`
	Command   string            `yaml:"command,omitempty"`
	Args      []string          `yaml:"args,omitempty"`
	Env       map[string]string `yaml:"env,omitempty"`
	CWD       string            `yaml:"cwd,omitempty"`
	URL       string            `yaml:"url,omitempty"`
	Headers   map[string]string `yaml:"headers,omitempty"`
	Scope     MCPServerScope    `yaml:"scope,omitempty"`
}

// MCPServerScope limits a server's tools to matching platforms or accounts.
type MCPServerScope struct {
	Platforms []string `yaml:"platforms,omitempty"`
	Accounts  []string `yaml:"accounts,omitempty"`
}

// ApplyDefaults normalizes optional MCP configuration fields.
func (c *MCPConfig) ApplyDefaults() {
	if c.Servers == nil {
		c.Servers = map[string]MCPServerConfig{}
	}
	for id, server := range c.Servers {
		server.Transport = strings.TrimSpace(server.Transport)
		server.Command = strings.TrimSpace(server.Command)
		server.CWD = strings.TrimSpace(server.CWD)
		server.URL = strings.TrimSpace(server.URL)
		server.Env = normalizeStringMap(server.Env)
		server.Headers = normalizeStringMap(server.Headers)
		server.Scope.ApplyDefaults()
		c.Servers[id] = server
	}
}

// Validate checks that enabled MCP servers are complete and usable.
func (c MCPConfig) Validate() error {
	for _, id := range c.ServerNames() {
		if err := ValidatePlatformID(id); err != nil {
			return fmt.Errorf("mcp.servers.%s: %w", id, err)
		}
		server := c.Servers[id]
		if !server.IsEnabled() {
			continue
		}
		if err := server.Scope.Validate(); err != nil {
			return fmt.Errorf("mcp.servers.%s.scope: %w", id, err)
		}
		switch server.Transport {
		case MCPTransportStdio:
			if strings.TrimSpace(server.Command) == "" {
				return fmt.Errorf("mcp.servers.%s.command is required for stdio transport", id)
			}
		case MCPTransportStreamableHTTP:
			rawURL := strings.TrimSpace(server.URL)
			if rawURL == "" {
				return fmt.Errorf("mcp.servers.%s.url is required for streamable_http transport", id)
			}
			parsed, err := url.Parse(rawURL)
			if err != nil || parsed.Scheme == "" || parsed.Host == "" {
				return fmt.Errorf("mcp.servers.%s.url must be an absolute HTTP(S) URL", id)
			}
			if parsed.Scheme != "http" && parsed.Scheme != "https" {
				return fmt.Errorf("mcp.servers.%s.url must use http or https", id)
			}
		default:
			return fmt.Errorf("mcp.servers.%s.transport must be stdio or streamable_http", id)
		}
	}
	return nil
}

// IsEnabled reports whether a server should be started. Omitted enabled means true.
func (s MCPServerConfig) IsEnabled() bool {
	return s.Enabled == nil || *s.Enabled
}

// ApplyDefaults normalizes optional MCP server scope fields.
func (s *MCPServerScope) ApplyDefaults() {
	s.Platforms = normalizeStringSlice(s.Platforms)
	s.Accounts = normalizeStringSlice(s.Accounts)
}

// IsZero reports whether the scope is omitted. Omitted scope means global.
func (s MCPServerScope) IsZero() bool {
	return len(s.Platforms) == 0 && len(s.Accounts) == 0
}

// Validate checks scope selectors.
func (s MCPServerScope) Validate() error {
	for i, platform := range s.Platforms {
		if strings.TrimSpace(platform) == "" {
			return fmt.Errorf("platforms[%d] must not be empty", i)
		}
		if err := ValidatePlatformID(platform); err != nil {
			return fmt.Errorf("platforms[%d]: %w", i, err)
		}
	}
	for i, account := range s.Accounts {
		account = strings.TrimSpace(account)
		if account == "" {
			return fmt.Errorf("accounts[%d] must not be empty", i)
		}
		if platform, name, ok := strings.Cut(account, "/"); ok {
			if strings.TrimSpace(platform) == "" || strings.TrimSpace(name) == "" {
				return fmt.Errorf("accounts[%d] must be platform/name or account_id", i)
			}
			if err := ValidatePlatformID(platform); err != nil {
				return fmt.Errorf("accounts[%d]: %w", i, err)
			}
		}
	}
	return nil
}

// ServerNames returns configured MCP server names in stable order.
func (c MCPConfig) ServerNames() []string {
	names := make([]string, 0, len(c.Servers))
	for name := range c.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func normalizeStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = strings.TrimSpace(value)
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
		if key == "" {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
