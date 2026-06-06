package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"lingobridge/internal/commands"
	"lingobridge/internal/config"
	"lingobridge/internal/core"
	"lingobridge/internal/logging"
	"lingobridge/internal/platform/feishu"
	"lingobridge/internal/session"
	"lingobridge/internal/store"
)

const unsupportedMessageText = "暂不支持此类飞书消息，请发送文字。"

var (
	feishuLog    = logging.For("feishu")
	feishuSDKLog = logging.For("feishu/lark")
)

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
	handler       textProcessor
	sender        textSender
	eventCommands map[string][]string
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
	return fmt.Errorf("feishu RunContext requires resolved platform account config")
}

type Platform struct {
	account store.Account
	config  feishu.Config
	level   logging.Level
}

var _ core.Platform = (*Platform)(nil)

func NewPlatform(acc store.Account, cfg feishu.Config, level logging.Level) *Platform {
	cfg.ApplyDefaults()
	return &Platform{account: acc, config: cfg, level: level}
}

func (p *Platform) Run(ctx context.Context, handler core.Handler) error {
	acc := p.account
	accountConfig, ok := p.config.Accounts[acc.Name]
	if !ok {
		return fmt.Errorf("platforms.feishu.accounts.%s is required", acc.Name)
	}
	creds := feishu.CredentialsFromConfig(accountConfig)
	if creds.AppID == "" {
		return fmt.Errorf("feishu account %s credentials app_id is required", acc.Name)
	}
	if creds.AppSecret == "" {
		return fmt.Errorf("feishu account %s credentials app_secret is required", acc.Name)
	}
	baseURL := accountConfig.BaseURL

	sdkLogLevel := feishuSDKLogLevel(p.level)
	restClient := newRESTClient(creds, baseURL, sdkLogLevel)
	b := &bot{
		handler:       handler,
		sender:        &sdkSender{client: restClient},
		eventCommands: map[string][]string{},
	}

	d := dispatcher.NewEventDispatcher("", "")
	d.Config.Logger = feishuSDKLog
	d, registeredEvents, err := b.configureEventHandlers(d.OnP2MessageReceiveV1(b.handleMessage), p.config.Events)
	if err != nil {
		return err
	}
	opts := []larkws.ClientOption{
		larkws.WithEventHandler(d),
		larkws.WithLogger(feishuSDKLog),
		larkws.WithLogLevel(sdkLogLevel),
		larkws.WithOnReady(func() {
			feishuLog.Info(ctx, "long connection ready for account %s (%s)", acc.Name, acc.ID)
		}),
		larkws.WithOnError(func(err error) {
			feishuLog.Error(ctx, "long connection error account=%s: %v", acc.Name, err)
		}),
	}
	if domain := strings.TrimRight(strings.TrimSpace(baseURL), "/"); domain != "" {
		opts = append(opts, larkws.WithDomain(domain))
	}
	wsClient := larkws.NewClient(creds.AppID, creds.AppSecret, opts...)
	feishuLog.Info(ctx, "registered events for account %s (%s): %s", acc.Name, acc.ID, strings.Join(registeredEvents, ", "))
	feishuLog.Info(ctx, "starting for account %s (%s)", acc.Name, acc.ID)
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

func newRESTClient(creds feishu.Credentials, baseURL string, level larkcore.LogLevel) *lark.Client {
	opts := []lark.ClientOptionFunc{
		lark.WithLogger(feishuSDKLog),
		lark.WithLogLevel(level),
	}
	if baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/"); baseURL != "" {
		opts = append(opts, lark.WithOpenBaseUrl(baseURL))
	}
	return lark.NewClient(creds.AppID, creds.AppSecret, opts...)
}

func feishuSDKLogLevel(level logging.Level) larkcore.LogLevel {
	switch level {
	case logging.Debug:
		return larkcore.LogLevelDebug
	case logging.Warn:
		return larkcore.LogLevelWarn
	case logging.Error:
		return larkcore.LogLevelError
	default:
		return larkcore.LogLevelInfo
	}
}

