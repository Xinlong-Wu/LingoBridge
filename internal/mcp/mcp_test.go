package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"lingobridge/internal/config"
	tooltypes "lingobridge/internal/tools"
)

type fakeSession struct {
	listResults []*mcpsdk.ListToolsResult
	listErr     error
	listCalls   []*mcpsdk.ListToolsParams
	callResult  *mcpsdk.CallToolResult
	callErr     error
	callParams  []*mcpsdk.CallToolParams
	closed      bool
}

func (f *fakeSession) ListTools(ctx context.Context, params *mcpsdk.ListToolsParams) (*mcpsdk.ListToolsResult, error) {
	f.listCalls = append(f.listCalls, params)
	if f.listErr != nil {
		return nil, f.listErr
	}
	idx := len(f.listCalls) - 1
	if idx >= len(f.listResults) {
		return &mcpsdk.ListToolsResult{}, nil
	}
	return f.listResults[idx], nil
}

func (f *fakeSession) CallTool(ctx context.Context, params *mcpsdk.CallToolParams) (*mcpsdk.CallToolResult, error) {
	f.callParams = append(f.callParams, params)
	if f.callErr != nil {
		return nil, f.callErr
	}
	return f.callResult, nil
}

func (f *fakeSession) Close() error {
	f.closed = true
	return nil
}

func TestHostReloadRegistersPrefixedToolsAndExecutes(t *testing.T) {
	fake := &fakeSession{
		listResults: []*mcpsdk.ListToolsResult{
			{
				NextCursor: "next",
				Tools: []*mcpsdk.Tool{{
					Name:        "Read File",
					Description: "Read a file",
					InputSchema: map[string]any{"type": "object"},
				}},
			},
			{
				Tools: []*mcpsdk.Tool{{
					Name:        "Write-File",
					Description: "Write a file",
					InputSchema: map[string]any{"type": "object"},
				}},
			},
		},
		callResult: &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "file text"}},
		},
	}
	host := &Host{
		connect: func(ctx context.Context, serverID string, server config.MCPServerConfig) (session, error) {
			return fake, nil
		},
		connectTimeout: time.Second,
	}

	err := host.Reload(context.Background(), config.MCPConfig{Servers: map[string]config.MCPServerConfig{
		"files": {Transport: config.MCPTransportStdio, Command: "mcp-files"},
	}})
	if err != nil {
		t.Fatalf("Reload returned error: %v", err)
	}
	tools := host.Resolve(tooltypes.Scope{Platform: "feishu"}).Tools
	if len(tools) != 2 {
		t.Fatalf("tools = %d, want 2", len(tools))
	}
	spec := tools[0].Spec()
	if spec.Name != "mcp_files_read_file" || spec.Description != "Read a file" {
		t.Fatalf("spec = %#v, want prefixed read file tool", spec)
	}
	if !strings.Contains(string(spec.Parameters), `"type":"object"`) {
		t.Fatalf("parameters = %s, want object schema", spec.Parameters)
	}
	if len(fake.listCalls) != 2 || fake.listCalls[1].Cursor != "next" {
		t.Fatalf("list calls = %#v, want paginated next cursor", fake.listCalls)
	}

	result := tools[0].Execute(context.Background(), tooltypes.Call{
		ID:        "call_1",
		Name:      "mcp_files_read_file",
		Arguments: json.RawMessage(`{"path":"/tmp/a.txt"}`),
	})
	if result.IsError || result.Content != "file text" {
		t.Fatalf("result = %#v, want file text", result)
	}
	if len(fake.callParams) != 1 || fake.callParams[0].Name != "Read File" {
		t.Fatalf("call params = %#v, want remote tool name", fake.callParams)
	}
	args := fake.callParams[0].Arguments.(map[string]any)
	if args["path"] != "/tmp/a.txt" {
		t.Fatalf("arguments = %#v, want path", args)
	}
}

