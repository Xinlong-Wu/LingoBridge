package github

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	cursorStatusReviewed            = "reviewed"
	cursorStatusMissingInstructions = "missing_instructions"
)

type cursorStore interface {
	GetSyncBuf(accountID string) (string, error)
	SaveSyncBuf(accountID, buf string) error
}

type cursorState struct {
	PRs map[string]cursorEntry `json:"prs"`
}

type cursorEntry struct {
	HeadSHA   string `json:"head_sha"`
	Status    string `json:"status"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

func loadCursor(st cursorStore, accountID string) (cursorState, error) {
	buf, err := st.GetSyncBuf(accountID)
	if err != nil {
		return cursorState{}, err
	}
	if strings.TrimSpace(buf) == "" {
		return cursorState{PRs: map[string]cursorEntry{}}, nil
	}
	var state cursorState
	if err := json.Unmarshal([]byte(buf), &state); err != nil {
		return cursorState{}, fmt.Errorf("parse github cursor: %w", err)
	}
	if state.PRs == nil {
		state.PRs = map[string]cursorEntry{}
	}
	return state, nil
}

func saveCursor(st cursorStore, accountID string, state cursorState) error {
	if state.PRs == nil {
		state.PRs = map[string]cursorEntry{}
	}
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal github cursor: %w", err)
	}
	return st.SaveSyncBuf(accountID, string(data))
}

func cursorKey(pr PullRequest) string {
	return fmt.Sprintf("%s#%d", pr.Base.Repo.FullName(), pr.Number)
}

func shouldProcessCursor(state cursorState, pr PullRequest) bool {
	entry, ok := state.PRs[cursorKey(pr)]
	if !ok {
		return true
	}
	if strings.TrimSpace(entry.HeadSHA) != strings.TrimSpace(pr.Head.SHA) {
		return true
	}
	return entry.Status != cursorStatusReviewed && entry.Status != cursorStatusMissingInstructions
}

func markCursor(state cursorState, pr PullRequest, status string, now time.Time) cursorState {
	if state.PRs == nil {
		state.PRs = map[string]cursorEntry{}
	}
	state.PRs[cursorKey(pr)] = cursorEntry{
		HeadSHA:   strings.TrimSpace(pr.Head.SHA),
		Status:    status,
		UpdatedAt: now.UTC().Format(time.RFC3339),
	}
	return state
}
