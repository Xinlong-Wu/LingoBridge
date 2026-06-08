package monitor

import "strings"

// wechatMarkdownFilter strips Markdown syntax that is not suitable for WeChat
// text messages while keeping the readable content.
type wechatMarkdownFilter struct {
	buf    strings.Builder
	inCode bool
}

func newWechatMarkdownFilter() *wechatMarkdownFilter {
	return &wechatMarkdownFilter{}
}

func (f *wechatMarkdownFilter) write(chunk string) string {
	f.buf.Reset()

	for i := 0; i < len(chunk); i++ {
		ch := chunk[i]

		if ch == '`' {
			tickCount := f.countConsecutive(chunk, i, '`')
			if tickCount >= 3 {
				if !f.inCode {
					f.inCode = true
					langEnd := i + tickCount
					for langEnd < len(chunk) && chunk[langEnd] != '\n' {
						langEnd++
					}
					i = langEnd
					continue
				}
				f.inCode = false
				i += tickCount - 1
				continue
			}
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

		f.buf.WriteByte(ch)
	}

	return f.buf.String()
}

func (f *wechatMarkdownFilter) countConsecutive(s string, start int, ch byte) int {
	count := 0
	for i := start; i < len(s) && s[i] == ch; i++ {
		count++
	}
	return count
}

func filterWechatMarkdownText(text string) string {
	return newWechatMarkdownFilter().write(text)
}
