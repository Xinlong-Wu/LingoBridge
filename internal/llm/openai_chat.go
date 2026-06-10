package llm

import (
	"encoding/json"
	"fmt"
	"strings"

	"lingobridge/internal/store"
	tooltypes "lingobridge/internal/tools"
)

type openaiChatClient struct {
	openaiBase
}

func (c *openaiChatClient) PrepareUserMessage(content string, attachments []InputAttachment) (store.Message, error) {
	return prepareTextUserMessage(content, attachments)
}

func (c *openaiChatClient) AssistantMessage(resp Response) (store.Message, error) {
	return defaultAssistantMessage(resp)
}

type openaiChatRequest struct {
	Model    string              `json:"model"`
	Messages []openaiChatMessage `json:"messages"`
	Stream   bool                `json:"stream"`
	Tools    []openaiChatTool    `json:"tools,omitempty"`
}

type openaiChatMessage struct {
	Role       string               `json:"role"`
	Content    string               `json:"content"`
	ToolCalls  []openaiChatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string               `json:"tool_call_id,omitempty"`
}

type openaiChatResponse struct {
	Choices []struct {
		Message openaiChatMessage `json:"message"`
	} `json:"choices"`
}

type openaiChatTool struct {
	Type     string             `json:"type"`
	Function openaiChatFunction `json:"function"`
}

type openaiChatFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type openaiChatToolCall struct {
	ID       string                 `json:"id"`
	Type     string                 `json:"type"`
	Function openaiChatToolFunction `json:"function"`
}

type openaiChatToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openaiStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

func (c *openaiChatClient) Chat(systemPrompt string, messages []store.Message) (Response, error) {
	chatMessages, err := convertToOpenAIChatMessages(messages)
	if err != nil {
		return Response{}, err
	}
	reqBody := openaiChatRequest{
		Model:    c.cfg.Model,
		Messages: chatMessages,
		Stream:   false,
	}

	body, err := postJSON(c.httpClient, c.chatCompletionsURL(), bearerHeaders(c.cfg.APIKey), reqBody, "openai")
	if err != nil {
		return Response{}, err
	}

	var chatResp openaiChatResponse
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return Response{}, fmt.Errorf("unmarshal response: %w", err)
	}

	if len(chatResp.Choices) > 0 {
		return Response{Text: chatResp.Choices[0].Message.Content}, nil
	}
	return Response{}, fmt.Errorf("no choices in response")
}

func (c *openaiChatClient) ChatStream(systemPrompt string, messages []store.Message, onChunk func(chunk string) error) (Response, error) {
	chatMessages, err := convertToOpenAIChatMessages(messages)
	if err != nil {
		return Response{}, err
	}
	reqBody := openaiChatRequest{
		Model:    c.cfg.Model,
		Messages: chatMessages,
		Stream:   true,
	}

	text, err := postStream(c.httpClient, c.chatCompletionsURL(), bearerHeaders(c.cfg.APIKey), reqBody, "openai", parseOpenAIStreamEvent, onChunk)
	if err != nil {
		return Response{}, err
	}
	return Response{Text: text}, nil
}

func (c *openaiChatClient) ChatStreamWithTools(systemPrompt string, messages []store.Message, providerContext store.ProviderContext, compact CompactConfig, tools []tooltypes.Spec, previous ToolState, results []tooltypes.Result, onChunk func(chunk string) error) (ToolResponse, error) {
	chatMessages, err := convertToOpenAIChatMessages(messages)
	if err != nil {
		return ToolResponse{}, err
	}
	if previous.Provider == c.refProvider() && previous.Endpoint == openAIEndpointChat {
		for _, raw := range previous.Items {
			var msg openaiChatMessage
			if len(raw) > 0 && json.Unmarshal(raw, &msg) == nil && msg.Role != "" {
				chatMessages = append(chatMessages, msg)
			}
		}
	}
	for _, result := range results {
		chatMessages = append(chatMessages, openaiChatMessage{
			Role:       "tool",
			Content:    toolResultOutput(result),
			ToolCallID: result.CallID,
		})
	}
	reqBody := openaiChatRequest{
		Model:    c.cfg.Model,
		Messages: chatMessages,
		Stream:   false,
		Tools:    openAIChatTools(tools),
	}

	body, err := postJSON(c.httpClient, c.chatCompletionsURL(), bearerHeaders(c.cfg.APIKey), reqBody, "openai")
	if err != nil {
		return ToolResponse{}, err
	}

	var chatResp openaiChatResponse
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return ToolResponse{}, fmt.Errorf("unmarshal response: %w", err)
	}
	if len(chatResp.Choices) == 0 {
		return ToolResponse{}, fmt.Errorf("no choices in response")
	}
	return parseOpenAIChatToolMessage(chatResp.Choices[0].Message), nil
}

func convertToOpenAIChatMessages(messages []store.Message) ([]openaiChatMessage, error) {
	out := make([]openaiChatMessage, 0, len(messages))
	for _, m := range messages {
		if len(m.Attachments) > 0 {
			return nil, fmt.Errorf("%w: OpenAI chat endpoint does not support attachments", ErrUnsupportedAttachment)
		}
		out = append(out, openaiChatMessage{Role: m.Role, Content: m.Content})
	}
	return out, nil
}

func openAIChatTools(tools []tooltypes.Spec) []openaiChatTool {
	out := make([]openaiChatTool, 0, len(tools))
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		out = append(out, openaiChatTool{
			Type: "function",
			Function: openaiChatFunction{
				Name:        name,
				Description: tool.Description,
				Parameters:  normalizeToolSchema(tool.Parameters),
			},
		})
	}
	return out
}

func parseOpenAIChatToolMessage(msg openaiChatMessage) ToolResponse {
	resp := ToolResponse{Response: Response{Text: msg.Content}}
	if len(msg.ToolCalls) == 0 {
		return resp
	}
	if msg.Role == "" {
		msg.Role = "assistant"
	}
	raw, _ := json.Marshal(msg)
	resp.ToolState = ToolState{
		Provider: openAIRefProvider,
		Endpoint: openAIEndpointChat,
		Items:    []json.RawMessage{raw},
	}
	for _, toolCall := range msg.ToolCalls {
		args := json.RawMessage(strings.TrimSpace(toolCall.Function.Arguments))
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		resp.ToolCalls = append(resp.ToolCalls, tooltypes.Call{
			ID:        toolCall.ID,
			Name:      toolCall.Function.Name,
			Arguments: args,
		})
	}
	return resp
}

func parseOpenAIStreamEvent(data string) string {
	var chunk openaiStreamChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return ""
	}
	if len(chunk.Choices) == 0 {
		return ""
	}
	return chunk.Choices[0].Delta.Content
}
