package monitor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"lingobridge/internal/config"
	"lingobridge/internal/core"
	"lingobridge/internal/llm"
	"lingobridge/internal/logging"
	"lingobridge/internal/platform/wechat/api"
	"lingobridge/internal/platform/wechat/cdn"
	"lingobridge/internal/platform/wechat/message"
	"lingobridge/internal/session"
	"lingobridge/internal/store"
)

const (
	sessionExpiryPause  = 1 * time.Hour
	maxConsecutiveFails = 3
	backoffBase         = 5 * time.Second
	textChunkLimit      = 4000
	typingKeepalive     = 5 * time.Second
	defaultImagePrompt  = "请描述这张图片。"
	maxVisionImageBytes = 20 * 1024 * 1024
)

var wechatLog = logging.For("wechat")

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

type imageDownloader func(item *api.ImageItem) ([]byte, string, error)
type mediaSaver func(userID, sessionID, role string, index int, mimeType string, data []byte) (*store.MediaFile, error)

type bot struct {
	client        wechatClient
	cursors       cursorStore
	typingTick    time.Duration
	cdnBaseURL    string
	cdnClient     *http.Client
	uploadImage   imageUploader
	downloadImage imageDownloader
	saveMedia     mediaSaver
	handler       core.Handler
}

// Run starts the single-threaded monitor loop for an account.
func Run(st *store.Store, sm *session.Manager, cfg config.LLMConfig, acc store.Account) error {
	return RunContext(context.Background(), st, sm, cfg, acc)
}

// RunContext starts the monitor loop for an account until ctx is canceled.
func RunContext(ctx context.Context, st *store.Store, sm *session.Manager, cfg config.LLMConfig, acc store.Account) error {
	return NewPlatform(st, sm, cfg, acc).Run(ctx, core.New(sm, cfg))
}

type Platform struct {
	store    *store.Store
	sessions *session.Manager
	cfg      config.LLMConfig
	account  store.Account
}

var _ core.Platform = (*Platform)(nil)

func NewPlatform(st *store.Store, sm *session.Manager, cfg config.LLMConfig, acc store.Account) *Platform {
	return &Platform{
		store:    st,
		sessions: sm,
		cfg:      cfg,
		account:  acc,
	}
}

func (p *Platform) Run(ctx context.Context, handler core.Handler) error {
	acc := p.account
	client := api.NewClient(acc.BaseURL, acc.Token)
	client.Debug = false

	b := &bot{
		client:     client,
		cursors:    p.store,
		cdnBaseURL: defaultWeixinCDNBaseURL,
		handler:    handler,
		saveMedia:  p.store.SaveMediaFile,
	}
	return b.runAccount(ctx, acc)
}

func (b *bot) runAccount(ctx context.Context, acc store.Account) error {
	wechatLog.Info(ctx, "Starting for account %s (%s)", acc.Name, acc.ID)

	if err := b.client.NotifyStart(); err != nil {
		wechatLog.Warn(ctx, "notifyStart failed: %v", err)
	}
	defer func() {
		if err := b.client.NotifyStop(); err != nil {
			wechatLog.Warn(ctx, "notifyStop failed: %v", err)
		}
	}()

	buf, err := b.cursors.GetSyncBuf(acc.ID)
	if err != nil {
		wechatLog.Warn(ctx, "load sync buf: %v", err)
	}

	consecutiveFails := 0
	sessionPausedUntil := time.Time{}

	for {
		if ctx.Err() != nil {
			return nil
		}

		if time.Now().Before(sessionPausedUntil) {
			wait := time.Until(sessionPausedUntil)
			wechatLog.Warn(ctx, "Session paused for %v", wait.Round(time.Second))
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
			wechatLog.Warn(ctx, "getUpdates failed: %v (fail %d/%d)", err, consecutiveFails, maxConsecutiveFails)
			if consecutiveFails >= maxConsecutiveFails {
				if !sleepContext(ctx, backoffBase) {
					return nil
				}
			}
			continue
		}

		consecutiveFails = 0

		if resp.Errcode == -14 {
			wechatLog.Warn(ctx, "Session expired, pausing for %v", sessionExpiryPause)
			sessionPausedUntil = time.Now().Add(sessionExpiryPause)
			continue
		}

		if resp.GetUpdatesBuf != "" {
			buf = resp.GetUpdatesBuf
			if err := b.cursors.SaveSyncBuf(acc.ID, buf); err != nil {
				wechatLog.Warn(ctx, "save sync buf: %v", err)
			}
		}

		for _, msg := range resp.Msgs {
			if err := b.processOne(msg); err != nil {
				wechatLog.Warn(ctx, "process message: %v", err)
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
	wechatLog.Debug(context.Background(), "msg from=%s len=%d", fromUserID, len(llmText))

	if strings.HasPrefix(strings.TrimSpace(commandText), "/") {
		return b.handleCore(context.Background(), core.InboundMessage{
			Platform:    store.PlatformWeChat,
			UserKey:     fromUserID,
			CommandText: commandText,
			LLMText:     llmText,
		}, wechatSender{bot: b, toUserID: fromUserID, contextToken: contextToken})
	}

	var stop bool
	llmText, stop = b.applyMedia(msg, fromUserID, contextToken, llmText)
	if stop || llmText == "" {
		if stop || len(imageItems(msg)) == 0 {
			return nil
		}
	}

	return b.replyWithLLM(fromUserID, contextToken, llmText, msg)
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
				wechatLog.Debug(context.Background(), "image from=%s", fromUserID)
			}
		case api.ItemTypeVoice:
			if item.VoiceItem != nil && item.VoiceItem.Text != "" {
				text = item.VoiceItem.Text
			} else if item.VoiceItem != nil && item.VoiceItem.Media != nil {
				wechatLog.Debug(context.Background(), "voice from=%s (no transcription)", fromUserID)
				b.sendText(fromUserID, "🎤 语音消息暂不支持自动识别，请发送文字。", contextToken)
				return text, true
			}
		case api.ItemTypeVideo:
			wechatLog.Debug(context.Background(), "video from=%s", fromUserID)
			b.sendText(fromUserID, "🎬 视频消息已收到，暂不支持视频理解。", contextToken)
			return text, true
		case api.ItemTypeFile:
			wechatLog.Debug(context.Background(), "file from=%s", fromUserID)
			b.sendText(fromUserID, "📎 文件消息已收到，暂不支持文件处理。", contextToken)
			return text, true
		}
	}

	return text, false
}

