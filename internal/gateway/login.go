package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
)

func routeMap(r Route) M { return M{"id": r.ID, "name": r.Name, "fingerprint": r.Fingerprint} }
func mapRoute(v any) Route {
	m := obj(v)
	return Route{ID: int(num(m["id"])), Name: text(m["name"]), Fingerprint: text(m["fingerprint"])}
}
func captchaFields(v any) (M, error) {
	c := obj(v)
	out := M{}
	for _, k := range []string{"lot_number", "captcha_output", "pass_token", "gen_time"} {
		switch c[k].(type) {
		case string, json.Number, float64, int, int64:
		default:
			return nil, errors.New("请先完成人工验证")
		}
		s := text(c[k])
		if s == "" || len(s) > 8192 {
			return nil, errors.New("请先完成人工验证")
		}
		out[k] = s
	}
	return out, nil
}
func (a *App) sendSMS(ctx context.Context, phone, kind string, captcha M, route *Route) error {
	if !phonePattern.MatchString(phone) {
		return errors.New("手机号格式不正确")
	}
	if kind != "login" && kind != "ip_verify" && kind != "bind_phone" {
		return errors.New("验证码类型不正确")
	}
	body, e := captchaFields(captcha)
	if e != nil {
		return e
	}
	body["phone"] = phone
	body["name"] = "user"
	body["type"] = kind
	_, e = a.requestJSON(ctx, "POST", a.cfg.PayBase+"/v1/verification-code", nil, route, body)
	return e
}
func (a *App) phoneLogin(ctx context.Context, phone, code string, route *Route) (M, error) {
	if !phonePattern.MatchString(phone) || !codePattern.MatchString(code) {
		return nil, errors.New("手机号或短信验证码格式不正确")
	}
	out, e := a.requestJSON(ctx, "POST", a.cfg.PayBase+"/v1/login", nil, route, M{"username": phone, "password": code})
	if e != nil {
		var ue *UpstreamError
		if !errors.As(e, &ue) || ue.Status != 201 {
			return nil, e
		}
		out = ue.Body
	}
	d := obj(out["data"])
	status := str(d["status"], text(out["status"]))
	token := str(d["token"], text(out["token"]))
	username := str(d["username"], text(out["username"]))
	if token == "" {
		return nil, errors.New("登录响应缺少令牌")
	}
	if num(out["code"]) == 201 || status == "ip_verify_required" {
		return M{"state": "ip_verify", "temp_token": token, "username": username, "phone": str(d["phone"], text(out["phone"])), "message": str(out["message"], "检测到新 IP，需要手机验证")}, nil
	}
	return M{"state": "success", "token": token, "username": username}, nil
}
func (a *App) verifyIP(ctx context.Context, user, phone, code, temp string, route *Route) (string, error) {
	if !codePattern.MatchString(code) {
		return "", errors.New("请填写短信验证码")
	}
	d, e := a.requestJSON(ctx, "POST", a.cfg.PayBase+"/v1/user/verify_ip", M{"token": temp}, route, M{"username": user, "phone": phone, "code": code})
	if e != nil {
		return "", e
	}
	token := str(obj(d["data"])["token"], text(d["token"]))
	if token == "" {
		return "", errors.New("安全验证响应缺少令牌")
	}
	return token, nil
}
func (a *App) newFlow(d M) string {
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	id := random(16)
	d["at"] = now()
	a.flows[id] = clone(d)
	return id
}
func (a *App) manualLogin(ctx context.Context, path string, p M) (M, error) {
	a.loginMu.Lock()
	for k, v := range a.flows {
		if now()-num(v["at"]) > 900 {
			delete(a.flows, k)
		}
	}
	for k, v := range a.qr {
		if now()-num(v["at"]) > 3600 {
			delete(a.qr, k)
		}
	}
	flow := clone(a.flows[text(p["flow"])])
	a.loginMu.Unlock()
	if text(p["flow"]) != "" && flow == nil {
		return nil, errors.New("安全验证已过期，请重新登录")
	}
	pid, e := parseProxyID(p["proxy_id"])
	if e != nil {
		return nil, e
	}
	route, e := a.proxies.Pin(pid)
	if e != nil {
		return nil, e
	}
	if flow != nil {
		route = mapRoute(flow["route"])
	}
	phone := strings.TrimSpace(text(p["phone"]))
	note := text(p["note"])
	switch path {
	case "/phone/code":
		kind := "login"
		if flow != nil {
			if flow["kind"] == "bind" {
				kind = "bind_phone"
			} else {
				kind = "ip_verify"
			}
			if flow["kind"] != "bind" && text(flow["phone"]) != "" {
				phone = text(flow["phone"])
			}
			if tail := text(flow["tail"]); tail != "" && !strings.HasSuffix(phone, tail) {
				return nil, errors.New("手机号与账号绑定尾号不一致")
			}
		}
		if e = a.sendSMS(ctx, phone, kind, obj(p["captcha"]), &route); e != nil {
			return nil, e
		}
		if flow != nil {
			a.loginMu.Lock()
			if f := a.flows[text(p["flow"])]; f != nil {
				f["phone"] = phone
			}
			a.loginMu.Unlock()
		}
		return M{"ok": true, "message": "验证码已发送", "phone": maskPhone(phone)}, nil
	case "/phone/login":
		result, e := a.phoneLogin(ctx, phone, text(p["code"]), &route)
		if e != nil {
			return nil, e
		}
		if result["state"] == "ip_verify" {
			id := a.newFlow(M{"kind": "ip", "username": result["username"], "phone": phone, "temp": result["temp_token"], "note": note, "via": "phone", "route": routeMap(route)})
			return M{"ok": true, "state": "ip_verify", "flow": id, "username": result["username"], "phone": maskPhone(phone), "message": result["message"]}, nil
		}
		d, e := a.importToken(ctx, text(result["token"]), note, "phone", &route)
		if e == nil {
			d["state"] = "success"
		}
		return d, e
	case "/phone/verify-ip", "/phone/bind":
		if flow == nil {
			return nil, errors.New("没有待完成的安全验证，请重新登录")
		}
		unlock := a.lockAccount("flow:" + text(p["flow"]))
		defer unlock()
		a.loginMu.Lock()
		current := clone(a.flows[text(p["flow"])])
		a.loginMu.Unlock()
		if current == nil {
			return nil, errors.New("此登录流程已完成，请刷新账号列表")
		}
		flow = current
		route = mapRoute(flow["route"])
		phone = text(flow["phone"])
		if !phonePattern.MatchString(phone) {
			return nil, errors.New("请先获取短信验证码")
		}
		code := text(p["code"])
		if !codePattern.MatchString(code) {
			return nil, errors.New("请填写短信验证码")
		}
		var token string
		if path == "/phone/bind" {
			if flow["kind"] != "bind" {
				return nil, errors.New("当前流程不需要绑定手机号")
			}
			d, err := a.requestJSON(ctx, "POST", a.cfg.PayBase+"/v1/bind-phone", nil, &route, M{"username": flow["username"], "phone": phone, "code": code})
			if err != nil {
				return nil, err
			}
			token = str(obj(d["data"])["token"], str(d["token"], text(flow["temp"])))
		} else {
			if flow["kind"] == "bind" {
				return nil, errors.New("请先完成手机号绑定")
			}
			token, e = a.verifyIP(ctx, text(flow["username"]), phone, code, text(flow["temp"]), &route)
			if e != nil {
				return nil, e
			}
		}
		d, e := a.importToken(ctx, token, str(p["note"], text(flow["note"])), str(flow["via"], "phone"), &route)
		if e != nil {
			return nil, e
		}
		a.loginMu.Lock()
		delete(a.flows, text(p["flow"]))
		a.loginMu.Unlock()
		d["state"] = "success"
		return d, nil
	case "/wechat/qrcode":
		out, e := a.requestJSON(ctx, "POST", a.cfg.PayBase+"/v1/wechat/login/qrcode", nil, &route, M{})
		if e != nil {
			return nil, e
		}
		d := obj(out["data"])
		if text(d["scene"]) == "" || text(d["qr_url"]) == "" {
			return nil, errors.New("上游没有返回二维码")
		}
		scene := text(d["scene"])
		a.loginMu.Lock()
		a.qr[scene] = M{"at": now(), "note": note, "route": routeMap(route)}
		a.loginMu.Unlock()
		return M{"ok": true, "scene": scene, "qr_url": d["qr_url"], "expires_in": 600}, nil
	case "/wechat/status":
		scene := text(p["scene"])
		unlock := a.lockAccount("qr:" + scene)
		defer unlock()
		a.loginMu.Lock()
		ent := clone(a.qr[scene])
		a.loginMu.Unlock()
		if ent == nil {
			return M{"state": "failed", "reason": "二维码不存在或已过期"}, nil
		}
		if d := obj(ent["result"]); len(d) > 0 {
			return d, nil
		}
		if now()-num(ent["at"]) > 600 {
			return M{"state": "failed", "reason": "二维码已过期，请重新获取"}, nil
		}
		route = mapRoute(ent["route"])
		out, e := a.requestJSON(ctx, "POST", a.cfg.PayBase+"/v1/wechat/login/status", nil, &route, M{"scene": scene})
		if e != nil {
			return M{"state": "pending", "warn": safeMessage(e.Error()), "left": 600 - (now() - num(ent["at"]))}, nil
		}
		st := obj(out["data"])
		status := text(st["status"])
		if status == "pending" || status == "" {
			return M{"state": "pending", "left": 600 - (now() - num(ent["at"]))}, nil
		}
		note = str(p["note"], text(ent["note"]))
		var result M
		if (status == "bind_phone_required" || status == "ip_verify_required") && text(st["token"]) != "" {
			kind, state, tail := "ip", "ip_verify", text(st["phone"])
			if status == "bind_phone_required" {
				kind = "bind"
				state = "bind_phone"
				tail = ""
			}
			id := a.newFlow(M{"kind": kind, "username": st["username"], "phone": "", "tail": tail, "temp": st["token"], "note": note, "via": "wechat", "route": routeMap(route)})
			result = M{"state": state, "flow": id, "username": st["username"], "tail": tail, "reason": str(st["message"], "请按提示完成手机号验证")}
		} else if status == "success" && text(st["token"]) != "" {
			result, e = a.importToken(ctx, text(st["token"]), note, "wechat", &route)
			if e != nil {
				result = M{"state": "failed", "reason": "扫码成功但账号保存失败：" + safeMessage(e.Error())}
			} else {
				result["state"] = "success"
			}
		} else {
			result = M{"state": "failed", "status": status, "reason": str(st["message"], "登录失败："+status)}
		}
		a.loginMu.Lock()
		if v := a.qr[scene]; v != nil {
			v["result"] = clone(result)
		}
		a.loginMu.Unlock()
		return result, nil
	}
	return nil, errors.New("未知登录操作")
}
func maskPhone(s string) string {
	if len(s) == 11 {
		return s[:3] + "****" + s[7:]
	}
	return s
}
func (a *App) localLogins() ([]M, error) {
	var d M
	if e := readJSON(a.cfg.LocalConfig, &d); e != nil {
		if errors.Is(e, os.ErrNotExist) {
			return []M{}, nil
		}
		return nil, errors.New("读取本机 Codar 配置失败")
	}
	out := []M{}
	seen := map[string]bool{}
	take := func(profile string, m M) {
		speedai := obj(m["speedai"])
		token := text(speedai["token"])
		if token == "" || seen[token] {
			return
		}
		seen[token] = true
		out = append(out, M{"profile": profile, "token": token, "username": str(speedai["username"], text(jwt(token)["username"])), "login_at": isoTime(speedai["login_at"])})
	}
	provider := obj(d["provider"])
	take(str(provider["active_profile"], "speedai"), provider)
	profiles := obj(provider["provider_profiles"])
	if len(profiles) == 0 {
		profiles = obj(d["provider_profiles"])
	}
	for id, v := range profiles {
		take(id, obj(v))
	}
	return out, nil
}
func (a *App) localLoginsView() M {
	found, e := a.localLogins()
	rows := []any{}
	for _, v := range found {
		token := text(v["token"])
		r := clone(v)
		delete(r, "token")
		r["token_masked"] = masked(token)
		exp := num(jwt(token)["exp"])
		r["token_exp"] = exp
		r["expired"] = exp > 0 && exp <= now()
		r["in_pool"] = false
		for _, acc := range a.store.List() {
			if v["username"] != "" && acc["username"] == v["username"] {
				r["in_pool"] = true
				r["same_token"] = acc["token"] == token
				r["uid"] = acc["uid"]
				break
			}
		}
		rows = append(rows, r)
	}
	out := M{"ok": e == nil, "path": a.cfg.LocalConfig, "logins": rows, "error": ""}
	if e != nil {
		out["error"] = e.Error()
	} else if len(rows) == 0 {
		out["error"] = "网关所在机器未发现 Codar 客户端登录记录"
	}
	return out
}
