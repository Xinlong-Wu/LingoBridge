package mcp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"lingobridge/internal/config"
	"lingobridge/internal/logging"
	tooltypes "lingobridge/internal/tools"
)

const defaultConnectTimeout = 15 * time.Second

var hostLog = logging.For("mcp")

// Host owns configured MCP client sessions and exposes their tools to core.
type Host struct {
	mu             sync.RWMutex
	connect        connector
	connectTimeout time.Duration
	servers        map[string]*serverConnection
	tools          []tooltypes.Tool
}

type serverConnection struct {
	id         string
	session    session
	redactions []string
}

// NewHost creates a config-driven MCP host.
func NewHost() *Host {
	return &Host{
		connect:        connectServer,
		connectTimeout: defaultConnectTimeout,
	}
}

// Reload replaces MCP server sessions with the servers described by cfg.
func (h *Host) Reload(ctx context.Context, cfg config.MCPConfig) error {
	if h == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return err
	}

	nextServers := map[string]*serverConnection{}
	var nextTools []tooltypes.Tool
	seenTools := map[string]bool{}

	for _, serverID := range cfg.ServerNames() {
		if err := ctx.Err(); err != nil {
			_ = closeServers(nextServers)
			return err
		}
		serverCfg := cfg.Servers[serverID]
		if !serverCfg.IsEnabled() {
			hostLog.Debug(ctx, "mcp server disabled server=%s", serverID)
			continue
		}

		conn, remoteTools, ok, err := h.loadServer(ctx, serverID, serverCfg)
		if err != nil {
			_ = closeServers(nextServers)
			return err
		}
		if !ok {
			continue
		}

		registered := 0
		for _, remote := range remoteTools {
			tool, ok := newTool(conn, remote)
			if !ok {
				hostLog.Warn(ctx, "skipping invalid mcp tool server=%s", serverID)
				continue
			}
			name := tool.Spec().Name
			if seenTools[name] {
				hostLog.Warn(ctx, "skipping duplicate mcp tool name=%s server=%s remote=%s", name, serverID, remote.Name)
				continue
			}
			seenTools[name] = true
			nextTools = append(nextTools, tool)
			registered++
		}
		nextServers[serverID] = conn
		hostLog.Info(ctx, "registered mcp server server=%s transport=%s tools=%d", serverID, serverCfg.Transport, registered)
	}

	if err := ctx.Err(); err != nil {
		_ = closeServers(nextServers)
		return err
	}
	oldServers := h.swap(nextServers, nextTools)
	if err := closeServers(oldServers); err != nil {
		hostLog.Warn(ctx, "close old mcp sessions failed: %v", err)
	}
	hostLog.Info(ctx, "mcp host ready servers=%d tools=%d", len(nextServers), len(nextTools))
	return nil
}

func (h *Host) loadServer(ctx context.Context, serverID string, serverCfg config.MCPServerConfig) (*serverConnection, []remoteTool, bool, error) {
	start := time.Now()
	connectCtx, cancel := context.WithTimeout(ctx, h.effectiveConnectTimeout())
	defer cancel()

	sess, err := h.connector()(connectCtx, serverID, serverCfg)
	if err != nil {
		if parentErr := ctx.Err(); parentErr != nil {
			return nil, nil, false, parentErr
		}
		hostLog.Warn(ctx, "mcp server unavailable server=%s transport=%s error=%s", serverID, serverCfg.Transport, sanitizeConfigError(err, serverCfg))
		return nil, nil, false, nil
	}

	remoteTools, err := listTools(connectCtx, sess)
	if err != nil {
		if parentErr := ctx.Err(); parentErr != nil {
			if closeErr := sess.Close(); closeErr != nil {
				hostLog.Warn(ctx, "close canceled mcp session failed server=%s error=%s", serverID, sanitizeConfigError(closeErr, serverCfg))
			}
			return nil, nil, false, parentErr
		}
		hostLog.Warn(ctx, "mcp tool list failed server=%s transport=%s error=%s", serverID, serverCfg.Transport, sanitizeConfigError(err, serverCfg))
		if closeErr := sess.Close(); closeErr != nil {
			hostLog.Warn(ctx, "close unavailable mcp session failed server=%s error=%s", serverID, sanitizeConfigError(closeErr, serverCfg))
		}
		return nil, nil, false, nil
	}

	hostLog.Debug(ctx, "mcp server loaded server=%s transport=%s remote_tools=%d duration_ms=%d", serverID, serverCfg.Transport, len(remoteTools), time.Since(start).Milliseconds())
	return &serverConnection{
		id:         serverID,
		session:    sess,
		redactions: redactionsForConfig(serverCfg),
	}, remoteTools, true, nil
}

// Tools returns the current MCP tool snapshot.
func (h *Host) Tools() []tooltypes.Tool {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return append([]tooltypes.Tool(nil), h.tools...)
}

// ToolOptions intentionally leaves core tool loop defaults in effect.
func (h *Host) ToolOptions() tooltypes.Options {
	return tooltypes.Options{}
}

// Close closes all active MCP sessions.
func (h *Host) Close(ctx context.Context) error {
	if h == nil {
		return nil
	}
	oldServers := h.swap(nil, nil)
	err := closeServers(oldServers)
	if err != nil {
		hostLog.Warn(ctx, "close mcp host failed: %v", err)
		return err
	}
	if len(oldServers) > 0 {
		hostLog.Info(ctx, "mcp host closed servers=%d", len(oldServers))
	}
	return nil
}

func (h *Host) connector() connector {
	if h.connect != nil {
		return h.connect
	}
	return connectServer
}

func (h *Host) effectiveConnectTimeout() time.Duration {
	if h.connectTimeout > 0 {
		return h.connectTimeout
	}
	return defaultConnectTimeout
}

func (h *Host) swap(servers map[string]*serverConnection, tools []tooltypes.Tool) map[string]*serverConnection {
	h.mu.Lock()
	defer h.mu.Unlock()
	old := h.servers
	h.servers = servers
	h.tools = tools
	return old
}

func closeServers(servers map[string]*serverConnection) error {
	var errs []error
	for _, server := range servers {
		if server == nil || server.session == nil {
			continue
		}
		if err := server.session.Close(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", server.id, err))
		}
	}
	return errors.Join(errs...)
}
