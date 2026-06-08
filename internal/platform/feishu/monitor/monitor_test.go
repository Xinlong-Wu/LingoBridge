package monitor

import (
	"bytes"
	"context"
	"errors"
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

type updatedText struct {
	messageID string
	text      string
}

type reactionAdd struct {
	messageID string
	emojiType string
}

type reactionDelete struct {
	messageID  string
	reactionID string
}

type fakeSender struct {
	mu                sync.Mutex
	chatID            string
	text              string
	called            bool
	messages          []sentText
	streamCreates     []sentText
	streamUpdates     []updatedText
	reactionAdds      []reactionAdd
	reactionDeletes   []reactionDelete
	addReactionErr    error
	deleteReactionErr error
	updateTextErr     error
}

type fakeClock struct {
	t time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Unix(1000, 0)}
}

func (c *fakeClock) now() time.Time {
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.t = c.t.Add(d)
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

func (f *fakeSender) CreateText(ctx context.Context, chatID, text string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.streamCreates = append(f.streamCreates, sentText{chatID: chatID, text: text})
	return "om_stream", nil
}

func (f *fakeSender) UpdateText(ctx context.Context, messageID, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.streamUpdates = append(f.streamUpdates, updatedText{messageID: messageID, text: text})
	return f.updateTextErr
}

func (f *fakeSender) AddReaction(ctx context.Context, messageID, emojiType string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reactionAdds = append(f.reactionAdds, reactionAdd{messageID: messageID, emojiType: emojiType})
	if f.addReactionErr != nil {
		return "", f.addReactionErr
	}
	return "reaction-1", nil
}

func (f *fakeSender) DeleteReaction(ctx context.Context, messageID, reactionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reactionDeletes = append(f.reactionDeletes, reactionDelete{messageID: messageID, reactionID: reactionID})
	return f.deleteReactionErr
}

type fakeSDKLogger struct {
	mu     sync.Mutex
	debugs int
	infos  int
	warns  int
	errors int
}

func (l *fakeSDKLogger) Debug(context.Context, ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.debugs++
}

func (l *fakeSDKLogger) Info(context.Context, ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.infos++
}

func (l *fakeSDKLogger) Warn(context.Context, ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns++
}

func (l *fakeSDKLogger) Error(context.Context, ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errors++
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
	streamCreates := append([]sentText(nil), f.streamCreates...)
	streamUpdates := append([]updatedText(nil), f.streamUpdates...)
	reactionAdds := append([]reactionAdd(nil), f.reactionAdds...)
	reactionDeletes := append([]reactionDelete(nil), f.reactionDeletes...)
	return fakeSender{
		chatID:          f.chatID,
		text:            f.text,
		called:          f.called,
		messages:        messages,
		streamCreates:   streamCreates,
		streamUpdates:   streamUpdates,
		reactionAdds:    reactionAdds,
		reactionDeletes: reactionDeletes,
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

func waitForReactionAdds(t *testing.T, sender *fakeSender, want int) fakeSender {
	t.Helper()
	deadline := time.After(time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		snap := sender.snapshot()
		if len(snap.reactionAdds) >= want {
			return snap
		}
		select {
		case <-deadline:
			t.Fatalf("reaction adds = %d, want at least %d", len(snap.reactionAdds), want)
		case <-ticker.C:
		}
	}
}

func waitForReactionDeletes(t *testing.T, sender *fakeSender, want int) fakeSender {
	t.Helper()
	deadline := time.After(time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		snap := sender.snapshot()
		if len(snap.reactionDeletes) >= want {
			return snap
		}
		select {
		case <-deadline:
			t.Fatalf("reaction deletes = %d, want at least %d", len(snap.reactionDeletes), want)
		case <-ticker.C:
		}
	}
}

func captureMonitorLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	base := logging.Shared()
	originalWriter := base.Writer()
	originalFlags := base.Flags()
	originalPrefix := base.Prefix()
	originalLevel := logging.GetLevel()
	t.Cleanup(func() {
		base.SetOutput(originalWriter)
		base.SetFlags(originalFlags)
		base.SetPrefix(originalPrefix)
		logging.SetLevel(originalLevel)
	})

	var buf bytes.Buffer
	base.SetOutput(&buf)
	base.SetFlags(0)
	base.SetPrefix("")
	return &buf
}

func TestNormalizeP2PTextMessage(t *testing.T) {
	in, ok := normalizeEvent(context.Background(), feishuEvent("p2p", "text", `{"text":"hi"}`, nil))
	if !ok {
		t.Fatal("normalizeEvent returned ok=false")
	}
	if in.UserID != "feishu:ou_user" || in.ChatID != "oc_chat" || in.MessageID != "om_message" || in.Text != "hi" || in.Unsupported {
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

func TestHandleMessageLogsReceivedMetadata(t *testing.T) {
	buf := captureMonitorLogs(t)
	logging.SetLevel(logging.Info)
	processor := &fakeProcessor{}
	sender := &fakeSender{}
	b := &bot{handler: processor, sender: sender}

	if err := b.handleMessage(context.Background(), feishuEvent("p2p", "text", `{"text":"hi"}`, nil)); err != nil {
		t.Fatalf("handleMessage returned error: %v", err)
	}

	got := buf.String()
	for _, want := range []string{
		"[INFO] - [feishu] received feishu message",
		"chat=oc_chat",
		"user=ou_user",
		"message=om_message",
		"type=text",
		"chat_type=p2p",
		"event=event_message",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("log output = %q, want %q", got, want)
		}
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
	if len(sender.snapshot().reactionAdds) != 0 {
		t.Fatal("reaction was added for unsupported message")
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
	reactionSnap := waitForReactionDeletes(t, sender, 1)
	if len(reactionSnap.reactionAdds) != 1 || reactionSnap.reactionAdds[0].messageID != "om_message" || reactionSnap.reactionAdds[0].emojiType != feishuProcessingReactionEmoji {
		t.Fatalf("reaction adds = %#v, want Typing reaction on om_message", reactionSnap.reactionAdds)
	}
	if len(reactionSnap.reactionDeletes) != 1 || reactionSnap.reactionDeletes[0].messageID != "om_message" || reactionSnap.reactionDeletes[0].reactionID != "reaction-1" {
		t.Fatalf("reaction deletes = %#v, want reaction-1 delete on om_message", reactionSnap.reactionDeletes)
	}
}

func TestFeishuResponderStreamsTextUpdatesOneMessage(t *testing.T) {
	sender := &fakeSender{}
	resp := feishuResponder{sender: sender, chatID: "oc_chat"}

	stream, err := resp.StartTextStream(context.Background())
	if err != nil {
		t.Fatalf("StartTextStream returned error: %v", err)
	}
	if err := stream.Update(context.Background(), "hello"); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	if err := stream.Update(context.Background(), "hello world"); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	if err := stream.Finish(context.Background(), "hello world"); err != nil {
		t.Fatalf("Finish returned error: %v", err)
	}

	snap := sender.snapshot()
	if len(snap.streamCreates) != 1 || snap.streamCreates[0].chatID != "oc_chat" || snap.streamCreates[0].text != "hello" {
		t.Fatalf("stream creates = %#v, want one initial message", snap.streamCreates)
	}
	if len(snap.streamUpdates) != 1 || snap.streamUpdates[0].messageID != "om_stream" || snap.streamUpdates[0].text != "hello world" {
		t.Fatalf("stream updates = %#v, want final update", snap.streamUpdates)
	}
	if len(snap.messages) != 0 {
		t.Fatalf("messages = %#v, want no separate SendText calls", snap.messages)
	}
}

func TestFeishuTextStreamCreateDoesNotCountAsEdit(t *testing.T) {
	clock := newFakeClock()
	stream := &feishuTextStream{
		sender: &fakeSender{},
		chatID: "oc_chat",
		now:    clock.now,
	}

	if err := stream.Update(context.Background(), "hello"); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	if stream.editCount != 0 {
		t.Fatalf("editCount = %d, want 0 after create", stream.editCount)
	}
}

func TestFeishuTextStreamUsesDynamicPreviewIntervals(t *testing.T) {
	tests := []struct {
		name      string
		editCount int
		interval  time.Duration
	}{
		{name: "first previews", editCount: 0, interval: 300 * time.Millisecond},
		{name: "middle previews", editCount: 3, interval: 800 * time.Millisecond},
		{name: "late previews", editCount: 8, interval: 1500 * time.Millisecond},
		{name: "last previews", editCount: 14, interval: 2500 * time.Millisecond},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clock := newFakeClock()
			sender := &fakeSender{}
			stream := &feishuTextStream{
				sender:       sender,
				chatID:       "oc_chat",
				messageID:    "om_stream",
				lastUpdateAt: clock.now(),
				lastSentText: "before",
				editCount:    tc.editCount,
				now:          clock.now,
			}

			clock.advance(tc.interval - time.Millisecond)
			if err := stream.Update(context.Background(), "too soon"); err != nil {
				t.Fatalf("Update before interval returned error: %v", err)
			}
			if got := len(sender.snapshot().streamUpdates); got != 0 {
				t.Fatalf("updates before interval = %d, want 0", got)
			}

			clock.advance(time.Millisecond)
			if err := stream.Update(context.Background(), "on time"); err != nil {
				t.Fatalf("Update at interval returned error: %v", err)
			}
			snap := sender.snapshot()
			if len(snap.streamUpdates) != 1 || snap.streamUpdates[0].text != "on time" {
				t.Fatalf("updates at interval = %#v, want one update", snap.streamUpdates)
			}
			if stream.editCount != tc.editCount+1 {
				t.Fatalf("editCount = %d, want %d", stream.editCount, tc.editCount+1)
			}
		})
	}
}

func TestFeishuTextStreamStopsPreviewAtBudgetButFinishUpdates(t *testing.T) {
	clock := newFakeClock()
	sender := &fakeSender{}
	stream := &feishuTextStream{
		sender:       sender,
		chatID:       "oc_chat",
		messageID:    "om_stream",
		lastUpdateAt: clock.now(),
		lastSentText: "preview",
		editCount:    feishuMaxStreamPreviewEdits,
		now:          clock.now,
	}

	clock.advance(10 * time.Second)
	if err := stream.Update(context.Background(), "ignored preview"); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	if got := len(sender.snapshot().streamUpdates); got != 0 {
		t.Fatalf("preview updates after budget = %d, want 0", got)
	}

	if err := stream.Finish(context.Background(), "final answer"); err != nil {
		t.Fatalf("Finish returned error: %v", err)
	}
	snap := sender.snapshot()
	if len(snap.streamUpdates) != 1 || snap.streamUpdates[0].text != "final answer" {
		t.Fatalf("updates after Finish = %#v, want final update", snap.streamUpdates)
	}
	if len(snap.messages) != 0 {
		t.Fatalf("messages = %#v, want no fallback send", snap.messages)
	}
}

func TestFeishuResponderFallsBackToSendWhenEditLimitReached(t *testing.T) {
	sender := &fakeSender{updateTextErr: ErrFeishuMessageEditLimit}
	resp := feishuResponder{sender: sender, chatID: "oc_chat"}

	stream, err := resp.StartTextStream(context.Background())
	if err != nil {
		t.Fatalf("StartTextStream returned error: %v", err)
	}
	if err := stream.Update(context.Background(), "partial"); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	if err := stream.Finish(context.Background(), "final answer"); err != nil {
		t.Fatalf("Finish returned error: %v", err)
	}

	snap := sender.snapshot()
	if len(snap.streamCreates) != 1 || snap.streamCreates[0].text != "partial" {
		t.Fatalf("stream creates = %#v, want partial create", snap.streamCreates)
	}
	if len(snap.streamUpdates) != 1 || snap.streamUpdates[0].text != "final answer" {
		t.Fatalf("stream updates = %#v, want attempted final update", snap.streamUpdates)
	}
	if len(snap.messages) != 1 || snap.messages[0].chatID != "oc_chat" || snap.messages[0].text != "final answer" {
		t.Fatalf("messages = %#v, want fallback final answer", snap.messages)
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
	if len(sender.snapshot().reactionAdds) != 0 {
		t.Fatal("reaction was added for slash command")
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
	reactionSnap := waitForReactionDeletes(t, sender, 1)
	if len(reactionSnap.reactionAdds) != 1 || len(reactionSnap.reactionDeletes) != 1 {
		t.Fatalf("reaction adds/deletes = %#v/%#v, want one each", reactionSnap.reactionAdds, reactionSnap.reactionDeletes)
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

func TestHandleTextMessageSkipsReactionWithoutMessageID(t *testing.T) {
	processor := &fakeProcessor{}
	sender := &fakeSender{}
	b := &bot{handler: processor, sender: sender}

	if err := b.handleMessage(context.Background(), feishuEventWithIDs("p2p", "text", `{"text":"hi"}`, nil, "", "event_no_message_id")); err != nil {
		t.Fatalf("handleMessage returned error: %v", err)
	}

	waitForProcessorCalls(t, processor, 1)
	waitForSentMessages(t, sender, 1)
	if len(sender.snapshot().reactionAdds) != 0 {
		t.Fatal("reaction was added without message_id")
	}
}

func TestHandleTextMessageContinuesWhenAddReactionFails(t *testing.T) {
	processor := &fakeProcessor{}
	sender := &fakeSender{addReactionErr: errors.New("reaction denied")}
	b := &bot{handler: processor, sender: sender}

	if err := b.handleMessage(context.Background(), feishuEvent("p2p", "text", `{"text":"hi"}`, nil)); err != nil {
		t.Fatalf("handleMessage returned error: %v", err)
	}

	waitForProcessorCalls(t, processor, 1)
	waitForSentMessages(t, sender, 1)
	snap := sender.snapshot()
	if len(snap.reactionAdds) != 1 {
		t.Fatalf("reaction adds = %#v, want one attempted add", snap.reactionAdds)
	}
	if len(snap.reactionDeletes) != 0 {
		t.Fatalf("reaction deletes = %#v, want none after add failure", snap.reactionDeletes)
	}
}

func TestHandleTextMessageContinuesWhenDeleteReactionFails(t *testing.T) {
	processor := &fakeProcessor{}
	sender := &fakeSender{deleteReactionErr: errors.New("delete denied")}
	b := &bot{handler: processor, sender: sender}

	if err := b.handleMessage(context.Background(), feishuEvent("p2p", "text", `{"text":"hi"}`, nil)); err != nil {
		t.Fatalf("handleMessage returned error: %v", err)
	}

	waitForProcessorCalls(t, processor, 1)
	waitForSentMessages(t, sender, 1)
	reactionSnap := waitForReactionDeletes(t, sender, 1)
	if len(reactionSnap.reactionAdds) != 1 || len(reactionSnap.reactionDeletes) != 1 {
		t.Fatalf("reaction adds/deletes = %#v/%#v, want one each", reactionSnap.reactionAdds, reactionSnap.reactionDeletes)
	}
}

func TestHandleTextMessageDelaysReactionDeleteAfterReply(t *testing.T) {
	processor := &fakeProcessor{}
	sender := &fakeSender{}
	b := &bot{handler: processor, sender: sender, reactionDelay: 200 * time.Millisecond}

	if err := b.handleMessage(context.Background(), feishuEvent("p2p", "text", `{"text":"hi"}`, nil)); err != nil {
		t.Fatalf("handleMessage returned error: %v", err)
	}

	waitForProcessorCalls(t, processor, 1)
	waitForSentMessages(t, sender, 1)
	waitForReactionAdds(t, sender, 1)
	if got := len(sender.snapshot().reactionDeletes); got != 0 {
		t.Fatalf("reaction deletes immediately after reply = %d, want none", got)
	}
	waitForReactionDeletes(t, sender, 1)
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
		{level: logging.All, want: larkcore.LogLevelDebug},
		{level: logging.Debug, want: larkcore.LogLevelInfo},
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

func TestSDKLevelLoggerFiltersBeforeSharedLogger(t *testing.T) {
	next := &fakeSDKLogger{}
	logger := newSDKLevelLogger(larkcore.LogLevelInfo, next)

	logger.Debug(context.Background(), "hidden")
	logger.Info(context.Background(), "visible")

	if next.debugs != 0 || next.infos != 1 {
		t.Fatalf("info sdk logger counts: debug=%d info=%d, want debug=0 info=1", next.debugs, next.infos)
	}

	next = &fakeSDKLogger{}
	logger = newSDKLevelLogger(larkcore.LogLevelDebug, next)
	logger.Debug(context.Background(), "visible")

	if next.debugs != 1 {
		t.Fatalf("debug sdk logger debug count = %d, want 1", next.debugs)
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
