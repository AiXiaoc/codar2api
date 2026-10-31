package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

func (a *App) panel(r *http.Request, path string, p M) (any, int, error) {
	ctx := r.Context()
	q := r.URL.Query()
	get := r.Method == "GET"
	force := q.Get("force") == "1"
	if get {
		switch path {
		case "/accounts":
			rows := []any{}
			for _, v := range a.store.List() {
				rows = append(rows, a.public(v))
			}
			a.store.mu.RLock()
			at := a.store.catalogAt
			a.store.mu.RUnlock()
			return M{"accounts": rows, "settings": a.store.Settings(), "catalog_at": at}, 200, nil
		case "/settings":
			return M{"settings": a.store.Settings(), "schema": schema}, 200, nil
		case "/keys":
			return M{"keys": a.auth.Keys()}, 200, nil
		case "/proxy-pool":
			return a.proxies.View(true), 200, nil
		case "/catalog":
			models, e := a.catalog(ctx, force)
			if e != nil && len(models) == 0 {
				return nil, 502, e
			}
			a.store.mu.RLock()
			at := a.store.catalogAt
			a.store.mu.RUnlock()
			out := M{"models": models, "at": at}
			if e != nil {
				out["warning"] = safeMessage(e.Error())
			}
			return out, 200, nil
		case "/stats":
			hours := num(q.Get("hours"))
			if hours <= 0 {
				hours = 24
			}
			hours = max(0.1, min(hours, 168))
			d := a.usage.Stats(now()-hours*3600, a.cost)
			d["hours"] = hours
			return d, 200, nil
		case "/prices":
			_, _ = a.catalog(ctx, false)
			return M{"prices": a.priceTable(), "sources": M{"cny": "原项目人民币参考价", "usd": "原项目美元参考价（1:1）", "catalog": "Codar 目录价", "custom": "自定义"}, "note": "参考价格沿用原项目，实际计费以上游为准，可在此调整。"}, 200, nil
		case "/credit-rate":
			return a.creditRate(ctx, force), 200, nil
		case "/announcements":
			return a.announcements(ctx, force), 200, nil
		case "/phone/captcha":
			var d M
			b, _ := assets.ReadFile("defaults.json")
			_ = json.Unmarshal(b, &d)
			return M{"captcha_id": d["CAPTCHA_ID"], "script": d["CAPTCHA_JS"], "product": "bind"}, 200, nil
		case "/local-login":
			return a.localLoginsView(), 200, nil
		case "/accounts/requests":
			acc, e := a.account(q.Get("uid"))
			if e != nil {
				return nil, 404, e
			}
			size := int(num(q.Get("page_size")))
			if size == 0 {
				size = 20
			}
			d := a.usage.Requests(text(acc["uid"]), text(acc["username"]), q.Get("failed") == "1", int(num(q.Get("page"))), size)
			d["ok"], d["uid"], d["username"] = true, acc["uid"], acc["username"]
			return d, 200, nil
		case "/accounts/usage", "/accounts/ledger":
			start, end := q.Get("start"), q.Get("end")
			if start == "" {
				start = time.Now().AddDate(0, 0, -6).Format("2006-01-02")
			}
			if end == "" {
				end = time.Now().Format("2006-01-02")
			}
			uid := q.Get("uid")
			if uid == "" && path == "/accounts/usage" {
				d, e := a.usageAll(ctx, start, end)
				return d, 400, e
			}
			acc, e := a.account(uid)
			if e != nil {
				return nil, 404, e
			}
			var d M
			if path == "/accounts/usage" {
				d, e = a.usageDaily(ctx, acc, start, end)
			} else {
				page, size := int(num(q.Get("page"))), int(num(q.Get("page_size")))
				if size == 0 {
					size = 20
				}
				d, e = a.ledger(ctx, acc, page, size)
			}
			if e != nil {
				return M{"ok": false, "error": safeMessage(e.Error())}, 200, nil
			}
			d["ok"] = true
			d["uid"] = uid
			d["username"] = acc["username"]
			return d, 200, nil
		}
		return nil, 404, errors.New("未知接口")
	}
	switch path {
	case "/auth/password":
		e := a.auth.ChangePassword(text(p["current_password"]), text(p["password"]))
		return M{"ok": true}, 400, e
	case "/keys":
		d, e := a.auth.KeysAction(p)
		return d, 400, e
	case "/proxy-pool/manage":
		d, e := a.proxies.Action(p)
		return d, 400, e
	case "/proxy-pool/check":
		id, e := parseProxyID(p["id"])
		if e != nil || id == 0 {
			return nil, 400, errors.New("请选择需要检测的代理")
		}
		d, e := a.proxies.Check(ctx, id)
		return M{"ok": e == nil, "check": d}, 400, e
	case "/accounts/proxy":
		uids := []string{}
		if yes(p["all"]) {
			for _, acc := range a.store.List() {
				uids = append(uids, text(acc["uid"]))
			}
		} else {
			uid := text(p["uid"])
			if _, e := a.account(uid); e != nil {
				return nil, 404, e
			}
			uids = append(uids, uid)
		}
		if len(uids) == 0 {
			return nil, 400, errors.New("当前没有账号")
		}
		if _, ok := p["proxy_id"]; !ok {
			return nil, 400, errors.New("请选择出口")
		}
		id, e := parseProxyID(p["proxy_id"])
		if e != nil {
			return nil, 400, e
		}
		route, e := a.proxies.Pin(id)
		if e != nil {
			return nil, 400, e
		}
		if e = a.proxies.Bind(uids, route); e != nil {
			return nil, 400, e
		}
		return M{"ok": true, "updated": len(uids)}, 200, nil
	case "/accounts/batch":
		d, e := a.store.Batch(p)
		return d, 400, e
	case "/accounts/import-batch":
		return a.importBatch(ctx, p), 200, nil
	case "/accounts/add":
		d, e := a.importToken(ctx, text(p["token"]), text(p["note"]), "token", nil)
		return d, statusOf(e), e
	case "/local-login/import":
		found, e := a.localLogins()
		if e != nil {
			return nil, 400, e
		}
		for _, login := range found {
			if text(p["profile"]) == "" || p["profile"] == login["profile"] {
				d, e := a.importToken(ctx, text(login["token"]), text(p["note"]), "local", nil)
				return d, statusOf(e), e
			}
		}
		return nil, 400, errors.New("本机配置中没有找到登录令牌")
	case "/phone/code", "/phone/login", "/phone/verify-ip", "/phone/bind", "/wechat/qrcode", "/wechat/status":
		d, e := a.manualLogin(ctx, path, p)
		if e != nil {
			return M{"ok": false, "error": safeMessage(e.Error())}, 200, nil
		}
		return d, 200, nil
	case "/settings":
		d, e := a.store.SaveSettings(p)
		return M{"ok": true, "settings": d}, 400, e
	case "/prices":
		if e := a.savePrice(p); e != nil {
			return nil, 400, e
		}
		return M{"ok": true, "price": a.price(text(p["model"]))}, 200, nil
	case "/accounts/cooldown/clear":
		cleared := []any{}
		for _, acc := range a.store.List() {
			if text(p["uid"]) != "" && acc["uid"] != p["uid"] {
				continue
			}
			if num(acc["cooldown_until"]) <= now() {
				continue
			}
			e := a.store.Update(text(acc["uid"]), func(c M) error {
				c["cooldown_until"] = 0
				c["cooldown_reason"] = ""
				c["cooldown_kind"] = ""
				c["refreshed_at"] = 0
				if !yes(c["restricted"]) {
					c["last_error"] = ""
				}
				return nil
			})
			if e != nil {
				return nil, 500, e
			}
			cleared = append(cleared, acc["username"])
		}
		return M{"ok": true, "cleared": cleared}, 200, nil
	case "/accounts/refresh", "/accounts/sign":
		if text(p["uid"]) == "" {
			targets := []M{}
			for _, acc := range a.store.List() {
				if yes(acc["enabled"]) {
					targets = append(targets, acc)
				}
			}
			rows := parallel(ctx, targets, func(acc M) M { return a.operateAccount(ctx, path, text(acc["uid"]), p) })
			ok := true
			items := []any{}
			for _, row := range rows {
				items = append(items, row)
				ok = ok && yes(row["ok"])
			}
			return M{"ok": ok, "results": items}, 200, nil
		}
		if path == "/accounts/refresh" {
			if _, e := a.account(text(p["uid"])); e != nil {
				return nil, 404, e
			}
			result := a.operateAccount(ctx, path, text(p["uid"]), p)
			return M{"ok": yes(result["ok"]), "results": []any{result}}, 200, nil
		}
	}
	if strings.HasPrefix(path, "/accounts/") {
		uid := text(p["uid"])
		if _, e := a.account(uid); e != nil {
			return nil, 404, e
		}
		return a.operateAccount(ctx, path, uid, p), 200, nil
	}
	return nil, 404, errors.New("未知接口")
}
func (a *App) operateAccount(ctx context.Context, path, uid string, p M) M {
	unlock := a.lockAccount(uid)
	defer unlock()
	acc := a.store.Get(uid)
	if acc == nil {
		return M{"ok": false, "error": "账号不存在"}
	}
	out := M{"ok": true, "uid": uid, "username": acc["username"]}
	var e error
	save := true
	switch path {
	case "/accounts/refresh":
		errs := a.refresh(ctx, acc, []string{"profile", "balance", "free", "mine", "invite", "sign"})
		if len(errs) > 0 {
			e = errors.New(strings.Join(errs, "；"))
		}
		a.autoActions(ctx, acc)
	case "/accounts/update":
		save = false
		if v, ok := p["enabled"]; ok {
			if _, ok := v.(bool); !ok {
				e = errors.New("enabled 必须是布尔值")
				break
			}
		}
		e = a.store.Update(uid, func(c M) error {
			if v, ok := p["enabled"]; ok {
				c["enabled"] = v
			}
			if v, ok := p["note"]; ok {
				n := []rune(text(v))
				c["note"] = string(n[:min(len(n), 200)])
			}
			if yes(p["reset"]) {
				c["cooldown_until"] = 0
				c["cooldown_reason"] = ""
				c["cooldown_kind"] = ""
				if !yes(c["restricted"]) {
					c["last_error"] = ""
				}
			}
			return nil
		})
	case "/accounts/delete":
		save = false
		_, e = a.store.Batch(M{"action": "delete", "uids": []any{uid}})
	case "/accounts/sign":
		out["data"], e = a.sign(ctx, acc, nil)
		out["granted"] = obj(out["data"])["granted"]
		out["expire_at"] = obj(acc["sign"])["expire_at"]
	case "/accounts/claim":
		out["free"], e = a.claim(ctx, acc, nil)
		if e == nil {
			a.refresh(ctx, acc, []string{"free", "mine"})
		}
	case "/accounts/renew":
		e = a.renew(ctx, acc, nil)
		out["token_exp"] = num(jwt(text(acc["token"]))["exp"])
	case "/accounts/redeem":
		code := strings.TrimSpace(text(p["code"]))
		if code == "" || len(code) > 128 || strings.ContainsAny(code, " \n\r\t") {
			e = errors.New("兑换码不能为空、不能含空格")
			break
		}
		d, err := a.requestJSON(ctx, "POST", a.cfg.PayBase+"/v1/redeem-code", acc, nil, M{"username": acc["username"], "code": code})
		message := str(d["message"], "兑换成功")
		if err != nil {
			message = safeMessage(err.Error())
		}
		items := append(arr(acc["redeems"]), M{"code": code, "ok": err == nil, "message": message, "data": d["data"], "at": now()})
		acc["redeems"] = items[max(0, len(items)-20):]
		out["message"] = message
		out["data"] = d["data"]
		e = err
		if e == nil {
			a.refresh(ctx, acc, []string{"balance"})
		}
	case "/accounts/test":
		return a.testAccount(ctx, acc, text(p["model"]))
	default:
		save = false
		e = errors.New("未知的账号操作")
	}
	if save {
		if se := a.saveBusiness(acc); se != nil {
			e = errors.Join(e, se)
		}
	}
	if e != nil {
		out["ok"] = false
		out["error"] = safeMessage(e.Error())
	}
	if current := a.store.Get(uid); current != nil {
		out["account"] = a.public(current)
	}
	return out
}
func parallel(ctx context.Context, accounts []M, fn func(M) M) []M {
	results := make([]M, len(accounts))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, acc := range accounts {
		select {
		case <-ctx.Done():
			wg.Wait()
			for j := i; j < len(results); j++ {
				results[j] = M{"ok": false, "error": "请求已取消"}
			}
			return results
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(i int, acc M) { defer wg.Done(); defer func() { <-sem }(); results[i] = fn(acc) }(i, acc)
	}
	wg.Wait()
	return results
}
func (a *App) importBatch(ctx context.Context, p M) M {
	input := text(p["text"])
	if input == "" {
		for _, v := range arr(p["tokens"]) {
			input += text(v) + "\n"
		}
	}
	tokens := []M{}
	seen := map[string]bool{}
	for _, t := range tokenPattern.FindAllString(input, -1) {
		if !seen[t] {
			seen[t] = true
			tokens = append(tokens, M{"token": t, "index": len(tokens) + 1})
		}
	}
	if len(tokens) == 0 || len(tokens) > 200 {
		return M{"ok": false, "error": "请提供 1 至 200 个有效 JWT"}
	}
	rows := parallel(ctx, tokens, func(v M) M {
		d, e := a.importToken(ctx, text(v["token"]), text(p["note"]), "token", nil)
		r := M{"index": v["index"], "token_masked": masked(text(v["token"]))}
		if e != nil {
			r["ok"] = false
			r["error"] = safeMessage(e.Error())
		} else {
			r["ok"] = true
			r["updated"] = d["updated"]
			r["uid"] = obj(d["account"])["uid"]
			r["username"] = obj(d["account"])["username"]
		}
		return r
	})
	added, updated, failed := 0, 0, 0
	items := []any{}
	for _, row := range rows {
		items = append(items, row)
		if !yes(row["ok"]) {
			failed++
		} else if yes(row["updated"]) {
			updated++
		} else {
			added++
		}
	}
	return M{"ok": true, "total": len(rows), "added": added, "updated": updated, "failed": failed, "results": items}
}
func (a *App) usageAll(ctx context.Context, start, end string) (M, error) {
	if e := usageRange(start, end); e != nil {
		return nil, e
	}
	targets := []M{}
	for _, acc := range a.store.List() {
		if yes(acc["enabled"]) && !yes(acc["token_invalid"]) {
			targets = append(targets, acc)
		}
	}
	rows := parallel(ctx, targets, func(acc M) M {
		d, e := a.usageDaily(ctx, acc, start, end)
		if e != nil {
			return M{"uid": acc["uid"], "username": acc["username"], "error": safeMessage(e.Error())}
		}
		d["uid"] = acc["uid"]
		d["username"] = acc["username"]
		return d
	})
	keys := strings.Fields("calls input_tokens output_tokens cached_tokens total_tokens cost")
	total := M{}
	days, models := map[string]M{}, map[string]M{}
	accounts, errs := []any{}, []any{}
	add := func(dst, src M) {
		for _, k := range keys {
			dst[k] = num(dst[k]) + num(src[k])
		}
	}
	for _, r := range rows {
		if text(r["error"]) != "" {
			errs = append(errs, r)
			continue
		}
		s := obj(r["summary"])
		add(total, s)
		account := clone(s)
		account["uid"] = r["uid"]
		account["username"] = r["username"]
		accounts = append(accounts, account)
		for _, v := range arr(r["daily"]) {
			x := obj(v)
			date := text(x["date"])
			if days[date] == nil {
				days[date] = M{"date": date}
			}
			add(days[date], x)
		}
		for _, v := range arr(r["models"]) {
			m := obj(v)
			id := text(m["model"])
			if models[id] == nil {
				models[id] = M{"model": id}
			}
			add(models[id], m)
		}
	}
	ml, dl := []any{}, []any{}
	for _, m := range models {
		m["cache_hit_rate"] = ratio(num(m["cached_tokens"]), num(m["input_tokens"]))
		ml = append(ml, m)
	}
	for _, d := range days {
		dl = append(dl, d)
	}
	sort.Slice(dl, func(i, j int) bool { return text(obj(dl[i])["date"]) < text(obj(dl[j])["date"]) })
	sort.Slice(ml, func(i, j int) bool { return num(obj(ml[i])["calls"]) > num(obj(ml[j])["calls"]) })
	total["cache_hit_rate"] = ratio(num(total["cached_tokens"]), num(total["input_tokens"]))
	total["unpriced"] = []any{}
	return M{"ok": true, "start": start, "end": end, "summary": total, "models": ml, "daily": dl, "accounts": accounts, "errors": errs}, nil
}
func (a *App) creditRate(ctx context.Context, force bool) M {
	a.rateMu.Lock()
	defer a.rateMu.Unlock()
	if !force && now()-num(a.rate["at"]) < 60 {
		return clone(a.rate)
	}
	targets := []M{}
	for _, acc := range a.store.List() {
		if yes(acc["enabled"]) && !yes(acc["token_invalid"]) {
			targets = append(targets, acc)
		}
	}
	t := now()
	rows := parallel(ctx, targets, func(acc M) M {
		r := M{"uid": acc["uid"], "username": acc["username"], "spent_window": 0, "spent_day": 0, "last_spend": 0, "error": ""}
		for page := 1; page <= 10; page++ {
			d, e := a.ledger(ctx, acc, page, 100)
			if e != nil {
				r["error"] = safeMessage(e.Error())
				break
			}
			items := arr(d["items"])
			oldest := t
			for _, v := range items {
				x := obj(v)
				at, change := num(x["at"]), num(x["change"])
				oldest = min(oldest, at)
				if change < 0 {
					r["last_spend"] = max(num(r["last_spend"]), at)
					if at >= t-86400 {
						r["spent_day"] = num(r["spent_day"]) - change
					}
					if at >= t-3600 {
						r["spent_window"] = num(r["spent_window"]) - change
					}
				}
			}
			if len(items) == 0 || oldest < t-86400 || float64(page*100) >= num(d["total"]) {
				break
			}
		}
		b, _ := spendable(acc, yes(a.store.Settings()["allow_general_balance"]))
		r["balance"] = b
		return r
	})
	out := M{"ok": true, "at": t, "window": 3600, "span": 86400, "spent_window": 0, "spent_day": 0, "balance": 0, "last_spend": 0, "accounts": []any{}, "errors": []any{}}
	for _, r := range rows {
		out["accounts"] = append(arr(out["accounts"]), r)
		for _, k := range []string{"spent_window", "spent_day", "balance"} {
			out[k] = num(out[k]) + num(r[k])
		}
		out["last_spend"] = max(num(out["last_spend"]), num(r["last_spend"]))
		if text(r["error"]) != "" {
			out["errors"] = append(arr(out["errors"]), r)
		}
	}
	out["per_second"] = num(out["spent_window"]) / 3600
	a.rate = out
	return clone(out)
}
func (a *App) announcements(ctx context.Context, force bool) M {
	a.annMu.Lock()
	defer a.annMu.Unlock()
	if force || now()-num(a.ann["at"]) > 1800 {
		for _, acc := range a.store.List() {
			if unavailable(acc, "", a.store.Settings()) != "" {
				continue
			}
			version, e := a.requestJSON(ctx, "GET", a.cfg.APIBase+"/api/codar/announcements/version", acc, nil, nil)
			if e == nil {
				v := text(obj(version["data"])["version"])
				if force || v == "" || v != text(a.ann["version"]) {
					var d M
					d, e = a.requestJSON(ctx, "GET", a.cfg.APIBase+"/api/codar/announcements", acc, nil, nil)
					if e == nil {
						a.ann["items"] = arr(d["data"])
						a.ann["version"] = v
					}
				}
			}
			a.ann["at"] = now()
			a.ann["error"] = ""
			if e != nil {
				a.ann["error"] = safeMessage(e.Error())
			}
			break
		}
	}
	d := clone(a.ann)
	d["ok"] = text(d["error"]) == ""
	return d
}
