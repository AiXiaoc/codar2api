package gateway

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const requestRetentionDays = 7

// requestErrorLog is guarded by UsageLog.mu. Normal traffic never appends to
// this file, and replaying usage.jsonl after a restart does not duplicate errors.
type requestErrorLog struct {
	path          string
	rows, pending []M
	seen          map[string]bool
	lastCompact   time.Time
}

func failedRequestAttempt(row M) bool {
	ok, hasOK := row["ok"]
	return (hasOK && !yes(ok)) || num(row["status"]) >= 400 || text(row["error"]) != ""
}

func requestHasError(row M) bool {
	if !yes(row["ok"]) || failedRequestAttempt(row) {
		return true
	}
	for _, attempt := range arr(row["tries"]) {
		if failedRequestAttempt(obj(attempt)) {
			return true
		}
	}
	return false
}

func requestErrorID(row M) string {
	if id := text(row["request_log_id"]); id != "" {
		return id
	}
	// Legacy rows have no identifier. JSON map keys are ordered by Marshal, so
	// this key remains stable when those rows are replayed on later startups.
	b, _ := json.Marshal(row)
	sum := sha256.Sum256(b)
	return "legacy-" + hex.EncodeToString(sum[:])
}

func newRequestErrorLog(dir string) (*requestErrorLog, error) {
	e := &requestErrorLog{path: filepath.Join(dir, "usage-errors.jsonl"), seen: map[string]bool{}, lastCompact: time.Now()}
	f, err := os.Open(e.path)
	if errors.Is(err, os.ErrNotExist) {
		return e, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 65536), 4<<20)
	cutoff := now() - requestRetentionDays*86400
	for scan.Scan() {
		var row M
		if json.Unmarshal(scan.Bytes(), &row) != nil || num(row["ts"]) < cutoff || !requestHasError(row) {
			continue
		}
		id := requestErrorID(row)
		if !e.seen[id] {
			e.seen[id] = true
			e.rows = append(e.rows, row)
		}
	}
	if err := scan.Err(); err != nil {
		return nil, err
	}
	e.sortRows()
	return e, nil
}

func (e *requestErrorLog) sortRows() {
	sort.SliceStable(e.rows, func(i, j int) bool { return num(e.rows[i]["ts"]) < num(e.rows[j]["ts"]) })
}

func (e *requestErrorLog) record(row M) {
	if num(row["ts"]) < now()-requestRetentionDays*86400 || !requestHasError(row) {
		return
	}
	id := requestErrorID(row)
	if e.seen[id] {
		return
	}
	r := clone(row)
	r["request_log_id"] = id
	e.seen[id] = true
	e.rows = append(e.rows, r)
	e.pending = append(e.pending, r)
}

func (e *requestErrorLog) flush() error {
	if len(e.pending) > 0 {
		var buf bytes.Buffer
		for _, row := range e.pending {
			if err := json.NewEncoder(&buf).Encode(row); err != nil {
				return err
			}
		}
		f, err := os.OpenFile(e.path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
		if err != nil {
			return err
		}
		start, err := f.Seek(0, io.SeekEnd)
		if err != nil {
			f.Close()
			return err
		}
		if start > 0 {
			var tail [1]byte
			_, err = f.ReadAt(tail[:], start-1)
			if err == nil && tail[0] != '\n' {
				// A terminated process may leave an incomplete last line. Start
				// a new line so that subsequent complete records remain readable.
				_, err = f.Write([]byte{'\n'})
			}
		}
		if err == nil {
			var n int
			n, err = f.Write(buf.Bytes())
			if err == nil && n != buf.Len() {
				err = io.ErrShortWrite
			}
		}
		if err == nil {
			err = f.Sync()
		}
		if err != nil {
			if start >= 0 {
				_ = f.Truncate(start)
			}
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		e.pending = nil
	}
	if time.Since(e.lastCompact) < time.Hour {
		return nil
	}
	cutoff := now() - requestRetentionDays*86400
	kept, seen := []M{}, map[string]bool{}
	var buf bytes.Buffer
	for _, row := range e.rows {
		if num(row["ts"]) >= cutoff {
			kept = append(kept, row)
			seen[requestErrorID(row)] = true
			if err := json.NewEncoder(&buf).Encode(row); err != nil {
				return err
			}
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(e.path), ".usage-errors-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	_ = tmp.Chmod(0600)
	_, err = tmp.Write(buf.Bytes())
	if err == nil {
		err = tmp.Sync()
	}
	if ce := tmp.Close(); err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	if err := os.Rename(name, e.path); err != nil {
		return err
	}
	e.rows, e.seen, e.lastCompact = kept, seen, time.Now()
	return nil
}

// RecentRequests pages the full retained history, rather than filtering the
// latest 30/50 rows. Error copies never contribute to Stats or token totals.
func (u *UsageLog) RecentRequests(since float64, errorsOnly bool, page, size int, cost func(string, float64, float64, float64) any) M {
	since = max(since, now()-requestRetentionDays*86400)
	size = max(1, min(size, 100))
	u.mu.Lock()
	rows := u.rows
	if errorsOnly && u.requestErrors != nil {
		rows = u.requestErrors.rows
	}
	indexes := []int{}
	for i := len(rows) - 1; i >= 0; i-- {
		if num(rows[i]["ts"]) >= since && (!errorsOnly || requestHasError(rows[i])) {
			indexes = append(indexes, i)
		}
	}
	total := len(indexes)
	pages := max(1, (total+size-1)/size)
	page = max(1, min(page, pages))
	items := []any{}
	start := (page - 1) * size
	for _, i := range indexes[start:min(start+size, total)] {
		items = append(items, clone(rows[i]))
	}
	u.mu.Unlock()
	for _, item := range items {
		r := obj(item)
		failed := []any{}
		for _, attempt := range arr(r["tries"]) {
			if failedRequestAttempt(obj(attempt)) {
				failed = append(failed, attempt)
			}
		}
		r["error_attempts"] = failed
		r["had_error"] = requestHasError(r)
		r["recovered"] = yes(r["ok"]) && len(failed) > 0
		r["price_cost"] = cost(text(r["model"]), num(r["prompt"]), num(r["cached"]), num(r["completion"]))
	}
	return M{"since": since, "errors_only": errorsOnly, "page": page, "page_size": size, "pages": pages, "total": total, "items": items, "retention_days": requestRetentionDays}
}
