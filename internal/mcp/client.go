package mcp

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"lingobridge/internal/config"
)

type session interface {
	ListTools(ctx context.Context, params *mcpsdk.ListToolsParams) (*mcpsdk.ListToolsResult, error)
	CallTool(ctx context.Context, params *mcpsdk.CallToolParams) (*mcpsdk.CallToolResult, error)
	Close() error
}

type connector func(ctx context.Context, serverID string, server config.MCPServerConfig) (session, error)

type remoteTool = mcpsdk.Tool

func connectServer(ctx context.Context, serverID string, server config.MCPServerConfig) (session, error) {
	client := mcpsdk.NewClient(&mcpsdk.Implementation{
		Name:    "lingobridge",
		Title:   "LingoBridge",
		Version: "dev",
	}, nil)

	switch server.Transport {
	case config.MCPTransportStdio:
		cmd := exec.Command(server.Command, server.Args...)
		cmd.Env = os.Environ()
		for key, value := range server.Env {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
		if server.CWD != "" {
			cmd.Dir = server.CWD
		}
		return client.Connect(ctx, &mcpsdk.CommandTransport{Command: cmd}, nil)
	case config.MCPTransportStreamableHTTP:
		return client.Connect(ctx, &mcpsdk.StreamableClientTransport{
			Endpoint:             server.URL,
			HTTPClient:           httpClientWithStaticHeaders(server.Headers),
			DisableStandaloneSSE: true,
		}, nil)
	default:
		return nil, errUnsupportedTransport(server.Transport)
	}
}

func listTools(ctx context.Context, sess session) ([]remoteTool, error) {
	var out []remoteTool
	cursor := ""
	for {
		var params *mcpsdk.ListToolsParams
		if cursor != "" {
			params = &mcpsdk.ListToolsParams{Cursor: cursor}
		}
		result, err := sess.ListTools(ctx, params)
		if err != nil {
			return nil, err
		}
		if result != nil {
			for _, tool := range result.Tools {
				if tool != nil {
					out = append(out, *tool)
				}
			}
			cursor = strings.TrimSpace(result.NextCursor)
		} else {
			cursor = ""
		}
		if cursor == "" {
			return out, nil
		}
	}
}

func httpClientWithStaticHeaders(headers map[string]string) *http.Client {
	if len(headers) == 0 {
		return http.DefaultClient
	}
	return &http.Client{
		Transport: staticHeaderRoundTripper{
			base:    http.DefaultTransport,
			headers: headers,
		},
	}
}

type staticHeaderRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t staticHeaderRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	cloned.Header = req.Header.Clone()
	for key, value := range t.headers {
		cloned.Header.Set(key, value)
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(cloned)
}
