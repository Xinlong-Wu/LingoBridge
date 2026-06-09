package core

import (
	"errors"
	"regexp"
	"strings"
)

const AIErrorSummaryRunes = 300

var (
	bearerTokenPattern = regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9._~+/=-]+`)
	openAIKeyPattern   = regexp.MustCompile(`sk-[A-Za-z0-9_-]{8,}`)
	hexTokenPattern    = regexp.MustCompile(`\b[0-9a-fA-F]{32,}\b`)
)

func AIErrorNotice(err error) string {
	summary := SummarizeError(err, AIErrorSummaryRunes)
	if summary == "" {
		summary = "未知错误"
	}
	return "❌ AI 响应失败：" + summary
}

func SummarizeError(err error, maxRunes int) string {
	if err == nil {
		return ""
	}
	summary := err.Error()
	summary = bearerTokenPattern.ReplaceAllString(summary, "Bearer [REDACTED]")
	summary = openAIKeyPattern.ReplaceAllString(summary, "sk-[REDACTED]")
	summary = hexTokenPattern.ReplaceAllString(summary, "[REDACTED]")
	summary = strings.Join(strings.Fields(summary), " ")
	return TruncateRunes(summary, maxRunes)
}

func TruncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}

var ErrUnsupportedImage = errors.New("platform does not support sending images")
