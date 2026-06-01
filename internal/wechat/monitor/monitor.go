package monitor

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

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
	textChunkLimit      = 4000
	typingKeepalive     = 5 * time.Second
)

type cursorStore interface {
	GetSyncBuf(accountID string) (string, error)
	SaveSyncBuf(accountID, buf string) error
}

type wechatClient interface {
	GetUpdatesContext(ctx context.Context, buf string) (*api.GetUpdatesResp, error)
	SendMessage(msg *api.WeixinMessage) error
	GetUploadUrl(req *api.GetUploadUrlReq) (*api.GetUploadUrlResp, error)
	GetConfig(ilinkUserID, contextToken string) (*api.GetConfigResp, error)
	SendTyping(ilinkUserID, typingTicket string, status int) error
	NotifyStart() error
	NotifyStop() error
}

type conversationManager interface {
	commands.SessionManager
	GetOrCreateCurrentSession(userID string) (*store.Session, error)
	LoadHistory(userID, sessionID string) (*store.Conversation, error)
	SaveHistory(userID, sessionID string, conv *store.Conversation) error
}

type llmFactory func(config.ResolvedModel) llm.Client

type bot struct {
	client      wechatClient
	cursors     cursorStore
	sessions    conversationManager
	cfg         config.LLMConfig
	llmClients  map[string]llm.Client
	newLLM      llmFactory
	typingTick  time.Duration
	cdnBaseURL  string
	cdnClient   *http.Client
	uploadImage imageUploader
}

// Run starts the single-threaded monitor loop for an account.
func Run(st *store.Store, sm *session.Manager, cfg config.LLMConfig, acc store.Account) error {
	return RunContext(context.Background(), st, sm, cfg, acc)
}

// RunContext starts the monitor loop for an account until ctx is canceled.
func RunContext(ctx context.Context, st *store.Store, sm *session.Manager, cfg config.LLMConfig, acc store.Account) error {
	client := api.NewClient(acc.BaseURL, acc.Token)
	client.Debug = false

	b := &bot{
		client:     client,
		cursors:    st,
		sessions:   sm,
		cfg:        cfg,
		llmClients: map[string]llm.Client{},
		newLLM:     defaultLLMFactory,
		cdnBaseURL: defaultWeixinCDNBaseURL,
	}
	return b.runAccount(ctx, acc)
}

func defaultLLMFactory(model config.ResolvedModel) llm.Client {
	return llm.NewClient(llm.Config{
		Provider: model.Provider,
		BaseURL:  model.BaseURL,
		APIKey:   model.APIKey,
		Model:    model.ID,
		Endpoint: model.Endpoint,
	})
}

