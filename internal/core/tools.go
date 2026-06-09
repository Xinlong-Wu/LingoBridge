package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"lingobridge/internal/commands"
	"lingobridge/internal/llm"
	"lingobridge/internal/store"
)

const (
	defaultMaxToolCalls       = 5
	defaultToolTimeout        = 15 * time.Second
	defaultToolResultLimit    = 12000
	defaultToolTraceTextLimit = 1024
)

type ToolSpec = llm.ToolSpec
type ToolCall = llm.ToolCall
type ToolResult = llm.ToolResult
type ToolTrace = store.ToolTrace

// Tool is a platform-provided function that can be exposed to a tool-capable LLM.
type Tool interface {
	Spec() ToolSpec
	Execute(ctx context.Context, call ToolCall) ToolResult
}

func toolSpecs(tools []Tool) []llm.ToolSpec {
	specs := make([]llm.ToolSpec, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		spec := tool.Spec()
		spec.Name = strings.TrimSpace(spec.Name)
		if spec.Name == "" {
			continue
		}
		specs = append(specs, spec)
	}
	return specs
}

func toolMap(tools []Tool) map[string]Tool {
	out := map[string]Tool{}
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		name := strings.TrimSpace(tool.Spec().Name)
		if name != "" {
			out[name] = tool
		}
	}
	return out
}

func commandToolSummaries(tools []Tool) []commands.ToolSummary {
	summaries := make([]commands.ToolSummary, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		spec := tool.Spec()
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			continue
		}
		summaries = append(summaries, commands.ToolSummary{
			Name:        name,
			Description: spec.Description,
		})
	}
	return summaries
}

func commandName(text string) string {
	parts := strings.Fields(strings.TrimSpace(text))
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

func runTool(ctx context.Context, tool Tool, call ToolCall, timeout time.Duration, resultLimit int) (llm.ToolResult, store.ToolTrace) {
	if timeout <= 0 {
		timeout = defaultToolTimeout
	}
	if resultLimit <= 0 {
		resultLimit = defaultToolResultLimit
	}

	start := time.Now()
	trace := store.ToolTrace{
		CallID:    call.ID,
		Name:      call.Name,
		Status:    "ok",
		Arguments: summarizeJSON(call.Arguments, defaultToolTraceTextLimit),
	}

	if tool == nil {
		result := llm.ToolResult{
			CallID:  call.ID,
			Name:    call.Name,
			Content: fmt.Sprintf("tool %q is not available", call.Name),
			IsError: true,
		}
		trace.Status = "error"
		trace.Error = result.Content
		trace.DurationMillis = time.Since(start).Milliseconds()
		return result, trace
	}

	toolCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	done := make(chan llm.ToolResult, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				done <- llm.ToolResult{
					CallID:  call.ID,
					Name:    call.Name,
					Content: fmt.Sprintf("tool panicked: %v", recovered),
					IsError: true,
				}
			}
		}()
		result := tool.Execute(toolCtx, call)
		if result.CallID == "" {
			result.CallID = call.ID
		}
		if result.Name == "" {
			result.Name = call.Name
		}
		done <- result
	}()

	var result llm.ToolResult
	select {
	case result = <-done:
	case <-toolCtx.Done():
		result = llm.ToolResult{
			CallID:  call.ID,
			Name:    call.Name,
			Content: fmt.Sprintf("tool timed out after %s", timeout),
			IsError: true,
		}
	}

	result.Content = truncateText(result.Content, resultLimit)
	if result.IsError {
		trace.Status = "error"
		trace.Error = truncateText(result.Content, defaultToolTraceTextLimit)
	} else {
		trace.Result = truncateText(result.Content, defaultToolTraceTextLimit)
	}
	trace.DurationMillis = time.Since(start).Milliseconds()
	return result, trace
}

func summarizeJSON(raw json.RawMessage, limit int) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err == nil {
		if normalized, err := json.Marshal(v); err == nil {
			return truncateText(string(normalized), limit)
		}
	}
	return truncateText(string(raw), limit)
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
