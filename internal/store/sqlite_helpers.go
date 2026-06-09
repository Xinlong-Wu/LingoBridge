package store

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func generateID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
