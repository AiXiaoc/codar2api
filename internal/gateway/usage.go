package gateway

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

func normalizeUsage(u M) M {
	if len(u) == 0 {
		return nil
	}
	ptd, itd := obj(u["prompt_tokens_details"]), obj(u["input_tokens_details"])
	cached := max(num(ptd["cached_tokens"]), num(itd["cached_tokens"]), num(u["cache_read_input_tokens"]))
	prompt := num(u["prompt_tokens"])
	if u["prompt_tokens"] == nil {
		prompt = num(u["input_tokens"])
		if _, has := u["input_tokens_details"]; !has {
			prompt += num(u["cache_read_input_tokens"]) + num(u["cache_creation_input_tokens"])
		}
	}
	completion := num(u["completion_tokens"])
	if u["completion_tokens"] == nil {
		completion = num(u["output_tokens"])
	}
	if prompt == 0 && completion == 0 {
		return nil
	}
	return M{"prompt": prompt, "cached": cached, "completion": completion}
}
func holders(v M) []M { return []M{v, obj(v["response"]), obj(v["message"])} }

func responseModel(v M) (string, bool) {
	var model string
	found := false
	for _, h := range holders(v) {
		if value, ok := h["model"].(string); ok {
			model, found = value, true
		}
	}
	return model, found
}

type streamTap struct {
	buf, data    []byte
	raw, billing M
	err          string
	terminal     string
	model        string
	hasModel     bool
	onError      func(M)
}

func newTap(onError func(M)) *streamTap { return &streamTap{raw: M{}, onError: onError} }
func (t *streamTap) inspect(v M) {
	if model, ok := responseModel(v); ok {
		t.model, t.hasModel = model, true
	}
	switch v["type"] {
	case "message_stop", "response.completed":
		t.terminal = text(v["type"])
	}
	for _, h := range holders(v) {
		for k, v := range obj(h["usage"]) {
			if v != nil {
				t.raw[k] = v
			}
		}
		if len(obj(h["billing"])) > 0 {
			t.billing = obj(h["billing"])
		}
	}
	err := obj(v["error"])
	if len(err) == 0 {
		err = obj(v["detail"])
	}
	if len(err) == 0 {
		err = obj(obj(v["response"])["error"])
	}
	if len(err) > 0 {
		if t.err == "" {
			t.err = safeMessage(str(err["message"], str(err["code"], "流式上游错误")))
		}
	}
	if reason := restriction(v); reason != "" && t.err == "" {
		t.err = "账号受限：" + reason
	}
	if t.onError != nil && (len(err) > 0 || restriction(v) != "") {
		t.onError(v)
	}
}
func (t *streamTap) event() {
	if len(t.data) > 0 {
		data := bytes.TrimSpace(t.data)
		if bytes.Equal(data, []byte("[DONE]")) {
			t.terminal = "[DONE]"
		} else {
			var v M
			if json.Unmarshal(data, &v) == nil && v != nil {
				t.inspect(v)
			}
		}
	}
	t.data = nil
}
func (t *streamTap) Feed(b []byte) {
	t.buf = append(t.buf, b...)
	for {
		idx := bytes.IndexByte(t.buf, '\n')
		if idx < 0 {
			break
		}
		line := bytes.TrimSuffix(t.buf[:idx], []byte{'\r'})
		if len(line) == 0 {
			t.event()
		} else if bytes.HasPrefix(line, []byte("data:")) {
			if len(t.data) > 0 {
				t.data = append(t.data, '\n')
			}
			t.data = append(t.data, bytes.TrimPrefix(line[5:], []byte{' '})...)
		}
		t.buf = t.buf[idx+1:]
		if len(t.data) > 256<<10 {
			t.data = nil
		}
	}
	if len(t.buf) > 256<<10 {
		t.buf = nil
	}
}
func (t *streamTap) Finish() {
	if len(t.buf) > 0 {
		t.Feed([]byte{'\n'})
	}
	t.event()
}

type UsageLog struct {
	mu                sync.Mutex
	path              string
	rows, pending     []M
	lastCompact       time.Time
	lastDetailCleanup time.Time
}

