package gateway

import (
	"encoding/json"
	"path/filepath"
	"sync"
)

// Store fingerprints only: request bodies and credentials are not copied here.
type requestBlocklist struct {
	mu    sync.RWMutex
	path  string
	items map[string]M
}

func newRequestBlocklist(dir string) (*requestBlocklist, error) {
	b := &requestBlocklist{path: filepath.Join(dir, "request-blocklist.json"), items: map[string]M{}}
	if err := loadOptional(b.path, &b.items); err != nil {
		return nil, err
	}
	if b.items == nil {
		b.items = map[string]M{}
	}
	return b, nil
}

func requestFingerprint(path string, payload M) string {
	// encoding/json sorts object keys; whitespace and key order do not bypass a
	// block. Include the protocol path and every original JSON parameter.
	raw, _ := json.Marshal(payload)
	return digest(path + "\n" + string(raw))
}

func (b *requestBlocklist) Has(id string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	_, found := b.items[id]
	return found
}

func (b *requestBlocklist) Count() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.items)
}

func (b *requestBlocklist) Add(id, path, model, reason string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.items[id]; exists {
		return nil
	}
	// Keep the in-memory block even if disk persistence fails.
	b.items[id] = M{"path": path, "model": model, "reason": safeMessage(reason), "at": now()}
	return writeJSON(b.path, b.items)
}

func (b *requestBlocklist) Clear() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	next := map[string]M{}
	if err := writeJSON(b.path, next); err != nil {
		return err
	}
	b.items = next
	return nil
}
