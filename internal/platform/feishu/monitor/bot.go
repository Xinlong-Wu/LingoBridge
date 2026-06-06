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
	deduper       *eventDeduper
	runCtx        context.Context
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
	dedupeKey := feishuDedupeKey(event)
	if dedupeKey == "" {
		feishuLog.Warn(ctx, "feishu message missing dedupe key; processing without dedupe")
	} else if b.deduperOrDefault().seenOrMark(dedupeKey) {
		feishuLog.Debug(ctx, "duplicate feishu message ignored key=%s", dedupeKey)
		return nil
	}

	go b.processMessage(in)
	return nil
}

func (b *bot) processMessage(in incomingMessage) {
	ctx := b.processingContext()
	resp := feishuResponder{sender: b.sender, chatID: in.ChatID}
	if in.Unsupported {
		if err := resp.Send(ctx, core.OutboundMessage{Text: unsupportedMessageText}); err != nil {
			feishuLog.Warn(ctx, "send unsupported feishu message notice failed chat=%s: %v", in.ChatID, err)
		}
		return
	}
	if err := b.handler.Handle(ctx, core.InboundMessage{
		Platform:    store.PlatformFeishu,
		UserKey:     in.UserID,
		CommandText: in.Text,
		LLMText:     in.Text,
	}, resp); err != nil {
		feishuLog.Warn(ctx, "process feishu message failed user=%s chat=%s: %v", in.UserID, in.ChatID, err)
	}
}

func (b *bot) deduperOrDefault() *eventDeduper {
	if b.deduper == nil {
		b.deduper = newEventDeduper(defaultFeishuDedupeTTL)
	}
	return b.deduper
}

func (b *bot) processingContext() context.Context {
	if b.runCtx != nil {
		return b.runCtx
	}
	return context.Background()
}

func feishuDedupeKey(event *larkim.P2MessageReceiveV1) string {
	if event == nil {
		return ""
	}
	if event.Event != nil && event.Event.Message != nil {
		if key := deref(event.Event.Message.MessageId); key != "" {
			return key
		}
	}
	if event.EventV2Base != nil && event.EventV2Base.Header != nil {
		return event.EventV2Base.Header.EventID
	}
	return ""
}
