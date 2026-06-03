package llm

import (
	"encoding/json"
	"fmt"

	"wechatbox/internal/store"
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
}

type openaiChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openaiChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
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
