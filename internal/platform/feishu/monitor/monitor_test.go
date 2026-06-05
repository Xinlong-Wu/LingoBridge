package monitor

import (
	"context"
	"testing"
	"time"

	"wechatbox/internal/core"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type fakeProcessor struct {
	userID string
	text   string
	called bool
}

func (f *fakeProcessor) Handle(ctx context.Context, msg core.InboundMessage, sender core.Sender) error {
	f.called = true
	f.userID = msg.UserKey
	f.text = msg.LLMText
	return sender.Send(ctx, core.OutboundMessage{Text: "ok"})
}

type fakeSender struct {
	chatID string
	text   string
	called bool
}

func (f *fakeSender) SendText(ctx context.Context, chatID, text string) error {
	f.called = true
	f.chatID = chatID
	f.text = text
	return nil
}

func TestNormalizeP2PTextMessage(t *testing.T) {
	in, ok := normalizeEvent(feishuEvent("p2p", "text", `{"text":"hi"}`, nil))
	if !ok {
		t.Fatal("normalizeEvent returned ok=false")
	}
	if in.UserID != "feishu:ou_user" || in.ChatID != "oc_chat" || in.Text != "hi" || in.Unsupported {
		t.Fatalf("incoming = %#v", in)
	}
}

func TestNormalizeGroupMessageRequiresBotMention(t *testing.T) {
	if _, ok := normalizeEvent(feishuEvent("group", "text", `{"text":"hi"}`, nil)); ok {
		t.Fatal("group message without bot mention was accepted")
	}
}

func TestNormalizeGroupMentionStripsMentionKey(t *testing.T) {
	mentions := []*larkim.MentionEvent{
		larkim.NewMentionEventBuilder().Key("@_user_1").MentionedType("app").Build(),
	}
	in, ok := normalizeEvent(feishuEvent("group", "text", `{"text":"@_user_1 hello"}`, mentions))
	if !ok {
		t.Fatal("normalizeEvent returned ok=false")
	}
	if in.UserID != "feishu:oc_chat:ou_user" || in.Text != "hello" {
		t.Fatalf("incoming = %#v", in)
	}
}

func TestHandleUnsupportedMessageSendsNotice(t *testing.T) {
	processor := &fakeProcessor{}
	sender := &fakeSender{}
	b := &bot{handler: processor, sender: sender}

	if err := b.handleMessage(context.Background(), feishuEvent("p2p", "image", `{}`, nil)); err != nil {
		t.Fatalf("handleMessage returned error: %v", err)
	}
	if processor.called {
		t.Fatal("processor was called for unsupported message")
	}
	if !sender.called || sender.chatID != "oc_chat" || sender.text != unsupportedMessageText {
		t.Fatalf("sender = %#v", sender)
	}
}

func TestHandleTextMessageUsesBridgeAndReplies(t *testing.T) {
	processor := &fakeProcessor{}
	sender := &fakeSender{}
	b := &bot{handler: processor, sender: sender}

	if err := b.handleMessage(context.Background(), feishuEvent("p2p", "text", `{"text":"hi"}`, nil)); err != nil {
		t.Fatalf("handleMessage returned error: %v", err)
	}
	if !processor.called || processor.userID != "feishu:ou_user" || processor.text != "hi" {
		t.Fatalf("processor = %#v", processor)
	}
	if !sender.called || sender.chatID != "oc_chat" || sender.text != "ok" {
		t.Fatalf("sender = %#v", sender)
	}
}

func TestRunClientClosesOnContextCancel(t *testing.T) {
	client := &blockingClient{closed: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runClient(ctx, client)
	}()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runClient returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runClient did not return after context cancel")
	}
	select {
	case <-client.closed:
	default:
		t.Fatal("client.Close was not called")
	}
}

type blockingClient struct {
	closed chan struct{}
}

func (b *blockingClient) Start(ctx context.Context) error {
	<-b.closed
	return nil
}

func (b *blockingClient) Close() {
	select {
	case <-b.closed:
	default:
		close(b.closed)
	}
}

func feishuEvent(chatType, messageType, content string, mentions []*larkim.MentionEvent) *larkim.P2MessageReceiveV1 {
	return &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: larkim.NewEventSenderBuilder().
				SenderId(larkim.NewUserIdBuilder().OpenId("ou_user").Build()).
				SenderType("user").
				Build(),
			Message: larkim.NewEventMessageBuilder().
				ChatId("oc_chat").
				ChatType(chatType).
				MessageType(messageType).
				Content(content).
				Mentions(mentions).
				Build(),
		},
	}
}
