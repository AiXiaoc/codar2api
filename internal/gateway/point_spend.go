package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// Point records have their own lifetime; pruning the request log must not
// erase the amount spent since an account was imported.
type pointDebit struct {
	ID     string  `json:"id,omitempty"`
	At     float64 `json:"at"`
	Points float64 `json:"points"`
	Model  string  `json:"model,omitempty"`
}

type pointSpendAccount struct {
	Since   float64      `json:"since"`
	At      float64      `json:"at"`
	Checked float64      `json:"checked"`
	Error   string       `json:"error,omitempty"`
	Debits  []pointDebit `json:"debits"`
}

type pointSpendData struct {
	Coverage float64                       `json:"coverage_from"`
	Accounts map[string]*pointSpendAccount `json:"accounts"`
}

type pointSpendLog struct {
	mu     sync.Mutex
	locks  sync.Map
	path   string
	source string
	data   pointSpendData
	ids    map[string]map[string]bool
	dirty  bool
}

func newPointSpendLog(dir, source string) (*pointSpendLog, error) {
	p := &pointSpendLog{path: filepath.Join(dir, "point-spend.json"), source: source,
		data: pointSpendData{Coverage: now() - 7*86400, Accounts: map[string]*pointSpendAccount{}}, ids: map[string]map[string]bool{}}
	if err := readJSON(p.path, &p.data); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		p.dirty = true
	}
	if p.data.Accounts == nil {
		p.data.Accounts = map[string]*pointSpendAccount{}
	}
	return p, nil
}

func (p *pointSpendLog) Flush() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.dirty {
		return nil
	}
	if err := writeJSON(p.path, p.data); err != nil {
		return err
	}
	p.dirty = false
	return nil
}

func validPointDebit(d pointDebit) bool {
	return d.At > 0 && d.Points > 0 && !math.IsNaN(d.At) && !math.IsInf(d.At, 0) && !math.IsNaN(d.Points) && !math.IsInf(d.Points, 0)
}

func pointAmount(value any) (float64, bool) {
	n, err := strconv.ParseFloat(text(value), 64)
	return n, err == nil && !math.IsNaN(n) && !math.IsInf(n, 0)
}

func (p *pointSpendLog) view(uid string, since, at float64) M {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.data.Accounts[uid]
	known := p.source == "gateway" || s != nil && s.At > 0 && s.Since == since
	r := M{"since": since, "at": 0.0, "source": p.source, "complete": known, "error": "",
		"spent_hour": nil, "spent_today": nil, "spent_day": nil, "spent_week": nil, "spent_total": nil}
	if s != nil {
		r["at"], r["error"] = s.At, s.Error
	}
	if !known {
		return r
	}
	if p.source == "gateway" {
		r["at"] = at
		r["complete"] = since >= p.data.Coverage
	}
	loc := time.FixedZone("CST", 8*3600)
	t := time.Unix(int64(at), 0).In(loc)
	today := float64(time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc).Unix())
	cutoffs := map[string]float64{"spent_hour": at - 3600, "spent_today": today,
		"spent_day": at - 86400, "spent_week": at - 7*86400, "spent_total": since}
	for key := range cutoffs {
		r[key] = 0.0
	}
	if s != nil {
		for _, d := range s.Debits {
			if !validPointDebit(d) || d.At < since || d.At > at {
				continue
			}
			for key, cutoff := range cutoffs {
				if d.At >= cutoff {
					r[key] = num(r[key]) + d.Points
				}
			}
		}
	}
	return r
}

// Refresh replaces an overlapping ledger interval. Re-fetching the overlap
// picks up delayed bills without counting the same charge twice. A failed
// page leaves the previous complete snapshot intact.
func (p *pointSpendLog) refresh(ctx context.Context, uid string, since float64, force bool, fetch func(context.Context, float64) ([]pointDebit, error)) M {
	lock, _ := p.locks.LoadOrStore(uid, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	t := now()
	p.mu.Lock()
	old := p.data.Accounts[uid]
	var snapshot pointSpendAccount
	if old != nil {
		snapshot = *old
		snapshot.Debits = append([]pointDebit(nil), old.Debits...)
	}
	p.mu.Unlock()
	if !force && snapshot.Since == since && t-snapshot.Checked < 60 {
		return p.view(uid, since, t)
	}
	from := since
	if snapshot.Since == since && snapshot.At > 0 {
		from = max(since, snapshot.At-86400)
	}
	debits, err := fetch(ctx, from)
	if err == nil {
		for _, d := range debits {
			if !validPointDebit(d) {
				err = errors.New("积分账单包含无效的扣费金额或时间")
				break
			}
		}
	}
	p.mu.Lock()
	if snapshot.Since != since {
		snapshot = pointSpendAccount{Since: since}
	}
	snapshot.Checked = t
	if err != nil {
		snapshot.Error = safeMessage(err.Error())
	} else {
		combined := make([]pointDebit, 0, len(snapshot.Debits)+len(debits))
		seen := map[string]bool{}
		add := func(d pointDebit) {
			if d.ID != "" && seen[d.ID] {
				return
			}
			seen[d.ID] = d.ID != ""
			combined = append(combined, d)
		}
		for _, d := range snapshot.Debits {
			if d.At >= since && d.At < from {
				add(d)
			}
		}
		for _, d := range debits {
			if d.At >= from {
				add(d)
			}
		}
		snapshot.At, snapshot.Error, snapshot.Debits = t, "", combined
	}
	p.data.Accounts[uid] = &snapshot
	p.dirty = true
	p.mu.Unlock()
	return p.view(uid, since, t)
}

// WorkBuddy supplies the actual credit on each response. Stable legacy hashes
// make migration from usage.jsonl repeatable; new requests also carry an ID.
func (p *pointSpendLog) recordUsage(row M) {
	uid := text(row["uid"])
	d := pointDebit{At: num(row["ts"]), Points: num(row["credit"]), Model: text(row["model"])}
	if uid == "" || !validPointDebit(d) {
		return
	}
	d.ID = text(row["point_id"])
	if d.ID == "" {
		raw, _ := json.Marshal(row)
		hash := sha256.Sum256(raw)
		d.ID = hex.EncodeToString(hash[:])
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.data.Accounts[uid]
	if s == nil {
		s = &pointSpendAccount{}
		p.data.Accounts[uid] = s
	}
	if p.ids[uid] == nil {
		p.ids[uid] = map[string]bool{}
		for _, old := range s.Debits {
			p.ids[uid][old.ID] = true
		}
	}
	if p.ids[uid][d.ID] {
		return
	}
	p.ids[uid][d.ID] = true
	s.Debits = append(s.Debits, d)
	s.At = max(s.At, d.At)
	p.dirty = true
}

func (a *App) pointSpending(ctx context.Context, uid string, force bool) (M, error) {
	targets := a.store.List()
	if uid != "" {
		account, err := a.account(uid)
		if err != nil {
			return nil, err
		}
		targets = []M{account}
	}
	results := parallel(ctx, targets, func(acc M) M {
		return a.accountPointSpending(ctx, acc, force)
	})
	rows := make([]any, 0, len(results))
	for i, r := range results {
		acc := targets[i]
		r["uid"], r["username"] = acc["uid"], str(acc["nickname"], text(acc["username"]))
		rows = append(rows, r)
	}
	if err := a.points.Flush(); err != nil {
		return nil, err
	}
	return M{"ok": true, "at": now(), "accounts": rows}, nil
}
