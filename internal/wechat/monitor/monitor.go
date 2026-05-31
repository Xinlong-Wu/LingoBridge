package monitor

import (
	"fmt"
	"log"
	"time"

	"wechatbox/internal/config"
	"wechatbox/internal/llm"
	"wechatbox/internal/session"
	"wechatbox/internal/store"
	"wechatbox/internal/wechat/api"
	"wechatbox/internal/wechat/commands"
	"wechatbox/internal/wechat/markdown"
	"wechatbox/internal/wechat/message"
)

const (
	sessionExpiryPause  = 1 * time.Hour
	maxConsecutiveFails = 3
	backoffBase         = 5 * time.Second
)

type cursorStore interface {
	GetSyncBuf(accountID string) (string, error)
	SaveSyncBuf(accountID, buf string) error
}

type wechatClient interface {
	GetUpdates(buf string) (*api.GetUpdatesResp, error)
	SendMessage(msg *api.WeixinMessage) error
	GetConfig(ilinkUserID, contextToken string) (*api.GetConfigResp, error)
	SendTyping(ilinkUserID, typingTicket string, status int) error
	NotifyStart() error
	NotifyStop() error
}

type conversationManager interface {
	commands.SessionManager
	GetOrCreateActiveSession(userID string) (*store.Session, error)
	LoadHistory(userID, sessionID string) (*store.Conversation, error)
	SaveHistory(userID, sessionID string, conv *store.Conversation) error
}

type bot struct {
	client    wechatClient
	cursors   cursorStore
	sessions  conversationManager
	llmClient llm.Client
	cfg       config.LLMConfig
}

// Run starts the single-threaded monitor loop for an account.
func Run(st *store.Store, sm *session.Manager, llmClient llm.Client, cfg config.LLMConfig, acc store.Account) error {
	client := api.NewClient(acc.BaseURL, acc.Token)
	client.Debug = false

	b := &bot{
		client:    client,
		cursors:   st,
		sessions:  sm,
		llmClient: llmClient,
		cfg:       cfg,
	}
	return b.runAccount(acc)
}

func (b *bot) runAccount(acc store.Account) error {
	log.Printf("[monitor] Starting for account %s (%s)", acc.Name, acc.ID)

	if err := b.client.NotifyStart(); err != nil {
		log.Printf("[monitor] notifyStart failed: %v", err)
	}
	defer func() {
		if err := b.client.NotifyStop(); err != nil {
			log.Printf("[monitor] notifyStop failed: %v", err)
		}
	}()

	buf, err := b.cursors.GetSyncBuf(acc.ID)
	if err != nil {
		log.Printf("[monitor] load sync buf: %v", err)
	}

	consecutiveFails := 0
	sessionPausedUntil := time.Time{}

	for {
		if time.Now().Before(sessionPausedUntil) {
			wait := time.Until(sessionPausedUntil)
			log.Printf("[monitor] Session paused for %v", wait.Round(time.Second))
			time.Sleep(wait)
		}

		resp, err := b.client.GetUpdates(buf)
		if err != nil {
			consecutiveFails++
			log.Printf("[monitor] getUpdates failed: %v (fail %d/%d)", err, consecutiveFails, maxConsecutiveFails)
			if consecutiveFails >= maxConsecutiveFails {
				time.Sleep(backoffBase)
			}
			continue
		}

		consecutiveFails = 0

		if resp.Errcode == -14 {
			log.Printf("[monitor] Session expired, pausing for %v", sessionExpiryPause)
			sessionPausedUntil = time.Now().Add(sessionExpiryPause)
			continue
		}

		if resp.GetUpdatesBuf != "" {
			buf = resp.GetUpdatesBuf
			if err := b.cursors.SaveSyncBuf(acc.ID, buf); err != nil {
				log.Printf("[monitor] save sync buf: %v", err)
			}
		}

		for _, msg := range resp.Msgs {
			if err := b.processOne(msg); err != nil {
				log.Printf("[monitor] process message: %v", err)
			}
		}
	}
}

