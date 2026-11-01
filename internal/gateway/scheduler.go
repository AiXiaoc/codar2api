package gateway

import (
	"errors"
	"fmt"
	"math"
)

var errAccountCapacity = errors.New("可用账号已达最大并发数，请稍后重试")
var errNoAccount = errors.New("没有可用的 Codar 账号")

var accountSchedulingSpecs = []struct {
	key, label    string
	min, max, def int
}{
	{"weight", "权重值", 1, 10000, 1},
	{"max_concurrency", "账号最大并发数", 0, 1000000, 0},
}

func schedulingInteger(value any, min, max int) (int, error) {
	var f float64
	switch v := value.(type) {
	case float64:
		f = v
	case int:
		f = float64(v)
	default:
		return 0, errors.New("必须是整数")
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) || f < float64(min) || f > float64(max) {
		return 0, fmt.Errorf("必须是 %d 至 %d 的整数", min, max)
	}
	return int(f), nil
}

func accountSchedulingPatch(p M) (M, error) {
	out := M{}
	for _, spec := range accountSchedulingSpecs {
		if v, ok := p[spec.key]; ok {
			n, err := schedulingInteger(v, spec.min, spec.max)
			if err != nil {
				return nil, fmt.Errorf("%s%s", spec.label, err)
			}
			out[spec.key] = n
		}
	}
	return out, nil
}

func (s *Store) atCapacityLocked(a M) bool {
	limit := int(num(a["max_concurrency"]))
	return limit > 0 && s.inflight[text(a["uid"])] >= limit
}

// Pick selects and reserves a model request slot atomically. Call Release once
// the attempt ends, including streaming, cancellation, or switching accounts.
func (s *Store) Pick(model, hint string, exclude map[string]bool, skip func(string) bool) (M, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ready := make([]int, 0, len(s.accounts))
	busy := false
	for i := 0; i < len(s.accounts); i++ {
		idx := (s.cursor + i) % len(s.accounts)
		a := s.accounts[idx]
		uid := text(a["uid"])
		if exclude[uid] || unavailable(a, model, s.settings) != "" || skip != nil && skip(uid) {
			s.weights[uid] = 0
			continue
		}
		if s.atCapacityLocked(a) {
			busy = true
			s.weights[uid] = 0
			continue
		}
		ready = append(ready, idx)
	}
	if len(ready) == 0 {
		if busy {
			return nil, errAccountCapacity
		}
		return nil, errNoAccount
	}
	strategy := text(s.settings["scheduling_strategy"])
	idx := ready[0]
	switch strategy {
	case "sticky":
		if hint != "" {
			for _, i := range ready {
				if text(s.accounts[i]["uid"]) == s.sticky[hint] {
					idx = i
					break
				}
			}
		}
	case "weighted_round_robin":
		var total int64
		best := int64(math.MinInt64)
		for _, i := range ready {
			a := s.accounts[i]
			uid := text(a["uid"])
			weight := int64(max(1, num(a["weight"])))
			s.weights[uid] += weight
			total += weight
			if s.weights[uid] > best {
				idx, best = i, s.weights[uid]
			}
		}
		s.weights[text(s.accounts[idx]["uid"])] -= total
	case "least_connections":
		for _, i := range ready[1:] {
			if s.inflight[text(s.accounts[i]["uid"])] < s.inflight[text(s.accounts[idx]["uid"])] {
				idx = i
			}
		}
	}
	account := s.accounts[idx]
	uid := text(account["uid"])
	s.cursor = (idx + 1) % len(s.accounts)
	if strategy == "sticky" && hint != "" {
		if len(s.sticky) >= 10000 {
			s.sticky = map[string]string{}
		}
		s.sticky[hint] = uid
	}
	s.inflight[uid]++
	return clone(account), nil
}

func (s *Store) Reserve(uid, model string, skip func(string) bool) (M, error) {
	return s.reserve(uid, model, skip, false)
}

// ReserveManual only ignores the local scheduling switch; token, restriction,
// balance, cooldown, exit and concurrency checks remain in effect.
func (s *Store) ReserveManual(uid, model string, skip func(string) bool) (M, error) {
	return s.reserve(uid, model, skip, true)
}

func (s *Store) reserve(uid, model string, skip func(string) bool, manual bool) (M, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.accounts {
		if text(a["uid"]) != uid {
			continue
		}
		if reason := modelUnavailable(a, model, s.settings, manual); reason != "" {
			return nil, errors.New(reason)
		}
		if skip != nil && skip(uid) {
			return nil, errors.New("出口 IP 封控")
		}
		if s.atCapacityLocked(a) {
			return nil, errAccountCapacity
		}
		s.inflight[uid]++
		return clone(a), nil
	}
	return nil, errNoAccount
}

func (s *Store) Release(uid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inflight[uid] <= 1 {
		delete(s.inflight, uid)
	} else {
		s.inflight[uid]--
	}
}

func (s *Store) Concurrency() []M {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows := make([]M, 0, len(s.accounts))
	for _, a := range s.accounts {
		uid := text(a["uid"])
		rows = append(rows, M{"uid": uid, "current_concurrency": s.inflight[uid], "weight": a["weight"], "max_concurrency": a["max_concurrency"]})
	}
	return rows
}
