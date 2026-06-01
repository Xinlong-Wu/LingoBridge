package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"wechatbox/internal/store"
)

const anthropicEndpointMessages = "messages"

type anthropicClient struct {
	cfg        Config
	httpClient *http.Client
}

func (c *anthropicClient) PrepareUserMessage(content string, attachments []InputAttachment) (store.Message, error) {
	return prepareTextUserMessage(content, attachments)
}

func (c *anthropicClient) AssistantMessage(resp Response) store.Message {
	return defaultAssistantMessage(resp)
}

type anthropicContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicMessage struct {
	Role    string             `json:"role"`
	Content []anthropicContent `json:"content,omitempty"`
	Text    string             `json:"text,omitempty"` // for user messages in older format
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	Messages  []anthropicMessage `json:"messages"`
	System    string             `json:"system,omitempty"`
	Stream    bool               `json:"stream"`
}

type anthropicResponse struct {
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
}

type anthropicStreamEvent struct {
	Type  string `json:"type"`
	Delta struct {
		Text string `json:"text"`
	} `json:"delta"`
	ContentBlock struct {
		Text string `json:"text"`
	} `json:"content_block"`
	Message struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
}

func convertToAnthropicMessages(messages []store.Message, systemPrompt string) ([]anthropicMessage, string, error) {
	var msgs []anthropicMessage
	system := systemPrompt

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
	body, err := postJSON(c.httpClient, reqURL, anthropicHeaders(c.cfg.APIKey), reqBody, "anthropic")
	if err != nil {
		return Response{}, err
	}

	var chatResp anthropicResponse
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return Response{}, fmt.Errorf("unmarshal response: %w", err)
	}

	if len(chatResp.Content) > 0 {
		return Response{Text: chatResp.Content[0].Text}, nil
	}
	return Response{}, fmt.Errorf("no content in response")
}

func (c *anthropicClient) ChatStream(systemPrompt string, messages []store.Message, onChunk func(chunk string) error) (Response, error) {
	anthropicMsgs, system, err := convertToAnthropicMessages(messages, systemPrompt)
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

	reqURL := c.requestURL()
	text, err := postStream(c.httpClient, reqURL, anthropicHeaders(c.cfg.APIKey), reqBody, "anthropic", parseAnthropicStreamEvent, onChunk)
	if err != nil {
		return Response{}, err
	}
	return Response{Text: text}, nil
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

func anthropicHeaders(apiKey string) http.Header {
	h := http.Header{}
	if apiKey != "" {
		h.Set("x-api-key", apiKey)
	}
	h.Set("anthropic-version", "2023-06-01")
	return h
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
