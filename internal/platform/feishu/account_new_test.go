package feishu

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseAccountNewFlagsPromptsForMissingCredentials(t *testing.T) {
	var out bytes.Buffer
	opts, err := ParseAccountNewFlags(nil, strings.NewReader("cli_prompt\nsecret_prompt\n\n"), &out)
	if err != nil {
		t.Fatalf("ParseAccountNewFlags returned error: %v", err)
	}
	if opts.Name != "default" || opts.AppID != "cli_prompt" || opts.AppSecret != "secret_prompt" || opts.BaseURL != "" {
		t.Fatalf("options = %#v", opts)
	}
	for _, want := range []string{"飞书 App ID", "飞书 App Secret", "飞书 API Base URL"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("prompt output = %q, want %q", out.String(), want)
		}
	}
}

func TestParseAccountNewFlagsPromptsOnlyMissingFields(t *testing.T) {
	var out bytes.Buffer
	opts, err := ParseAccountNewFlags(
		[]string{"--name", "fsbot", "--app-id", "cli_flag"},
		strings.NewReader("secret_prompt\nhttps://open.feishu.cn\n"),
		&out,
	)
	if err != nil {
		t.Fatalf("ParseAccountNewFlags returned error: %v", err)
	}
	if opts.Name != "fsbot" || opts.AppID != "cli_flag" || opts.AppSecret != "secret_prompt" || opts.BaseURL != "https://open.feishu.cn" {
		t.Fatalf("options = %#v", opts)
	}
	if strings.Contains(out.String(), "飞书 App ID") {
		t.Fatalf("prompt output = %q, did not want App ID prompt", out.String())
	}
	if !strings.Contains(out.String(), "飞书 App Secret") {
		t.Fatalf("prompt output = %q, want App Secret prompt", out.String())
	}
}

func TestParseAccountNewFlagsDoesNotPromptWhenRequiredFlagsProvided(t *testing.T) {
	var out bytes.Buffer
	opts, err := ParseAccountNewFlags(
		[]string{"--name", "fsbot", "--app-id", "cli_flag", "--app-secret", "secret_flag"},
		strings.NewReader(""),
		&out,
	)
	if err != nil {
		t.Fatalf("ParseAccountNewFlags returned error: %v", err)
	}
	if opts.Name != "fsbot" || opts.AppID != "cli_flag" || opts.AppSecret != "secret_flag" || opts.BaseURL != "" {
		t.Fatalf("options = %#v", opts)
	}
	if out.String() != "" {
		t.Fatalf("prompt output = %q, want no prompt", out.String())
	}
}

func TestNewAccountUsesParsedPromptCredentials(t *testing.T) {
	opts, err := ParseAccountNewFlags([]string{"--name", "fsbot"}, strings.NewReader("cli_prompt\nsecret_prompt\n\n"), &bytes.Buffer{})
	if err != nil {
		t.Fatalf("ParseAccountNewFlags returned error: %v", err)
	}
	acc, err := NewAccount(opts.Name, opts.AppID, opts.AppSecret, opts.BaseURL)
	if err != nil {
		t.Fatalf("NewAccount returned error: %v", err)
	}
	if acc.Name != "fsbot" || acc.UserID != "cli_prompt" || acc.CredentialsJSON != "{}" {
		t.Fatalf("account = %#v", acc)
	}
}
