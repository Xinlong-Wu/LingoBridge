package markdown

import "strings"

// Filter is a streaming markdown-to-WeChat filter.
// It strips unsupported markdown syntax and converts supported elements.
type Filter struct {
	buf      strings.Builder
	inCode   bool
	codeTick int // number of consecutive backticks for current code fence
	codeLang string
}

// NewFilter creates a new streaming markdown filter.
func NewFilter() *Filter {
	return &Filter{}
}

// Write processes a chunk of text and writes filtered output.
func (f *Filter) Write(chunk string) string {
	f.buf.Reset()

	for i := 0; i < len(chunk); i++ {
		ch := chunk[i]

		// Handle backtick code fences
		if ch == '`' {
			tickCount := f.countConsecutive(chunk, i, '`')
			if tickCount >= 3 {
				// Code fence
				if !f.inCode {
					f.inCode = true
					f.codeTick = tickCount
					// Read language
					langEnd := i + tickCount
					for langEnd < len(chunk) && chunk[langEnd] != '\n' {
						langEnd++
					}
					f.codeLang = strings.TrimSpace(chunk[i+tickCount : langEnd])
					i = langEnd
					continue
				}
				// Closing fence
				f.inCode = false
				f.codeTick = 0
				f.codeLang = ""
				i += tickCount - 1
				continue
			}
			// Inline code
			if !f.inCode {
				endTick := strings.IndexByte(chunk[i+1:], '`')
				if endTick >= 0 {
					code := chunk[i+1 : i+1+endTick]
					f.buf.WriteString(code)
					i += endTick + 1
					continue
				}
			}
		}

		if f.inCode {
			f.buf.WriteByte(ch)
			continue
		}

		// Strip images: ![alt](url)
		if ch == '!' && i+1 < len(chunk) && chunk[i+1] == '[' {
			closeBracket := strings.IndexByte(chunk[i+2:], ']')
			if closeBracket >= 0 && i+3+closeBracket < len(chunk) && chunk[i+3+closeBracket] == '(' {
				closeParen := strings.IndexByte(chunk[i+4+closeBracket:], ')')
				if closeParen >= 0 {
					i += 4 + closeBracket + closeParen
					continue
				}
			}
		}

		// Convert bold: **text** -> text (WeChat doesn't support bold natively, keep as-is)
		// WeChat supports some markdown in newer versions, so pass through.
		f.buf.WriteByte(ch)
	}

	return f.buf.String()
}

// Flush returns any remaining buffered output.
func (f *Filter) Flush() string {
	return ""
}

func (f *Filter) countConsecutive(s string, start int, ch byte) int {
	count := 0
	for i := start; i < len(s) && s[i] == ch; i++ {
		count++
	}
	return count
}

// FilterText applies the markdown filter to a complete text string.
func FilterText(text string) string {
	f := NewFilter()
	return f.Write(text)
}