func imageItems(msg *api.WeixinMessage) []*api.ImageItem {
	if msg == nil {
		return nil
	}
	var images []*api.ImageItem
	for _, item := range msg.ItemList {
		if item == nil || item.Type != api.ItemTypeImage || item.ImageItem == nil {
			continue
		}
		if item.ImageItem.Media != nil || item.ImageItem.ThumbMedia != nil || item.ImageItem.URL != "" {
			images = append(images, item.ImageItem)
		}
	}
	return images
}

func (b *bot) imageInputAttachments(userID, sessionID string, msg *api.WeixinMessage) ([]llm.InputAttachment, error) {
	images := imageItems(msg)
	if len(images) == 0 {
		return nil, nil
	}

	downloader := b.downloadImage
	if downloader == nil {
		downloader = downloadImageFromWeixin
	}

	attachments := make([]llm.InputAttachment, 0, len(images))
	for i, image := range images {
		data, mimeType, err := downloader(image)
		if err != nil {
			return nil, fmt.Errorf("download image: %w", err)
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("download image: empty image data")
		}
		if len(data) > maxVisionImageBytes {
			return nil, fmt.Errorf("download image: image is too large (%d bytes)", len(data))
		}
		if mimeType == "" {
			mimeType = detectImageMIME(data)
		}
		if !strings.HasPrefix(mimeType, "image/") {
			return nil, fmt.Errorf("download image: unsupported MIME type %q", mimeType)
		}

		mediaFile, err := b.saveMediaFile(userID, sessionID, "user", i, mimeType, data)
		if err != nil {
			return nil, fmt.Errorf("save image: %w", err)
		}
		attachments = append(attachments, llm.InputAttachment{
			Type:      "image",
			MIMEType:  mimeType,
			Filename:  mediaFile.Filename,
			Size:      len(data),
			Data:      data,
			LocalPath: mediaFile.RelativePath,
		})
	}
	return attachments, nil
}

func imageInputErrorText(err error) string {
	if errors.Is(err, llm.ErrUnsupportedAttachment) {
		return "当前模型暂不支持图片上下文，请切换支持图片的模型后重试。"
	}
	return "❌ 图片处理失败，请重试。"
}

func downloadImageFromWeixin(item *api.ImageItem) ([]byte, string, error) {
	if item == nil {
		return nil, "", fmt.Errorf("image item is nil")
	}
	if item.Media != nil {
		data, err := downloadCDNMedia(item.Media, item.AESKey)
		if err != nil {
			return nil, "", err
		}
		return data, detectImageMIME(data), nil
	}
	if item.URL != "" {
		data, err := downloadPlainImageURL(item.URL)
		if err != nil {
			return nil, "", err
		}
		return data, detectImageMIME(data), nil
	}
	if item.ThumbMedia != nil {
		data, err := downloadCDNMedia(item.ThumbMedia, item.AESKey)
		if err != nil {
			return nil, "", err
		}
		return data, detectImageMIME(data), nil
	}
	return nil, "", fmt.Errorf("no image media available")
}

func downloadCDNMedia(media *api.CDNMedia, fallbackAESKey string) ([]byte, error) {
	if media == nil {
		return nil, fmt.Errorf("media is nil")
	}
	aesKey := media.AESKey
	if aesKey == "" {
		aesKey = fallbackAESKey
	}
	return cdn.DownloadAndDecrypt(media.EncryptQueryParam, aesKey, media.FullURL)
}

