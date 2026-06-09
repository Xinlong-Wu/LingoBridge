package core

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

func SplitTextChunks(text string, limit int) []string {
	if limit <= 0 {
		return []string{text}
	}
	return splitTextChunksByLine(text, limit)
}

func SplitTextChunksByRunes(text string, limit int) []string {
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

		split := findRuneChunkSplit(runes, start, end)
		chunks = append(chunks, string(runes[start:split]))
		start = split
	}
	return chunks
}

func findRuneChunkSplit(runes []rune, start, end int) int {
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

func splitTextChunksByLine(text string, limit int) []string {
	chunks := []string{""}
	for text != "" {
		line, rest := splitFirstLine(text)
		chunks = appendLineChunks(chunks, line, limit)
		text = rest
	}
	if len(chunks) == 1 && chunks[0] == "" {
		return []string{""}
	}
	if chunks[0] == "" {
		return chunks[1:]
	}
	return chunks
}

func splitFirstLine(text string) (string, string) {
	if text == "" {
		return "", ""
	}
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		return text[:i+1], text[i+1:]
	}
	return text, ""
}

func appendLineChunks(chunks []string, line string, limit int) []string {
	for line != "" {
		if len(line) > limit {
			longChunks := splitLongLine(line, limit)
			for _, chunk := range longChunks {
				if chunk == "" {
					continue
				}
				if len(chunks) > 0 && chunks[len(chunks)-1] == "" {
					chunks[len(chunks)-1] = chunk
				} else {
					chunks = append(chunks, chunk)
				}
			}
			return chunks
		}
		if len(chunks) == 0 {
			chunks = append(chunks, line)
			return chunks
		}
		last := chunks[len(chunks)-1]
		if last == "" || len(last)+len(line) <= limit {
			chunks[len(chunks)-1] = last + line
			return chunks
		}
		chunks = append(chunks, line)
		return chunks
	}
	return chunks
}

func splitLongLine(text string, limit int) []string {
	var chunks []string
	for text != "" {
		prefix, rest := utf8SafePrefix(text, limit)
		chunks = append(chunks, prefix)
		text = rest
	}
	return chunks
}

func utf8SafePrefix(text string, limit int) (string, string) {
	if limit <= 0 || len(text) <= limit {
		return text, ""
	}
	cut := limit
	for cut > 0 && !utf8.ValidString(text[:cut]) {
		cut--
	}
	if cut == 0 {
		_, size := utf8.DecodeRuneInString(text)
		if size <= 0 {
			size = 1
		}
		return text[:size], text[size:]
	}
	return text[:cut], text[cut:]
}
