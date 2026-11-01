package gateway

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type M = map[string]any

func text(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	}
	return 0
}
func yes(v any) bool { b, _ := v.(bool); return b }
func obj(v any) M {
	if m, ok := v.(map[string]any); ok && m != nil {
		return m
	}
	return M{}
}
func arr(v any) []any {
	if a, ok := v.([]any); ok && a != nil {
		return a
	}
	return []any{}
}
func str(v any, fallback string) string {
	if s := text(v); s != "" {
		return s
	}
	return fallback
}
func now() float64 { return float64(time.Now().UnixMilli()) / 1000 }
func random(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func clone[T any](v T) T { b, _ := json.Marshal(v); var out T; _ = json.Unmarshal(b, &out); return out }
func readJSON(path string, out any) error {
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	if e = json.Unmarshal(b, out); e != nil {
		return fmt.Errorf("%s: %w", filepath.Base(path), e)
	}
	return nil
}
func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".codar-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(b, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
func loadOptional(path string, v any) error {
	err := readJSON(path, v)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
func jwt(token string) M {
	p := strings.Split(token, ".")
	if len(p) != 3 {
		return M{}
	}
	b, e := base64.RawURLEncoding.DecodeString(p[1])
	if e != nil {
		return M{}
	}
	var d M
	_ = json.Unmarshal(b, &d)
	return obj(d)
}
func masked(token string) string {
	if len(token) < 20 {
		return "***"
	}
	return token[:10] + "…" + token[len(token)-6:]
}

type settingSpec struct {
	Key     string `json:"key"`
	Type    string `json:"type"`
	Default any    `json:"default"`
	Min     any    `json:"min"`
	Max     any    `json:"max"`
	Label   string `json:"label"`
	Help    string `json:"help"`
}

var schedulingOptions = []M{
	{"value": "round_robin", "label": "轮询"},
	{"value": "weighted_round_robin", "label": "加权轮询"},
	{"value": "sticky", "label": "粘性"},
	{"value": "least_connections", "label": "最小连接"},
}

var schema = []settingSpec{
	{"use_codar_ua", "bool", true, nil, nil, "启用自定义 UA", "关闭后 API 请求沿用调用方 UA，面板测试和模型目录使用 Go HTTP 客户端 UA。"},
	{"codar_ua", "string", defaultCodarUA, nil, 1024, "自定义 UA", "开启后模型请求、模型目录和面板测试使用右侧 UA；关闭后 API 沿用调用方 UA。"},
	{"auto_passthrough", "bool", true, nil, nil, "自动透传", "保留原始正文、查询参数和普通请求头，仍按启用的 UA 和提示词规则处理；不转发本地鉴权、Cookie 和连接控制头。"},
	{"detailed_debug", "bool", false, nil, nil, "启用详细调试日志", "记录失败请求的完整请求头、请求体和响应，包含 API Key、账号令牌等敏感信息；仅管理员可查看。"},
	{"allow_remote_management", "bool", true, nil, nil, "允许远程访问", "开启后允许从其他主机访问管理页面和管理接口，仍需管理员密钥；关闭后仅允许本机管理，API Key 调用不受影响。"},
	{"dsh_identity_rewrite", "bool", true, nil, nil, "DSH身份提示词替换", "替换各模型接口系统或开发者提示词开头的固定身份描述，保留其余内容。"},
	{"identity_first_line_fill", "bool", false, nil, nil, "身份提示词首行填充", "在系统或开发者提示词首行开头添加一个点和一个零宽空格（U+200B）。"},
	{"auto_claim", "bool", true, nil, nil, "自动领取免费模型活动", "导入与后台检查时自动领取可参加的免费模型活动。"},
	{"allow_general_balance", "bool", true, nil, nil, "允许使用通用余额", "Agent 余额用完后允许使用通用余额。"},
	{"auto_renew_token", "bool", true, nil, nil, "令牌自动续期", "令牌接近到期时自动续期。"},
	{"renew_before_hours", "int", 48, 1, 160, "提前续期（小时）", "在到期前多少小时续期，失败后 30 分钟重试。"},
	{"auto_sign", "bool", true, nil, nil, "自动每日签到", "导入、刷新及可再次签到时自动签到。"},
	{"cooldown_429_seconds", "int", 60, 0, 86400, "限流冷却（秒）", "上游返回 429 后暂停调度该账号。"},
	{"cooldown_balance_seconds", "int", 600, 0, 86400, "余额不足冷却（秒）", "上游返回 402 或余额不足时暂停收费模型。"},
	{"cooldown_error_seconds", "int", 30, 0, 3600, "上游错误冷却（秒）", "网络错误或 5xx 时暂停该账号；503「No available channel」直接返回、不冷却。"},
	{"max_attempts", "int", 3, 1, 10, "单次请求最多尝试账号数", "收到响应前的可重试错误切换到其他可用账号。"},
	{"ip_block", "bool", true, nil, nil, "IP 封控时停止调度该 IP", "上游返回 403 拦截时把当前出口 IP 标记为封控，不再调度，换其他出口的账号重试。"},
	{"scheduling_strategy", "select", "round_robin", nil, nil, "调度策略", "轮询依次选号；加权轮询按账号权重分配；粘性优先沿用同一会话账号；最小连接优先使用当前并发最少的账号。所有策略均跳过已达并发上限的账号。"},
	{"refresh_minutes", "int", 30, 5, 1440, "余额 / 活动刷新间隔（分钟）", "完整刷新账号信息的间隔。"},
	{"free_check_minutes", "int", 60, 5, 1440, "免费活动检查间隔（分钟）", "检查免费活动与自动领取的间隔。"},
	{"catalog_ttl_minutes", "int", 10, 1, 1440, "模型目录缓存（分钟）", "后台缓存上游模型目录。"},
}

func defaults() M {
	d := M{}
	for _, s := range schema {
		d[s.Key] = s.Default
	}
	return d
}
func cleanSettings(p M, base M) (M, error) {
	d := clone(base)
	for _, s := range schema {
		v, ok := p[s.Key]
		if !ok {
			continue
		}
		if s.Type == "bool" {
			if _, ok := v.(bool); !ok {
				return nil, fmt.Errorf("%s 必须是布尔值", s.Key)
			}
		} else if s.Type == "select" {
			valid := false
			for _, option := range schedulingOptions {
				if v == option["value"] {
					valid = true
					break
				}
			}
			if !valid {
				return nil, errors.New("请选择有效的调度策略")
			}
		} else if s.Type == "string" {
			value, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("%s 必须是文本", s.Label)
			}
			value = strings.TrimSpace(value)
			if value == "" || len(value) > int(num(s.Max)) {
				return nil, fmt.Errorf("%s 不能为空，且不能超过 %v 个字符", s.Label, s.Max)
			}
			for _, c := range value {
				if c < 32 || c > 126 {
					return nil, fmt.Errorf("%s 只能包含可打印的英文字符、数字和符号，不能换行", s.Label)
				}
			}
			v = value
		} else {
			f, ok := v.(float64)
			if !ok {
				f = num(v)
				_, ok = v.(int)
			}
			if !ok || f != float64(int(f)) || f < num(s.Min) || f > num(s.Max) {
				return nil, fmt.Errorf("%s 必须是 %v 至 %v 的整数", s.Key, s.Min, s.Max)
			}
		}
		d[s.Key] = v
	}
	return d, nil
}

type Store struct {
	mu        sync.RWMutex
	dir       string
	accounts  []M
	settings  M
	catalog   []any
	catalogAt float64
	prices    M
	sticky    map[string]string
	cursor    int
	inflight  map[string]int
	weights   map[string]int64
	dirty     bool
}

func NewStore(dir string) (*Store, error) {
	return newStore(dir, true)
}

func newStore(dir string, allowRemoteManagement bool) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, accounts: []M{}, settings: defaults(), catalog: []any{}, prices: M{}, sticky: map[string]string{}, inflight: map[string]int{}, weights: map[string]int64{}}
	s.settings["allow_remote_management"] = allowRemoteManagement
	if err := loadOptional(filepath.Join(dir, "accounts.json"), &s.accounts); err != nil {
		return nil, err
	}
	var saved M
	if err := loadOptional(filepath.Join(dir, "settings.json"), &saved); err != nil {
		return nil, err
	}
	for k, v := range saved {
		if k == "session_sticky" {
			continue
		}
		s.settings[k] = v
	}
	if _, ok := saved["scheduling_strategy"]; !ok && yes(saved["session_sticky"]) {
		s.settings["scheduling_strategy"] = "sticky"
	}
	const oldDefaultUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) codar-desktop/0.3.2 Chrome/138.0.7204.251 Electron/37.10.3 Safari/537.36"
	if s.settings["codar_ua"] == oldDefaultUA {
		s.settings["codar_ua"] = defaultCodarUA
	}
	var cat M
	if err := loadOptional(filepath.Join(dir, "catalog.json"), &cat); err != nil {
		return nil, err
	}
	s.catalog = arr(cat["models"])
	s.catalogAt = num(cat["at"])
	if err := loadOptional(filepath.Join(dir, "prices.json"), &s.prices); err != nil {
		return nil, err
	}
	if s.prices == nil {
		s.prices = M{}
	}
	seen := map[string]bool{}
	for _, a := range s.accounts {
		uid := text(a["uid"])
		if uid == "" || seen[uid] {
			return nil, fmt.Errorf("账号数据包含空 ID 或重复 ID")
		}
		seen[uid] = true
		normalizeAccount(a)
	}
	return s, nil
}
func normalizeAccount(a M) {
	for _, f := range accountSchedulingSpecs {
		value, err := schedulingInteger(a[f.key], f.min, f.max)
		if err != nil {
			value = f.def
		}
		a[f.key] = value
	}
	delete(a, "current_concurrency")
	for _, k := range []string{"balance", "free", "invite", "profile", "stats", "sign", "renew"} {
		a[k] = obj(a[k])
	}
	for _, k := range []string{"activities", "redeems"} {
		a[k] = arr(a[k])
	}
	if _, ok := a["enabled"]; !ok {
		a["enabled"] = true
	}
	if num(a["added_at"]) == 0 {
		a["added_at"] = now()
	}
	for _, k := range []string{"restricted", "token_invalid"} {
		a[k] = yes(a[k])
	}
	if text(a["cooldown_kind"]) == "" && a["cooldown_reason"] == "余额不足" {
		a["cooldown_kind"] = "balance"
	}
}
func (s *Store) Settings() M { s.mu.RLock(); defer s.mu.RUnlock(); return clone(s.settings) }
func (s *Store) List() []M   { s.mu.RLock(); defer s.mu.RUnlock(); return clone(s.accounts) }
func (s *Store) Get(uid string) M {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, a := range s.accounts {
		if text(a["uid"]) == uid {
			return clone(a)
		}
	}
	return nil
}
func (s *Store) saveLocked() error {
	if err := writeJSON(filepath.Join(s.dir, "accounts.json"), s.accounts); err != nil {
		return err
	}
	s.dirty = false
	return nil
}
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dirty {
		return s.saveLocked()
	}
	return nil
}
func (s *Store) Update(uid string, fn func(M) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, a := range s.accounts {
		if text(a["uid"]) == uid {
			b := clone(a)
			if err := fn(b); err != nil {
				return err
			}
			s.accounts[i] = b
			if e := s.saveLocked(); e != nil {
				s.accounts[i] = a
				return e
			}
			if num(a["weight"]) != num(b["weight"]) {
				s.weights = map[string]int64{}
			}
			return nil
		}
	}
	return fmt.Errorf("未找到账号 %s", uid)
}
func (s *Store) Upsert(a M) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	normalizeAccount(a)
	old := s.accounts
	next := append([]M{}, old...)
	updated := false
	for i, b := range next {
		if text(b["uid"]) == text(a["uid"]) {
			n := clone(b)
			for k, v := range a {
				switch k {
				case "enabled", "added_at", "stats", "redeems", "restricted", "restricted_reason", "restricted_at", "weight", "max_concurrency":
					continue
				}
				n[k] = v
			}
			if text(a["note"]) == "" {
				n["note"] = b["note"]
			}
			next[i] = n
			updated = true
			break
		}
	}
	if !updated {
		next = append(next, clone(a))
	}
	s.accounts = next
	if e := s.saveLocked(); e != nil {
		s.accounts = old
		return false, e
	}
	return updated, nil
}
func (s *Store) SaveSettings(p M) (M, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, e := cleanSettings(p, s.settings)
	if e != nil {
		return nil, e
	}
	if e = writeJSON(filepath.Join(s.dir, "settings.json"), d); e != nil {
		return nil, e
	}
	if text(d["scheduling_strategy"]) != text(s.settings["scheduling_strategy"]) {
		s.cursor = 0
		s.weights = map[string]int64{}
		s.sticky = map[string]string{}
	}
	s.settings = d
	return clone(d), nil
}
func (s *Store) Restrict(uid, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, a := range s.accounts {
		if text(a["uid"]) != uid {
			continue
		}
		found = true
		if !yes(a["restricted"]) {
			a["restricted"] = true
			a["restricted_at"] = now()
			a["restricted_reason"] = reason
		}
		a["last_error"] = "账号受限：" + reason
		a["last_error_at"] = now()
		break
	}
	if !found {
		return fmt.Errorf("未找到账号 %s", uid)
	}
	for h, u := range s.sticky {
		if u == uid {
			delete(s.sticky, h)
		}
	}
	// Fail closed in memory when a disk error prevents persisting a restriction.
	s.dirty = true
	return s.saveLocked()
}
func (s *Store) Batch(p M) (M, error) {
	action := text(p["action"])
	if action != "enable" && action != "disable" && action != "delete" && action != "clear_restriction" {
		return nil, errors.New("未知的批量操作")
	}
	if a, ok := p["all"]; ok {
		if _, ok = a.(bool); !ok {
			return nil, errors.New("all 必须是布尔值")
		}
	}
	all := yes(p["all"])
	ids := map[string]bool{}
	if v, ok := p["uids"]; ok {
		list, ok := v.([]any)
		if !ok {
			return nil, errors.New("uids 必须是数组")
		}
		for _, id := range list {
			u, ok := id.(string)
			if !ok || u == "" {
				return nil, errors.New("账号 ID 必须是非空字符串")
			}
			ids[u] = true
		}
	}
	if all && len(ids) > 0 {
		return nil, errors.New("全部账号与账号列表不能同时指定")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if all {
		for _, a := range s.accounts {
			ids[text(a["uid"])] = true
		}
	}
	if len(ids) == 0 {
		return nil, errors.New("请先选择账号")
	}
	found := 0
	for _, a := range s.accounts {
		if ids[text(a["uid"])] {
			found++
		}
	}
	if found != len(ids) {
		return nil, errors.New("部分账号已不存在，请刷新后重试")
	}
	next := []M{}
	changed := 0
	for _, a := range s.accounts {
		if !ids[text(a["uid"])] {
			next = append(next, a)
			continue
		}
		if action == "delete" {
			changed++
			continue
		}
		b := clone(a)
		switch action {
		case "enable":
			if !yes(b["enabled"]) {
				changed++
			}
			b["enabled"] = true
		case "disable":
			if yes(b["enabled"]) {
				changed++
			}
			b["enabled"] = false
		case "clear_restriction":
			if yes(b["restricted"]) {
				changed++
			}
			b["restricted"] = false
			b["restricted_reason"] = ""
			b["restricted_at"] = 0
			if strings.HasPrefix(text(b["last_error"]), "账号受限") {
				b["last_error"] = ""
			}
		}
		next = append(next, b)
	}
	old := s.accounts
	s.accounts = next
	if e := s.saveLocked(); e != nil {
		s.accounts = old
		return nil, e
	}
	if action == "disable" || action == "delete" {
		for uid := range ids {
			delete(s.weights, uid)
		}
		for h, u := range s.sticky {
			if ids[u] {
				delete(s.sticky, h)
			}
		}
	}
	return M{"ok": true, "action": action, "count": len(ids), "updated": changed}, nil
}
func freeModels(a M, t float64) []string {
	set := map[string]bool{}
	f := obj(a["free"])
	if num(f["free_until"]) > t {
		for _, m := range arr(f["model_ids"]) {
			set[text(m)] = true
		}
	}
	for _, v := range arr(a["activities"]) {
		f := obj(v)
		if f["status"] == "active" && num(f["free_until"]) > t {
			for _, m := range arr(f["model_ids"]) {
				set[text(m)] = true
			}
		}
	}
	out := []string{}
	for m := range set {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}
func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
func spendable(a M, allow bool) (float64, bool) {
	bal := obj(a["balance"])
	n, known := 0.0, false
	for _, k := range []string{"agent_token", "token"} {
		if k == "token" && !allow {
			continue
		}
		if v, ok := bal[k]; ok && v != nil {
			n += num(v)
			known = true
		}
	}
	return n, known
}
func unavailable(a M, model string, settings M) string {
	return modelUnavailable(a, model, settings, false)
}

func modelUnavailable(a M, model string, settings M, manual bool) string {
	t := now()
	if yes(a["restricted"]) {
		return "账号受限，请手动解除受限标记"
	}
	if !manual && !yes(a["enabled"]) {
		return "已关闭"
	}
	if text(a["token"]) == "" {
		return "没有令牌"
	}
	if yes(a["token_invalid"]) {
		return "令牌无效或已过期，请重新导入"
	}
	if exp := num(jwt(text(a["token"]))["exp"]); exp > 0 && exp <= t {
		return "令牌已过期，请重新导入"
	}
	free := freeModels(a, t)
	isFree := contains(free, model) || (model == "" && len(free) > 0)
	if num(a["cooldown_until"]) > t && !(a["cooldown_kind"] == "balance" && isFree) {
		return fmt.Sprintf("冷却中（%s，还有 %d 秒）", str(a["cooldown_reason"], "上游错误"), int(num(a["cooldown_until"])-t))
	}
	if model != "" && !isFree {
		if bal, ok := spendable(a, yes(settings["allow_general_balance"])); ok && bal <= 0 {
			return "余额不足，该模型不在免费模型内"
		}
	}
	return ""
}

func (s *Store) Public(a M, proxy M) M {
	d := clone(a)
	delete(d, "token")
	d["token_masked"] = masked(text(a["token"]))
	d["token_exp"] = num(jwt(text(a["token"]))["exp"])
	d["free_models"] = freeModels(a, now())
	s.mu.RLock()
	settings := clone(s.settings)
	current := s.inflight[text(a["uid"])]
	s.mu.RUnlock()
	d["current_concurrency"] = current
	reason := unavailable(a, "", settings)
	if reason == "" && num(a["max_concurrency"]) > 0 && current >= int(num(a["max_concurrency"])) {
		reason = "已达账号最大并发数"
	}
	d["reason"] = reason
	d["ready"] = reason == ""
	block := ""
	if b, ok := spendable(a, yes(settings["allow_general_balance"])); ok && b <= 0 {
		block = "余额已用完"
	}
	if a["cooldown_kind"] == "balance" && num(a["cooldown_until"]) > now() {
		block = "余额冷却中"
	}
	d["paid_block"] = block
	d["proxy"] = proxy
	return d
}
