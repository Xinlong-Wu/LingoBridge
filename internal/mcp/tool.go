package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	tooltypes "lingobridge/internal/tools"
)

type tool struct {
	server      *serverConnection
	remoteName  string
	exposedName string
	spec        tooltypes.Spec
}

func newTool(server *serverConnection, remote remoteTool) (*tool, bool) {
	if server == nil || server.session == nil {
		return nil, false
	}
	remoteName := strings.TrimSpace(remote.Name)
	if remoteName == "" {
		return nil, false
	}
	serverPart := sanitizeToolNamePart(server.id)
	toolPart := sanitizeToolNamePart(remoteName)
	if serverPart == "" || toolPart == "" {
		return nil, false
	}
	exposedName := "mcp_" + serverPart + "_" + toolPart
	return &tool{
		server:      server,
		remoteName:  remoteName,
		exposedName: exposedName,
		spec: tooltypes.Spec{
			Name:        exposedName,
			Description: remote.Description,
			Parameters:  jsonSchemaRaw(remote.InputSchema),
		},
	}, true
}

func (t *tool) Spec() tooltypes.Spec {
	return t.spec
}

func (t *tool) Execute(ctx context.Context, call tooltypes.Call) tooltypes.Result {
	args, err := callArguments(call.Arguments)
	if err != nil {
		return tooltypes.Result{CallID: call.ID, Name: t.exposedName, Content: err.Error(), IsError: true}
	}
	result, err := t.server.session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      t.remoteName,
		Arguments: args,
	})
	if err != nil {
		return tooltypes.Result{
			CallID:  call.ID,
			Name:    t.exposedName,
			Content: fmt.Sprintf("call mcp tool %s/%s: %s", t.server.id, t.remoteName, sanitizeError(err, t.server.redactions)),
			IsError: true,
		}
	}
	content, err := resultContent(result)
	if err != nil {
		return tooltypes.Result{CallID: call.ID, Name: t.exposedName, Content: err.Error(), IsError: true}
	}
	return tooltypes.Result{CallID: call.ID, Name: t.exposedName, Content: content, IsError: result != nil && result.IsError}
}

func callArguments(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("parse arguments: %w", err)
	}
	if args == nil {
		args = map[string]any{}
	}
	return args, nil
}

func sanitizeToolNamePart(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	lastUnderscore := false
	for _, r := range name {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if valid {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if r == '_' || r == '-' || unicode.IsSpace(r) {
			if !lastUnderscore && b.Len() > 0 {
				b.WriteByte('_')
				lastUnderscore = true
			}
			continue
		}
		if !lastUnderscore && b.Len() > 0 {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}
