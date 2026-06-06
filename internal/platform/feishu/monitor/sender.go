package monitor

import (
	"context"
	"encoding/json"
	"fmt"

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

func (s *sdkSender) SendText(ctx context.Context, chatID, text string) error {
	body, err := json.Marshal(textContent{Text: text})
	if err != nil {
		return fmt.Errorf("marshal feishu text content: %w", err)
	}
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType("text").
			Content(string(body)).
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