func (b *bot) processOne(msg *api.WeixinMessage) error {
	if msg == nil || msg.FromUserID == "" {
		return nil
	}

	fromUserID := msg.FromUserID
	contextToken := msg.ContextToken
	text := message.ExtractText(msg)
	log.Printf("[monitor] msg from=%s len=%d", fromUserID, len(text))

	if resp, handled, err := commands.Handle(text, fromUserID, b.sessions); handled {
		if err != nil {
			log.Printf("[monitor] command error: %v", err)
			b.sendText(fromUserID, fmt.Sprintf("❌ 错误：%v", err), contextToken)
			return nil
		}
		b.sendText(fromUserID, resp, contextToken)
		return nil
	}

	var stop bool
	text, stop = b.applyMedia(msg, fromUserID, contextToken, text)
	if stop || text == "" {
		return nil
	}

	return b.replyWithLLM(fromUserID, contextToken, text)
}

func (b *bot) applyMedia(msg *api.WeixinMessage, fromUserID, contextToken, text string) (string, bool) {
	if !message.HasMedia(msg) {
		return text, false
	}

	for _, item := range msg.ItemList {
		if item == nil {
			continue
		}
		switch item.Type {
		case api.ItemTypeImage:
			if item.ImageItem != nil && item.ImageItem.Media != nil {
				log.Printf("[monitor] image from=%s", fromUserID)
			}
		case api.ItemTypeVoice:
			if item.VoiceItem != nil && item.VoiceItem.Text != "" {
				text = item.VoiceItem.Text
			} else if item.VoiceItem != nil && item.VoiceItem.Media != nil {
				log.Printf("[monitor] voice from=%s (no transcription)", fromUserID)
				b.sendText(fromUserID, "🎤 语音消息暂不支持自动识别，请发送文字。", contextToken)
				return text, true
			}
		case api.ItemTypeVideo:
			log.Printf("[monitor] video from=%s", fromUserID)
			b.sendText(fromUserID, "🎬 视频消息已收到，暂不支持视频理解。", contextToken)
			return text, true
		case api.ItemTypeFile:
			log.Printf("[monitor] file from=%s", fromUserID)
			b.sendText(fromUserID, "📎 文件消息已收到，暂不支持文件处理。", contextToken)
			return text, true
		}
	}

	return text, false
}

func (b *bot) replyWithLLM(fromUserID, contextToken, text string) error {
	sess, err := b.sessions.GetOrCreateActiveSession(fromUserID)
	if err != nil {
		log.Printf("[monitor] get session: %v", err)
		b.sendText(fromUserID, "❌ 会话加载失败，请重试。", contextToken)
		return err
	}

	conv, err := b.sessions.LoadHistory(fromUserID, sess.ID)
	if err != nil {
		log.Printf("[monitor] load history: %v", err)
		conv = &store.Conversation{}
	}

	msgs := message.ToLLMMessages(b.cfg.SystemPrompt, conv, text, b.cfg.MaxHistory)

	b.sendTyping(fromUserID, contextToken, api.TypingStatusTyping)
	fullResponse, err := b.llmClient.ChatStream(b.cfg.SystemPrompt, msgs, nil)
	b.sendTyping(fromUserID, contextToken, api.TypingStatusCancel)

	if err != nil {
		log.Printf("[monitor] LLM error: %v", err)
		b.sendText(fromUserID, "❌ AI 响应失败，请重试。", contextToken)
		return err
	}

	conv.Messages = append(conv.Messages,
		store.Message{Role: "user", Content: text},
		store.Message{Role: "assistant", Content: fullResponse},
	)

	if err := b.sessions.SaveHistory(fromUserID, sess.ID, conv); err != nil {
		log.Printf("[monitor] save history: %v", err)
	}

	filtered := markdown.FilterText(fullResponse)
	b.sendText(fromUserID, filtered, contextToken)

	log.Printf("[monitor] reply to=%s len=%d", fromUserID, len(fullResponse))
	return nil
}

func (b *bot) sendText(toUserID, text, contextToken string) {
	msg := message.BuildTextMessage(toUserID, text, contextToken)
	if err := b.client.SendMessage(msg); err != nil {
		log.Printf("[monitor] sendMessage failed: %v", err)
	}
}

func (b *bot) sendTyping(toUserID, contextToken string, status int) {
	resp, err := b.client.GetConfig(toUserID, contextToken)
	if err != nil {
		return
	}
	if err := b.client.SendTyping(toUserID, resp.TypingTicket, status); err != nil {
		log.Printf("[monitor] sendTyping failed: %v", err)
	}
}