func (b *bot) configureEventHandlers(d *dispatcher.EventDispatcher, events []feishu.EventConfig) (*dispatcher.EventDispatcher, []string, error) {
	if b.eventCommands == nil {
		b.eventCommands = map[string][]string{}
	}
	registered := []string{"im.message.receive_v1"}
	for i, event := range events {
		name := strings.TrimSpace(event.Name)
		run := feishu.ShellRun(event.Run)
		if name == "" {
			return nil, nil, fmt.Errorf("platforms.feishu.events[%d].name is required", i)
		}
		if name == "im.message.receive_v1" {
			return nil, nil, fmt.Errorf("platforms.feishu.events[%d].name %q is built in and cannot be configured", i, name)
		}
		if len(run) == 0 {
			return nil, nil, fmt.Errorf("platforms.feishu.events[%d].run is required", i)
		}
		switch name {
		case "p2p_chat_create":
			b.eventCommands[name] = append(b.eventCommands[name], run...)
		default:
			return nil, nil, fmt.Errorf("unsupported feishu event %q", name)
		}
	}
	if len(b.eventCommands["p2p_chat_create"]) > 0 {
		d = d.OnP1P2PChatCreatedV1(b.handleP2PChatCreated)
		registered = append(registered, "p2p_chat_create")
	}
	return d, registered, nil
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

func (b *bot) handleP2PChatCreated(ctx context.Context, event *larkim.P1P2PChatCreatedV1) error {
	if event == nil || event.Event == nil || event.Event.ChatID == "" {
		return nil
	}
	return runFeishuEventCommands(ctx, b.sender, "p2p_chat_create", event.Event.ChatID, b.eventCommands["p2p_chat_create"], p2pChatCreatedEnv(event))
}

type incomingMessage struct {
	UserID      string
	ChatID      string
	Text        string
	Unsupported bool
}

func normalizeEvent(ctx context.Context, event *larkim.P2MessageReceiveV1) (incomingMessage, bool) {
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
		feishuLog.Warn(ctx, "parse text message: %v", err)
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

func runFeishuEventCommands(ctx context.Context, sender textSender, eventName, chatID string, scripts []string, env map[string]string) error {
	for i, script := range scripts {
		out, err := runShellScript(ctx, script, env)
		if err != nil {
			return fmt.Errorf("run feishu event %s command %d: %w", eventName, i+1, err)
		}
		text := strings.TrimRight(out, "\r\n")
		if strings.TrimSpace(text) == "" {
			continue
		}
		if err := sender.SendText(ctx, chatID, text); err != nil {
			return err
		}
	}
	return nil
}

func runShellScript(ctx context.Context, script string, env map[string]string) (string, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Env = os.Environ()
	for name, value := range env {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return stdout.String(), fmt.Errorf("%w: %s", err, msg)
		}
		return stdout.String(), err
	}
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		feishuLog.Warn(ctx, "event command stderr: %s", msg)
	}
	return stdout.String(), nil
}

func p2pChatCreatedEnv(event *larkim.P1P2PChatCreatedV1) map[string]string {
	env := map[string]string{
		"LINGOBRIDGE_PLATFORM":     store.PlatformFeishu,
		"LINGOBRIDGE_EVENT_NAME":   "p2p_chat_create",
		"LINGOBRIDGE_COMMAND_HELP": commands.HelpText(commands.DefaultPolicy()),
	}
	if event == nil || event.Event == nil {
		return env
	}
	if data, err := json.Marshal(event.Event); err == nil {
		env["LINGOBRIDGE_EVENT_JSON"] = string(data)
	}
	env["LINGOBRIDGE_FEISHU_APP_ID"] = event.Event.AppID
	env["LINGOBRIDGE_FEISHU_CHAT_ID"] = event.Event.ChatID
	env["LINGOBRIDGE_FEISHU_TENANT_KEY"] = event.Event.TenantKey
	env["LINGOBRIDGE_FEISHU_TYPE"] = event.Event.Type
	if event.Event.Operator != nil {
		env["LINGOBRIDGE_FEISHU_OPERATOR_OPEN_ID"] = event.Event.Operator.OpenId
		env["LINGOBRIDGE_FEISHU_OPERATOR_USER_ID"] = event.Event.Operator.UserId
	}
	if event.Event.User != nil {
		env["LINGOBRIDGE_FEISHU_USER_OPEN_ID"] = event.Event.User.OpenId
		env["LINGOBRIDGE_FEISHU_USER_USER_ID"] = event.Event.User.UserId
		env["LINGOBRIDGE_FEISHU_USER_NAME"] = event.Event.User.Name
	}
	return env
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
