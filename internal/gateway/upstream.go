package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type UpstreamError struct {
	Status  int
	Message string
	Body    M
}

func (e *UpstreamError) Error() string { return safeMessage(e.Message) }

var tokenPattern = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)
var phonePattern = regexp.MustCompile(`^1[3-9][0-9]{9}$`)
var codePattern = regexp.MustCompile(`^[0-9]{4,8}$`)

func safeMessage(s string, secrets ...string) string {
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, v := range secrets {
		if v != "" {
			s = strings.ReplaceAll(s, v, "[已隐藏]")
		}
	}
	s = tokenPattern.ReplaceAllString(s, "[令牌已隐藏]")
	r := []rune(s)
	if len(r) > 300 {
		s = string(r[:300])
	}
	return s
}
func restriction(v M) string {
	candidates := []M{v, obj(v["detail"]), obj(v["error"]), obj(obj(v["response"])["error"])}
	for _, m := range candidates {
		if m["code"] == "account_restricted" || m["type"] == "account_restricted" {
			return str(m["message"], "API access is restricted. Please contact support.")
		}
	}
	return ""
}

var htmlTitle = regexp.MustCompile(`(?is)<title>\s*([^<]{1,80}?)\s*</title>`)
var htmlServer = regexp.MustCompile(`(?is)<hr>\s*<center>\s*([^<]{1,80}?)\s*</center>`)

// ipBlocked recognizes the upstream edge refusing the exit IP: HTTP 403 with an
// HTML page (for example nginx's default 403) instead of an API error body.
func ipBlocked(status int, body []byte, server string) string {
	if status != 403 || json.Valid(bytes.TrimSpace(body)) {
		return ""
	}
	lower := strings.ToLower(string(body))
	if !strings.Contains(lower, "<html") && !strings.Contains(lower, "forbidden") {
		return ""
	}
	parts := []string{"HTTP 403"}
	if m := htmlTitle.FindSubmatch(body); m != nil {
		parts = append(parts, string(m[1]))
	}
	if m := htmlServer.FindSubmatch(body); m != nil {
		parts = append(parts, string(m[1]))
	} else if server != "" {
		parts = append(parts, server)
	}
	return safeMessage(strings.Join(parts, " · "))
}
func ipBlockedMessage(r Route, reason string) string {
	return "出口 IP 被上游封控（" + r.Name + "）：" + reason
}
func errorText(v M) string {
	for _, m := range []M{obj(v["error"]), obj(v["detail"]), v} {
		if s := text(m["message"]); s != "" {
			return s
		}
		if s := text(m["code"]); s == "account_restricted" {
			return s
		}
	}
	if s, ok := v["detail"].(string); ok {
		return s
	}
	return "上游请求失败"
}
func (a *App) route(account M, pinned *Route) (Route, error) {
	if pinned != nil {
		return *pinned, nil
	}
	if account != nil {
		uid := text(account["uid"])
		if uid == "" {
			token := text(account["token"])
			user := text(jwt(token)["username"])
			for _, old := range a.store.List() {
				if token != "" && (old["token"] == token || user != "" && old["username"] == user) {
					uid = text(old["uid"])
					break
				}
			}
		}
		return a.proxies.Account(uid)
	}
	return a.proxies.Pin(0)
}
func (a *App) requestJSON(ctx context.Context, method, endpoint string, account M, pinned *Route, body any) (M, error) {
	return a.requestJSONWithin(ctx, 45*time.Second, method, endpoint, account, pinned, body)
}

