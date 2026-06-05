package message

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"wechatbox/internal/platform/wechat/api"
)

func generateClientID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "wechatbox-" + strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return "wechatbox-" + hex.EncodeToString(b)
}

// ExtractText extracts the text content from a WeChat message.
func ExtractText(msg *api.WeixinMessage) string {
	if msg == nil || msg.ItemList == nil {
		return ""
	}
	var texts []string
	for _, item := range msg.ItemList {
		if item != nil && item.Type == api.ItemTypeText && item.TextItem != nil {
			texts = append(texts, item.TextItem.Text)
		}
	}
	return strings.Join(texts, "")
}

// ExtractLLMText extracts the message body sent to the LLM. When a text
// message replies to another text message, it prefixes the current text with
// quoted context so the model can understand what the user is referring to.
func ExtractLLMText(msg *api.WeixinMessage) string {
	if msg == nil {
		return ""
	}
	return bodyFromItemList(msg.ItemList)
}

func bodyFromItemList(items []*api.MessageItem) string {
	if len(items) == 0 {
		return ""
	}
	for _, item := range items {
		if item == nil {
			continue
		}
		switch item.Type {
		case api.ItemTypeText:
			if item.TextItem == nil {
				continue
			}
			text := item.TextItem.Text
			if item.RefMsg == nil {
				return text
			}
			if item.RefMsg.MessageItem != nil && isMediaItem(item.RefMsg.MessageItem) {
				return text
			}

			var parts []string
			if item.RefMsg.Title != "" {
				parts = append(parts, item.RefMsg.Title)
			}
			if item.RefMsg.MessageItem != nil {
				refBody := bodyFromItemList([]*api.MessageItem{item.RefMsg.MessageItem})
				if refBody != "" {
					parts = append(parts, refBody)
				}
			}
			if len(parts) == 0 {
				return text
			}
			return "[引用: " + strings.Join(parts, " | ") + "]\n" + text
		case api.ItemTypeVoice:
			if item.VoiceItem != nil && item.VoiceItem.Text != "" {
				return item.VoiceItem.Text
			}
		}
	}
	return ""
}

// HasMedia checks if the message contains any media items.
func HasMedia(msg *api.WeixinMessage) bool {
	if msg == nil || msg.ItemList == nil {
		return false
	}
	for _, item := range msg.ItemList {
		if item != nil && isMediaItem(item) {
			return true
		}
	}
	return false
}

func isMediaItem(item *api.MessageItem) bool {
	if item == nil {
		return false
	}
	switch item.Type {
	case api.ItemTypeImage, api.ItemTypeVoice, api.ItemTypeVideo, api.ItemTypeFile:
		return true
	default:
		return false
	}
}

// BuildTextMessage creates a WeixinMessage for sending text.
func BuildTextMessage(toUserID, text, contextToken string) *api.WeixinMessage {
	return &api.WeixinMessage{
		ToUserID:     toUserID,
		ClientID:     generateClientID(),
		MessageType:  api.MessageTypeBot,
		MessageState: api.MessageStateFinish,
		ItemList: []*api.MessageItem{
			{
				Type:        api.ItemTypeText,
				IsCompleted: true,
				TextItem:    &api.TextItem{Text: text},
			},
		},
		ContextToken: contextToken,
	}
}

// BuildImageMessage creates a WeixinMessage for sending an image.
func BuildImageMessage(toUserID string, media *api.CDNMedia, midSize int, contextToken string) *api.WeixinMessage {
	return &api.WeixinMessage{
		ToUserID:     toUserID,
		ClientID:     generateClientID(),
		MessageType:  api.MessageTypeBot,
		MessageState: api.MessageStateFinish,
		ItemList: []*api.MessageItem{
			{
				Type:        api.ItemTypeImage,
				IsCompleted: true,
				ImageItem:   &api.ImageItem{Media: media, MidSize: midSize},
			},
		},
		ContextToken: contextToken,
	}
}