func TestHostReloadSkipsFailedServersAndClosesOld(t *testing.T) {
	oldSession := &fakeSession{listResults: []*mcpsdk.ListToolsResult{{Tools: []*mcpsdk.Tool{{Name: "ok"}}}}}
	host := &Host{
		connect: func(ctx context.Context, serverID string, server config.MCPServerConfig) (session, error) {
			if serverID == "bad" {
				return nil, errors.New("boom")
			}
			return oldSession, nil
		},
		connectTimeout: time.Second,
	}
	if err := host.Reload(context.Background(), config.MCPConfig{Servers: map[string]config.MCPServerConfig{
		"good": {Transport: config.MCPTransportStdio, Command: "ok"},
	}}); err != nil {
		t.Fatalf("initial Reload returned error: %v", err)
	}
	if got := len(host.Resolve(tooltypes.Scope{}).Tools); got != 1 {
		t.Fatalf("initial tools = %d, want 1", got)
	}
	if err := host.Reload(context.Background(), config.MCPConfig{Servers: map[string]config.MCPServerConfig{
		"bad": {Transport: config.MCPTransportStdio, Command: "bad"},
	}}); err != nil {
		t.Fatalf("reload with failed server returned error: %v", err)
	}
	if got := len(host.Resolve(tooltypes.Scope{}).Tools); got != 0 {
		t.Fatalf("tools after failed reload = %d, want 0", got)
	}
	if !oldSession.closed {
		t.Fatal("old session was not closed")
	}
}

func TestHostReloadClosesSessionWhenListToolsFails(t *testing.T) {
	badSession := &fakeSession{listErr: errors.New("list boom")}
	host := &Host{
		connect: func(ctx context.Context, serverID string, server config.MCPServerConfig) (session, error) {
			return badSession, nil
		},
		connectTimeout: time.Second,
	}

	if err := host.Reload(context.Background(), config.MCPConfig{Servers: map[string]config.MCPServerConfig{
		"bad": {Transport: config.MCPTransportStdio, Command: "bad"},
	}}); err != nil {
		t.Fatalf("Reload returned error: %v", err)
	}
	if !badSession.closed {
		t.Fatal("failed list session was not closed")
	}
	if got := len(host.Resolve(tooltypes.Scope{}).Tools); got != 0 {
		t.Fatalf("tools = %d, want 0", got)
	}
}

func TestHostReloadSkipsDuplicateExposedToolNames(t *testing.T) {
	fake := &fakeSession{listResults: []*mcpsdk.ListToolsResult{{
		Tools: []*mcpsdk.Tool{
			{Name: "Read File"},
			{Name: "read-file"},
		},
	}}}
	host := &Host{
		connect: func(ctx context.Context, serverID string, server config.MCPServerConfig) (session, error) {
			return fake, nil
		},
		connectTimeout: time.Second,
	}

	if err := host.Reload(context.Background(), config.MCPConfig{Servers: map[string]config.MCPServerConfig{
		"files": {Transport: config.MCPTransportStdio, Command: "ok"},
	}}); err != nil {
		t.Fatalf("Reload returned error: %v", err)
	}
	tools := host.Resolve(tooltypes.Scope{}).Tools
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want duplicate skipped", len(tools))
	}
	if tools[0].Spec().Name != "mcp_files_read_file" {
		t.Fatalf("tool name = %q, want mcp_files_read_file", tools[0].Spec().Name)
	}
}