// requestJSONWithin is requestJSON with a caller-chosen deadline, for panel
// model calls that may generate longer replies.
func (a *App) requestJSONWithin(ctx context.Context, limit time.Duration, method, endpoint string, account M, pinned *Route, body any) (M, error) {
	u, e := url.Parse(endpoint)
	chat := e == nil && isChat(u.Path)
	if chat && account != nil {
		if current := a.store.Get(text(account["uid"])); current != nil && yes(current["restricted"]) {
			return nil, &UpstreamError{Status: 403, Message: "账号受限，请手动解除受限标记"}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	route, e := a.route(account, pinned)
	if e != nil {
		return nil, e
	}
	var data io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return nil, e
		}
		data = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, endpoint, data)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "node")
	if account != nil && text(account["token"]) != "" {
		req.Header.Set("Authorization", "Bearer "+text(account["token"]))
	}
	resp, e := a.proxies.Do(req, route)
	if e != nil {
		return nil, &UpstreamError{Status: 0, Message: e.Error()}
	}
	defer resp.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(resp.Body, 4<<20+1))
	if e != nil {
		return nil, &UpstreamError{Status: 502, Message: "读取上游响应失败"}
	}
	if len(raw) > 4<<20 {
		return nil, &UpstreamError{Status: 502, Message: "上游响应过大"}
	}
	if reason := ipBlocked(resp.StatusCode, raw, resp.Header.Get("Server")); reason != "" {
		// Only model calls mark the exit; other hosts may filter IPs differently.
		if chat && account != nil && yes(a.store.Settings()["ip_block"]) {
			a.logPersistence(a.proxies.Block(route, reason))
		}
		return nil, &UpstreamError{Status: 403, Message: ipBlockedMessage(route, reason)}
	}
	var out M
	if e = json.Unmarshal(raw, &out); e != nil || out == nil {
		return nil, &UpstreamError{Status: resp.StatusCode, Message: fmt.Sprintf("上游返回非 JSON 响应（HTTP %d）", resp.StatusCode)}
	}
	if reason := restriction(out); reason != "" {
		if account != nil {
			account["restricted"] = true
			account["restricted_reason"] = reason
			if a.store.Get(text(account["uid"])) != nil {
				a.logPersistence(a.store.Restrict(text(account["uid"]), reason))
			}
		}
		return nil, &UpstreamError{Status: 403, Message: "账号受限：" + reason, Body: out}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &UpstreamError{Status: resp.StatusCode, Message: errorText(out), Body: out}
	}
	if code, ok := out["code"].(float64); ok && code != 200 {
		return nil, &UpstreamError{Status: int(code), Message: errorText(out), Body: out}
	}
	if success, ok := out["success"].(bool); ok && !success {
		return nil, &UpstreamError{Status: 400, Message: errorText(out), Body: out}
	}
	return out, nil
}
func (a *App) catalog(ctx context.Context, force bool) ([]any, error) {
	a.catalogMu.Lock()
	defer a.catalogMu.Unlock()
	a.store.mu.RLock()
	models, at := clone(a.store.catalog), a.store.catalogAt
	a.store.mu.RUnlock()
	if !force && len(models) > 0 && now()-at < num(a.store.Settings()["catalog_ttl_minutes"])*60 {
		return models, nil
	}
	out, e := a.requestJSON(ctx, "GET", a.cfg.APIBase+"/v1/codar/models", nil, nil, nil)
	if e != nil {
		return models, e
	}
	flat := []any{}
	for _, v := range arr(out["data"]) {
		m := obj(v)
		if text(m["id"]) == "" {
			continue
		}
		reasoning, caps, props := obj(m["reasoning"]), obj(m["capabilities"]), obj(obj(m["request_schema"])["properties"])
		levels := []any{}
		labels := M{}
		for _, v := range arr(reasoning["levels"]) {
			l := obj(v)
			id := text(l["id"])
			levels = append(levels, id)
			labels[id] = str(obj(l["label"])["zh"], id)
		}
		maximum := obj(props["max_tokens"])["maximum"]
		if maximum == nil {
			maximum = obj(props["max_completion_tokens"])["maximum"]
		}
		flat = append(flat, M{"id": m["id"], "name": str(m["display_name"], text(m["id"])), "protocol": text(obj(m["protocol"])["type"]), "context_window": m["context_window"], "input_modalities": arr(m["input_modalities"]), "pricing": obj(m["pricing"]), "rate_badge": text(m["rate_badge"]), "reasoning_levels": levels, "reasoning_labels": labels, "reasoning_default": text(reasoning["default"]), "max_output": maximum, "tools": yes(caps["function_tools"]), "streaming": yes(caps["streaming"]), "summary": text(obj(m["summary"])["zh"]), "sort_order": num(m["sort_order"])})
	}
	sort.Slice(flat, func(i, j int) bool {
		x, y := obj(flat[i]), obj(flat[j])
		if num(x["sort_order"]) != num(y["sort_order"]) {
			return num(x["sort_order"]) < num(y["sort_order"])
		}
		return text(x["id"]) < text(y["id"])
	})
	t := now()
	if e = writeJSON(filepath.Join(a.cfg.DataDir, "catalog.json"), M{"at": t, "models": flat}); e != nil {
		return models, e
	}
	a.store.mu.Lock()
	a.store.catalog = flat
	a.store.catalogAt = t
	a.store.mu.Unlock()
	return clone(flat), nil
}
func profileView(p M) M {
	out := M{}
	for _, k := range strings.Fields("id username display_name role status email group request_count quota used_quota aff_code aff_count aff_quota aff_history_quota inviter_id created_time") {
		if v, ok := p[k]; ok {
			out[k] = v
		}
	}
	return out
}
func (a *App) importToken(ctx context.Context, token, note, via string, pinned *Route) (M, error) {
	token = strings.TrimSpace(token)
	if strings.HasPrefix(strings.ToLower(token), "bearer ") {
		token = strings.TrimSpace(token[7:])
	}
	if !tokenPattern.MatchString(token) || tokenPattern.FindString(token) != token {
		return nil, errors.New("令牌格式不正确，请填写完整 JWT")
	}
	candidate := M{"token": token}
	profile, e := a.requestJSON(ctx, "GET", a.cfg.APIBase+"/api/user/self", candidate, pinned, nil)
	if e != nil {
		return nil, e
	}
	p := obj(profile["data"])
	uid := str(p["id"], text(p["username"]))
	if uid == "" {
		return nil, errors.New("令牌校验失败：上游未返回账号身份")
	}
	if u := text(jwt(token)["username"]); u != "" && text(p["username"]) != "" && u != text(p["username"]) {
		return nil, errors.New("令牌用户与上游返回的账号身份不一致，未保存")
	}
	unlock := a.lockAccount(uid)
	defer unlock()
	candidate = M{"uid": uid, "token": token, "username": str(p["username"], text(jwt(token)["username"])), "display_name": text(p["display_name"]), "note": note, "profile": profileView(p), "login_via": via}
	normalizeAccount(candidate)
	// Reuse an existing account's configured exit for import follow-up operations.
	route := pinned
	if route == nil {
		r, e := a.proxies.Account(uid)
		if e != nil {
			return nil, e
		}
		route = &r
	}
	if e = a.proxies.Bind([]string{uid}, *route); e != nil {
		return nil, e
	}
	a.refreshAt(ctx, candidate, []string{"balance", "free", "mine", "invite", "sign"}, route)
	a.autoActionsAt(ctx, candidate, route)
	if via == "wechat" {
		if d, e := a.requestJSON(ctx, "POST", a.cfg.WebBase+"/api/web/auth/speedai", candidate, route, M{"login_at": time.Now().UnixMilli(), "username": candidate["username"]}); e == nil && yes(d["logged_in"]) {
			candidate["web_id"] = text(obj(d["user"])["id"])
		}
	}
	updated, e := a.store.Upsert(candidate)
	if e != nil {
		return nil, e
	}
	return M{"ok": true, "updated": updated, "account": a.public(a.store.Get(uid))}, nil
}
func (a *App) refresh(ctx context.Context, account M, parts []string) []string {
	return a.refreshAt(ctx, account, parts, nil)
}
func (a *App) refreshAt(ctx context.Context, acc M, parts []string, pinned *Route) []string {
	errs := []string{}
	call := func(method, url string, body any) (M, error) {
		return a.requestJSON(ctx, method, url, acc, pinned, body)
	}
	for _, part := range parts {
		var e error
		user := acc["username"]
		var out M
		switch part {
		case "profile":
			out, e = call("GET", a.cfg.APIBase+"/api/user/self", nil)
			if e == nil {
				p := obj(out["data"])
				if text(p["id"]) != "" && text(p["id"]) != text(acc["uid"]) {
					e = errors.New("上游账号身份变化，未更新")
				} else {
					acc["profile"] = profileView(p)
					acc["username"] = str(p["username"], text(acc["username"]))
					acc["display_name"] = str(p["display_name"], text(acc["display_name"]))
				}
			}
		case "balance":
			out, e = call("POST", a.cfg.PayBase+"/v1/gettoken", M{"username": user, "token": 0})
			if e == nil {
				acc["balance"] = M{"token": out["token"], "agent_token": out["agent_token"], "rewrite_token": out["rewrite_token"], "at": now()}
			} else {
				var ue *UpstreamError
				if !errors.As(e, &ue) || ue.Status != 401 && ue.Status != 0 {
					if fallback, fe := call("POST", a.cfg.PayBase+"/v1/agent/balance", M{"username": user}); fe == nil {
						b := obj(acc["balance"])
						b["agent_token"] = obj(fallback["data"])["agent_balance"]
						b["at"] = now()
						b["partial"] = true
						acc["balance"] = b
						e = nil
					}
				}
			}
		case "free":
			out, e = call("GET", a.cfg.APIBase+"/api/codar/activities/free-model", nil)
			if e == nil {
				d := obj(out["data"])
				d["at"] = now()
				acc["free"] = d
				acc["free_checked_at"] = now()
			}
		case "mine":
			out, e = call("GET", a.cfg.APIBase+"/api/codar/activities/mine", nil)
			if e == nil {
				acc["activities"] = arr(out["data"])
			}
		case "invite":
			out, e = call("POST", a.cfg.PayBase+"/v1/promotion/info", M{"username": user, "token": 0})
			if e == nil {
				info := obj(out["data"])
				out, e = call("POST", a.cfg.PayBase+"/v1/promotion/stats", M{"username": user, "token": 0})
				if e == nil {
					for k, v := range obj(out["data"]) {
						info[k] = v
					}
					info["at"] = now()
					acc["invite"] = info
				}
			}
		case "sign":
			out, e = call("POST", a.cfg.PayBase+"/v1/daily-sign/status", M{"username": user})
			if e == nil {
				d := obj(out["data"])
				s := obj(acc["sign"])
				s["signed"] = yes(d["isSigned"])
				s["retry_after"] = d["retry_after"]
				s["at"] = now()
				acc["sign"] = s
			}
		}
		if e != nil {
			var ue *UpstreamError
			if errors.As(e, &ue) && ue.Status == 401 {
				acc["token_invalid"] = true
			}
			errs = append(errs, part+"："+safeMessage(e.Error()))
		}
		if ctx.Err() != nil {
			break
		}
	}
	if len(errs) == 0 {
		acc["token_invalid"] = false
	}
	if len(parts) > 2 {
		acc["refreshed_at"] = now()
	}
	if len(errs) > 0 {
		acc["last_error"] = "刷新失败：" + strings.Join(errs, "；")
		acc["last_error_at"] = now()
	}
	return errs
}
func isoTime(v any) float64 {
	if f := num(v); f != 0 {
		if f > 1e12 {
			return f / 1000
		}
		return f
	}
	s := text(v)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05"} {
		if t, e := time.Parse(layout, s); e == nil {
			return float64(t.UnixMilli()) / 1000
		}
	}
	return 0
}
func (a *App) sign(ctx context.Context, acc M, route *Route) (M, error) {
	out, e := a.requestJSON(ctx, "POST", a.cfg.PayBase+"/v1/daily-sign", acc, route, M{"username": acc["username"]})
	if e != nil {
		return nil, e
	}
	d := obj(out["data"])
	s := obj(acc["sign"])
	for k, v := range (M{"signed": true, "at": now(), "signed_at": now(), "granted": d["granted"], "expire_at": isoTime(d["expire_at"]), "error": ""}) {
		s[k] = v
	}
	acc["sign"] = s
	if v := d["agent_balance"]; v != nil {
		b := obj(acc["balance"])
		b["agent_token"] = v
		b["at"] = now()
		acc["balance"] = b
	}
	return d, nil
}
func (a *App) claim(ctx context.Context, acc M, route *Route) (M, error) {
	d, e := a.requestJSON(ctx, "POST", a.cfg.APIBase+"/api/codar/activities/free-model/claim", acc, route, nil)
	if e != nil {
		return nil, e
	}
	f := obj(d["data"])
	f["at"] = now()
	acc["free"] = f
	acc["free_checked_at"] = now()
	return f, nil
}
func (a *App) renew(ctx context.Context, acc M, route *Route) error {
	out, e := a.requestJSON(ctx, "POST", a.cfg.PayBase+"/v1/token/refresh", acc, route, nil)
	r := obj(acc["renew"])
	r["at"] = now()
	r["ok"] = e == nil
	if e == nil {
		d := obj(out["data"])
		token := str(d["token"], text(out["token"]))
		if tokenPattern.FindString(token) != token || token == "" {
			e = errors.New("续期响应缺少有效令牌")
		} else if u := text(jwt(token)["username"]); u != "" && u != text(acc["username"]) {
			e = errors.New("续期令牌身份不一致")
		} else {
			acc["token"] = token
			acc["token_invalid"] = false
			r["count"] = num(r["count"]) + 1
		}
	}
	r["ok"] = e == nil
	r["error"] = ""
	if e != nil {
		r["error"] = safeMessage(e.Error())
	}
	acc["renew"] = r
	return e
}
func (a *App) autoActions(ctx context.Context, acc M) { a.autoActionsAt(ctx, acc, nil) }
func (a *App) autoActionsAt(ctx context.Context, acc M, route *Route) {
	cfg := a.store.Settings()
	t := now()
	exp := num(jwt(text(acc["token"]))["exp"])
	r := obj(acc["renew"])
	if yes(cfg["auto_renew_token"]) && exp > 0 && exp-t < num(cfg["renew_before_hours"])*3600 && t-num(r["at"]) > 1800 {
		_ = a.renew(ctx, acc, route)
	}
	if yes(cfg["auto_claim"]) && obj(acc["free"])["status"] == "available" {
		_, _ = a.claim(ctx, acc, route)
	}
	s := obj(acc["sign"])
	due := !yes(s["signed"]) || (num(s["retry_after"]) > 0 && t >= num(s["at"])+num(s["retry_after"]))
	if yes(cfg["auto_sign"]) && due && t-num(s["attempt_at"]) > 1800 {
		s["attempt_at"] = t
		acc["sign"] = s
		if _, e := a.sign(ctx, acc, route); e != nil {
			s["error"] = safeMessage(e.Error())
		}
	}
}
func (a *App) price(model string) M {
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	if p := obj(a.store.prices[model]); len(p) > 0 {
		d := clone(p)
		d["source"] = "custom"
		return d
	}
	if d := arr(a.priceDefaults[model]); len(d) >= 5 {
		return M{"input": d[0], "output": d[1], "cached": d[2], "source": d[3], "note": d[4]}
	}
	for _, v := range a.store.catalog {
		m := obj(v)
		if m["id"] == model {
			p := obj(m["pricing"])
			if p["input"] != nil {
				return M{"input": num(p["input"]), "output": num(p["output"]), "cached": num(p["cached_input"]), "source": "catalog", "note": "Codar 目录价"}
			}
		}
	}
	return nil
}
func (a *App) cost(model string, prompt, cached, completion float64) any {
	p := a.price(model)
	if p == nil {
		return nil
	}
	return (math.Max(prompt-cached, 0)*num(p["input"]) + cached*num(p["cached"]) + completion*num(p["output"])) / 1e6
}
func (a *App) priceTable() []any {
	set := map[string]bool{}
	a.store.mu.RLock()
	for k := range a.store.prices {
		set[k] = true
	}
	for _, v := range a.store.catalog {
		set[text(obj(v)["id"])] = true
	}
	cat := clone(a.store.catalog)
	a.store.mu.RUnlock()
	for k := range a.priceDefaults {
		set[k] = true
	}
	out := []any{}
	for id := range set {
		p := a.price(id)
		if p == nil {
			continue
		}
		p["model"] = id
		p["source_label"] = map[string]string{"cny": "原项目人民币参考价", "usd": "原项目美元参考价（1:1）", "catalog": "Codar 目录价", "custom": "自定义"}[text(p["source"])]
		p["default"] = nil
		if d := arr(a.priceDefaults[id]); len(d) >= 5 {
			p["default"] = M{"input": d[0], "output": d[1], "cached": d[2], "source": d[3], "note": d[4]}
		}
		p["in_catalog"] = false
		for _, v := range cat {
			if obj(v)["id"] == id {
				p["in_catalog"] = true
			}
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return text(obj(out[i])["model"]) < text(obj(out[j])["model"]) })
	return out
}
func (a *App) savePrice(p M) error {
	model := strings.TrimSpace(text(p["model"]))
	if model == "" || len(model) > 100 {
		return errors.New("模型 ID 格式不对")
	}
	a.store.mu.Lock()
	defer a.store.mu.Unlock()
	next := clone(a.store.prices)
	if yes(p["reset"]) {
		delete(next, model)
	} else {
		d := M{"note": text(p["note"])}
		for _, k := range []string{"input", "output", "cached"} {
			v, ok := p[k].(float64)
			if !ok || v < 0 || v > 100000 || math.IsNaN(v) || math.IsInf(v, 0) {
				return errors.New("价格必须是 0 至 100000 的数字")
			}
			d[k] = v
		}
		next[model] = d
	}
	if e := writeJSON(filepath.Join(a.cfg.DataDir, "prices.json"), next); e != nil {
		return e
	}
	a.store.prices = next
	return nil
}
func usageRange(start, end string) error {
	s, e := time.Parse("2006-01-02", start)
	if e != nil {
		return errors.New("日期格式应为 YYYY-MM-DD")
	}
	t, e := time.Parse("2006-01-02", end)
	if e != nil || t.Before(s) {
		return errors.New("结束日期无效或早于开始日期")
	}
	if t.Sub(s) >= 31*24*time.Hour {
		return errors.New("最多查询 31 天")
	}
	return nil
}
func (a *App) usageDaily(ctx context.Context, acc M, start, end string) (M, error) {
	if e := usageRange(start, end); e != nil {
		return nil, e
	}
	out, e := a.requestJSON(ctx, "GET", a.cfg.APIBase+"/v1/codar/usage/daily?start_date="+url.QueryEscape(start)+"&end_date="+url.QueryEscape(end), acc, nil, nil)
	if e != nil {
		return nil, e
	}
	d := obj(out["data"])
	summary := obj(d["summary"])
	models := map[string]M{}
	byDay := map[string]float64{}
	items := []any{}
	for _, v := range arr(d["items"]) {
		x := obj(v)
		if num(x["calls"]) == 0 {
			continue
		}
		items = append(items, x)
		id := str(x["model_name"], "?")
		m := models[id]
		if m == nil {
			m = M{"model": id}
			models[id] = m
		}
		for _, k := range []string{"calls", "input_tokens", "output_tokens", "cached_tokens"} {
			m[k] = num(m[k]) + num(x[k])
		}
		byDay[text(x["date"])] += num(a.cost(id, num(x["input_tokens"]), num(x["cached_tokens"]), num(x["output_tokens"])))
	}
	rows := []any{}
	sum := 0.0
	unpriced := []any{}
	for id, m := range models {
		m["cache_hit_rate"] = ratio(num(m["cached_tokens"]), num(m["input_tokens"]))
		m["cost"] = a.cost(id, num(m["input_tokens"]), num(m["cached_tokens"]), num(m["output_tokens"]))
		if m["cost"] == nil {
			unpriced = append(unpriced, id)
		}
		sum += num(m["cost"])
		rows = append(rows, m)
	}
	sort.Slice(rows, func(i, j int) bool { return num(obj(rows[i])["calls"]) > num(obj(rows[j])["calls"]) })
	summary["cost"] = sum
	summary["unpriced"] = unpriced
	summary["cache_hit_rate"] = ratio(num(summary["cached_tokens"]), num(summary["input_tokens"]))
	daily := arr(d["daily"])
	for _, v := range daily {
		x := obj(v)
		x["cost"] = byDay[text(x["date"])]
	}
	return M{"summary": summary, "models": rows, "items": items, "daily": daily, "start": start, "end": end, "timezone": d["timezone"], "generated_at": d["generated_at"]}, nil
}

