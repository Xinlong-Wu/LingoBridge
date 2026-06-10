package tools

import (
	"context"
	"encoding/json"
	"time"
)

// Spec describes one client-side function exposed to an LLM provider.
type Spec struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// Call is a provider-neutral request to run a client-side tool.
type Call struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// Result is the result returned to the provider for one tool call.
type Result struct {
	CallID  string
	Name    string
	Content string
	IsError bool
}

// Options controls generic tool loop execution limits.
type Options struct {
	MaxCalls    int
	Timeout     time.Duration
	ResultLimit int
}

// Tool is a function that can be exposed to a tool-capable LLM.
type Tool interface {
	Spec() Spec
	Execute(ctx context.Context, call Call) Result
}

// Provider supplies global tools and their generic execution options.
type Provider interface {
	Tools() []Tool
	ToolOptions() Options
}
