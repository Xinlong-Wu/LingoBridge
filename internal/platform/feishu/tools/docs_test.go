package tools

import (
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestDocsToolConfigDefaultsAndRegistration(t *testing.T) {
	cfg := NormalizeConfig(Config{})
	if cfg.Docs.Enabled {
		t.Fatal("docs tools enabled by default, want disabled")
	}
	if cfg.Docs.AllowWrite {
		t.Fatal("docs tools allow_write = true by default, want false")
	}
	if cfg.MaxResults != DefaultMaxResults || cfg.MaxChars != DefaultMaxChars {
		t.Fatalf("defaults = %#v, want max defaults", cfg)
	}

	client := &lark.Client{}
	if got := NewDocsTools(client, cfg); len(got) != 0 {
		t.Fatalf("disabled tools = %d, want 0", len(got))
	}
	cfg.Docs.Enabled = true
	if got := NewDocsTools(client, cfg); len(got) != 2 {
		t.Fatalf("read-only tools = %d, want search/read", len(got))
	}
	cfg.Docs.AllowWrite = true
	if got := NewDocsTools(client, cfg); len(got) != 2 {
		t.Fatalf("write tools without folder allowlist = %d, want read-only", len(got))
	}
	cfg.AllowedFolderTokens = []string{"fld_token"}
	if got := NewDocsTools(client, cfg); len(got) != 4 {
		t.Fatalf("write tools with folder allowlist = %d, want four tools", len(got))
	}
}

func TestParseDocRef(t *testing.T) {
	tests := []struct {
		name      string
		token     string
		rawURL    string
		kind      string
		wantKind  string
		wantToken string
	}{
		{name: "direct token", token: "doxcnabcdef123456", wantKind: "docx", wantToken: "doxcnabcdef123456"},
		{name: "docx url", rawURL: "https://docs.feishu.cn/docx/doxcnabcdef123456", wantKind: "docx", wantToken: "doxcnabcdef123456"},
		{name: "wiki url", rawURL: "https://wiki.feishu.cn/wiki/wikcnabcdef123456", wantKind: "wiki", wantToken: "wikcnabcdef123456"},
		{name: "kind alias", token: "doxcnabcdef123456", kind: "docs", wantKind: "docx", wantToken: "doxcnabcdef123456"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDocRef(tt.token, tt.rawURL, tt.kind)
			if err != nil {
				t.Fatalf("parseDocRef returned error: %v", err)
			}
			if got.Kind != tt.wantKind || got.Token != tt.wantToken {
				t.Fatalf("parseDocRef = %#v, want kind=%s token=%s", got, tt.wantKind, tt.wantToken)
			}
		})
	}
}

func TestTruncateRunes(t *testing.T) {
	got, truncated := truncateRunes("你好世界", 2)
	if got != "你好" || !truncated {
		t.Fatalf("truncateRunes = %q %v, want 你好 true", got, truncated)
	}
	got, truncated = truncateRunes("hello", 10)
	if got != "hello" || truncated {
		t.Fatalf("truncateRunes = %q %v, want hello false", got, truncated)
	}
}