var ledgerNote = regexp.MustCompile(`^\s*([^|]+?)\s*\|\s*token[：:]\s*输入\s*(\d+)\s*[，,]\s*输出\s*(\d+)(?:\s*[，,]\s*缓存\s*(\d+))?`)

func (a *App) ledger(ctx context.Context, acc M, page, size int) (M, error) {
	page = max(1, page)
	size = max(1, min(size, 100))
	out, e := a.requestJSON(ctx, "POST", a.cfg.PayBase+"/v1/agent/ledger", acc, nil, M{"username": acc["username"], "page": page, "page_size": size})
	if e != nil {
		return nil, e
	}
	d := obj(out["data"])
	rows := []any{}
	for _, v := range arr(d["items"]) {
		x := obj(v)
		r := M{"id": x["id"], "change": x["change"], "type": text(x["point_type"]), "note": text(x["note"]), "at": isoTime(x["created_at"]), "expire_at": isoTime(x["expire_at"])}
		if m := ledgerNote.FindStringSubmatch(text(x["note"])); m != nil {
			r["model"] = strings.TrimSpace(m[1])
			r["input"] = num(m[2])
			r["output"] = num(m[3])
			r["cached"] = num(m[4])
		}
		rows = append(rows, r)
	}
	return M{"total": num(d["total"]), "page": page, "page_size": size, "items": rows}, nil
}
func ratio(a, b float64) any {
	if b == 0 {
		return nil
	}
	return a / b
}
