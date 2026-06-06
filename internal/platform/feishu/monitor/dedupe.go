package monitor

import (
	"sync"
	"time"
)

const defaultFeishuDedupeTTL = 10 * time.Minute

type eventDeduper struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]time.Time
}

func newEventDeduper(ttl time.Duration) *eventDeduper {
	if ttl <= 0 {
		ttl = defaultFeishuDedupeTTL
	}
	return &eventDeduper{
		ttl:     ttl,
		entries: map[string]time.Time{},
	}
}

func (d *eventDeduper) seenOrMark(key string) bool {
	if key == "" {
		return false
	}
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cleanupExpired(now)
	if expiresAt, ok := d.entries[key]; ok && now.Before(expiresAt) {
		return true
	}
	d.entries[key] = now.Add(d.ttl)
	return false
}

func (d *eventDeduper) cleanupExpired(now time.Time) {
	for key, expiresAt := range d.entries {
		if !now.Before(expiresAt) {
			delete(d.entries, key)
		}
	}
}