func TestHostResolveFiltersToolsByScope(t *testing.T) {
	host := &Host{
		connect: func(ctx context.Context, serverID string, server config.MCPServerConfig) (session, error) {
			return &fakeSession{listResults: []*mcpsdk.ListToolsResult{{
				Tools: []*mcpsdk.Tool{{Name: serverID}},
			}}}, nil
		},
		connectTimeout: time.Second,
	}
	if err := host.Reload(context.Background(), config.MCPConfig{Servers: map[string]config.MCPServerConfig{
		"global": {
			Transport: config.MCPTransportStdio,
			Command:   "ok",
		},
		"platform": {
			Transport: config.MCPTransportStdio,
			Command:   "ok",
			Scope:     config.MCPServerScope{Platforms: []string{"feishu"}},
		},
		"account_name": {
			Transport: config.MCPTransportStdio,
			Command:   "ok",
			Scope:     config.MCPServerScope{Accounts: []string{"feishu/admin-bot"}},
		},
		"account_id": {
			Transport: config.MCPTransportStdio,
			Command:   "ok",
			Scope:     config.MCPServerScope{Accounts: []string{"feishu:cli_xxx"}},
		},
	}}); err != nil {
		t.Fatalf("Reload returned error: %v", err)
	}

	admin := host.Resolve(tooltypes.Scope{
		Platform:    "feishu",
		AccountID:   "feishu:cli_xxx",
		AccountName: "admin-bot",
	}).Tools
	if got := len(admin); got != 4 {
		t.Fatalf("admin scoped tools = %d, want 4", got)
	}

	otherFeishu := host.Resolve(tooltypes.Scope{
		Platform:    "feishu",
		AccountID:   "feishu:other",
		AccountName: "other",
	}).Tools
	if got := len(otherFeishu); got != 2 {
		t.Fatalf("other feishu scoped tools = %d, want global + platform", got)
	}

	wechat := host.Resolve(tooltypes.Scope{
		Platform:    "wechat",
		AccountID:   "wechat:test",
		AccountName: "admin-bot",
	}).Tools
	if got := len(wechat); got != 1 {
		t.Fatalf("wechat scoped tools = %d, want global only", got)
	}
}

func TestHostResolveReturnsNoToolsForNonMatchingScope(t *testing.T) {
	host := &Host{
		connect: func(ctx context.Context, serverID string, server config.MCPServerConfig) (session, error) {
			return &fakeSession{listResults: []*mcpsdk.ListToolsResult{{
				Tools: []*mcpsdk.Tool{{Name: "read"}},
			}}}, nil
		},
		connectTimeout: time.Second,
	}
	if err := host.Reload(context.Background(), config.MCPConfig{Servers: map[string]config.MCPServerConfig{
		"feishu_only": {
			Transport: config.MCPTransportStdio,
			Command:   "ok",
			Scope:     config.MCPServerScope{Platforms: []string{"feishu"}},
		},
	}}); err != nil {
		t.Fatalf("Reload returned error: %v", err)
	}

	tools := host.Resolve(tooltypes.Scope{Platform: "wechat"}).Tools
	if len(tools) != 0 {
		t.Fatalf("wechat scoped tools = %d, want no tools", len(tools))
	}
}

func TestResultContentConversion(t *testing.T) {
	structured, err := resultContent(&mcpsdk.CallToolResult{
		StructuredContent: map[string]any{"ok": true},
	})
	if err != nil {
		t.Fatalf("structured content returned error: %v", err)
	}
	if structured != `{"ok":true}` {
		t.Fatalf("structured content = %q, want JSON object", structured)
	}

	mixed, err := resultContent(&mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{
			&mcpsdk.TextContent{Text: "hello"},
			&mcpsdk.ImageContent{MIMEType: "image/png", Data: []byte("abcdef")},
		},
	})
	if err != nil {
		t.Fatalf("mixed content returned error: %v", err)
	}
	if !strings.Contains(mixed, `"type":"image"`) || !strings.Contains(mixed, `"data_base64_chars":6`) || strings.Contains(mixed, "abcdef") {
		t.Fatalf("mixed content = %s, want compact image metadata without raw data", mixed)
	}
}

func TestStaticHeaderRoundTripperAddsHeadersWithoutMutatingRequest(t *testing.T) {
	transport := staticHeaderRoundTripper{
		base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if got := req.Header.Get("Authorization"); got != "Bearer token" {
				t.Fatalf("Authorization = %q, want Bearer token", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("ok")),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		}),
		headers: map[string]string{"Authorization": "Bearer token"},
	}
	req, err := http.NewRequest(http.MethodGet, "https://example.com/mcp", nil)
	if err != nil {
		t.Fatalf("NewRequest returned error: %v", err)
	}

	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip returned error: %v", err)
	}
	_ = resp.Body.Close()
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("original request Authorization = %q, want empty", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
