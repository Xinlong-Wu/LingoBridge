package llm

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"

	"lingobridge/internal/store"
	tooltypes "lingobridge/internal/tools"
)

const (
	anthropicEndpointMessages        = "messages"
	anthropicRefProvider             = "anthropic"
	anthropicCompactBeta             = "compact-2026-01-12"
	anthropicCompactType             = "compaction"
	anthropicCompactDeltaType        = "compaction_delta"
	anthropicCompactEditType         = "compact_20260112"
	anthropicMinCompactTriggerTokens = 50000
)

type anthropicClient struct {
	cfg        Config
	httpClient *http.Client
}

func (c *anthropicClient) PrepareUserMessage(content string, attachments []InputAttachment) (store.Message, error) {
	return prepareTextUserMessage(content, attachments)
}

func (c *anthropicClient) AssistantMessage(resp Response) (store.Message, error) {
	return defaultAssistantMessage(resp)
}

type anthropicContent struct {
	Type    string  `json:"type"`
	Text    string  `json:"text,omitempty"`
	Content *string `json:"content,omitempty"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthropicToolUse struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type anthropicToolResult struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
	IsError   bool   `json:"is_error,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content,omitempty"`
	Text    string `json:"text,omitempty"` // for user messages in older format
}

type anthropicRequest struct {
	Model             string                      `json:"model"`
	MaxTokens         int                         `json:"max_tokens"`
	Messages          []anthropicMessage          `json:"messages"`
	System            string                      `json:"system,omitempty"`
	Stream            bool                        `json:"stream"`
	Tools             []anthropicTool             `json:"tools,omitempty"`
	ContextManagement *anthropicContextManagement `json:"context_management,omitempty"`
}

type anthropicResponse struct {
	Content []json.RawMessage `json:"content"`
}

type anthropicContextManagement struct {
	Edits []anthropicContextEdit `json:"edits,omitempty"`
}

type anthropicContextEdit struct {
	Type                 string            `json:"type"`
	Trigger              *anthropicTrigger `json:"trigger,omitempty"`
	Instructions         string            `json:"instructions,omitempty"`
	PauseAfterCompaction bool              `json:"pause_after_compaction,omitempty"`
}

type anthropicTrigger struct {
	Type  string `json:"type"`
	Value int    `json:"value"`
}

type anthropicStreamEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Delta struct {
		Type    string  `json:"type"`
		Text    string  `json:"text"`
		Content *string `json:"content"`
	} `json:"delta"`
	ContentBlock struct {
		Type    string  `json:"type"`
		Text    string  `json:"text"`
		Content *string `json:"content"`
	} `json:"content_block"`
	Message struct {
		Content []json.RawMessage `json:"content"`
	} `json:"message"`
}

func convertToAnthropicMessages(messages []store.Message, systemPrompt string) ([]anthropicMessage, string, error) {
	return convertToAnthropicMessagesWithContext(messages, systemPrompt, store.ProviderContext{})
}

func convertToAnthropicMessagesWithContext(messages []store.Message, systemPrompt string, providerContext store.ProviderContext) ([]anthropicMessage, string, error) {
	var msgs []anthropicMessage
	system := systemPrompt

	if providerContext.Provider == anthropicRefProvider && providerContext.Endpoint == anthropicEndpointMessages && len(providerContext.Items) > 0 {
		content := make([]any, 0, len(providerContext.Items))
		for _, item := range providerContext.Items {
			if len(item) > 0 {
				content = append(content, item)
			}
		}
		if len(content) > 0 {
			msgs = append(msgs, anthropicMessage{Role: "assistant", Content: content})
		}
	}

	for _, m := range messages {
		if m.Role == "system" {
			system = m.Content
			continue
		}
		if len(m.Attachments) > 0 {
			return nil, "", fmt.Errorf("%w: anthropic messages endpoint does not support attachments", ErrUnsupportedAttachment)
		}
		msgs = append(msgs, anthropicMessage{
			Role:    m.Role,
			Content: []anthropicContent{{Type: "text", Text: m.Content}},
		})
	}
	return msgs, system, nil
}

func (c *anthropicClient) Chat(systemPrompt string, messages []store.Message) (Response, error) {
	anthropicMsgs, system, err := convertToAnthropicMessages(messages, systemPrompt)
	if err != nil {
		return Response{}, err
	}

	reqBody := anthropicRequest{
		Model:     c.cfg.Model,
		MaxTokens: 4096,
		Messages:  anthropicMsgs,
		System:    system,
		Stream:    false,
	}

	reqURL := c.requestURL()
	body, err := postJSON(c.httpClient, reqURL, anthropicHeaders(c.cfg.APIKey, false), reqBody, "anthropic")
	if err != nil {
		return Response{}, err
	}

	return parseAnthropicResponse(body)
}

func (c *anthropicClient) ChatStream(systemPrompt string, messages []store.Message, onChunk func(chunk string) error) (Response, error) {
	return c.chatStream(systemPrompt, messages, store.ProviderContext{}, CompactConfig{}, false, onChunk)
}

func (c *anthropicClient) ChatStreamWithContext(systemPrompt string, messages []store.Message, providerContext store.ProviderContext, compact CompactConfig, onChunk func(chunk string) error) (Response, error) {
	return c.chatStream(systemPrompt, messages, providerContext, compact, true, onChunk)
}

func (c *anthropicClient) ChatStreamWithTools(systemPrompt string, messages []store.Message, providerContext store.ProviderContext, compact CompactConfig, tools []tooltypes.Spec, previous ToolState, results []tooltypes.Result, onChunk func(chunk string) error) (ToolResponse, error) {
	anthropicMsgs, system, err := convertToAnthropicMessagesWithContext(messages, systemPrompt, providerContext)
	if err != nil {
		return ToolResponse{}, err
	}
	if previous.Provider == anthropicRefProvider && previous.Endpoint == anthropicEndpointMessages && len(previous.Items) > 0 {
		content := make([]any, 0, len(previous.Items))
		for _, item := range previous.Items {
			if len(item) > 0 {
				content = append(content, item)
			}
		}
		anthropicMsgs = append(anthropicMsgs, anthropicMessage{Role: "assistant", Content: content})
	}
	if len(results) > 0 {
		content := make([]any, 0, len(results))
		for _, result := range results {
			content = append(content, anthropicToolResult{
				Type:      "tool_result",
				ToolUseID: result.CallID,
				Content:   toolResultOutput(result),
				IsError:   result.IsError,
			})
		}
		anthropicMsgs = append(anthropicMsgs, anthropicMessage{Role: "user", Content: content})
	}

	reqBody := anthropicRequest{
		Model:     c.cfg.Model,
		MaxTokens: 4096,
		Messages:  anthropicMsgs,
		System:    system,
		Stream:    false,
		Tools:     anthropicTools(tools),
	}
	body, err := postJSON(c.httpClient, c.requestURL(), anthropicHeaders(c.cfg.APIKey, !providerContext.IsEmpty()), reqBody, "anthropic")
	if err != nil {
		return ToolResponse{}, err
	}
	return parseAnthropicToolResponse(body)
}

func (c *anthropicClient) CompactContext(systemPrompt string, messages []store.Message, providerContext store.ProviderContext, compact CompactConfig) (store.ProviderContext, error) {
	anthropicMsgs, system, err := convertToAnthropicMessagesWithContext(messages, systemPrompt, providerContext)
	if err != nil {
		return store.ProviderContext{}, err
	}
	if len(anthropicMsgs) == 0 {
		return providerContext, nil
	}

	reqBody := anthropicRequest{
		Model:             c.cfg.Model,
		MaxTokens:         4096,
		Messages:          anthropicMsgs,
		System:            system,
		Stream:            false,
		ContextManagement: anthropicCompactContextManagement(compact, true),
	}
	body, err := postJSON(c.httpClient, c.requestURL(), anthropicHeaders(c.cfg.APIKey, true), reqBody, "anthropic compact")
	if err != nil {
		return store.ProviderContext{}, err
	}
	compactedContext, err := parseAnthropicCompactionContext(body)
	if err != nil {
		return store.ProviderContext{}, err
	}
	return compactedContext, nil
}

func (c *anthropicClient) chatStream(systemPrompt string, messages []store.Message, providerContext store.ProviderContext, compact CompactConfig, contextManagement bool, onChunk func(chunk string) error) (Response, error) {
	anthropicMsgs, system, err := convertToAnthropicMessagesWithContext(messages, systemPrompt, providerContext)
	if err != nil {
		return Response{}, err
	}

	reqBody := anthropicRequest{
		Model:     c.cfg.Model,
		MaxTokens: 4096,
		Messages:  anthropicMsgs,
		System:    system,
		Stream:    true,
	}
	beta := !providerContext.IsEmpty()
	if contextManagement && compact.Mode != "false" {
		reqBody.ContextManagement = anthropicCompactContextManagement(compact, false)
		beta = true
	}

	reqURL := c.requestURL()
	return postAnthropicStream(c.httpClient, reqURL, anthropicHeaders(c.cfg.APIKey, beta), reqBody, onChunk)
}

func (c *anthropicClient) requestURL() string {
	base := strings.TrimRight(c.cfg.BaseURL, "/")
	endpoint := c.cfg.Endpoint
	if endpoint == "" || endpoint == anthropicEndpointMessages {
		return base + "/v1/messages"
	}
	if strings.HasPrefix(endpoint, "/") {
		return base + endpoint
	}
	return base + "/v1/" + endpoint
}

func anthropicHeaders(apiKey string, beta bool) http.Header {
	h := http.Header{}
	if apiKey != "" {
		h.Set("x-api-key", apiKey)
	}
	h.Set("anthropic-version", "2023-06-01")
	if beta {
		h.Set("anthropic-beta", anthropicCompactBeta)
	}
	return h
}

func anthropicCompactContextManagement(compact CompactConfig, pauseAfterCompaction bool) *anthropicContextManagement {
	edit := anthropicContextEdit{
		Type:                 anthropicCompactEditType,
		Instructions:         compact.Instructions,
		PauseAfterCompaction: pauseAfterCompaction,
	}
	if triggerTokens := anthropicCompactTriggerTokens(compact); triggerTokens > 0 {
		edit.Trigger = &anthropicTrigger{
			Type:  "input_tokens",
			Value: triggerTokens,
		}
	}
	return &anthropicContextManagement{
		Edits: []anthropicContextEdit{edit},
	}
}

func anthropicCompactTriggerTokens(compact CompactConfig) int {
	if compact.ContextWindow <= 0 || compact.Threshold <= 0 {
		return 0
	}
	tokens := int(math.Ceil(float64(compact.ContextWindow) * compact.Threshold))
	if tokens < anthropicMinCompactTriggerTokens {
		return anthropicMinCompactTriggerTokens
	}
	return tokens
}

func anthropicTools(tools []tooltypes.Spec) []anthropicTool {
	out := make([]anthropicTool, 0, len(tools))
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		out = append(out, anthropicTool{
			Name:        name,
			Description: tool.Description,
			InputSchema: normalizeToolSchema(tool.Parameters),
		})
	}
	return out
}

func parseAnthropicResponse(body []byte) (Response, error) {
	var chatResp anthropicResponse
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return Response{}, fmt.Errorf("unmarshal response: %w", err)
	}

	var resp Response
	for _, raw := range chatResp.Content {
		text, isCompaction := parseAnthropicContentBlock(raw)
		if text != "" {
			resp.Text += text
		}
		if isCompaction {
			appendAnthropicCompaction(&resp, raw)
		}
	}
	if resp.Text == "" && resp.ProviderContext.IsEmpty() {
		return Response{}, fmt.Errorf("no content in response")
	}
	return resp, nil
}

func parseAnthropicToolResponse(body []byte) (ToolResponse, error) {
	var chatResp anthropicResponse
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return ToolResponse{}, fmt.Errorf("unmarshal response: %w", err)
	}

	var resp ToolResponse
	for _, raw := range chatResp.Content {
		text, isCompaction := parseAnthropicContentBlock(raw)
		if text != "" {
			resp.Text += text
		}
		if isCompaction {
			appendAnthropicCompaction(&resp.Response, raw)
			continue
		}
		var meta struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &meta); err != nil || meta.Type != "tool_use" {
			continue
		}
		var toolUse anthropicToolUse
		if err := json.Unmarshal(raw, &toolUse); err != nil {
			return ToolResponse{}, fmt.Errorf("unmarshal anthropic tool_use: %w", err)
		}
		args := toolUse.Input
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		resp.ToolCalls = append(resp.ToolCalls, tooltypes.Call{
			ID:        toolUse.ID,
			Name:      toolUse.Name,
			Arguments: args,
		})
		resp.ToolState.Provider = anthropicRefProvider
		resp.ToolState.Endpoint = anthropicEndpointMessages
		resp.ToolState.Items = append(resp.ToolState.Items, raw)
	}
	if resp.Text == "" && len(resp.ToolCalls) == 0 && resp.ProviderContext.IsEmpty() {
		return ToolResponse{}, fmt.Errorf("no content or tool_use in response")
	}
	return resp, nil
}

func parseAnthropicCompactionContext(body []byte) (store.ProviderContext, error) {
	var chatResp anthropicResponse
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return store.ProviderContext{}, fmt.Errorf("unmarshal response: %w", err)
	}

	ctx := store.ProviderContext{
		Provider: anthropicRefProvider,
		Endpoint: anthropicEndpointMessages,
	}
	for _, raw := range chatResp.Content {
		if _, isCompaction := parseAnthropicContentBlock(raw); isCompaction {
			ctx.Items = append(ctx.Items, raw)
		}
	}
	if ctx.IsEmpty() {
		return store.ProviderContext{}, ErrCompactionNotTriggered
	}
	return ctx, nil
}

func postAnthropicStream(client *http.Client, reqURL string, headers http.Header, reqBody any, onChunk func(chunk string) error) (Response, error) {
	resp, err := sendJSON(client, reqURL, headers, reqBody)
	if err != nil {
		return Response{}, fmt.Errorf("anthropic stream request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return Response{}, newHTTPError("anthropic stream", resp.StatusCode, body)
	}

	return parseAnthropicSSE(resp.Body, onChunk)
}

func parseAnthropicSSE(body io.Reader, onChunk func(chunk string) error) (Response, error) {
	var resp Response
	var textBuilder strings.Builder
	var compactionBuilder strings.Builder
	var compactions []json.RawMessage

	err := readSSEData(body, func(data string) (bool, error) {
		var event anthropicStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return false, nil
		}

		switch event.Type {
		case "content_block_start":
			if event.ContentBlock.Text != "" {
				textBuilder.WriteString(event.ContentBlock.Text)
				resp.Text = textBuilder.String()
				if onChunk != nil {
					if err := onChunk(event.ContentBlock.Text); err != nil {
						return false, err
					}
				}
			}
		case "content_block_delta":
			if event.Delta.Type == anthropicCompactDeltaType {
				if event.Delta.Content != nil {
					compactionBuilder.WriteString(*event.Delta.Content)
				}
				return false, nil
			}
			if event.Delta.Text != "" {
				textBuilder.WriteString(event.Delta.Text)
				resp.Text = textBuilder.String()
				if onChunk != nil {
					if err := onChunk(event.Delta.Text); err != nil {
						return false, err
					}
				}
			}
		case "message_delta", "message_stop":
			for _, raw := range event.Message.Content {
				if _, isCompaction := parseAnthropicContentBlock(raw); isCompaction {
					compactions = append(compactions, raw)
				}
			}
		}
		return false, nil
	})
	if err != nil {
		return Response{}, err
	}
	if compactionBuilder.Len() > 0 && len(compactions) == 0 {
		raw, err := newAnthropicCompactionRaw(compactionBuilder.String())
		if err != nil {
			return Response{}, err
		}
		if _, isCompaction := parseAnthropicContentBlock(raw); isCompaction {
			compactions = append(compactions, raw)
		}
	}
	for _, raw := range compactions {
		appendAnthropicCompaction(&resp, raw)
	}
	return resp, nil
}

func appendAnthropicCompaction(resp *Response, raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	resp.ProviderContext.Provider = anthropicRefProvider
	resp.ProviderContext.Endpoint = anthropicEndpointMessages
	resp.ProviderContext.Items = append(resp.ProviderContext.Items, raw)
	resp.Compacted = true
}

func newAnthropicCompactionRaw(content string) (json.RawMessage, error) {
	return json.Marshal(anthropicContent{Type: anthropicCompactType, Content: &content})
}

func parseAnthropicContentBlock(raw json.RawMessage) (text string, isCompaction bool) {
	var block anthropicContent
	if err := json.Unmarshal(raw, &block); err != nil {
		return "", false
	}
	switch block.Type {
	case "text":
		return block.Text, false
	case anthropicCompactType:
		if block.Content == nil || strings.TrimSpace(*block.Content) == "" {
			return "", false
		}
		return "", true
	default:
		return "", false
	}
}

func parseAnthropicStreamEvent(data string) string {
	var event anthropicStreamEvent
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return ""
	}

	switch event.Type {
	case "content_block_delta":
		return event.Delta.Text
	case "content_block_start":
		return event.ContentBlock.Text
	default:
		return ""
	}
}
