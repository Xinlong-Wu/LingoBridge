package monitor

import (
	"context"

	"lingobridge/internal/core"
	"lingobridge/internal/store"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

const unsupportedMessageText = "暂不支持此类飞书消息，请发送文字。"

type textProcessor interface {
	Handle(ctx context.Context, msg core.InboundMessage, sender core.Sender) error
}

type bot struct {
	handler       textProcessor
	sender        textSender
	eventCommands map[string][]string
}

type feishuResponder struct {
	sender textSender
	chatID string
}

func (r feishuResponder) Send(ctx context.Context, msg core.OutboundMessage) error {
	if msg.Text != "" {
		return r.sender.SendText(ctx, r.chatID, msg.Text)
	}
	if len(msg.Image.Data) > 0 || msg.Image.Filename != "" || msg.Image.LocalPath != "" {
		return core.ErrUnsupportedImage
	}
	return nil
}

func (r feishuResponder) StartTyping(ctx context.Context) func() {
	return func() {}
}

func (b *bot) handleMessage(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	in, ok := normalizeEvent(ctx, event)
	if !ok {
		return nil
	}
	resp := feishuResponder{sender: b.sender, chatID: in.ChatID}
	if in.Unsupported {
		return resp.Send(ctx, core.OutboundMessage{Text: unsupportedMessageText})
	}
	return b.handler.Handle(ctx, core.InboundMessage{
		Platform:    store.PlatformFeishu,
		UserKey:     in.UserID,
		CommandText: in.Text,
		LLMText:     in.Text,
	}, resp)
}