func (b *bot) runAccount(ctx context.Context, acc store.Account) error {
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
		if ctx.Err() != nil {
			return nil
		}

		if time.Now().Before(sessionPausedUntil) {
			wait := time.Until(sessionPausedUntil)
			log.Printf("[monitor] Session paused for %v", wait.Round(time.Second))
			if !sleepContext(ctx, wait) {
				return nil
			}
		}

		resp, err := b.client.GetUpdatesContext(ctx, buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			consecutiveFails++
			log.Printf("[monitor] getUpdates failed: %v (fail %d/%d)", err, consecutiveFails, maxConsecutiveFails)
			if consecutiveFails >= maxConsecutiveFails {
				if !sleepContext(ctx, backoffBase) {
					return nil
				}
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

func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (b *bot) processOne(msg *api.WeixinMessage) error {
	if msg == nil || msg.FromUserID == "" {
		return nil
	}

	fromUserID := msg.FromUserID
	contextToken := msg.ContextToken
	commandText := message.ExtractText(msg)
	llmText := message.ExtractLLMText(msg)
	log.Printf("[monitor] msg from=%s len=%d", fromUserID, len(llmText))

	if resp, handled, err := commands.Handle(commandText, fromUserID, b.sessions); handled {
		if err != nil {
			log.Printf("[monitor] command error: %v", err)
			b.sendText(fromUserID, fmt.Sprintf("❌ 错误：%v", err), contextToken)
			return nil
		}
		b.sendText(fromUserID, resp, contextToken)
		return nil
	}

	var stop bool
	llmText, stop = b.applyMedia(msg, fromUserID, contextToken, llmText)
	if stop || llmText == "" {
		return nil
	}

	return b.replyWithLLM(fromUserID, contextToken, llmText)
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
	sess, err := b.sessions.GetOrCreateCurrentSession(fromUserID)
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
	modelName, llmClient, err := b.llmForUser(fromUserID)
	if err != nil {
		log.Printf("[monitor] resolve LLM: %v", err)
		b.sendText(fromUserID, "❌ 模型配置不可用，请检查配置。", contextToken)
		return err
	}

	stopTyping := b.startTypingKeepalive(fromUserID, contextToken)
	llmResponse, err := llmClient.ChatStream(b.cfg.SystemPrompt, msgs, nil)
	stopTyping()

	if err != nil {
		log.Printf("[monitor] LLM error model=%s: %v", modelName, err)
		b.sendText(fromUserID, "❌ AI 响应失败，请重试。", contextToken)
		return err
	}

	assistantHistory := responseHistoryContent(llmResponse)
	conv.Messages = append(conv.Messages,
		store.Message{Role: "user", Content: text},
		store.Message{Role: "assistant", Content: assistantHistory},
	)

	if err := b.sessions.SaveHistory(fromUserID, sess.ID, conv); err != nil {
		log.Printf("[monitor] save history: %v", err)
	}

	if llmResponse.Text != "" {
		filtered := markdown.FilterText(llmResponse.Text)
		if err := b.sendText(fromUserID, filtered, contextToken); err != nil {
			return err
		}
	}

	for i, image := range llmResponse.Images {
		if err := b.sendImage(fromUserID, contextToken, image); err != nil {
			log.Printf("[monitor] send image failed image=%d/%d: %v", i+1, len(llmResponse.Images), err)
			b.sendText(fromUserID, "❌ AI 响应失败，请重试。", contextToken)
			return err
		}
	}

	log.Printf("[monitor] reply to=%s model=%s len=%d images=%d", fromUserID, modelName, len(llmResponse.Text), len(llmResponse.Images))
	return nil
}

func responseHistoryContent(resp llm.Response) string {
	var parts []string
	if resp.Text != "" {
		parts = append(parts, resp.Text)
	}
	for _, image := range resp.Images {
		mimeType := image.MIMEType
		if mimeType == "" {
			mimeType = "image/png"
		}
		filename := image.Filename
		if filename == "" {
			filename = "image.png"
		}
		parts = append(parts, fmt.Sprintf("[图片: mime=%s filename=%s base64=%s]", mimeType, filename, base64.StdEncoding.EncodeToString(image.Data)))
	}
	return strings.Join(parts, "\n")
}

func (b *bot) llmForUser(userID string) (string, llm.Client, error) {
	modelName, err := b.sessions.CurrentModel(userID)
	if err != nil {
		return "", nil, err
	}
	model, err := b.cfg.ResolveModel(modelName)
	if err != nil {
		model, err = b.cfg.ResolveModel(b.cfg.DefaultModel)
		if err != nil {
			return "", nil, err
		}
	}
	if b.llmClients == nil {
		b.llmClients = map[string]llm.Client{}
	}
	if b.newLLM == nil {
		b.newLLM = defaultLLMFactory
	}
	client, ok := b.llmClients[model.Name]
	if !ok {
		client = b.newLLM(model)
		b.llmClients[model.Name] = client
	}
	return model.Name, client, nil
}

func (b *bot) sendText(toUserID, text, contextToken string) error {
	chunks := splitTextChunks(text, textChunkLimit)
	for i, chunk := range chunks {
		msg := message.BuildTextMessage(toUserID, chunk, contextToken)
		if err := b.client.SendMessage(msg); err != nil {
			log.Printf("[monitor] sendMessage failed chunk=%d/%d len=%d: %v", i+1, len(chunks), len(chunk), err)
			return err
		}
	}
	return nil
}

func (b *bot) sendImage(toUserID, contextToken string, image llm.Image) error {
	uploader := b.uploadImage
	if uploader == nil {
		uploader = uploadImageToWeixinCDN
	}
	uploaded, err := uploader(b.client, b.cdnClient, b.cdnBaseURL, toUserID, image)
	if err != nil {
		return err
	}

	msg := message.BuildImageMessage(toUserID, uploaded.Media, uploaded.MidSize, contextToken)
	if err := b.client.SendMessage(msg); err != nil {
		log.Printf("[monitor] sendImage failed len=%d: %v", len(image.Data), err)
		return err
	}
	return nil
}

func splitTextChunks(text string, limit int) []string {
	if limit <= 0 {
		return []string{text}
	}

	runes := []rune(text)
	if len(runes) <= limit {
		return []string{text}
	}

	chunks := make([]string, 0, len(runes)/limit+1)
	for start := 0; start < len(runes); {
		end := start + limit
		if end >= len(runes) {
			chunks = append(chunks, string(runes[start:]))
			break
		}

		split := findChunkSplit(runes, start, end)
		chunks = append(chunks, string(runes[start:split]))
		start = split
	}
	return chunks
}

func findChunkSplit(runes []rune, start, end int) int {
	for i := end - 1; i >= start; i-- {
		if runes[i] == '\n' {
			return i + 1
		}
	}
	for i := end - 1; i >= start; i-- {
		if unicode.IsSpace(runes[i]) {
			return i + 1
		}
	}
	return end
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

func (b *bot) startTypingKeepalive(toUserID, contextToken string) func() {
	interval := b.typingTick
	if interval <= 0 {
		interval = typingKeepalive
	}

	stop := make(chan struct{})
	done := make(chan struct{})

	b.sendTyping(toUserID, contextToken, api.TypingStatusTyping)
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				b.sendTyping(toUserID, contextToken, api.TypingStatusTyping)
			case <-stop:
				return
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			close(stop)
			<-done
			b.sendTyping(toUserID, contextToken, api.TypingStatusCancel)
		})
	}
}
