package llm

import (
	"encoding/json"
	"fmt"
	"strings"
)

func toolResultOutput(result ToolResult) string {
	if result.IsError {
		return fmt.Sprintf("ERROR: %s", strings.TrimSpace(result.Content))
	}
	return result.Content
}

func normalizeToolSchema(schema json.RawMessage) json.RawMessage {
	if len(schema) == 0 {
		return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
	}
	return schema
}
