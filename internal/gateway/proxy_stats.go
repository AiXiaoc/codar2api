package gateway

import (
	"io"
	"path/filepath"
	"strconv"
	"sync"
)

type proxyRequestCounts struct {
	Requests  uint64 `json:"requests"`
	Succeeded uint64 `json:"succeeded"`
	Failed    uint64 `json:"failed"`
}

type proxyRequestStats struct {
	mu       sync.Mutex
	flushMu  sync.Mutex
	path     string
	rows     map[string]proxyRequestCounts
	dirty    bool
	revision uint64
}

func newProxyRequestStats(dir string) (*proxyRequestStats, error) {
	s := &proxyRequestStats{path: filepath.Join(dir, "proxy-stats.json"), rows: map[string]proxyRequestCounts{}}
	if err := loadOptional(s.path, &s.rows); err != nil {
		return nil, err
	}
	if s.rows == nil {
		s.rows = map[string]proxyRequestCounts{}
	}
	return s, nil
}

func (s *proxyRequestStats) record(id int, success bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strconv.Itoa(id)
	c := s.rows[key]
	c.Requests++
	if success {
		c.Succeeded++
	} else {
		c.Failed++
	}
	s.rows[key], s.dirty = c, true
	s.revision++
}

func (s *proxyRequestStats) view(id int) M {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.rows[strconv.Itoa(id)]
	return M{"requests": float64(c.Requests), "succeeded": float64(c.Succeeded), "failed": float64(c.Failed), "success_rate": ratio(float64(c.Succeeded), float64(c.Requests))}
}

func (s *proxyRequestStats) flush() error {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	rows := make(map[string]proxyRequestCounts, len(s.rows))
	for id, counts := range s.rows {
		rows[id] = counts
	}
	revision := s.revision
	s.mu.Unlock()
	// Disk sync must not hold up response completion on concurrent requests.
	if err := writeJSON(s.path, rows); err != nil {
		return err
	}
	s.mu.Lock()
	if s.revision == revision {
		s.dirty = false
	}
	s.mu.Unlock()
	return nil
}

// Every HTTP attempt is completed exactly once, including callers that close
// a response after reading only a bounded prefix. Read errors override HTTP 2xx.
type proxyStatsBody struct {
	io.ReadCloser
	once   sync.Once
	finish func(bool)
}

func (b *proxyStatsBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.once.Do(func() { b.finish(err == io.EOF) })
	}
	return n, err
}

func (b *proxyStatsBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(func() { b.finish(err == nil) })
	return err
}
