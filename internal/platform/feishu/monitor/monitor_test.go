package monitor

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"lingobridge/internal/commands"
	"lingobridge/internal/core"
	"lingobridge/internal/logging"
	"lingobridge/internal/platform/feishu"
	"lingobridge/internal/store"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type fakeProcessor struct {
	mu          sync.Mutex
	userID      string
	text        string
	commandText string
	called      bool
	calls       int
	started     chan struct{}
	release     chan struct{}
}

func (f *fakeProcessor) Handle(ctx context.Context, msg core.InboundMessage, sender core.Sender) error {
	f.mu.Lock()
	f.called = true
	f.userID = msg.UserKey
	f.text = msg.LLMText
	f.commandText = msg.CommandText
	f.calls++
	started := f.started
	release := f.release
	f.mu.Unlock()
	if started != nil {
		select {
		case <-started:
		default:
			close(started)
		}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return sender.Send(ctx, core.OutboundMessage{Text: "ok"})
}

type sentText struct {
	chatID string
	text   string
}

type fakeSender struct {
	mu       sync.Mutex
	chatID   string
	text     string
	called   bool
	messages []sentText
}

func (f *fakeSender) SendText(ctx context.Context, chatID, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called = true
	f.chatID = chatID
	f.text = text
	f.messages = append(f.messages, sentText{chatID: chatID, text: text})
	return nil
}

func (f *fakeProcessor) snapshot() fakeProcessor {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fakeProcessor{
		userID:      f.userID,
		text:        f.text,
		commandText: f.commandText,
		called:      f.called,
		calls:       f.calls,
	}
}

func (f *fakeSender) snapshot() fakeSender {
	f.mu.Lock()
	defer f.mu.Unlock()
	messages := append([]sentText(nil), f.messages...)
	return fakeSender{
		chatID:   f.chatID,
		text:     f.text,
		called:   f.called,
		messages: messages,
	}
}

func waitForProcessorCalls(t *testing.T, processor *fakeProcessor, want int) fakeProcessor {
	t.Helper()
	deadline := time.After(time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		snap := processor.snapshot()
		if snap.calls >= want {
			return snap
		}
		select {
		case <-deadline:
			t.Fatalf("processor calls = %d, want at least %d", snap.calls, want)
		case <-ticker.C:
		}
	}
}

func waitForSentMessages(t *testing.T, sender *fakeSender, want int) fakeSender {
	t.Helper()
	deadline := time.After(time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		snap := sender.snapshot()
		if len(snap.messages) >= want {
			return snap
		}
		select {
		case <-deadline:
			t.Fatalf("sent messages = %d, want at least %d", len(snap.messages), want)
		case <-ticker.C:
		}
	}
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
	senderSnap := waitForSentMessages(t, sender, 1)
	if processor.snapshot().called {
		t.Fatal("processor was called for unsupported message")
	}
	if !senderSnap.called || senderSnap.chatID != "oc_chat" || senderSnap.text != unsupportedMessageText {
		t.Fatalf("sender = %#v", senderSnap)
	}
}

func TestHandleTextMessageUsesBridgeAndReplies(t *testing.T) {
	processor := &fakeProcessor{}
	sender := &fakeSender{}
	b := &bot{handler: processor, sender: sender}

	if err := b.handleMessage(context.Background(), feishuEvent("p2p", "text", `{"text":"hi"}`, nil)); err != nil {
		t.Fatalf("handleMessage returned error: %v", err)
	}
	processorSnap := waitForProcessorCalls(t, processor, 1)
	senderSnap := waitForSentMessages(t, sender, 1)
	if !processorSnap.called || processorSnap.userID != "feishu:ou_user" || processorSnap.text != "hi" {
		t.Fatalf("processor = %#v", processorSnap)
	}
	if !senderSnap.called || senderSnap.chatID != "oc_chat" || senderSnap.text != "ok" {
		t.Fatalf("sender = %#v", senderSnap)
	}
}

func TestHandleHelpMessagePassesCommandToBridge(t *testing.T) {
	processor := &fakeProcessor{}
	sender := &fakeSender{}
	b := &bot{handler: processor, sender: sender}

	if err := b.handleMessage(context.Background(), feishuEvent("p2p", "text", `{"text":"/help"}`, nil)); err != nil {
		t.Fatalf("handleMessage returned error: %v", err)
	}
	processorSnap := waitForProcessorCalls(t, processor, 1)
	if !processorSnap.called || processorSnap.userID != "feishu:ou_user" || processorSnap.commandText != "/help" || processorSnap.text != "/help" {
		t.Fatalf("processor = %#v", processorSnap)
	}
}

func TestHandleDuplicateMessageIDIgnored(t *testing.T) {
	processor := &fakeProcessor{}
	sender := &fakeSender{}
	b := &bot{handler: processor, sender: sender}
	event := feishuEventWithIDs("p2p", "text", `{"text":"hi"}`, nil, "om_same", "event_one")

	if err := b.handleMessage(context.Background(), event); err != nil {
		t.Fatalf("first handleMessage returned error: %v", err)
	}
	if err := b.handleMessage(context.Background(), event); err != nil {
		t.Fatalf("second handleMessage returned error: %v", err)
	}

	waitForProcessorCalls(t, processor, 1)
	waitForSentMessages(t, sender, 1)
	time.Sleep(50 * time.Millisecond)
	if got := processor.snapshot().calls; got != 1 {
		t.Fatalf("processor calls = %d, want one", got)
	}
	if got := len(sender.snapshot().messages); got != 1 {
		t.Fatalf("sent messages = %d, want one", got)
	}
}

func TestHandleDuplicateFallsBackToEventID(t *testing.T) {
	processor := &fakeProcessor{}
	sender := &fakeSender{}
	b := &bot{handler: processor, sender: sender}
	event := feishuEventWithIDs("p2p", "text", `{"text":"hi"}`, nil, "", "event_same")

	if err := b.handleMessage(context.Background(), event); err != nil {
		t.Fatalf("first handleMessage returned error: %v", err)
	}
	if err := b.handleMessage(context.Background(), event); err != nil {
		t.Fatalf("second handleMessage returned error: %v", err)
	}

	waitForProcessorCalls(t, processor, 1)
	time.Sleep(50 * time.Millisecond)
	if got := processor.snapshot().calls; got != 1 {
		t.Fatalf("processor calls = %d, want one", got)
	}
}

func TestHandleDifferentMessageIDsProcessed(t *testing.T) {
	processor := &fakeProcessor{}
	sender := &fakeSender{}
	b := &bot{handler: processor, sender: sender}

	if err := b.handleMessage(context.Background(), feishuEventWithIDs("p2p", "text", `{"text":"one"}`, nil, "om_one", "event_one")); err != nil {
		t.Fatalf("first handleMessage returned error: %v", err)
	}
	if err := b.handleMessage(context.Background(), feishuEventWithIDs("p2p", "text", `{"text":"two"}`, nil, "om_two", "event_two")); err != nil {
		t.Fatalf("second handleMessage returned error: %v", err)
	}

	waitForProcessorCalls(t, processor, 2)
	waitForSentMessages(t, sender, 2)
	if got := processor.snapshot().calls; got != 2 {
		t.Fatalf("processor calls = %d, want two", got)
	}
	if got := len(sender.snapshot().messages); got != 2 {
		t.Fatalf("sent messages = %d, want two", got)
	}
}

func TestHandleDuplicateUnsupportedMessageSendsOneNotice(t *testing.T) {
	processor := &fakeProcessor{}
	sender := &fakeSender{}
	b := &bot{handler: processor, sender: sender}
	event := feishuEventWithIDs("p2p", "image", `{}`, nil, "om_image", "event_image")

	if err := b.handleMessage(context.Background(), event); err != nil {
		t.Fatalf("first handleMessage returned error: %v", err)
	}
	if err := b.handleMessage(context.Background(), event); err != nil {
		t.Fatalf("second handleMessage returned error: %v", err)
	}

	waitForSentMessages(t, sender, 1)
	time.Sleep(50 * time.Millisecond)
	if processor.snapshot().called {
		t.Fatal("processor was called for unsupported message")
	}
	messages := sender.snapshot().messages
	if len(messages) != 1 || messages[0].text != unsupportedMessageText {
		t.Fatalf("messages = %#v, want one unsupported notice", messages)
	}
}

func TestHandleMessageReturnsBeforeProcessorFinishes(t *testing.T) {
	processor := &fakeProcessor{started: make(chan struct{}), release: make(chan struct{})}
	sender := &fakeSender{}
	b := &bot{handler: processor, sender: sender}
	done := make(chan error, 1)

	go func() {
		done <- b.handleMessage(context.Background(), feishuEvent("p2p", "text", `{"text":"hi"}`, nil))
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("handleMessage returned error: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("handleMessage did not return before processor finished")
	}
	select {
	case <-processor.started:
	case <-time.After(time.Second):
		t.Fatal("processor did not start")
	}
	if got := len(sender.snapshot().messages); got != 0 {
		t.Fatalf("sent messages before release = %d, want none", got)
	}
	close(processor.release)
	waitForSentMessages(t, sender, 1)
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
	return feishuEventWithIDs(chatType, messageType, content, mentions, "om_message", "event_message")
}

func feishuEventWithIDs(chatType, messageType, content string, mentions []*larkim.MentionEvent, messageID, eventID string) *larkim.P2MessageReceiveV1 {
	return &larkim.P2MessageReceiveV1{
		EventV2Base: &larkevent.EventV2Base{
			Header: &larkevent.EventHeader{EventID: eventID},
		},
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: larkim.NewEventSenderBuilder().
				SenderId(larkim.NewUserIdBuilder().OpenId("ou_user").Build()).
				SenderType("user").
				Build(),
			Message: larkim.NewEventMessageBuilder().
				ChatId("oc_chat").
				ChatType(chatType).
				MessageId(messageID).
				MessageType(messageType).
				Content(content).
				Mentions(mentions).
				Build(),
		},
	}
}
