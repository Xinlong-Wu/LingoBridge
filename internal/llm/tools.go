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

func toolResultsByCallID(results []tooltypes.Result) map[string]tooltypes.Result {
	out := make(map[string]tooltypes.Result, len(results))
	for _, result := range results {
		callID := strings.TrimSpace(result.CallID)
		if callID == "" {
			callID = strings.TrimSpace(result.Name)
		}
		if callID != "" {
			out[callID] = result
		}
	}
	return out
}

func normalizeToolSchema(schema json.RawMessage) json.RawMessage {
	if len(schema) == 0 {
		return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
	}
	return schema
}
