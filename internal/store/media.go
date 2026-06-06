package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MediaFile describes a locally persisted media file.
type MediaFile struct {
	RelativePath string
	Filename     string
	Size         int
}

// SaveMediaFile writes media bytes under data/media/{user}/{session}/ in this platform store and
// returns the path relative to the data directory.
func (s *Store) SaveMediaFile(userID, sessionID, role string, index int, mimeType string, data []byte) (*MediaFile, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("media data is empty")
	}

	userPart := safeMediaPathComponent(userID)
	sessionPart := safeMediaPathComponent(sessionID)
	filename := mediaFilename(role, index, mimeType)
	relPath := filepath.ToSlash(filepath.Join("media", userPart, sessionPart, filename))
	absPath := filepath.Join(s.dataDir, filepath.FromSlash(relPath))

	if err := os.MkdirAll(filepath.Dir(absPath), 0700); err != nil {
		return nil, fmt.Errorf("create media dir: %w", err)
	}
	if err := os.WriteFile(absPath, data, 0600); err != nil {
		return nil, fmt.Errorf("write media file: %w", err)
	}

	return &MediaFile{
		RelativePath: relPath,
		Filename:     filename,
		Size:         len(data),
	}, nil
}

func safeMediaPathComponent(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
		if b.Len() >= 48 {
			break
		}
	}
	base := strings.Trim(b.String(), "._-")
	if base == "" {
		base = "unknown"
	}
	return base + "-" + shortMediaHash(raw)
}

func mediaFilename(role string, index int, mimeType string) string {
	prefix := safeMediaFilenamePrefix(role)
	if index < 0 {
		index = 0
	}
	now := time.Now().UTC().Format("20060102T150405Z")
	return fmt.Sprintf("%s-%d-%s-%s.%s", prefix, index+1, now, randomHex(4), mediaExtension(mimeType))
}

func safeMediaFilenamePrefix(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	prefix := strings.Trim(b.String(), "-_")
	if prefix == "" {
		return "media"
	}
	return prefix
}

func mediaExtension(mimeType string) string {
	switch strings.ToLower(mimeType) {
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/png":
		return "png"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	default:
		return "bin"
	}
}

func shortMediaHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:4])
}

func randomHex(n int) string {
	if n <= 0 {
		return ""
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return shortMediaHash(time.Now().UTC().String())[:n*2]
	}
	return hex.EncodeToString(buf)
}