func NewUsageLog(dir string) (*UsageLog, error) {
	u := &UsageLog{path: filepath.Join(dir, "usage.jsonl"), rows: []M{}, pending: []M{}, lastCompact: time.Now(), lastDetailCleanup: time.Now()}
	if err := u.pruneRequestDetails(); err != nil {
		log.Printf("request detail cleanup failed: %v", err)
	}
	f, e := os.Open(u.path)
	if errors.Is(e, os.ErrNotExist) {
		return u, nil
	}
	if e != nil {
		return nil, e
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 65536), 256<<10)
	cutoff := now() - 7*86400
	for scan.Scan() {
		var row M
		if json.Unmarshal(scan.Bytes(), &row) == nil && num(row["ts"]) >= cutoff {
			u.rows = append(u.rows, row)
		}
	}
	if e = scan.Err(); e != nil {
		return nil, e
	}
	return u, nil
}
func (u *UsageLog) Record(row M) {
	row = clone(row)
	row["ts"] = now()
	row["tps"] = nil
	if yes(row["ok"]) && num(row["completion"]) > 0 {
		gen := num(row["ms"])
		if yes(row["stream"]) {
			gen -= num(row["ttft_ms"])
		}
		if gen < 50 {
			gen = num(row["ms"])
		}
		if gen > 0 {
			row["tps"] = num(row["completion"]) * 1000 / gen
		}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.rows = append(u.rows, row)
	u.pending = append(u.pending, row)
}
func (u *UsageLog) Flush() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if time.Since(u.lastDetailCleanup) >= time.Hour {
		if err := u.pruneRequestDetails(); err != nil {
			log.Printf("request detail cleanup failed: %v", err)
		}
		u.lastDetailCleanup = time.Now()
	}
	if len(u.pending) == 0 {
		return nil
	}
	var buf bytes.Buffer
	for _, row := range u.pending {
		if e := json.NewEncoder(&buf).Encode(row); e != nil {
			return e
		}
	}
	f, e := os.OpenFile(u.path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if e != nil {
		return e
	}
	start, e := f.Seek(0, io.SeekEnd)
	if e != nil {
		f.Close()
		return e
	}
	_, e = f.Write(buf.Bytes())
	if e == nil {
		e = f.Sync()
	}
	if e != nil {
		_ = f.Truncate(start)
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	u.pending = nil
	if time.Since(u.lastCompact) > time.Hour {
		cutoff := now() - 7*86400
		n := 0
		for _, row := range u.rows {
			if num(row["ts"]) >= cutoff {
				u.rows[n] = row
				n++
			}
		}
		u.rows = u.rows[:n]
		buf.Reset()
		for _, row := range u.rows {
			_ = json.NewEncoder(&buf).Encode(row)
		}
		tmp, e := os.CreateTemp(filepath.Dir(u.path), ".usage-*")
		if e != nil {
			return e
		}
		path := tmp.Name()
		defer os.Remove(path)
		_ = tmp.Chmod(0600)
		_, e = tmp.Write(buf.Bytes())
		if e == nil {
			e = tmp.Sync()
		}
		ce := tmp.Close()
		if e == nil {
			e = ce
		}
		if e != nil {
			return e
		}
		if e = os.Rename(path, u.path); e != nil {
			return e
		}
		u.lastCompact = time.Now()
	}
	return nil
}

// RequestDetail accepts only opaque random identifiers and reads regular files
// from the detail directory; no request path can escape the data directory.
func (u *UsageLog) RequestDetail(id string) (M, error) {
	if !validDetailID(id) {
		return nil, os.ErrNotExist
	}
	path := filepath.Join(filepath.Dir(u.path), "request-details", id+".json")
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.ModTime().Before(time.Now().Add(-7*24*time.Hour)) {
		return nil, os.ErrNotExist
	}
	var detail M
	if err := readJSON(path, &detail); err != nil {
		return nil, err
	}
	if detail == nil {
		return nil, errors.New("详细记录格式错误")
	}
	return detail, nil
}

func validDetailID(id string) bool {
	if len(id) != 32 || strings.ToLower(id) != id {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// Keep complete error details for seven days. Abandoned in-progress captures
// older than a day are safe to remove: requests have much shorter deadlines.
func (u *UsageLog) pruneRequestDetails() error {
	dir := filepath.Join(filepath.Dir(u.path), "request-details")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	tempCutoff := time.Now().Add(-24 * time.Hour)
	base, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		isTempDir := strings.HasPrefix(name, ".active-") && entry.IsDir() && entry.Type()&os.ModeSymlink == 0
		isTempFile := strings.HasPrefix(name, ".detail-") && entry.Type().IsRegular()
		if isTempDir || isTempFile {
			info, err := entry.Info()
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			if info.ModTime().Before(tempCutoff) {
				target, err := filepath.Abs(filepath.Join(base, name))
				if err != nil {
					return err
				}
				if filepath.Dir(target) != base {
					return errors.New("临时记录路径超出目录")
				}
				if isTempDir {
					err = os.RemoveAll(target)
				} else {
					err = os.Remove(target)
				}
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".json") || !validDetailID(strings.TrimSuffix(entry.Name(), ".json")) || !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}
func (u *UsageLog) Stats(since float64, cost func(string, float64, float64, float64) any) M {
	u.mu.Lock()
	rows := []M{}
	for _, r := range u.rows {
		if num(r["ts"]) >= since {
			rows = append(rows, clone(r))
		}
	}
	u.mu.Unlock()
	total := M{}
	models, accounts := map[string]M{}, map[string]M{}
	for _, r := range rows {
		model, account := str(r["model"], "(空)"), str(r["account"], "(无账号)")
		if models[model] == nil {
			models[model] = M{}
		}
		if accounts[account] == nil {
			accounts[account] = M{}
		}
		pc := cost(model, num(r["prompt"]), num(r["cached"]), num(r["completion"]))
		for _, b := range []M{total, models[model], accounts[account]} {
			b["requests"] = num(b["requests"]) + 1
			k := "failed"
			if yes(r["ok"]) {
				k = "ok"
			}
			b[k] = num(b[k]) + 1
			if yes(r["stream"]) {
				b["stream"] = num(b["stream"]) + 1
			}
			hasUsage := num(r["prompt"]) > 0 || num(r["completion"]) > 0
			if hasUsage {
				b["usage_rows"] = num(b["usage_rows"]) + 1
				for _, k := range []string{"prompt", "cached", "completion"} {
					b[k] = num(b[k]) + num(r[k])
				}
				if pc == nil {
					b["unpriced"] = num(b["unpriced"]) + 1
				} else {
					b["price_cost"] = num(b["price_cost"]) + num(pc)
				}
			}
			if num(r["tps"]) > 0 {
				b["gen_tokens"] = num(b["gen_tokens"]) + num(r["completion"])
				b["gen_ms"] = num(b["gen_ms"]) + num(r["completion"])*1000/num(r["tps"])
			}
			if yes(r["ok"]) && yes(r["stream"]) && r["ttft_ms"] != nil {
				b["ttft_sum"] = num(b["ttft_sum"]) + num(r["ttft_ms"])
				b["ttft_n"] = num(b["ttft_n"]) + 1
			}
			b["cost_cny"] = num(b["cost_cny"]) + num(r["cost_cny"])
		}
	}
	finish := func(b M) M {
		d := M{}
		for _, k := range strings.Fields("requests ok failed stream prompt cached completion cost_cny usage_rows price_cost unpriced") {
			d[k] = num(b[k])
		}
		d["success_rate"] = ratio(num(b["ok"]), num(b["requests"]))
		d["cache_hit_rate"] = ratio(num(b["cached"]), num(b["prompt"]))
		d["tps"] = ratio(num(b["gen_tokens"])*1000, num(b["gen_ms"]))
		d["ttft_ms"] = ratio(num(b["ttft_sum"]), num(b["ttft_n"]))
		return d
	}
	ml, al, recent := []any{}, []any{}, []any{}
	for id, b := range models {
		d := finish(b)
		d["model"] = id
		ml = append(ml, d)
	}
	for id, b := range accounts {
		d := finish(b)
		d["account"] = id
		al = append(al, d)
	}
	for _, list := range [][]any{ml, al} {
		sort.Slice(list, func(i, j int) bool { return num(obj(list[i])["requests"]) > num(obj(list[j])["requests"]) })
	}
	for i := len(rows) - 1; i >= max(0, len(rows)-50); i-- {
		r := rows[i]
		r["price_cost"] = cost(text(r["model"]), num(r["prompt"]), num(r["cached"]), num(r["completion"]))
		recent = append(recent, r)
	}
	return M{"since": since, "total": finish(total), "models": ml, "accounts": al, "recent": recent}
}

// Requests lists one account's attempts, newest first. A request that switched
// accounts contributes one attempt per account; token usage belongs to its last
// attempt. Rows written before uid was recorded match by username.
func (u *UsageLog) Requests(uid, username string, failedOnly bool, page, size int) M {
	page, size = max(1, page), max(1, min(size, 100))
	u.mu.Lock()
	defer u.mu.Unlock()
	items := []any{}
	all, failed, matched := 0, 0, 0
	for i := len(u.rows) - 1; i >= 0; i-- {
		r := u.rows[i]
		tries := arr(r["tries"])
		if len(tries) == 0 {
			tries = []any{M{"uid": r["uid"], "account": r["account"], "status": r["status"], "ok": r["ok"], "error": r["error"]}}
		}
		for j := len(tries) - 1; j >= 0; j-- {
			t := obj(tries[j])
			if id := text(t["uid"]); id != uid && (id != "" || username == "" || text(t["account"]) != username) {
				continue
			}
			all++
			ok := yes(t["ok"])
			if !ok {
				failed++
			}
			if failedOnly && ok {
				continue
			}
			matched++
			if matched <= (page-1)*size || matched > page*size {
				continue
			}
			item := M{"ts": r["ts"], "model": r["model"], "path": r["path"], "source": r["source"], "stream": r["stream"], "status": t["status"], "ok": ok, "error": t["error"], "switched": j < len(tries)-1}
			if requested, ok := r["requested_model"].(string); ok {
				item["requested_model"] = requested
			}
			if model, ok := t["response_model"].(string); ok {
				item["response_model"] = model
			} else if j == len(tries)-1 {
				if model, ok := r["response_model"].(string); ok {
					item["response_model"] = model
				}
			}
			if id := text(r["diagnostic_id"]); id != "" {
				item["diagnostic_id"] = id
			}
			if j == len(tries)-1 {
				item["prompt"], item["cached"], item["completion"], item["ms"] = r["prompt"], r["cached"], r["completion"], r["ms"]
			}
			items = append(items, item)
		}
	}
	return M{"requests": all, "succeeded": all - failed, "failed": failed, "total": matched, "page": page, "page_size": size, "items": items}
}
