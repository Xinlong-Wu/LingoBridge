package monitor

import (
	"context"
	"strings"
	"testing"
	"time"

	"lingobridge/internal/commands"
	"lingobridge/internal/core"
	"lingobridge/internal/logging"
	"lingobridge/internal/platform/feishu"
	"lingobridge/internal/store"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type fakeProcessor struct {
	userID      string
	text        string
	commandText string
	called      bool
}

func (f *fakeProcessor) Handle(ctx context.Context, msg core.InboundMessage, sender core.Sender) error {
	f.called = true
	f.userID = msg.UserKey
	f.text = msg.LLMText
	f.commandText = msg.CommandText
	return sender.Send(ctx, core.OutboundMessage{Text: "ok"})
}

type sentText struct {
	chatID string
	text   string
}

type fakeSender struct {
	chatID   string
	text     string
	called   bool
	messages []sentText
}

func (f *fakeSender) SendText(ctx context.Context, chatID, text string) error {
	f.called = true
	f.chatID = chatID
	f.text = text
	f.messages = append(f.messages, sentText{chatID: chatID, text: text})
	return nil
}

func TestNormalizeP2PTextMessage(t *testing.T) {
	in, ok := normalizeEvent(context.Background(), feishuEvent("p2p", "text", `{"text":"hi"}`, nil))
	if !ok {
		t.Fatal("normalizeEvent returned ok=false")
	}
	if in.UserID != "feishu:ou_user" || in.ChatID != "oc_chat" || in.Text != "hi" || in.Unsupported {
		t.Fatalf("incoming = %#v", in)
	}
}

func TestNormalizeGroupMessageRequiresBotMention(t *testing.T) {
	if _, ok := normalizeEvent(context.Background(), feishuEvent("group", "text", `{"text":"hi"}`, nil)); ok {
		t.Fatal("group message without bot mention was accepted")
	}
}

func TestNormalizeGroupMentionStripsMentionKey(t *testing.T) {
	mentions := []*larkim.MentionEvent{
		larkim.NewMentionEventBuilder().Key("@_user_1").MentionedType("app").Build(),
	}
	in, ok := normalizeEvent(context.Background(), feishuEvent("group", "text", `{"text":"@_user_1 hello"}`, mentions))
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

func TestHandleHelpMessagePassesCommandToBridge(t *testing.T) {
	processor := &fakeProcessor{}
	sender := &fakeSender{}
	b := &bot{handler: processor, sender: sender}

	if err := b.handleMessage(context.Background(), feishuEvent("p2p", "text", `{"text":"/help"}`, nil)); err != nil {
		t.Fatalf("handleMessage returned error: %v", err)
	}
	if !processor.called || processor.userID != "feishu:ou_user" || processor.commandText != "/help" || processor.text != "/help" {
		t.Fatalf("processor = %#v", processor)
	}
}

func TestConfigureP2PChatCreatedSendsCommandOutput(t *testing.T) {
	processor := &fakeProcessor{}
	sender := &fakeSender{}
	b := &bot{handler: processor, sender: sender, eventCommands: map[string][]string{}}

	d, registered, err := b.configureEventHandlers(dispatcher.NewEventDispatcher("", ""), []feishu.EventConfig{
		{Name: "p2p_chat_create", Version: "1.0", Run: feishu.ShellRun{
			`printf 'hello %s' "$LINGOBRIDGE_FEISHU_CHAT_ID"`,
			`printf '%s' "$LINGOBRIDGE_COMMAND_HELP"`,
		}},
	})
	if err != nil {
		t.Fatalf("configureEventHandlers returned error: %v", err)
	}
	if d == nil {
		t.Fatal("configureEventHandlers returned nil dispatcher")
	}
	if got, want := strings.Join(registered, ", "), "im.message.receive_v1, p2p_chat_create"; got != want {
		t.Fatalf("registered events = %q, want %q", got, want)
	}

	_, err = d.Do(context.Background(), []byte(`{
		"type":"event_callback",
		"event":{
			"type":"p2p_chat_create",
			"app_id":"cli_xxx",
			"chat_id":"oc_chat",
			"tenant_key":"tenant_xxx"
		}
	}`))
	if err != nil {
		t.Fatalf("dispatcher.Do returned error: %v", err)
	}
	if processor.called {
		t.Fatal("processor was called for p2p_chat_create")
	}
	if len(sender.messages) != 2 {
		t.Fatalf("messages = %#v, want two messages", sender.messages)
	}
	if sender.messages[0].chatID != "oc_chat" || sender.messages[0].text != "hello oc_chat" {
		t.Fatalf("first message = %#v, want greeting", sender.messages[0])
	}
	if sender.messages[1].chatID != "oc_chat" || sender.messages[1].text != commands.HelpText(commands.DefaultPolicy()) {
		t.Fatalf("second message = %#v, want help", sender.messages[1])
	}
	if !strings.Contains(sender.messages[1].text, "/help") || !strings.Contains(sender.messages[1].text, "/model") {
		t.Fatalf("help message = %q, want command help", sender.messages[1].text)
	}
}

func TestConfigureBotP2PChatEnteredV2SendsCommandOutput(t *testing.T) {
	sender := &fakeSender{}
	b := &bot{handler: &fakeProcessor{}, sender: sender, eventCommands: map[string][]string{}}

	d, registered, err := b.configureEventHandlers(dispatcher.NewEventDispatcher("", ""), []feishu.EventConfig{
		{Name: "im.chat.access_event.bot_p2p_chat_entered_v1", Version: "2.0", Run: feishu.ShellRun{
			`printf 'entered %s %s' "$LINGOBRIDGE_FEISHU_CHAT_ID" "$LINGOBRIDGE_FEISHU_OPERATOR_OPEN_ID"`,
		}},
	})
	if err != nil {
		t.Fatalf("configureEventHandlers returned error: %v", err)
	}
	if got, want := strings.Join(registered, ", "), "im.message.receive_v1, im.chat.access_event.bot_p2p_chat_entered_v1"; got != want {
		t.Fatalf("registered events = %q, want %q", got, want)
	}

	_, err = d.Do(context.Background(), []byte(`{
		"schema":"2.0",
		"header":{
			"event_type":"im.chat.access_event.bot_p2p_chat_entered_v1",
			"tenant_key":"tenant_xxx"
		},
		"event":{
			"chat_id":"oc_chat",
			"operator_id":{"open_id":"ou_operator","user_id":"user_operator"}
		}
	}`))
	if err != nil {
		t.Fatalf("dispatcher.Do returned error: %v", err)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("messages = %#v, want one message", sender.messages)
	}
	if sender.messages[0].chatID != "oc_chat" || sender.messages[0].text != "entered oc_chat ou_operator" {
		t.Fatalf("message = %#v, want v2 greeting", sender.messages[0])
	}
}

func TestConfigureEventHandlersReportsBuiltInMessageEvent(t *testing.T) {
	b := &bot{eventCommands: map[string][]string{}}
	d, registered, err := b.configureEventHandlers(dispatcher.NewEventDispatcher("", ""), nil)
	if err != nil {
		t.Fatalf("configureEventHandlers returned error: %v", err)
	}
	if d == nil {
		t.Fatal("configureEventHandlers returned nil dispatcher")
	}
	if got, want := strings.Join(registered, ", "), "im.message.receive_v1"; got != want {
		t.Fatalf("registered events = %q, want %q", got, want)
	}
}

func TestConfigureEventHandlersRejectsBuiltInMessageEvent(t *testing.T) {
	b := &bot{eventCommands: map[string][]string{}}
	_, _, err := b.configureEventHandlers(dispatcher.NewEventDispatcher("", ""), []feishu.EventConfig{
		{Name: "im.message.receive_v1", Version: "2.0", Run: feishu.ShellRun{"echo nope"}},
	})
	if err == nil || !strings.Contains(err.Error(), "built in") {
		t.Fatalf("configureEventHandlers error = %v, want built in event error", err)
	}
}

func TestConfigureEventHandlersRejectsMissingVersion(t *testing.T) {
	b := &bot{eventCommands: map[string][]string{}}
	_, _, err := b.configureEventHandlers(dispatcher.NewEventDispatcher("", ""), []feishu.EventConfig{
		{Name: "unknown", Run: feishu.ShellRun{"echo nope"}},
	})
	if err == nil || !strings.Contains(err.Error(), "version is required") {
		t.Fatalf("configureEventHandlers error = %v, want missing version error", err)
	}
}

func TestConfigureEventHandlersRejectsUnsupportedVersion(t *testing.T) {
	b := &bot{eventCommands: map[string][]string{}}
	_, _, err := b.configureEventHandlers(dispatcher.NewEventDispatcher("", ""), []feishu.EventConfig{
		{Name: "p2p_chat_create", Version: "3.0", Run: feishu.ShellRun{"echo nope"}},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("configureEventHandlers error = %v, want unsupported version error", err)
	}
}

func TestConfigureEventHandlersRejectsUnsupportedV2Event(t *testing.T) {
	b := &bot{eventCommands: map[string][]string{}}
	_, _, err := b.configureEventHandlers(dispatcher.NewEventDispatcher("", ""), []feishu.EventConfig{
		{Name: "unknown", Version: "2.0", Run: feishu.ShellRun{"echo nope"}},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported feishu v2 event") {
		t.Fatalf("configureEventHandlers error = %v, want unsupported v2 event error", err)
	}
}

func TestRunFeishuEventCommandsSkipsStdoutWithoutChatID(t *testing.T) {
	sender := &fakeSender{}
	env := map[string]string{
		"LINGOBRIDGE_PLATFORM":   store.PlatformFeishu,
		"LINGOBRIDGE_EVENT_NAME": "event_without_chat",
	}
	if err := runFeishuEventCommands(context.Background(), sender, "event_without_chat", "", []string{"printf 'hello'"}, env); err != nil {
		t.Fatalf("runFeishuEventCommands returned error: %v", err)
	}
	if len(sender.messages) != 0 {
		t.Fatalf("messages = %#v, want none", sender.messages)
	}
}

func TestPlatformRunRequiresAccountCredentials(t *testing.T) {
	acc := store.Account{
		ID:              "feishu:cli_xxx",
		Name:            "fsbot",
		Platform:        store.PlatformFeishu,
		CredentialsJSON: `{}`,
	}

	err := NewPlatform(acc, feishu.Config{
		Accounts: map[string]feishu.AccountConfig{
			"fsbot": {},
		},
	}, logging.Info).Run(context.Background(), &fakeProcessor{})
	if err == nil || !strings.Contains(err.Error(), "app_id is required") {
		t.Fatalf("Run error = %v, want missing credentials error", err)
	}
}

func TestPlatformRunRequiresConfiguredAccount(t *testing.T) {
	acc := store.Account{
		ID:              "feishu:cli_xxx",
		Name:            "fsbot",
		Platform:        store.PlatformFeishu,
		CredentialsJSON: `{}`,
	}

	err := NewPlatform(acc, feishu.Config{}, logging.Info).Run(context.Background(), &fakeProcessor{})
	if err == nil || !strings.Contains(err.Error(), "platforms.feishu.accounts.fsbot is required") {
		t.Fatalf("Run error = %v, want missing account config error", err)
	}
}

func TestFeishuSDKLogLevel(t *testing.T) {
	tests := []struct {
		level logging.Level
		want  larkcore.LogLevel
	}{
		{level: logging.Debug, want: larkcore.LogLevelDebug},
		{level: logging.Info, want: larkcore.LogLevelInfo},
		{level: logging.Warn, want: larkcore.LogLevelWarn},
		{level: logging.Error, want: larkcore.LogLevelError},
	}

	for _, tc := range tests {
		if got := feishuSDKLogLevel(tc.level); got != tc.want {
			t.Fatalf("feishuSDKLogLevel(%v) = %v, want %v", tc.level, got, tc.want)
		}
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