func downloadPlainImageURL(rawURL string) ([]byte, error) {
	resp, err := http.Get(rawURL)
	if err != nil {
		return nil, fmt.Errorf("download URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("download URL HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxVisionImageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read image URL: %w", err)
	}
	return data, nil
}

func detectImageMIME(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	return http.DetectContentType(data)
}

func (b *bot) replyWithLLM(fromUserID, contextToken, text string, msg *api.WeixinMessage) error {
	in := core.InboundMessage{
		Platform:       store.PlatformWeChat,
		UserKey:        fromUserID,
		CommandText:    text,
		LLMText:        text,
		MutateResponse: b.persistResponseImages,
		ErrorNotice: func(err error) string {
			if errors.Is(err, llm.ErrUnsupportedAttachment) {
				return imageInputErrorText(err)
			}
			return aiErrorNotice(err)
		},
	}
	if len(imageItems(msg)) > 0 {
		in.PrepareErrorNotice = imageInputErrorText
		in.PrepareUserMessage = func(ctx context.Context, userID, sessionID string, client llm.Client) (store.Message, error) {
			prompt := text
			if prompt == "" {
				prompt = defaultImagePrompt
			}
			attachments, err := b.imageInputAttachments(userID, sessionID, msg)
			if err != nil {
				return store.Message{}, err
			}
			return client.PrepareUserMessage(prompt, attachments)
		}
	}
	return b.handleCore(context.Background(), in, wechatSender{
		bot:          b,
		toUserID:     fromUserID,
		contextToken: contextToken,
	})
}

func (b *bot) handleCore(ctx context.Context, msg core.InboundMessage, sender core.Sender) error {
	if b.handler == nil {
		return fmt.Errorf("core handler is not configured")
	}
	return b.handler.Handle(ctx, msg, sender)
}

type wechatSender struct {
	bot          *bot
	toUserID     string
	contextToken string
}

func (s wechatSender) Send(ctx context.Context, msg core.OutboundMessage) error {
	if msg.Text != "" {
		return s.bot.sendText(s.toUserID, msg.Text, s.contextToken)
	}
	if len(msg.Image.Data) > 0 || msg.Image.Filename != "" || msg.Image.LocalPath != "" {
		return s.bot.sendImage(s.toUserID, s.contextToken, msg.Image)
	}
	return nil
}

func (s wechatSender) StartTyping(ctx context.Context) func() {
	return s.bot.startTypingKeepalive(s.toUserID, s.contextToken)
}

func (s wechatSender) StartCompactNotice(ctx context.Context, notice core.CompactNotice) (core.CompactNoticeHandle, error) {
	return core.CompactNoticeHandle{}, s.bot.sendText(s.toUserID, core.CompactStartText(), s.contextToken)
}

func (s wechatSender) FinishCompactNotice(ctx context.Context, handle core.CompactNoticeHandle, notice core.CompactNotice) error {
	return s.bot.sendText(s.toUserID, core.CompactSuccessText(notice), s.contextToken)
}

func aiErrorNotice(err error) string {
	return core.AIErrorNotice(err)
}

func (b *bot) persistResponseImages(userID, sessionID string, resp llm.Response) llm.Response {
	for i := range resp.Images {
		if len(resp.Images[i].Data) == 0 {
			continue
		}
		mimeType := resp.Images[i].MIMEType
		if mimeType == "" {
			mimeType = "image/png"
		}
		resp.Images[i].MIMEType = mimeType
		mediaFile, err := b.saveMediaFile(userID, sessionID, "assistant", i, mimeType, resp.Images[i].Data)
		if err != nil {
			wechatLog.Warn(context.Background(), "save response image failed image=%d: %v", i+1, err)
			continue
		}
		resp.Images[i].Filename = mediaFile.Filename
		resp.Images[i].LocalPath = mediaFile.RelativePath
	}
	return resp
}

func (b *bot) saveMediaFile(userID, sessionID, role string, index int, mimeType string, data []byte) (*store.MediaFile, error) {
	saver := b.saveMedia
	if saver == nil {
		return nil, fmt.Errorf("media saver is not configured")
	}
	return saver(userID, sessionID, role, index, mimeType, data)
}

func (b *bot) sendText(toUserID, text, contextToken string) error {
	chunks := core.SplitTextChunksByRunes(text, textChunkLimit)
	for i, chunk := range chunks {
		msg := message.BuildTextMessage(toUserID, chunk, contextToken)
		if err := b.client.SendMessage(msg); err != nil {
			wechatLog.Error(context.Background(), "sendMessage failed chunk=%d/%d len=%d: %v", i+1, len(chunks), len(chunk), err)
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
		wechatLog.Error(context.Background(), "sendImage failed len=%d: %v", len(image.Data), err)
		return err
	}
	return nil
}

func (b *bot) sendTyping(toUserID, contextToken string, status int) {
	resp, err := b.client.GetConfig(toUserID, contextToken)
	if err != nil {
		return
	}
	if err := b.client.SendTyping(toUserID, resp.TypingTicket, status); err != nil {
		wechatLog.Warn(context.Background(), "sendTyping failed: %v", err)
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
