package message

import (
	"testing"

	"lingobridge/internal/platform/wechat/api"
)

func TestExtractLLMText(t *testing.T) {
	tests := []struct {
		name string
		msg  *api.WeixinMessage
		want string
	}{
		{
			name: "plain text",
			msg: &api.WeixinMessage{ItemList: []*api.MessageItem{
				{Type: api.ItemTypeText, TextItem: &api.TextItem{Text: "hello"}},
			}},
			want: "hello",
		},
		{
			name: "quote title",
			msg: &api.WeixinMessage{ItemList: []*api.MessageItem{
				{
					Type:     api.ItemTypeText,
					TextItem: &api.TextItem{Text: "reply"},
					RefMsg:   &api.RefMessage{Title: "original title"},
				},
			}},
			want: "[引用: original title]\nreply",
		},
		{
			name: "quote title and text item",
			msg: &api.WeixinMessage{ItemList: []*api.MessageItem{
				{
					Type:     api.ItemTypeText,
					TextItem: &api.TextItem{Text: "my reply"},
					RefMsg: &api.RefMessage{
						Title: "Author",
						MessageItem: &api.MessageItem{
							Type:     api.ItemTypeText,
							TextItem: &api.TextItem{Text: "original text"},
						},
					},
				},
			}},
			want: "[引用: Author | original text]\nmy reply",
		},
		{
			name: "quote text item only",
			msg: &api.WeixinMessage{ItemList: []*api.MessageItem{
				{
					Type:     api.ItemTypeText,
					TextItem: &api.TextItem{Text: "reply"},
					RefMsg: &api.RefMessage{MessageItem: &api.MessageItem{
						Type:     api.ItemTypeText,
						TextItem: &api.TextItem{Text: "quoted"},
					}},
				},
			}},
			want: "[引用: quoted]\nreply",
		},
		{
			name: "empty quote",
			msg: &api.WeixinMessage{ItemList: []*api.MessageItem{
				{
					Type:     api.ItemTypeText,
					TextItem: &api.TextItem{Text: "reply"},
					RefMsg:   &api.RefMessage{},
				},
			}},
			want: "reply",
		},
		{
			name: "media quote",
			msg: &api.WeixinMessage{ItemList: []*api.MessageItem{
				{
					Type:     api.ItemTypeText,
					TextItem: &api.TextItem{Text: "reply"},
					RefMsg: &api.RefMessage{MessageItem: &api.MessageItem{
						Type: api.ItemTypeImage,
					}},
				},
			}},
			want: "reply",
		},
		{
			name: "voice transcription",
			msg: &api.WeixinMessage{ItemList: []*api.MessageItem{
				{Type: api.ItemTypeVoice, VoiceItem: &api.VoiceItem{Text: "voice text"}},
			}},
			want: "voice text",
		},
		{
			name: "nil message",
			msg:  nil,
			want: "",
		},
		{
			name: "non text",
			msg: &api.WeixinMessage{ItemList: []*api.MessageItem{
				{Type: api.ItemTypeImage},
			}},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractLLMText(tt.msg); got != tt.want {
				t.Fatalf("ExtractLLMText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractTextIgnoresQuotedContext(t *testing.T) {
	msg := &api.WeixinMessage{ItemList: []*api.MessageItem{
		{
			Type:     api.ItemTypeText,
			TextItem: &api.TextItem{Text: "/list"},
			RefMsg: &api.RefMessage{MessageItem: &api.MessageItem{
				Type:     api.ItemTypeText,
				TextItem: &api.TextItem{Text: "not a command"},
			}},
		},
	}}

	if got := ExtractText(msg); got != "/list" {
		t.Fatalf("ExtractText() = %q, want /list", got)
	}
}
