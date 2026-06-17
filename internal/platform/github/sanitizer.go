package github

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var (
	reviewHTMLCommentRe     = regexp.MustCompile(`(?s)<!--.*?-->`)
	reviewHTMLHiddenAttrRe  = regexp.MustCompile(`(?i)\s(?:alt|title|aria-label|placeholder|data-[a-z0-9_:-]+)\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]+)`)
	reviewMarkdownImageRe   = regexp.MustCompile(`!\[[^\]\n]*\]`)
	reviewMarkdownTitleRe   = regexp.MustCompile(`(\[[^\]\n]*\]\([^\)\s]+)(?:\s+(?:"[^"]*"|'[^']*'|\([^)]*\)))(\))`)
	reviewHTMLEntityRe      = regexp.MustCompile(`&#(x[0-9A-Fa-f]+|[0-9]+);?`)
	reviewGitHubTokenLikeRe = regexp.MustCompile(`\b(?:gh[pors]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,})\b`)
)

func sanitizeReviewPromptText(value string) string {
	value = normalizeReviewASCIIEntities(value)
	value = reviewHTMLCommentRe.ReplaceAllString(value, "")
	value = reviewHTMLHiddenAttrRe.ReplaceAllString(value, "")
	value = reviewMarkdownImageRe.ReplaceAllString(value, "![]")
	value = reviewMarkdownTitleRe.ReplaceAllString(value, "$1$2")
	value = reviewGitHubTokenLikeRe.ReplaceAllString(value, "[REDACTED_GITHUB_TOKEN]")
	value = stripReviewInvisibleChars(value)
	return strings.TrimSpace(value)
}

func normalizeReviewASCIIEntities(value string) string {
	return reviewHTMLEntityRe.ReplaceAllStringFunc(value, func(match string) string {
		raw := reviewHTMLEntityRe.FindStringSubmatch(match)
		if len(raw) != 2 {
			return match
		}
		var n int64
		var err error
		if strings.HasPrefix(raw[1], "x") || strings.HasPrefix(raw[1], "X") {
			n, err = strconv.ParseInt(raw[1][1:], 16, 32)
		} else {
			n, err = strconv.ParseInt(raw[1], 10, 32)
		}
		if err != nil || n < 32 || n > 126 {
			return ""
		}
		return string(rune(n))
	})
}

func stripReviewInvisibleChars(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch r {
		case '\n', '\r', '\t':
			b.WriteRune(r)
			continue
		case '\u00ad', '\u061c', '\u180e', '\u200b', '\u200c', '\u200d', '\u200e', '\u200f', '\u2060', '\ufeff':
			continue
		}
		if unicode.IsControl(r) || (r >= '\u202a' && r <= '\u202e') || (r >= '\u2066' && r <= '\u2069') {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
