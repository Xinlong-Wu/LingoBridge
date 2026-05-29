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
	"wechatbox/internal/wechat/cdn"
	"wechatbox/internal/wechat/commands"
	"wechatbox/internal/wechat/markdown"
	"wechatbox/internal/wechat/message"
)

const (
	// Coalescing config for streaming
	minCharsCoalesce = 200
	idleMsCoalesce   = 3000 * time.Millisecond

	// Session expiry pause
	sessionExpiryPause = 1 * time.Hour

	// Backoff config
	maxConsecutiveFails = 3
	backoffBase         = 5 * time.Second
	longPollTimeout     = 35 * time.Second
)

// Run starts the single-threaded monitor loop for an account.
func Run(st *store.Store, sm *session.Manager, llmClient llm.Client, cfg config.LLMConfig, acc store.Account) error {
	client := api.NewClient(acc.BaseURL, acc.Token)
	client.Debug = false

	log.Printf("[monitor] Starting for account %s (%s)", acc.Name, acc.ID)

	// Notify server of startup
	if err := client.NotifyStart(); err != nil {
		log.Printf("[monitor] notifyStart failed: %v", err)
	}
	defer func() {
		if err := client.NotifyStop(); err != nil {
			log.Printf("[monitor] notifyStop failed: %v", err)
		}
	}()

	// Load sync cursor
	buf, _ := st.GetSyncBuf(acc.ID)

	consecutiveFails := 0
	sessionPausedUntil := time.Time{}

	for {
		// Check session pause
		if time.Now().Before(sessionPausedUntil) {
			wait := time.Until(sessionPausedUntil)
			log.Printf("[monitor] Session paused for %v", wait.Round(time.Second))
			time.Sleep(wait)
		}

		// Get updates
		resp, err := client.GetUpdates(buf)
		if err != nil {
			consecutiveFails++
			log.Printf("[monitor] getUpdates failed: %v (fail %d/%d)", err, consecutiveFails, maxConsecutiveFails)
			if consecutiveFails >= maxConsecutiveFails {
				time.Sleep(backoffBase)
			}
			continue
		}

		consecutiveFails = 0

		// Check for session expiry
		if resp.Errcode == -14 {
			log.Printf("[monitor] Session expired, pausing for %v", sessionExpiryPause)
			sessionPausedUntil = time.Now().Add(sessionExpiryPause)
			continue
		}

		// Update sync cursor
		if resp.GetUpdatesBuf != "" {
			buf = resp.GetUpdatesBuf
			if err := st.SaveSyncBuf(acc.ID, buf); err != nil {
				log.Printf("[monitor] save sync buf: %v", err)
			}
		}

		// Process messages sequentially
		for _, msg := range resp.Msgs {
			if err := processOne(client, st, sm, llmClient, cfg, msg); err != nil {
				log.Printf("[monitor] process message: %v", err)
			}
		}
	}
}

