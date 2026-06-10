package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"lingobridge/internal/config"
)

const errorTextLimit = 1024

func resultContent(result *mcpsdk.CallToolResult) (string, error) {
	if result == nil {
		return "", nil
	}
	if result.StructuredContent != nil {
		data, err := json.Marshal(result.StructuredContent)
		if err != nil {
			return "", fmt.Errorf("marshal mcp structured content: %w", err)
		}
		return string(data), nil
	}
	if len(result.Content) == 0 {
		if result.IsError {
			return "mcp tool returned an error without content", nil
		}
		return "", nil
	}
	if text, ok := textOnlyContent(result.Content); ok {
		return text, nil
	}
	summary := make([]map[string]any, 0, len(result.Content))
	for _, content := range result.Content {
		summary = append(summary, summarizeContent(content))
	}
	data, err := json.Marshal(summary)
	if err != nil {
		return "", fmt.Errorf("marshal mcp content: %w", err)
	}
	return string(data), nil
}

func textOnlyContent(contents []mcpsdk.Content) (string, bool) {
	parts := make([]string, 0, len(contents))
	for _, content := range contents {
		text, ok := content.(*mcpsdk.TextContent)
		if !ok {
			return "", false
		}
		parts = append(parts, text.Text)
	}
	return strings.Join(parts, "\n"), true
}

func summarizeContent(content mcpsdk.Content) map[string]any {
	switch c := content.(type) {
	case *mcpsdk.TextContent:
		return map[string]any{"type": "text", "text": c.Text}
	case *mcpsdk.ImageContent:
		return map[string]any{"type": "image", "mime_type": c.MIMEType, "data_base64_chars": len(c.Data)}
	case *mcpsdk.AudioContent:
		return map[string]any{"type": "audio", "mime_type": c.MIMEType, "data_base64_chars": len(c.Data)}
	case *mcpsdk.ResourceLink:
		item := map[string]any{"type": "resource_link", "uri": c.URI, "name": c.Name, "title": c.Title, "mime_type": c.MIMEType}
		if c.Size != nil {
			item["size"] = *c.Size
		}
		return item
	case *mcpsdk.EmbeddedResource:
		item := map[string]any{"type": "resource"}
		if c.Resource != nil {
			item["uri"] = c.Resource.URI
			item["mime_type"] = c.Resource.MIMEType
			if c.Resource.Text != "" {
				item["text"] = c.Resource.Text
			}
			if len(c.Resource.Blob) > 0 {
				item["blob_base64_chars"] = len(c.Resource.Blob)
			}
		}
		return item
	default:
		data, err := content.MarshalJSON()
		if err != nil {
			return map[string]any{"type": "unknown"}
		}
		var item map[string]any
		if err := json.Unmarshal(data, &item); err != nil {
			return map[string]any{"type": "unknown"}
		}
		return item
	}
}

func jsonSchemaRaw(schema any) json.RawMessage {
	if schema == nil {
		return nil
	}
	data, err := json.Marshal(schema)
	if err != nil {
		return nil
	}
	return json.RawMessage(data)
}

func sanitizeConfigError(err error, server config.MCPServerConfig) string {
	return sanitizeError(err, redactionsForConfig(server))
}

func sanitizeError(err error, redactions []string) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	for _, value := range redactions {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		text = strings.ReplaceAll(text, value, "<redacted>")
	}
	return truncateText(text, errorTextLimit)
}

func redactionsForConfig(server config.MCPServerConfig) []string {
	values := make([]string, 0, 1+len(server.Headers)+len(server.Env))
	values = append(values, server.URL)
	for _, value := range server.Headers {
		values = append(values, value)
	}
	for _, value := range server.Env {
		values = append(values, value)
	}
	return values
}

func truncateText(text string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	if limit <= 16 {
		return string(runes[:limit])
	}
	return string(runes[:limit-14]) + "\n[truncated]"
}

func errUnsupportedTransport(transport string) error {
	return errors.New("unsupported mcp transport " + transport)
}
