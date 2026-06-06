package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type textSender interface {
	SendText(ctx context.Context, chatID, text string) error
	AddReaction(ctx context.Context, messageID, emojiType string) (string, error)
	DeleteReaction(ctx context.Context, messageID, reactionID string) error
}

type sdkSender struct {
	client *lark.Client
}

type richTextContent struct {
	ZhCN richTextLanguage `json:"zh_cn"`
}

type richTextLanguage struct {
	Content [][]richTextTextElement `json:"content"`
}

type richTextTextElement struct {
	Tag  string `json:"tag"`
	Text string `json:"text"`
}

func (s *sdkSender) SendText(ctx context.Context, chatID, text string) error {
	body, err := marshalRichTextContent(text)
	if err != nil {
		return fmt.Errorf("marshal feishu rich text content: %w", err)
	}
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType(larkim.MsgTypePost).
			Content(body).
			Build()).
		Build()
	resp, err := s.client.Im.Message.Create(ctx, req)
	if err != nil {
		return fmt.Errorf("send feishu message: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("send feishu message code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

func marshalRichTextContent(text string) (string, error) {
	body, err := json.Marshal(buildRichTextContent(text))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func buildRichTextContent(text string) richTextContent {
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(normalized, "\n")
	content := make([][]richTextTextElement, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			line = " "
		}
		content = append(content, []richTextTextElement{{
			Tag:  "text",
			Text: line,
		}})
	}
	return richTextContent{
		ZhCN: richTextLanguage{Content: content},
	}
}

func (s *sdkSender) AddReaction(ctx context.Context, messageID, emojiType string) (string, error) {
	req := larkim.NewCreateMessageReactionReqBuilder().
		MessageId(messageID).
		Body(larkim.NewCreateMessageReactionReqBodyBuilder().
			ReactionType(larkim.NewEmojiBuilder().EmojiType(emojiType).Build()).
			Build()).
		Build()
	resp, err := s.client.Im.MessageReaction.Create(ctx, req)
	if err != nil {
		return "", fmt.Errorf("add feishu reaction: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("add feishu reaction code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.ReactionId == nil || *resp.Data.ReactionId == "" {
		return "", fmt.Errorf("add feishu reaction missing reaction_id")
	}
	return *resp.Data.ReactionId, nil
}

func (s *sdkSender) DeleteReaction(ctx context.Context, messageID, reactionID string) error {
	req := larkim.NewDeleteMessageReactionReqBuilder().
		MessageId(messageID).
		ReactionId(reactionID).
		Build()
	resp, err := s.client.Im.MessageReaction.Delete(ctx, req)
	if err != nil {
		return fmt.Errorf("delete feishu reaction: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("delete feishu reaction code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}
