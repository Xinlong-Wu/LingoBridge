package llm

import (
	"encoding/json"
	"fmt"
	"strings"

	tooltypes "lingobridge/internal/tools"
)

func toolResultOutput(result tooltypes.Result) string {
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