func processOne(client *api.Client, st *store.Store, sm *session.Manager, llmClient llm.Client, cfg config.LLMConfig, msg *api.WeixinMessage) error {
	fromUserID := msg.FromUserID
	if fromUserID == "" {
		return nil
	}

	text := message.ExtractText(msg)
	contextToken := msg.ContextToken
	log.Printf("[monitor] msg from=%s len=%d", fromUserID, len(text))

	// --- Handle slash commands ---
	if resp, handled, err := commands.Handle(text, fromUserID, sm); handled {
		if err != nil {
			log.Printf("[monitor] command error: %v", err)
			sendText(client, fromUserID, fmt.Sprintf("❌ 错误：%v", err), contextToken)
			return nil
		}
		sendText(client, fromUserID, resp, contextToken)
		return nil
	}

	// --- Handle media ---
	if message.HasMedia(msg) {
		for _, item := range msg.ItemList {
			switch item.Type {
			case api.ItemTypeImage:
				if item.ImageItem != nil && item.ImageItem.Media != nil {
					log.Printf("[monitor] image from=%s", fromUserID)
				}
			case api.ItemTypeVoice:
				if item.VoiceItem != nil && item.VoiceItem.Text != "" {
					// Use pre-transcribed voice text
					text = item.VoiceItem.Text
				} else if item.VoiceItem != nil && item.VoiceItem.Media != nil {
					log.Printf("[monitor] voice from=%s (no transcription)", fromUserID)
					sendText(client, fromUserID, "🎤 语音消息暂不支持自动识别，请发送文字。", contextToken)
					return nil
				}
			case api.ItemTypeVideo:
				log.Printf("[monitor] video from=%s", fromUserID)
				sendText(client, fromUserID, "🎬 视频消息已收到，暂不支持视频理解。", contextToken)
				return nil
			case api.ItemTypeFile:
				log.Printf("[monitor] file from=%s", fromUserID)
				sendText(client, fromUserID, "📎 文件消息已收到，暂不支持文件处理。", contextToken)
				return nil
			}
		}
	}

	if text == "" {
		return nil
	}

	// --- Load session and history ---
	sess, err := sm.GetOrCreateActiveSession(fromUserID)
	if err != nil {
		log.Printf("[monitor] get session: %v", err)
		sendText(client, fromUserID, "❌ 会话加载失败，请重试。", contextToken)
		return err
	}

	conv, err := sm.LoadHistory(fromUserID, sess.ID)
	if err != nil {
		log.Printf("[monitor] load history: %v", err)
		conv = &store.Conversation{}
	}

	// --- Call LLM ---
	msgs := message.ToLLMMessages(cfg.SystemPrompt, conv, text, cfg.MaxHistory)

	// Send typing indicator
	sendTyping(client, fromUserID, contextToken, api.TypingStatusTyping)

	// Stream response with coalescing
	fullResponse, err := streamWithCoalesce(llmClient, cfg.SystemPrompt, msgs, func(chunk string) error {
		// We don't send partial chunks to WeChat; we coalesce and send once.
		return nil
	})

	// Cancel typing
	sendTyping(client, fromUserID, contextToken, api.TypingStatusCancel)

	if err != nil {
		log.Printf("[monitor] LLM error: %v", err)
		sendText(client, fromUserID, "❌ AI 响应失败，请重试。", contextToken)
		return err
	}

	// --- Save conversation ---
	conv.Messages = append(conv.Messages,
		store.Message{Role: "user", Content: text},
		store.Message{Role: "assistant", Content: fullResponse},
	)

	if err := sm.SaveHistory(fromUserID, sess.ID, conv); err != nil {
		log.Printf("[monitor] save history: %v", err)
	}

	// --- Send response ---
	filtered := markdown.FilterText(fullResponse)
	sendText(client, fromUserID, filtered, contextToken)

	log.Printf("[monitor] reply to=%s len=%d", fromUserID, len(fullResponse))
	return nil
}

func streamWithCoalesce(llmClient llm.Client, systemPrompt string, msgs []store.Message, onChunk func(chunk string) error) (string, error) {
	return llmClient.ChatStream(systemPrompt, msgs, onChunk)
}

func sendText(client *api.Client, toUserID, text, contextToken string) {
	msg := message.BuildTextMessage(toUserID, text, contextToken)
	if err := client.SendMessage(msg); err != nil {
		log.Printf("[monitor] sendMessage failed: %v", err)
	}
}

func sendTyping(client *api.Client, toUserID, contextToken string, status int) {
	// Get typing ticket from config
	resp, err := client.GetConfig(toUserID, contextToken)
	if err != nil {
		return // Non-critical, ignore
	}
	if err := client.SendTyping(toUserID, resp.TypingTicket, status); err != nil {
		log.Printf("[monitor] sendTyping failed: %v", err)
	}
}

// Ensure unused imports for future use
var _ = cdn.DownloadAndDecrypt
