package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"lingobridge/internal/config"
	"lingobridge/internal/core"
	"lingobridge/internal/platform/feishu"
	"lingobridge/internal/session"
	"lingobridge/internal/store"
)

const unsupportedMessageText = "暂不支持此类飞书消息，请发送文字。"

type starter interface {
	Start(ctx context.Context) error
}

type closer interface {
	Close()
}

type textSender interface {
	SendText(ctx context.Context, chatID, text string) error
}

type textProcessor interface {
	Handle(ctx context.Context, msg core.InboundMessage, sender core.Sender) error
}

type bot struct {
	handler textProcessor
	sender  textSender
}

type textContent struct {
	Text string `json:"text"`
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

func RunContext(ctx context.Context, st *store.Store, sm *session.Manager, cfg config.LLMConfig, acc store.Account) error {
	return NewPlatform(acc, config.Config{LLM: cfg}).Run(ctx, core.New(sm, cfg))
}

type Platform struct {
	account store.Account
	config  config.Config
}

var _ core.Platform = (*Platform)(nil)

func NewPlatform(acc store.Account, cfg config.Config) *Platform {
	return &Platform{account: acc, config: cfg}
}

func (p *Platform) Run(ctx context.Context, handler core.Handler) error {
	acc := p.account
	feishuAccount, ok, err := p.config.ResolveFeishuAccount(acc.Name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("platforms.feishu.accounts.%s is required", acc.Name)
	}
	creds := feishu.CredentialsFromConfig(feishuAccount)
	baseURL := feishuAccount.BaseURL

	restClient := newRESTClient(creds, baseURL)
	b := &bot{
		handler: handler,
		sender:  &sdkSender{client: restClient},
	}

	d := dispatcher.NewEventDispatcher("", "").OnP2MessageReceiveV1(b.handleMessage)
	opts := []larkws.ClientOption{
		larkws.WithEventHandler(d),
		larkws.WithLogLevel(larkcore.LogLevelError),
		larkws.WithOnReady(func() {
			log.Printf("[feishu] long connection ready for account %s (%s)", acc.Name, acc.ID)
		}),
		larkws.WithOnError(func(err error) {
			log.Printf("[feishu] long connection error account=%s: %v", acc.Name, err)
		}),
	}
	if domain := strings.TrimRight(strings.TrimSpace(baseURL), "/"); domain != "" {
		opts = append(opts, larkws.WithDomain(domain))
	}
	wsClient := larkws.NewClient(creds.AppID, creds.AppSecret, opts...)
	log.Printf("[feishu] Starting for account %s (%s)", acc.Name, acc.ID)
	return runClient(ctx, wsClient)
}

func runClient(ctx context.Context, client interface {
	starter
	closer
}) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- client.Start(ctx)
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		client.Close()
		return nil
	}
}

func newRESTClient(creds feishu.Credentials, baseURL string) *lark.Client {
	opts := []lark.ClientOptionFunc{lark.WithLogLevel(larkcore.LogLevelError)}
	if baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/"); baseURL != "" {
		opts = append(opts, lark.WithOpenBaseUrl(baseURL))
	}
	return lark.NewClient(creds.AppID, creds.AppSecret, opts...)
}

func (b *bot) handleMessage(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	in, ok := normalizeEvent(event)
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

type incomingMessage struct {
	UserID      string
	ChatID      string
	Text        string
	Unsupported bool
}

func normalizeEvent(event *larkim.P2MessageReceiveV1) (incomingMessage, bool) {
	if event == nil || event.Event == nil || event.Event.Sender == nil || event.Event.Message == nil {
		return incomingMessage{}, false
	}
	msg := event.Event.Message
	chatID := deref(msg.ChatId)
	if chatID == "" {
		return incomingMessage{}, false
	}
	openID := userOpenID(event.Event.Sender.SenderId)
	if openID == "" {
		openID = userID(event.Event.Sender.SenderId)
	}
	if openID == "" {
		return incomingMessage{}, false
	}

	chatType := deref(msg.ChatType)
	if chatType != "p2p" && !mentionsBot(msg.Mentions) {
		return incomingMessage{}, false
	}

	userKey := "feishu:" + openID
	if chatType != "p2p" {
		userKey = "feishu:" + chatID + ":" + openID
	}

	if deref(msg.MessageType) != "text" {
		return incomingMessage{UserID: userKey, ChatID: chatID, Unsupported: true}, true
	}

	text, err := extractText(deref(msg.Content), msg.Mentions)
	if err != nil {
		log.Printf("[feishu] parse text message: %v", err)
		return incomingMessage{UserID: userKey, ChatID: chatID, Unsupported: true}, true
	}
	return incomingMessage{UserID: userKey, ChatID: chatID, Text: text}, true
}

func mentionsBot(mentions []*larkim.MentionEvent) bool {
	for _, mention := range mentions {
		if deref(mention.MentionedType) == "app" {
			return true
		}
	}
	return false
}

func extractText(raw string, mentions []*larkim.MentionEvent) (string, error) {
	var content textContent
	if err := json.Unmarshal([]byte(raw), &content); err != nil {
		return "", err
	}
	text := content.Text
	for _, mention := range mentions {
		if key := deref(mention.Key); key != "" {
			text = strings.ReplaceAll(text, key, "")
		}
	}
	return strings.TrimSpace(text), nil
}

func userOpenID(id *larkim.UserId) string {
	if id == nil {
		return ""
	}
	return deref(id.OpenId)
}

func userID(id *larkim.UserId) string {
	if id == nil {
		return ""
	}
	return deref(id.UserId)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
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
